package handlers

import (
	"context"
	"strings"
	"testing"

	"github.com/akmatori/akmatori/internal/database"
	"github.com/akmatori/akmatori/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestSlugFromUUID(t *testing.T) {
	cases := map[string]string{
		"abc-123-def-456":                   "abc123de",
		"":                                  "",
		"!!!@@@##":                          "",
		"550e8400-e29b-41d4-a716-446655440000": "550e8400",
	}
	for in, want := range cases {
		if got := slugFromUUID(in); got != want {
			t.Errorf("slugFromUUID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncateBytesUTF8Safe_FitsBudget(t *testing.T) {
	// ASCII just-too-long case.
	in := strings.Repeat("x", 510)
	got := truncateBytesUTF8Safe(in, 500)
	if len(got) > 500 {
		t.Errorf("got %d bytes, want ≤ 500", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected ellipsis suffix, got %q", got[len(got)-5:])
	}
}

func TestTruncateBytesUTF8Safe_NoTruncationWhenShort(t *testing.T) {
	in := "short string"
	if got := truncateBytesUTF8Safe(in, 500); got != in {
		t.Errorf("got %q, want %q", got, in)
	}
}

func TestTruncateBytesUTF8Safe_PreservesRuneBoundaries(t *testing.T) {
	// 3-byte UTF-8 chars; ensure we don't slice mid-character.
	in := strings.Repeat("日本", 200) // 6 bytes per pair
	got := truncateBytesUTF8Safe(in, 100)
	if len(got) > 100 {
		t.Errorf("got %d bytes, want ≤ 100", len(got))
	}
	// The result should still be valid UTF-8 (Go strings carry bytes, but the
	// suffix "…" is 3 bytes and the prefix should land on a rune boundary).
	if got != "" && !strings.HasSuffix(got, "…") {
		t.Errorf("expected ellipsis suffix, got tail %q", got[len(got)-6:])
	}
}

func TestBuildFeedbackMemory_DerivedFields(t *testing.T) {
	verdict := services.FeedbackVerdict{
		IsFeedback: true,
		Summary:    "data dir is /mnt/data",
		Confidence: 0.95,
	}
	mem := buildFeedbackMemory("Postgres data dir is on /mnt/data, not /var/lib/postgresql", verdict, "abc-123")

	if mem.Scope != services.MemoryScopeGlobal {
		t.Errorf("scope = %q, want global", mem.Scope)
	}
	if mem.Type != services.MemoryTypeFeedback {
		t.Errorf("type = %q, want feedback", mem.Type)
	}
	if mem.IncidentUUID != "abc-123" {
		t.Errorf("incident UUID = %q", mem.IncidentUUID)
	}
	if mem.CreatedBy != services.MemoryCreatedByOperator {
		t.Errorf("created_by = %q, want operator", mem.CreatedBy)
	}
	if !strings.Contains(mem.Body, "/mnt/data") {
		t.Errorf("body should preserve original message, got %q", mem.Body)
	}
	if !strings.HasSuffix(mem.Name, "-abc123") {
		t.Errorf("name should end with UUID prefix, got %q", mem.Name)
	}
	if !strings.HasPrefix(mem.Description, "data dir") {
		t.Errorf("description should start with summary, got %q", mem.Description)
	}
}

func TestBuildFeedbackMemory_FallsBackToTextWhenSummaryEmpty(t *testing.T) {
	verdict := services.FeedbackVerdict{IsFeedback: true, Summary: "  ", Confidence: 0.9}
	mem := buildFeedbackMemory("the data dir is /mnt/data", verdict, "u")
	if !strings.Contains(mem.Description, "data dir") {
		t.Errorf("expected description to fall back to message text, got %q", mem.Description)
	}
}

func TestBuildFeedbackMemory_LongMultibyteBodyStaysValidUTF8(t *testing.T) {
	// Regression: previously the body was sliced by raw byte count, which
	// could split a multi-byte UTF-8 rune. Postgres would then reject the
	// INSERT. Same input shape as the HTTP feedback regression test.
	long := strings.Repeat("日", (services.MemoryBodyMaxBytes/3)+10)
	verdict := services.FeedbackVerdict{IsFeedback: true, Summary: "long jp note", Confidence: 0.9}

	mem := buildFeedbackMemory(long, verdict, "abc-123")

	if len(mem.Body) > services.MemoryBodyMaxBytes {
		t.Errorf("body len = %d, want ≤ %d", len(mem.Body), services.MemoryBodyMaxBytes)
	}
	if len(mem.Body)%3 != 0 {
		t.Errorf("body len %d not on a 3-byte UTF-8 boundary — body was sliced mid-rune", len(mem.Body))
	}
}

func TestLookupIncidentByThread_DMOriginated(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	if err := db.AutoMigrate(&database.Incident{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database.DB = db

	if err := db.Create(&database.Incident{
		UUID:     "uuid-dm",
		Source:   "slack",
		SourceID: "T1",
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	incident, err := lookupIncidentByThread("T1")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if incident.UUID != "uuid-dm" {
		t.Errorf("got UUID %q", incident.UUID)
	}
}

func TestLookupIncidentByThread_AlertChannel(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	if err := db.AutoMigrate(&database.Incident{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database.DB = db

	if err := db.Create(&database.Incident{
		UUID:           "uuid-alert",
		Source:         "zabbix",
		SourceID:       "alert-99",
		SlackMessageTS: "T2",
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	incident, err := lookupIncidentByThread("T2")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if incident.UUID != "uuid-alert" {
		t.Errorf("got UUID %q", incident.UUID)
	}
}

func TestLookupIncidentByThread_NoMatch(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	if err := db.AutoMigrate(&database.Incident{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database.DB = db

	if _, err := lookupIncidentByThread("does-not-exist"); err == nil {
		t.Fatal("expected error for unknown thread")
	}
}

// TestMaybeCaptureSlackFeedback_PersistsConfidentFeedback exercises the full
// integration with mock dependencies. We don't drive a real slack.Client —
// the handler tolerates a nil client (skipping the reaction/post calls).
func TestMaybeCaptureSlackFeedback_PersistsConfidentFeedback(t *testing.T) {
	mock := newMockMemoryService()
	caller := &fakeOneShotLLMCallerH{response: `{"is_feedback": true, "summary": "data dir is /mnt/data", "confidence": 0.92}`}

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	if err := db.AutoMigrate(&database.Incident{}, &database.LLMSettings{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database.DB = db
	if err := db.Create(&database.LLMSettings{
		Name: "t", Provider: database.LLMProviderAnthropic, APIKey: "k",
		Model: "claude-sonnet-4-6", Active: true, Enabled: true,
	}).Error; err != nil {
		t.Fatalf("seed llm: %v", err)
	}
	if err := db.Create(&database.Incident{
		UUID: "inc-99", Source: "slack", SourceID: "TX", Title: "Postgres outage",
		Response: "agent investigated postgres",
	}).Error; err != nil {
		t.Fatalf("seed incident: %v", err)
	}

	classifier := services.NewFeedbackClassifier(caller)
	h := &SlackHandler{
		memoryManager:      mock,
		feedbackClassifier: classifier,
		botUserID:          "BOT",
	}

	h.maybeCaptureSlackFeedback("C", "TX", "M-1", "the postgres data dir is /mnt/data not /var/lib/postgresql", "U")

	if mock.lastUpserted == nil {
		t.Fatalf("expected memory upserted")
	}
	if mock.lastUpserted.IncidentUUID != "inc-99" {
		t.Errorf("incident UUID not propagated: %+v", mock.lastUpserted)
	}
	if !strings.Contains(mock.lastUpserted.Description, "data dir") {
		t.Errorf("description should reflect summary, got %q", mock.lastUpserted.Description)
	}
}

// TestMaybeCaptureSlackFeedback_NotConfidentDoesNothing verifies the silent-
// on-negatives behavior — chatty replies must NEVER write a memory.
func TestMaybeCaptureSlackFeedback_NotConfidentDoesNothing(t *testing.T) {
	mock := newMockMemoryService()
	caller := &fakeOneShotLLMCallerH{response: `{"is_feedback": false, "summary": "casual chat", "confidence": 0.95}`}

	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	_ = db.AutoMigrate(&database.Incident{}, &database.LLMSettings{})
	database.DB = db
	_ = db.Create(&database.LLMSettings{
		Name: "t", Provider: database.LLMProviderAnthropic, APIKey: "k",
		Model: "x", Active: true, Enabled: true,
	}).Error
	_ = db.Create(&database.Incident{UUID: "i", Source: "slack", SourceID: "T"}).Error

	classifier := services.NewFeedbackClassifier(caller)
	h := &SlackHandler{
		memoryManager:      mock,
		feedbackClassifier: classifier,
	}

	h.maybeCaptureSlackFeedback("C", "T", "M", "any update?", "U")

	if mock.lastUpserted != nil {
		t.Errorf("expected NO memory written for non-feedback, got %+v", mock.lastUpserted)
	}
}

func TestMaybeCaptureSlackFeedback_MissingDepsIsNoOp(t *testing.T) {
	// No classifier wired — should not panic, should not call DB.
	h := &SlackHandler{}
	h.maybeCaptureSlackFeedback("C", "T", "M", "anything", "U")

	// Memory manager nil should also short-circuit.
	mock := newMockMemoryService()
	h2 := &SlackHandler{memoryManager: mock} // classifier nil
	h2.maybeCaptureSlackFeedback("C", "T", "M", "anything", "U")
	if mock.lastUpserted != nil {
		t.Error("nil classifier should skip memory write")
	}
}

func TestMaybeCaptureSlackFeedback_BotMessageSkipped(t *testing.T) {
	mock := newMockMemoryService()
	caller := &fakeOneShotLLMCallerH{response: `{"is_feedback": true, "summary": "x", "confidence": 0.99}`}
	classifier := services.NewFeedbackClassifier(caller)
	h := &SlackHandler{
		memoryManager:      mock,
		feedbackClassifier: classifier,
		botUserID:          "BOT",
	}

	// User == botUserID — should bail before classifying.
	h.maybeCaptureSlackFeedback("C", "T", "M", "anything", "BOT")

	if caller.calls != 0 {
		t.Errorf("expected 0 LLM calls when message is from bot, got %d", caller.calls)
	}
}

// fakeOneShotLLMCallerH is a small inline OneShotLLMCaller test double for
// the handler-package tests. Mirrors fakeOneShotLLMCaller in the services
// package; we redefine it here because cross-package use isn't possible.
type fakeOneShotLLMCallerH struct {
	calls    int
	response string
	err      error
}

func (f *fakeOneShotLLMCallerH) OneShotLLM(_ context.Context, _ *services.LLMSettingsForWorker, _, _ string, _ int, _ float64) (string, error) {
	f.calls++
	return f.response, f.err
}
