package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/akmatori/akmatori/internal/api"
	"github.com/akmatori/akmatori/internal/database"
	"github.com/akmatori/akmatori/internal/testhelpers"
	"github.com/google/uuid"
)

// TestHandleIncidents_TrendEnrichment verifies that GET /api/incidents returns
// first_seen, last_seen, alert_count, and a 12-element trend slice for incidents
// that have associated alert rows.
func TestHandleIncidents_TrendEnrichment(t *testing.T) {
	testhelpers.NewGlobalSQLiteDB(t,
		&database.Incident{},
		&database.Alert{},
	)
	db := database.GetDB()

	incUUID := uuid.New().String()
	now := time.Now().UTC()
	firstFired := now.Add(-30 * time.Minute)
	lastFired := now.Add(-5 * time.Minute)

	if err := db.Create(&database.Incident{
		UUID:       incUUID,
		Source:     "test",
		SourceKind: database.IncidentSourceKindAlert,
		SourceUUID: "src-trend-test",
		Title:      "trend enrichment test",
		Status:     database.IncidentStatusRunning,
		StartedAt:  firstFired,
	}).Error; err != nil {
		t.Fatalf("seed incident: %v", err)
	}

	alerts := []database.Alert{
		{
			UUID:         uuid.New().String(),
			IncidentUUID: incUUID,
			Status:       database.AlertStatusFiring,
			AlertName:    "TestAlert",
			TargetHost:   "host1",
			FiredAt:      firstFired,
		},
		{
			UUID:         uuid.New().String(),
			IncidentUUID: incUUID,
			Status:       database.AlertStatusFiring,
			AlertName:    "TestAlert",
			TargetHost:   "host1",
			FiredAt:      lastFired,
		},
	}
	for _, a := range alerts {
		if err := db.Create(&a).Error; err != nil {
			t.Fatalf("seed alert: %v", err)
		}
	}

	h := NewAPIHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/incidents?trend_window=1h", nil)
	rec := httptest.NewRecorder()
	h.handleIncidents(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp api.PaginatedResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	dataBytes, err := json.Marshal(resp.Data)
	if err != nil {
		t.Fatalf("re-encode data: %v", err)
	}
	var incidents []map[string]interface{}
	if err := json.Unmarshal(dataBytes, &incidents); err != nil {
		t.Fatalf("decode incidents: %v", err)
	}
	if len(incidents) != 1 {
		t.Fatalf("expected 1 incident, got %d", len(incidents))
	}

	inc := incidents[0]

	// alert_count should be 2.
	if v, _ := inc["alert_count"].(float64); v != 2 {
		t.Errorf("alert_count = %v, want 2", v)
	}

	// first_seen and last_seen should be present.
	if inc["first_seen"] == nil {
		t.Error("first_seen should be set")
	}
	if inc["last_seen"] == nil {
		t.Error("last_seen should be set")
	}

	// trend should be a 12-element array.
	trend, ok := inc["trend"].([]interface{})
	if !ok {
		t.Fatalf("trend field missing or wrong type: %v", inc["trend"])
	}
	if len(trend) != 12 {
		t.Errorf("trend length = %d, want 12", len(trend))
	}

	// The two alerts fired within the 1h window, so total across buckets should be 2.
	total := 0
	for _, v := range trend {
		total += int(v.(float64))
	}
	if total != 2 {
		t.Errorf("trend sum = %d, want 2", total)
	}
}

// TestHandleIncidents_NoAlerts_ZeroTrend verifies that incidents with no alert
// rows get a zero-filled 12-element trend slice.
func TestHandleIncidents_NoAlerts_ZeroTrend(t *testing.T) {
	testhelpers.NewGlobalSQLiteDB(t,
		&database.Incident{},
		&database.Alert{},
	)
	db := database.GetDB()

	if err := db.Create(&database.Incident{
		UUID:       uuid.New().String(),
		Source:     "api",
		SourceKind: database.IncidentSourceKindCron,
		SourceUUID: "src-no-alerts",
		Title:      "no alerts incident",
		Status:     database.IncidentStatusCompleted,
		StartedAt:  time.Now().Add(-2 * time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed incident: %v", err)
	}

	h := NewAPIHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/incidents", nil)
	rec := httptest.NewRecorder()
	h.handleIncidents(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp api.PaginatedResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	dataBytes, _ := json.Marshal(resp.Data)
	var incidents []map[string]interface{}
	_ = json.Unmarshal(dataBytes, &incidents)

	if len(incidents) != 1 {
		t.Fatalf("expected 1 incident, got %d", len(incidents))
	}
	trend, ok := incidents[0]["trend"].([]interface{})
	if !ok {
		t.Fatalf("trend field missing or wrong type")
	}
	if len(trend) != 12 {
		t.Errorf("trend length = %d, want 12", len(trend))
	}
	for i, v := range trend {
		if int(v.(float64)) != 0 {
			t.Errorf("trend[%d] = %v, want 0", i, v)
		}
	}
}
