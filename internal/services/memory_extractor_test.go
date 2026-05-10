package services

import (
	"context"
	"strings"
	"testing"

	"github.com/akmatori/akmatori/internal/database"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupMemoryExtractorTest wires an in-memory MemoryService and a seeded
// LLMSettings row (so the extractor doesn't bail at the API-key check).
func setupMemoryExtractorTest(t *testing.T) (*MemoryService, *fakeOneShotLLMCaller, *MemoryExtractor) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	if err := db.AutoMigrate(&database.Memory{}, &database.LLMSettings{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database.DB = db

	settings := &database.LLMSettings{
		Name:     "test",
		Provider: database.LLMProviderAnthropic,
		APIKey:   "test-key",
		Model:    "claude-sonnet-4-6",
		Active:   true,
		Enabled:  true,
	}
	if err := db.Create(settings).Error; err != nil {
		t.Fatalf("seed llm settings: %v", err)
	}

	memSvc := NewMemoryService(t.TempDir())
	caller := &fakeOneShotLLMCaller{}
	extractor := NewMemoryExtractor(caller, memSvc)
	return memSvc, caller, extractor
}

func TestParseExtractionResponse_ValidJSON(t *testing.T) {
	raw := `{
  "edits": [
    {"op": "upsert", "scope": "global", "type": "host", "name": "prod-db", "description": "data dir on /mnt/data", "body": "see notes"}
  ],
  "reasoning": "investigated postgres"
}`
	got, err := parseExtractionResponse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(got))
	}
	if got[0].Op != "upsert" || got[0].Name != "prod-db" {
		t.Errorf("got %+v", got[0])
	}
}

func TestParseExtractionResponse_StripsCodeFence(t *testing.T) {
	raw := "```json\n{\"edits\":[]}\n```"
	got, err := parseExtractionResponse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty edits, got %+v", got)
	}
}

func TestParseExtractionResponse_FiltersInvalidEntries(t *testing.T) {
	raw := `{"edits":[
    {"op":"unknown","scope":"global","type":"host","name":"x","description":"d","body":"b"},
    {"op":"upsert","scope":"global","type":"bogus","name":"y","description":"d","body":"b"},
    {"op":"upsert","scope":"","type":"host","name":"z","description":"d","body":"b"},
    {"op":"upsert","scope":"global","type":"host","name":"keep","description":"d","body":"b"}
]}`
	got, err := parseExtractionResponse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].Name != "keep" {
		t.Fatalf("expected only the valid edit, got %+v", got)
	}
}

func TestParseExtractionResponse_RejectsEmpty(t *testing.T) {
	if _, err := parseExtractionResponse("   "); err == nil {
		t.Fatal("expected error on empty response")
	}
}

func TestParseExtractionResponse_RejectsMalformedJSON(t *testing.T) {
	if _, err := parseExtractionResponse("not json"); err == nil {
		t.Fatal("expected error on malformed JSON")
	}
}

func TestExtract_AppliesUpsertEdits(t *testing.T) {
	memSvc, caller, extractor := setupMemoryExtractorTest(t)

	caller.respond = func(ctx context.Context) (string, error) {
		return `{"edits":[{"op":"upsert","scope":"global","type":"host","name":"prod-db","description":"data dir on /mnt/data","body":"see runbook 4"}]}`, nil
	}

	incident := &database.Incident{
		UUID:     "inc-1",
		Response: "Investigated postgres outage. Determined data dir is /mnt/data.",
		FullLog:  "long log",
	}
	extractor.Extract(context.Background(), incident)

	mems, err := memSvc.ListMemoriesByScope("global")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(mems) != 1 {
		t.Fatalf("expected 1 memory, got %d: %+v", len(mems), mems)
	}
	got := mems[0]
	if got.Name != "prod-db" || got.IncidentUUID != "inc-1" || got.CreatedBy != MemoryCreatedByAgent {
		t.Errorf("memory persisted with wrong fields: %+v", got)
	}
	if !strings.Contains(caller.lastUser, "Existing memories") {
		t.Errorf("user prompt should include existing memories block: %s", caller.lastUser)
	}
	if !strings.Contains(caller.lastUser, "data dir is /mnt/data") {
		t.Errorf("user prompt should include incident transcript tail: %s", caller.lastUser)
	}
	if !strings.Contains(caller.lastSystem, "incident_pattern") {
		t.Errorf("system prompt should advertise incident_pattern type: %s", caller.lastSystem)
	}
}

func TestExtract_OperatorFeedbackDoesNotBlockExtraction(t *testing.T) {
	// Regression: previously CountByIncidentUUID counted ALL memories tied
	// to the incident. If an operator left feedback before completion (UI
	// endpoint or Slack classifier), Extract would think it had already
	// run and skip the LLM call, losing the post-completion host/pattern
	// extraction entirely.
	memSvc, caller, extractor := setupMemoryExtractorTest(t)

	if _, err := memSvc.UpsertByName(&database.Memory{
		Scope:        MemoryScopeGlobal,
		Type:         MemoryTypeFeedback,
		Name:         "operator-feedback-on-incident",
		Description:  "operator left feedback before completion",
		Body:         "feedback text",
		IncidentUUID: "inc-mixed",
		CreatedBy:    MemoryCreatedByOperator,
	}); err != nil {
		t.Fatalf("seed operator feedback: %v", err)
	}

	caller.respond = func(ctx context.Context) (string, error) {
		return `{"edits":[{"op":"upsert","scope":"global","type":"host","name":"prod-db","description":"data dir on /mnt/data","body":"see notes"}]}`, nil
	}

	incident := &database.Incident{UUID: "inc-mixed", Response: "investigated postgres"}
	extractor.Extract(context.Background(), incident)

	if caller.callCount() != 1 {
		t.Fatalf("expected LLM to be called despite operator feedback, got %d calls", caller.callCount())
	}
	mems, _ := memSvc.ListMemoriesByScope(MemoryScopeGlobal)
	foundExtraction := false
	for _, m := range mems {
		if m.Name == "prod-db" && m.CreatedBy == MemoryCreatedByAgent {
			foundExtraction = true
		}
	}
	if !foundExtraction {
		t.Fatalf("agent extraction should have produced prod-db memory, got %+v", mems)
	}
}

func TestExtract_IsIdempotent(t *testing.T) {
	memSvc, caller, extractor := setupMemoryExtractorTest(t)

	// Pre-seed a memory tagged with this incident UUID. Extraction should bail
	// before calling the LLM.
	if _, err := memSvc.UpsertByName(&database.Memory{
		Scope:        MemoryScopeGlobal,
		Type:         MemoryTypeHost,
		Name:         "prior-fact",
		Description:  "from earlier extraction",
		Body:         "body",
		IncidentUUID: "inc-2",
		CreatedBy:    MemoryCreatedByAgent,
	}); err != nil {
		t.Fatalf("seed prior memory: %v", err)
	}

	caller.respond = func(ctx context.Context) (string, error) {
		t.Fatal("LLM should NOT be called when incident already has memories")
		return "", nil
	}

	incident := &database.Incident{UUID: "inc-2", Response: "anything"}
	extractor.Extract(context.Background(), incident)

	if caller.callCount() != 0 {
		t.Fatalf("expected 0 calls, got %d", caller.callCount())
	}
}

func TestExtract_InvalidJSONIsNoOp(t *testing.T) {
	memSvc, caller, extractor := setupMemoryExtractorTest(t)
	caller.respond = func(ctx context.Context) (string, error) {
		return "this is not json at all", nil
	}

	incident := &database.Incident{UUID: "inc-3", Response: "transcript"}
	extractor.Extract(context.Background(), incident)

	mems, _ := memSvc.ListMemoriesByScope(MemoryScopeGlobal)
	if len(mems) != 0 {
		t.Errorf("expected no memories from invalid response, got %+v", mems)
	}
}

func TestExtract_WorkerNotConnectedIsSilent(t *testing.T) {
	memSvc, caller, extractor := setupMemoryExtractorTest(t)
	caller.respond = func(ctx context.Context) (string, error) {
		return "", ErrWorkerNotConnected
	}

	incident := &database.Incident{UUID: "inc-4", Response: "transcript"}
	extractor.Extract(context.Background(), incident)

	mems, _ := memSvc.ListMemoriesByScope(MemoryScopeGlobal)
	if len(mems) != 0 {
		t.Errorf("expected no memories when worker is offline, got %+v", mems)
	}
}

func TestExtract_SkipsWhenNoAPIKey(t *testing.T) {
	memSvc, caller, extractor := setupMemoryExtractorTest(t)
	// Wipe the API key — extraction should bail before calling LLM.
	if err := database.DB.Model(&database.LLMSettings{}).Where("active = ?", true).Update("api_key", "").Error; err != nil {
		t.Fatalf("clear api key: %v", err)
	}

	incident := &database.Incident{UUID: "inc-5", Response: "transcript"}
	extractor.Extract(context.Background(), incident)

	if caller.callCount() != 0 {
		t.Errorf("expected 0 LLM calls when API key blank, got %d", caller.callCount())
	}
	if mems, _ := memSvc.ListMemoriesByScope(MemoryScopeGlobal); len(mems) != 0 {
		t.Errorf("expected no memories when API key blank, got %+v", mems)
	}
}

func TestExtract_ClampsUpsertScopeToComputedScope(t *testing.T) {
	// Regression: previously applyEdit trusted edit.Scope from the LLM JSON.
	// A misbehaving or prompt-injected response could write under any
	// scope it pleased — including skill scopes whose SKILL.md the
	// extractor doesn't regenerate. Clamp every edit to scopeForIncident's
	// choice (currently always "global").
	memSvc, caller, extractor := setupMemoryExtractorTest(t)

	caller.respond = func(ctx context.Context) (string, error) {
		return `{"edits":[
            {"op":"upsert","scope":"redis-skill","type":"host","name":"sneaky","description":"injected","body":"would land in redis-skill if scope were trusted"}
        ]}`, nil
	}

	incident := &database.Incident{UUID: "inc-clamp", Response: "transcript"}
	extractor.Extract(context.Background(), incident)

	// The memory must end up in global, NOT in redis-skill.
	globalMems, _ := memSvc.ListMemoriesByScope(MemoryScopeGlobal)
	if len(globalMems) != 1 || globalMems[0].Name != "sneaky" {
		t.Fatalf("expected one memory under global, got %+v", globalMems)
	}
	skillMems, _ := memSvc.ListMemoriesByScope("redis-skill")
	if len(skillMems) != 0 {
		t.Errorf("expected NO memories under redis-skill (LLM scope must be ignored), got %+v", skillMems)
	}
}

func TestExtract_DeleteEditsAreIgnored(t *testing.T) {
	// Regression: delete edits used to be applied. They're now stripped
	// at parse time — a single incident transcript isn't authoritative
	// enough to retract an existing memory, and a prompt-injected
	// transcript could otherwise weaponize the LLM into removing operator-
	// curated entries that share a name. The seeded memory MUST survive.
	memSvc, caller, extractor := setupMemoryExtractorTest(t)

	seed, err := memSvc.CreateMemory(&database.Memory{
		Scope:       MemoryScopeGlobal,
		Type:        MemoryTypeFeedback,
		Name:        "should-survive",
		Description: "important note",
		Body:        "do not let extraction touch this",
		CreatedBy:   MemoryCreatedByOperator,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	caller.respond = func(ctx context.Context) (string, error) {
		// Mix: one "delete" (must be ignored) and one valid "upsert"
		// (must still apply, so we know parse-time filtering doesn't
		// over-match).
		return `{"edits":[
            {"op":"delete","scope":"global","name":"should-survive"},
            {"op":"upsert","scope":"global","type":"host","name":"new-fact","description":"learned a thing","body":"body"}
        ]}`, nil
	}

	incident := &database.Incident{UUID: "inc-no-deletes", Response: "transcript"}
	extractor.Extract(context.Background(), incident)

	if _, err := memSvc.GetMemory(seed.ID); err != nil {
		t.Fatalf("seeded memory must survive — delete edits are not supported, got err: %v", err)
	}
	all, _ := memSvc.ListMemoriesByScope(MemoryScopeGlobal)
	foundUpsert := false
	for _, m := range all {
		if m.Name == "new-fact" {
			foundUpsert = true
		}
	}
	if !foundUpsert {
		t.Errorf("upsert in the same edits list should still have applied, got %+v", all)
	}
}

func TestParseExtractionResponse_FiltersDeleteOps(t *testing.T) {
	raw := `{"edits":[
        {"op":"delete","scope":"global","type":"host","name":"x","description":"d","body":"b"},
        {"op":"upsert","scope":"global","type":"host","name":"y","description":"d","body":"b"}
    ]}`
	got, err := parseExtractionResponse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 edit (upsert kept, delete filtered), got %d: %+v", len(got), got)
	}
	if got[0].Op != "upsert" || got[0].Name != "y" {
		t.Errorf("got %+v, want upsert of y", got[0])
	}
}

func TestApplyEdit_DefaultBranchRejectsUnsupportedOp(t *testing.T) {
	// Defense-in-depth: even if a future change bypasses parse-time
	// filtering, applyEdit must reject anything that isn't "upsert".
	memSvc, _, extractor := setupMemoryExtractorTest(t)
	_ = memSvc // unused, kept for setup symmetry

	err := extractor.applyEdit(memoryEdit{Op: "delete", Scope: "global", Name: "x"}, "inc", "global")
	if err == nil {
		t.Fatal("expected error from default branch")
	}
	if !strings.Contains(err.Error(), "unsupported op") {
		t.Errorf("expected 'unsupported op' error, got %v", err)
	}
}

func TestBuildExtractorUserPrompt_AdvertisesScopeContract(t *testing.T) {
	// The clamp is enforced in Go, but the user prompt should also tell
	// the LLM about it so the model doesn't waste tokens guessing scopes
	// that get rewritten anyway.
	got := buildExtractorUserPrompt("global", "(no existing memories)", "transcript")
	for _, want := range []string{
		`"global"`,
		"other scopes are ignored",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("user prompt missing %q\nfull:\n%s", want, got)
		}
	}
}

// Delete-related extractor tests have been removed alongside delete
// support. See TestExtract_DeleteEditsAreIgnored,
// TestParseExtractionResponse_FiltersDeleteOps, and
// TestApplyEdit_DefaultBranchRejectsUnsupportedOp for the new contract.

func TestExtract_NilDepsIsNoOp(t *testing.T) {
	// Constructed without the manager dep — should not panic.
	extractor := NewMemoryExtractor(&fakeOneShotLLMCaller{}, nil)
	extractor.Extract(context.Background(), &database.Incident{UUID: "x"})

	// Constructed without the caller — should not panic.
	memSvc := setupMemoryServiceTest(t)
	extractor2 := NewMemoryExtractor(nil, memSvc)
	extractor2.Extract(context.Background(), &database.Incident{UUID: "y"})
}

func TestBuildTranscriptTail_PrefersResponse(t *testing.T) {
	got := buildTranscriptTail(&database.Incident{Response: "short response", FullLog: "long log"})
	if got != "short response" {
		t.Errorf("got %q, want %q", got, "short response")
	}
}

func TestBuildTranscriptTail_FallsBackToFullLog(t *testing.T) {
	got := buildTranscriptTail(&database.Incident{Response: "  ", FullLog: "fallback log"})
	if got != "fallback log" {
		t.Errorf("got %q, want %q", got, "fallback log")
	}
}

func TestBuildTranscriptTail_TruncatesLargeInput(t *testing.T) {
	big := strings.Repeat("x", memoryExtractionMaxTailBytes+5000)
	got := buildTranscriptTail(&database.Incident{Response: big})
	if !strings.Contains(got, "earlier output truncated") {
		t.Errorf("expected truncation marker in output, got prefix %q", got[:200])
	}
	if len(got) > memoryExtractionMaxTailBytes+200 {
		t.Errorf("truncated tail too long: %d", len(got))
	}
}

func TestCondenseLine(t *testing.T) {
	cases := map[string]string{
		"already short":             "already short",
		"line one\nline two":        "line one line two",
		"  excess   whitespace  ":   "excess whitespace",
		strings.Repeat("a", 200):    strings.Repeat("a", 157) + "...",
	}
	for in, want := range cases {
		if got := condenseLine(in); got != want {
			t.Errorf("condenseLine(%q) = %q, want %q", in, got, want)
		}
	}
}
