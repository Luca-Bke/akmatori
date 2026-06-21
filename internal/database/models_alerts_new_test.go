package database

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestAlertStatusConstants verifies the AlertStatus constants introduced for
// the first-class alerts table.
func TestAlertStatusConstants(t *testing.T) {
	if AlertStatusFiring != "firing" {
		t.Errorf("AlertStatusFiring = %q, want %q", AlertStatusFiring, "firing")
	}
	if AlertStatusResolved != "resolved" {
		t.Errorf("AlertStatusResolved = %q, want %q", AlertStatusResolved, "resolved")
	}
}

// TestIncidentStatusMonitor verifies the new monitor status constant.
func TestIncidentStatusMonitor(t *testing.T) {
	if IncidentStatusMonitor != "monitor" {
		t.Errorf("IncidentStatusMonitor = %q, want %q", IncidentStatusMonitor, "monitor")
	}
}

// TestAlert_TableName verifies the table name for the Alert model.
func TestAlert_TableName(t *testing.T) {
	if got := (Alert{}).TableName(); got != "alerts" {
		t.Errorf("Alert.TableName() = %q, want %q", got, "alerts")
	}
}

// TestAlert_AutoMigrate verifies that AutoMigrate succeeds and basic CRUD works.
func TestAlert_AutoMigrate(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&Alert{}); err != nil {
		t.Fatalf("AutoMigrate Alert: %v", err)
	}

	now := time.Now().UTC()
	row := Alert{
		UUID:              "test-alert-uuid-1",
		IncidentUUID:      "test-inc-uuid-1",
		Status:            AlertStatusFiring,
		Fingerprint:       "abcdef1234567890abcdef1234567890",
		SourceUUID:        "src-uuid-1",
		SourceFingerprint: "sfp-1",
		AlertName:         "HighCPU",
		TargetHost:        "prod-01",
		FiredAt:           now,
		RawPayload:        JSONB{"severity": "critical"},
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create alert row: %v", err)
	}

	var reloaded Alert
	if err := db.First(&reloaded, "uuid = ?", row.UUID).Error; err != nil {
		t.Fatalf("reload alert: %v", err)
	}
	if reloaded.IncidentUUID != "test-inc-uuid-1" {
		t.Errorf("IncidentUUID = %q, want %q", reloaded.IncidentUUID, "test-inc-uuid-1")
	}
	if reloaded.Status != AlertStatusFiring {
		t.Errorf("Status = %q, want %q", reloaded.Status, AlertStatusFiring)
	}
	if reloaded.AlertName != "HighCPU" {
		t.Errorf("AlertName = %q, want %q", reloaded.AlertName, "HighCPU")
	}
	if reloaded.ResolvedAt != nil {
		t.Errorf("ResolvedAt = %v, want nil", reloaded.ResolvedAt)
	}
}

// TestIncident_MonitorFields verifies that MonitorUntil and ResolvedAt fields
// were added to the Incident struct and are handled correctly by AutoMigrate.
func TestIncident_MonitorFields(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&Incident{}); err != nil {
		t.Fatalf("AutoMigrate Incident: %v", err)
	}

	now := time.Now().UTC()
	inc := Incident{
		UUID:   "inc-monitor-test-1",
		Source: "test",
		Status: IncidentStatusMonitor,
	}
	if err := db.Create(&inc).Error; err != nil {
		t.Fatalf("create incident: %v", err)
	}

	monitorUntil := now.Add(60 * time.Minute)
	if err := db.Model(&inc).Updates(map[string]interface{}{
		"monitor_until": monitorUntil,
	}).Error; err != nil {
		t.Fatalf("set monitor_until: %v", err)
	}

	var reloaded Incident
	if err := db.First(&reloaded, "uuid = ?", inc.UUID).Error; err != nil {
		t.Fatalf("reload incident: %v", err)
	}
	if reloaded.Status != IncidentStatusMonitor {
		t.Errorf("Status = %q, want %q", reloaded.Status, IncidentStatusMonitor)
	}
	if reloaded.MonitorUntil == nil {
		t.Error("MonitorUntil is nil, want non-nil")
	}
}
