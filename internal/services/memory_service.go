package services

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/akmatori/akmatori/internal/database"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MemoryService manages cross-incident memory CRUD and on-disk synchronization.
// Mirrors RunbookService: PostgreSQL is source of truth, files mirror to
// /akmatori/memory/<scope>/<id>-<slug>.md plus a per-scope MEMORY.md manifest,
// QMD re-indexes after every sync.
type MemoryService struct {
	db         *gorm.DB
	memoryDir  string
	syncMu     sync.Mutex
	qmdURL     string
	httpClient *http.Client
}

// NewMemoryService creates a new memory service rooted at <dataDir>/memory.
func NewMemoryService(dataDir string) *MemoryService {
	dir := filepath.Join(dataDir, "memory")
	if err := os.MkdirAll(dir, 0755); err != nil {
		slog.Warn("failed to create memory directory", "dir", dir, "err", err)
	}
	return &MemoryService{
		db:        database.GetDB(),
		memoryDir: dir,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// SetQMDURL configures the QMD service URL used to trigger re-indexes.
func (s *MemoryService) SetQMDURL(url string) {
	s.qmdURL = strings.TrimRight(url, "/")
}

// MemoryDir returns the root memory directory (used by tests and the gateway tool).
func (s *MemoryService) MemoryDir() string {
	return s.memoryDir
}

// CreateMemory inserts a new memory and syncs files. Caller-supplied
// IncidentUUID and CreatedBy are passed through verbatim.
func (s *MemoryService) CreateMemory(m *database.Memory) (*database.Memory, error) {
	if err := s.validate(m); err != nil {
		return nil, err
	}
	m.Scope = strings.TrimSpace(m.Scope)
	m.Name = strings.TrimSpace(m.Name)
	if err := s.db.Create(m).Error; err != nil {
		return nil, fmt.Errorf("failed to create memory: %w", err)
	}
	if err := s.SyncMemoryFiles(); err != nil {
		return nil, fmt.Errorf("memory created but file sync failed: %w", err)
	}
	return m, nil
}

// UpdateMemory mutates an existing memory by ID. All fields use the same
// "empty = leave unchanged" convention so that PUT /api/memories/{id} can
// supply only the fields the caller actually wants to change. Without this
// guard a partial PUT (e.g. just {"type":"feedback"}) would clobber the
// existing Description with "" and fail validation.
func (s *MemoryService) UpdateMemory(id uint, m *database.Memory) (*database.Memory, error) {
	var existing database.Memory
	if err := s.db.First(&existing, id).Error; err != nil {
		return nil, errMemoryNotFound
	}
	merged := existing
	if m.Scope != "" {
		merged.Scope = strings.TrimSpace(m.Scope)
	}
	if m.Type != "" {
		merged.Type = m.Type
	}
	if m.Name != "" {
		merged.Name = strings.TrimSpace(m.Name)
	}
	if m.Description != "" {
		merged.Description = m.Description
	}
	if m.Body != "" {
		merged.Body = m.Body
	}
	if m.IncidentUUID != "" {
		merged.IncidentUUID = m.IncidentUUID
	}
	if m.CreatedBy != "" {
		merged.CreatedBy = m.CreatedBy
	}
	if err := s.validate(&merged); err != nil {
		return nil, err
	}
	if err := s.db.Save(&merged).Error; err != nil {
		return nil, fmt.Errorf("failed to update memory: %w", err)
	}
	if err := s.SyncMemoryFiles(); err != nil {
		return nil, fmt.Errorf("memory updated but file sync failed: %w", err)
	}
	return &merged, nil
}

// UpsertByName inserts or updates a memory keyed by (scope, name).
// Used by the extractor and Slack feedback flows for idempotent writes —
// e.g. a Slack retry firing a second classify on the same message, or two
// feedback submissions producing the same generated name.
//
// The implementation uses a database-level upsert (ON CONFLICT (scope, name)
// DO UPDATE) so concurrent callers cannot collide on the unique index. The
// previous SELECT-then-INSERT-or-UPDATE pattern was racy: two callers could
// both miss the lookup and then one Create would fail with a unique-constraint
// error, dropping the later update on a path that's contractually idempotent.
//
// On conflict, type/description/body/incident_uuid/created_by are all
// overwritten with the new request — every caller of UpsertByName supplies
// a complete record, so there's no "merge selectively" semantic. The
// existing row's ID and created_at are preserved.
//
// Both PostgreSQL and SQLite (≥3.24) support the ON CONFLICT clause used here.
func (s *MemoryService) UpsertByName(m *database.Memory) (*database.Memory, error) {
	if err := s.validate(m); err != nil {
		return nil, err
	}
	m.Scope = strings.TrimSpace(m.Scope)
	m.Name = strings.TrimSpace(m.Name)

	if err := s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "scope"}, {Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"type", "description", "body", "incident_uuid", "created_by", "updated_at",
		}),
	}).Create(m).Error; err != nil {
		return nil, fmt.Errorf("failed to upsert memory: %w", err)
	}

	// Re-read so callers receive the canonical row. After ON CONFLICT
	// DO UPDATE, m.ID is reliably set on insert but the post-update
	// state varies by driver — a fresh SELECT keeps the contract uniform.
	var saved database.Memory
	if err := s.db.Where("scope = ? AND name = ?", m.Scope, m.Name).First(&saved).Error; err != nil {
		return nil, fmt.Errorf("failed to read upserted memory: %w", err)
	}
	if err := s.SyncMemoryFiles(); err != nil {
		return nil, fmt.Errorf("memory upserted but file sync failed: %w", err)
	}
	return &saved, nil
}

// DeleteMemory removes a memory by ID and re-syncs files.
func (s *MemoryService) DeleteMemory(id uint) error {
	result := s.db.Delete(&database.Memory{}, id)
	if result.Error != nil {
		return fmt.Errorf("failed to delete memory: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return errMemoryNotFound
	}
	if err := s.SyncMemoryFiles(); err != nil {
		return fmt.Errorf("memory deleted but file sync failed: %w", err)
	}
	return nil
}

// GetMemory retrieves a single memory by ID.
func (s *MemoryService) GetMemory(id uint) (*database.Memory, error) {
	var m database.Memory
	if err := s.db.First(&m, id).Error; err != nil {
		return nil, errMemoryNotFound
	}
	return &m, nil
}

// ListMemoriesByScope returns all memories in a scope ordered by created_at desc.
// If scope is empty, returns all memories across scopes.
func (s *MemoryService) ListMemoriesByScope(scope string) ([]database.Memory, error) {
	var memories []database.Memory
	q := s.db.Order("created_at desc")
	if scope != "" {
		q = q.Where("scope = ?", scope)
	}
	if err := q.Find(&memories).Error; err != nil {
		return nil, fmt.Errorf("failed to list memories: %w", err)
	}
	return memories, nil
}

// ListMemories applies optional scope and type filters. Empty filters mean
// "no filter on that field". Used by the REST API filter endpoint.
func (s *MemoryService) ListMemories(scope, memType string) ([]database.Memory, error) {
	var memories []database.Memory
	q := s.db.Order("created_at desc")
	if scope != "" {
		q = q.Where("scope = ?", scope)
	}
	if memType != "" {
		q = q.Where("type = ?", memType)
	}
	if err := q.Find(&memories).Error; err != nil {
		return nil, fmt.Errorf("failed to list memories: %w", err)
	}
	return memories, nil
}

// ListAllScopes returns the set of distinct scopes present in the table.
func (s *MemoryService) ListAllScopes() ([]string, error) {
	var scopes []string
	if err := s.db.Model(&database.Memory{}).Distinct("scope").Order("scope asc").Pluck("scope", &scopes).Error; err != nil {
		return nil, fmt.Errorf("failed to list scopes: %w", err)
	}
	return scopes, nil
}

// CountByIncidentUUID returns how many memories already record this incident
// as origin. When createdBy is non-empty, the count is restricted to memories
// authored by that role (e.g. "agent" or "operator").
//
// The extractor passes MemoryCreatedByAgent so that operator feedback written
// against the same incident — either via the UI feedback endpoint or the
// Slack feedback classifier — does NOT mark extraction as "already done"
// and short-circuit the post-completion run.
func (s *MemoryService) CountByIncidentUUID(incidentUUID string, createdBy string) (int64, error) {
	var n int64
	q := s.db.Model(&database.Memory{}).Where("incident_uuid = ?", incidentUUID)
	if createdBy != "" {
		q = q.Where("created_by = ?", createdBy)
	}
	if err := q.Count(&n).Error; err != nil {
		return 0, fmt.Errorf("failed to count memories by incident: %w", err)
	}
	return n, nil
}

// TruncateMemoryBody trims s to at most MemoryBodyMaxBytes bytes without
// splitting a UTF-8 character mid-byte. No ellipsis is added — body content
// is large and the size cap is approximate by design.
//
// PostgreSQL text columns require valid UTF-8, so naive byte slicing on
// multibyte input would cause INSERT to fail with "invalid byte sequence".
// Used by both feedback ingest paths (HTTP and Slack).
func TruncateMemoryBody(s string) string {
	if len(s) <= MemoryBodyMaxBytes {
		return s
	}
	cut := MemoryBodyMaxBytes
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut]
}

// errMemoryNotFound is the canonical "not found" error returned to handlers.
// Use errors.Is(err, errMemoryNotFound) to detect.
var errMemoryNotFound = errors.New("memory not found")

// IsMemoryNotFoundErr reports whether err signals a missing memory.
func IsMemoryNotFoundErr(err error) bool {
	return errors.Is(err, errMemoryNotFound)
}

// validate enforces field caps and type membership.
func (s *MemoryService) validate(m *database.Memory) error {
	scope := strings.TrimSpace(m.Scope)
	if scope == "" {
		return fmt.Errorf("scope cannot be empty")
	}
	// validMemoryName enforces both the slug pattern AND the
	// MemoryNameMaxLen cap, which keeps the scope under the filesystem
	// NAME_MAX limit. Same helper for both fields by design.
	if !validMemoryName(scope) {
		return fmt.Errorf("scope must be slug-safe (lowercase a-z, 0-9, hyphen) and ≤%d chars", MemoryNameMaxLen)
	}
	if !ValidMemoryType(m.Type) {
		return fmt.Errorf("invalid memory type %q (allowed: %s)", m.Type, strings.Join(AllMemoryTypes(), ", "))
	}
	name := strings.TrimSpace(m.Name)
	if name == "" {
		return fmt.Errorf("name cannot be empty")
	}
	if !validMemoryName(name) {
		return fmt.Errorf("name must be slug-safe (lowercase a-z, 0-9, hyphen) and ≤%d chars", MemoryNameMaxLen)
	}
	if len(m.Description) > MemoryDescriptionMaxLen {
		return fmt.Errorf("description exceeds %d chars", MemoryDescriptionMaxLen)
	}
	if strings.TrimSpace(m.Description) == "" {
		return fmt.Errorf("description cannot be empty")
	}
	if len(m.Body) > MemoryBodyMaxBytes {
		return fmt.Errorf("body exceeds %d bytes", MemoryBodyMaxBytes)
	}
	if m.CreatedBy != "" && m.CreatedBy != MemoryCreatedByAgent && m.CreatedBy != MemoryCreatedByOperator {
		return fmt.Errorf("created_by must be %q, %q, or empty", MemoryCreatedByAgent, MemoryCreatedByOperator)
	}
	return nil
}

var memoryNameRegex = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// validMemoryName enforces a slug-safe name. We use the same convention for
// scope dirs and filenames so the on-disk layout has no surprises.
func validMemoryName(name string) bool {
	if name == "" || len(name) > MemoryNameMaxLen {
		return false
	}
	return memoryNameRegex.MatchString(name)
}

// SlugifyMemoryName converts a free-form description/title into a slug-safe
// memory name. Returns "memory" if the input is empty after slugification.
// Exposed so the Slack feedback flow and extractor can derive names from
// LLM-generated summaries.
func SlugifyMemoryName(s string) string {
	out := slugify(s)
	// slugify already lowercases, hyphenates, and trims to 100 chars; "runbook"
	// fallback is its choice. We override to "memory" so log lines disambiguate.
	if out == "" || out == "runbook" {
		return "memory"
	}
	return out
}

// triggerQMDReindex POSTs to QMD's /update endpoint. Mirrors RunbookService.
func (s *MemoryService) triggerQMDReindex() {
	if s.qmdURL == "" {
		return
	}
	url := s.qmdURL + "/update"
	resp, err := s.httpClient.Post(url, "application/json", nil)
	if err != nil {
		slog.Warn("failed to trigger QMD re-index for memory", "url", url, "error", err)
		return
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		slog.Warn("QMD re-index returned non-200 status for memory", "url", url, "status", resp.StatusCode)
		return
	}
	slog.Info("QMD re-index triggered for memory")
}

// Manifest caps. Slack/QMD are happiest when manifests stay small; a hard byte
// cap also protects the prompt from runaway growth as memory accumulates.
const (
	manifestMaxLines = 200
	manifestMaxBytes = 25 * 1024
	manifestFile     = "MEMORY.md"
)

// SyncMemoryFiles writes one directory per scope under memoryDir:
//
//	<memoryDir>/<scope>/MEMORY.md           — manifest (≤ manifestMax* caps)
//	<memoryDir>/<scope>/<id>-<name>.md      — one file per memory, with
//	                                          YAML frontmatter and body.
//
// Stale scope directories and files are removed. After a successful write,
// QMD is asked to re-index in a non-blocking goroutine.
func (s *MemoryService) SyncMemoryFiles() error {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	if err := os.MkdirAll(s.memoryDir, 0755); err != nil {
		return fmt.Errorf("failed to create memory directory: %w", err)
	}

	var memories []database.Memory
	if err := s.db.Order("created_at desc").Find(&memories).Error; err != nil {
		return fmt.Errorf("failed to query memories: %w", err)
	}

	byScope := make(map[string][]database.Memory)
	for _, m := range memories {
		byScope[m.Scope] = append(byScope[m.Scope], m)
	}

	expectedScopes := make(map[string]bool, len(byScope))
	for scope, entries := range byScope {
		expectedScopes[scope] = true
		scopeDir := filepath.Join(s.memoryDir, scope)
		if err := os.MkdirAll(scopeDir, 0755); err != nil {
			return fmt.Errorf("failed to create scope dir %s: %w", scope, err)
		}

		expectedFiles := map[string]bool{manifestFile: true}
		for _, m := range entries {
			fileName := fmt.Sprintf("%d-%s.md", m.ID, m.Name)
			expectedFiles[fileName] = true
			body := renderMemoryFile(m)
			path := filepath.Join(scopeDir, fileName)
			if err := os.WriteFile(path, []byte(body), 0644); err != nil {
				return fmt.Errorf("failed to write memory file %s: %w", path, err)
			}
		}

		manifest := renderManifest(scope, entries)
		manifestPath := filepath.Join(scopeDir, manifestFile)
		if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
			return fmt.Errorf("failed to write manifest %s: %w", manifestPath, err)
		}

		if err := removeStaleFiles(scopeDir, expectedFiles); err != nil {
			slog.Warn("failed to clean stale memory files", "scope", scope, "err", err)
		}
	}

	if err := removeStaleScopes(s.memoryDir, expectedScopes); err != nil {
		slog.Warn("failed to clean stale memory scope dirs", "err", err)
	}

	go s.triggerQMDReindex()
	return nil
}

// memoryFrontmatter is the on-disk YAML schema for memory files. yaml.Marshal
// handles quoting and escaping for values containing YAML-significant chars
// (colons, quotes, brackets) — interpolating m.Description raw into the
// frontmatter would let a description like "prod-db: data dir moved" turn
// the file into invalid YAML and break QMD's frontmatter consumers.
type memoryFrontmatter struct {
	Name         string `yaml:"name"`
	Description  string `yaml:"description"`
	Type         string `yaml:"type"`
	Scope        string `yaml:"scope"`
	IncidentUUID string `yaml:"incident_uuid,omitempty"`
	CreatedBy    string `yaml:"created_by,omitempty"`
}

// renderMemoryFile produces the full markdown body for a single memory file.
// YAML frontmatter contains the metadata QMD's lex/vector indexers will surface.
func renderMemoryFile(m database.Memory) string {
	fm := memoryFrontmatter{
		Name: m.Name,
		// Flatten newlines so the frontmatter stays single-line per field —
		// QMD's lex indexer reads the rendered file and a single-line
		// description keeps each entry on one indexable row.
		Description:  strings.ReplaceAll(m.Description, "\n", " "),
		Type:         m.Type,
		Scope:        m.Scope,
		IncidentUUID: m.IncidentUUID,
		CreatedBy:    m.CreatedBy,
	}
	yamlBytes, err := yaml.Marshal(fm)
	if err != nil {
		// Defensive fallback — yaml.Marshal of a flat struct of strings
		// shouldn't fail in practice. Log so we notice if it ever does.
		slog.Warn("failed to marshal memory frontmatter, falling back to minimal", "name", m.Name, "err", err)
		yamlBytes = []byte(fmt.Sprintf("name: %q\ntype: %q\nscope: %q\n", m.Name, m.Type, m.Scope))
	}

	var b strings.Builder
	b.WriteString("---\n")
	b.Write(yamlBytes)
	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "# %s\n\n", m.Name)
	fmt.Fprintf(&b, "%s\n\n", m.Description)
	if strings.TrimSpace(m.Body) != "" {
		b.WriteString(m.Body)
		if !strings.HasSuffix(m.Body, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// renderManifest builds the per-scope MEMORY.md table. Hard-capped at
// manifestMaxLines / manifestMaxBytes — the manifest is injected into prompts,
// so it MUST stay small even as memory accumulates. When an entry would push
// the manifest past either cap, we stop and emit a truncation marker telling
// the agent to use memory.search for the rest.
func renderManifest(scope string, entries []database.Memory) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Memory Manifest — scope: %s\n\n", scope)
	if len(entries) == 0 {
		b.WriteString("_No memories yet._\n")
		return b.String()
	}
	b.WriteString("| name | type | description |\n")
	b.WriteString("| --- | --- | --- |\n")

	header := b.Len()
	emitted := 0
	truncated := 0

	for _, m := range entries {
		// Inline pipes break Markdown table parsing; replace with bullets.
		desc := strings.ReplaceAll(strings.ReplaceAll(m.Description, "\n", " "), "|", "·")
		row := fmt.Sprintf("| `%s` | %s | %s |\n", m.Name, m.Type, desc)
		// linesEmitted = header table (2 lines: header + separator) + rows so far.
		linesEmitted := 2 + emitted
		if linesEmitted+1 > manifestMaxLines || b.Len()+len(row) > manifestMaxBytes {
			truncated = len(entries) - emitted
			break
		}
		b.WriteString(row)
		emitted++
	}

	if truncated > 0 {
		fmt.Fprintf(&b, "\n_… %d more memories truncated. Use `gateway_call(\"memory.search\", {…})` to find them._\n", truncated)
	}

	// If the table never emitted a row (e.g., header alone exceeded the cap),
	// keep at least the header for diagnostic clarity.
	if b.Len() == header {
		b.WriteString("_Manifest exceeded byte cap; use memory.search to access entries._\n")
	}

	return b.String()
}

// removeStaleFiles deletes regular files in dir whose names are not in keep.
// Subdirectories are left alone.
func removeStaleFiles(dir string, keep map[string]bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !keep[e.Name()] {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				slog.Warn("failed to remove stale memory file", "file", e.Name(), "err", err)
			}
		}
	}
	return nil
}

// removeStaleScopes deletes scope directories no longer present in keep.
// Files inside such directories are removed first to keep the operation
// best-effort even when ordering matters (e.g. on Windows-style locks).
func removeStaleScopes(root string, keep map[string]bool) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if keep[e.Name()] {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if err := os.RemoveAll(dir); err != nil {
			slog.Warn("failed to remove stale scope dir", "dir", dir, "err", err)
		}
	}
	return nil
}
