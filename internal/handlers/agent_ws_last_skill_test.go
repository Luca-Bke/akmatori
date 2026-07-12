package handlers

import (
	"testing"

	"github.com/akmatori/akmatori/internal/database"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupLastSkillDB(t *testing.T, incidentUUID string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&database.Incident{}); err != nil {
		t.Fatalf("migrate incident: %v", err)
	}
	prevDB := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = prevDB })

	if err := db.Create(&database.Incident{
		UUID:   incidentUUID,
		Status: database.IncidentStatusRunning,
	}).Error; err != nil {
		t.Fatalf("seed incident: %v", err)
	}
	return db
}

// TestHandleAgentCompleted_PersistsLastSkillBeforeCallback verifies that the
// last_skill from an agent_completed frame lands on the incident row before
// OnCompleted fires, so the finalizer's BuildFormatFlow read observes it.
func TestHandleAgentCompleted_PersistsLastSkillBeforeCallback(t *testing.T) {
	db := setupLastSkillDB(t, "incident-skill")

	handler := NewAgentWSHandler()
	var skillAtCallbackTime string
	handler.callbackMu.Lock()
	handler.callbacks["incident-skill"] = incidentCallbackEntry{
		runID: "run-1",
		callback: IncidentCallback{
			OnCompleted: func(sessionID, response string, tokensUsed int, executionTimeMs int64) {
				var row database.Incident
				if err := db.Where("uuid = ?", "incident-skill").First(&row).Error; err != nil {
					t.Errorf("read incident inside callback: %v", err)
					return
				}
				skillAtCallbackTime = row.LastSkillUsed
			},
		},
	}
	handler.callbackMu.Unlock()

	handler.handleAgentCompleted(AgentMessage{
		Type:       AgentMessageTypeAgentCompleted,
		IncidentID: "incident-skill",
		Output:     "final response",
		SessionID:  "session-1",
		RunID:      "run-1",
		LastSkill:  "victoria-metrics",
	})

	if skillAtCallbackTime != "victoria-metrics" {
		t.Errorf("last_skill_used at callback time = %q, want victoria-metrics", skillAtCallbackTime)
	}
}

// TestHandleAgentCompleted_SupersededRunDoesNotOverwriteLastSkill verifies
// that a late completion frame from a superseded run (run_id mismatch with
// the live entry) cannot overwrite the current run's last_skill_used.
func TestHandleAgentCompleted_SupersededRunDoesNotOverwriteLastSkill(t *testing.T) {
	db := setupLastSkillDB(t, "incident-skill-stale")
	if err := db.Model(&database.Incident{}).
		Where("uuid = ?", "incident-skill-stale").
		Update("last_skill_used", "netbox").Error; err != nil {
		t.Fatalf("seed last_skill_used: %v", err)
	}

	handler := NewAgentWSHandler()
	handler.callbackMu.Lock()
	handler.callbacks["incident-skill-stale"] = incidentCallbackEntry{runID: "run-2"}
	handler.callbackMu.Unlock()

	handler.handleAgentCompleted(AgentMessage{
		Type:       AgentMessageTypeAgentCompleted,
		IncidentID: "incident-skill-stale",
		Output:     "stale response",
		RunID:      "run-1",
		LastSkill:  "grafana-watcher",
	})

	var got database.Incident
	if err := db.Where("uuid = ?", "incident-skill-stale").First(&got).Error; err != nil {
		t.Fatalf("re-read incident: %v", err)
	}
	if got.LastSkillUsed != "netbox" {
		t.Errorf("last_skill_used overwritten by superseded run: got %q, want netbox", got.LastSkillUsed)
	}
}

// TestHandleAgentCompleted_LegacyFallbackPersistsLastSkill verifies the
// no-callback, no-run-id fallback path also records the last skill.
func TestHandleAgentCompleted_LegacyFallbackPersistsLastSkill(t *testing.T) {
	db := setupLastSkillDB(t, "incident-skill-legacy")

	handler := NewAgentWSHandler()
	handler.handleAgentCompleted(AgentMessage{
		Type:       AgentMessageTypeAgentCompleted,
		IncidentID: "incident-skill-legacy",
		Output:     "legacy response",
		SessionID:  "session-legacy",
		LastSkill:  "linux-engineer",
	})

	var got database.Incident
	if err := db.Where("uuid = ?", "incident-skill-legacy").First(&got).Error; err != nil {
		t.Fatalf("re-read incident: %v", err)
	}
	if got.LastSkillUsed != "linux-engineer" {
		t.Errorf("last_skill_used = %q, want linux-engineer", got.LastSkillUsed)
	}
}
