package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/akmatori/akmatori/internal/database"
)

// memoryExtractionTimeout caps a single extraction call. Extraction runs in a
// goroutine spawned from UpdateIncidentComplete, so we want it bounded to
// avoid leaking goroutines if the worker hangs.
const memoryExtractionTimeout = 2 * time.Minute

// memoryExtractionMaxTailBytes caps the slice of incident output we feed the
// extractor. Mirrors title_generator.go's truncateForPrompt convention but at
// a larger budget — patterns can be subtle and need a few KB of context.
const memoryExtractionMaxTailBytes = 6000

// MemoryExtractor distills incident transcripts into memory entries via a
// one-shot LLM call. Idempotent: re-running on the same incident is a no-op
// once any extracted memory has landed (we use IncidentUUID as the cursor).
type MemoryExtractor struct {
	caller  OneShotLLMCaller
	manager MemoryManager
}

// NewMemoryExtractor wires the extractor. Pass nil for either dep to disable
// extraction entirely (useful in tests / partially-initialized startup).
func NewMemoryExtractor(caller OneShotLLMCaller, manager MemoryManager) *MemoryExtractor {
	return &MemoryExtractor{caller: caller, manager: manager}
}

// Extract runs end-to-end: builds the prompt from the incident's response/log
// tail and the existing per-scope manifests, calls the LLM, parses strict
// JSON, and applies the resulting upsert/delete edits. All errors are
// swallowed (logged once at warn level) — extraction is best-effort and must
// never block or affect the caller.
func (e *MemoryExtractor) Extract(ctx context.Context, incident *database.Incident) {
	if e == nil || e.caller == nil || e.manager == nil || incident == nil {
		return
	}
	// Idempotency: if any AGENT-authored memories already record this incident
	// as origin, extraction has already run. The created_by filter is critical:
	// operator feedback (UI endpoint or Slack classifier) writes memories with
	// the same incident_uuid but created_by=operator, and we must not let that
	// short-circuit the post-completion extraction run.
	if existing, err := e.manager.CountByIncidentUUID(incident.UUID, MemoryCreatedByAgent); err == nil && existing > 0 {
		slog.Debug("memory extraction skipped: already extracted", "incident", incident.UUID, "count", existing)
		return
	}

	settings, err := database.GetLLMSettings()
	if err != nil {
		slog.Warn("memory extraction: failed to load LLM settings", "incident", incident.UUID, "err", err)
		return
	}
	if settings == nil || settings.APIKey == "" {
		slog.Debug("memory extraction skipped: no API key configured", "incident", incident.UUID)
		return
	}
	worker := BuildLLMSettingsForWorker(settings)
	if worker == nil {
		slog.Debug("memory extraction skipped: settings could not be built", "incident", incident.UUID)
		return
	}

	scope := scopeForIncident(incident)
	manifest := e.manifestForPrompt(scope)
	tail := buildTranscriptTail(incident)
	systemPrompt := memoryExtractorSystemPrompt
	userPrompt := buildExtractorUserPrompt(scope, manifest, tail)

	callCtx, cancel := context.WithTimeout(ctx, memoryExtractionTimeout)
	defer cancel()
	raw, err := e.caller.OneShotLLM(callCtx, worker, systemPrompt, userPrompt, 1500, 0.1)
	if err != nil {
		if errors.Is(err, ErrWorkerNotConnected) {
			slog.Debug("memory extraction: worker disconnected", "incident", incident.UUID)
			return
		}
		slog.Warn("memory extraction: LLM call failed", "incident", incident.UUID, "err", err)
		return
	}

	edits, err := parseExtractionResponse(raw)
	if err != nil {
		slog.Warn("memory extraction: invalid LLM response", "incident", incident.UUID, "err", err)
		return
	}

	applied := 0
	for _, edit := range edits {
		if err := e.applyEdit(edit, incident.UUID, scope); err != nil {
			slog.Warn("memory extraction: edit failed", "incident", incident.UUID, "op", edit.Op, "name", edit.Name, "err", err)
			continue
		}
		applied++
	}
	if applied > 0 {
		slog.Info("memory extraction completed", "incident", incident.UUID, "applied", applied, "total_edits", len(edits))
	}
}

// memoryEdit is the strict shape we accept from the LLM. Anything else is
// dropped — we don't attempt to coerce ambiguous responses.
type memoryEdit struct {
	Op           string `json:"op"`
	Scope        string `json:"scope"`
	Type         string `json:"type"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Body         string `json:"body"`
	IncidentUUID string `json:"incident_uuid,omitempty"`
}

type extractionResponse struct {
	Edits     []memoryEdit `json:"edits"`
	Reasoning string       `json:"reasoning,omitempty"`
}

// applyEdit clamps every edit to the caller-supplied scope (the value that
// scopeForIncident chose for this incident). The LLM's edit.Scope is treated
// as a hint and ignored — without this clamp, a misbehaving or
// prompt-injected response could write memories under any arbitrary scope,
// potentially destroying skill-scoped memory the operator has curated. The
// clamp also keeps extraction from quietly leaking into scopes whose
// SKILL.md regeneration this path doesn't trigger.
//
// Only the "upsert" op is supported here. parseExtractionResponse filters
// non-upsert ops out at the JSON-parse layer; this default-case error is
// defense-in-depth so any future change can't silently expose deletion.
func (e *MemoryExtractor) applyEdit(edit memoryEdit, incidentUUID string, scope string) error {
	if edit.Scope != "" && edit.Scope != scope {
		slog.Debug("memory extractor: clamping edit scope",
			"requested", edit.Scope, "enforced", scope, "name", edit.Name, "op", edit.Op)
	}
	switch edit.Op {
	case "upsert":
		m := &database.Memory{
			Scope:        scope,
			Type:         edit.Type,
			Name:         edit.Name,
			Description:  edit.Description,
			Body:         edit.Body,
			IncidentUUID: incidentUUID,
			CreatedBy:    MemoryCreatedByAgent,
		}
		_, err := e.manager.UpsertByName(m)
		return err
	default:
		// Reached only if parseExtractionResponse's filter is bypassed
		// (e.g. a future refactor adds a new op without thinking about
		// privilege). Fail loudly rather than silently.
		return fmt.Errorf("unsupported op %q (only \"upsert\" is allowed; memory removal is operator-only)", edit.Op)
	}
}

// scopeForIncident chooses where the extractor should write memories. Today
// we always pick global, since incidents aren't tagged with a single skill;
// future work may refine this by inspecting the resolved skills used.
func scopeForIncident(_ *database.Incident) string {
	return MemoryScopeGlobal
}

// manifestForPrompt loads the on-disk MEMORY.md for the given scope by
// listing the existing memories. We render a compact summary the LLM can
// use to deduplicate (e.g. "this fact is already in memory under name X").
func (e *MemoryExtractor) manifestForPrompt(scope string) string {
	mems, err := e.manager.ListMemoriesByScope(scope)
	if err != nil || len(mems) == 0 {
		return "(no existing memories)"
	}
	var b strings.Builder
	for _, m := range mems {
		fmt.Fprintf(&b, "- %s [%s]: %s\n", m.Name, m.Type, condenseLine(m.Description))
		// Hard cap so we don't blow the prompt budget on a large existing manifest.
		if b.Len() > 4*1024 {
			b.WriteString("- (additional memories truncated)\n")
			break
		}
	}
	return b.String()
}

// buildTranscriptTail produces the slice of incident output the LLM sees.
// Prefers the structured Response when present; otherwise falls back to
// FullLog. Either way we cap at memoryExtractionMaxTailBytes from the end.
func buildTranscriptTail(incident *database.Incident) string {
	source := incident.Response
	if strings.TrimSpace(source) == "" {
		source = incident.FullLog
	}
	if len(source) <= memoryExtractionMaxTailBytes {
		return source
	}
	return "[…earlier output truncated…]\n" + source[len(source)-memoryExtractionMaxTailBytes:]
}

// memoryExtractorSystemPrompt is the LLM-facing taxonomy. The four memory
// types are repeated from memory_types.go's consts so prompt drift gets
// caught at code-review time.
//
// Note: only the "upsert" op is supported. Deletes were intentionally
// removed — a single incident transcript is not authoritative enough to
// retract an existing memory, and a prompt-injected transcript that asked
// the LLM to delete operator-curated entries is a real risk. Memory
// removal is reserved for the operator-driven paths (UI, Slack feedback,
// API CRUD).
var memoryExtractorSystemPrompt = strings.Join([]string{
	"You distill an incident transcript into long-lived knowledge entries called \"memories\".",
	"",
	"You MUST return strict JSON of the form:",
	"  {\"edits\": [{\"op\": \"upsert\", \"scope\": string, \"type\": \"host\"|\"incident_pattern\"|\"tool_quirk\"|\"feedback\", \"name\": slug, \"description\": <≤500 chars>, \"body\": <≤8000 bytes>}], \"reasoning\": <≤500 chars>}",
	"",
	"Memory types:",
	"  - host: facts about a specific host or fleet (e.g., \"prod-db-01 has data dir on /mnt/data\")",
	"  - incident_pattern: recurring alert→cause→fix patterns",
	"  - tool_quirk: credential, naming, or routing oddities about a tool integration",
	"  - feedback: explicit operator corrections (rare from this prompt — usually written by the Slack flow)",
	"",
	"Rules:",
	"  - Only \"upsert\" is supported. To CORRECT a stale fact, upsert with the same name and the new content (overwrites in place).",
	"  - Deletion is NOT available to you. If a memory is no longer accurate, upsert with the corrected wording instead.",
	"  - Names are slug-safe: lowercase a-z, 0-9, hyphens. Reuse existing names from the manifest when updating a fact.",
	"  - Only emit edits for facts that are GENUINELY long-lived and reusable. Do NOT log play-by-play of this single incident.",
	"  - If nothing useful was learned, return {\"edits\": []}. Empty is the right answer most of the time.",
	"  - Do NOT include code fences. Output JSON only, nothing else.",
}, "\n")

func buildExtractorUserPrompt(scope, manifest, tail string) string {
	var b strings.Builder
	// The Go side overrides edit.Scope with this value before applying.
	// Make that contract explicit to the LLM so it doesn't waste tokens
	// trying to choose a scope and so an unexpected scope hint doesn't
	// silently get rewritten.
	fmt.Fprintf(&b, "Scope for ALL edits: %q (other scopes are ignored — every edit is written under %q)\n\n", scope, scope)
	b.WriteString("Existing memories in this scope (do NOT duplicate):\n")
	b.WriteString(manifest)
	b.WriteString("\n\nIncident transcript tail:\n")
	b.WriteString(tail)
	return b.String()
}

// parseExtractionResponse strips an optional fenced code block and decodes
// the JSON. Filters edits with unknown ops or types so a partially-malformed
// response still yields the valid edits inside it.
func parseExtractionResponse(raw string) ([]memoryEdit, error) {
	cleaned := strings.TrimSpace(raw)
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return nil, fmt.Errorf("empty response")
	}

	var resp extractionResponse
	if err := json.Unmarshal([]byte(cleaned), &resp); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	var out []memoryEdit
	for _, e := range resp.Edits {
		// Only "upsert" is accepted. "delete" is intentionally rejected
		// at parse time — a single incident transcript isn't authoritative
		// enough to retract an existing memory, and prompt-injected
		// transcripts could otherwise weaponize the LLM into removing
		// operator-curated entries that share a name. Memory removal is
		// reserved for operator-driven paths (UI, Slack feedback, API
		// CRUD). Stale facts are corrected via upsert (overwrite in place).
		if e.Op != "upsert" {
			continue
		}
		if !ValidMemoryType(e.Type) {
			continue
		}
		if strings.TrimSpace(e.Scope) == "" || strings.TrimSpace(e.Name) == "" {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// condenseLine flattens whitespace so a multiline description fits one
// manifest row in the prompt.
func condenseLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		s = s[:157] + "..."
	}
	return s
}
