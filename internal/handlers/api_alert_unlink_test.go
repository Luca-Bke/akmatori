package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/akmatori/akmatori/internal/database"
	"github.com/akmatori/akmatori/internal/services"
	"github.com/akmatori/akmatori/internal/testhelpers"
	"github.com/google/uuid"
)

// unlinkSkillService embeds corrGateSkillService for all no-op stubs and
// overrides UnlinkAlertFromIncident with a configurable hook.
type unlinkSkillService struct {
	corrGateSkillService
	unlinkFn func(ctx context.Context, alertUUID string) (string, error)
}

func (s *unlinkSkillService) UnlinkAlertFromIncident(ctx context.Context, alertUUID string) (string, error) {
	if s.unlinkFn != nil {
		return s.unlinkFn(ctx, alertUUID)
	}
	return "new-incident-uuid", nil
}

// seedUnlinkTestAlert inserts an alert row suitable for unlink handler tests.
func seedUnlinkTestAlert(t *testing.T, incidentUUID string, correlated bool) string {
	t.Helper()
	db := database.GetDB()
	alertUUID := uuid.New().String()
	a := database.Alert{
		UUID:         alertUUID,
		IncidentUUID: incidentUUID,
		Status:       database.AlertStatusFiring,
		AlertName:    "HighCPU",
		TargetHost:   "host-01",
		Correlated:   correlated,
	}
	if correlated {
		conf := 0.9
		a.CorrelationConfidence = &conf
		a.CorrelationReasoning = "same host"
		a.CorrelationDecision = "linked"
	} else {
		a.CorrelationDecision = "new_incident"
	}
	if err := db.Create(&a).Error; err != nil {
		t.Fatalf("seed alert: %v", err)
	}
	return alertUUID
}

func TestHandleAlertUnlink_200_HappyPath(t *testing.T) {
	testhelpers.NewGlobalSQLiteDB(t, &database.Incident{}, &database.Alert{})

	incUUID := "inc-" + uuid.New().String()
	alertUUID := seedUnlinkTestAlert(t, incUUID, true)

	newIncidentUUID := "new-" + uuid.New().String()
	var capturedAlertUUID string
	svc := &unlinkSkillService{
		unlinkFn: func(ctx context.Context, auuid string) (string, error) {
			capturedAlertUUID = auuid
			return newIncidentUUID, nil
		},
	}

	h := NewAPIHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	mux := http.NewServeMux()
	h.SetupRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/alerts/"+alertUUID+"/unlink", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if capturedAlertUUID != alertUUID {
		t.Errorf("UnlinkAlertFromIncident called with UUID %q, want %q", capturedAlertUUID, alertUUID)
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["incident_uuid"] != newIncidentUUID {
		t.Errorf("incident_uuid = %q, want %q", resp["incident_uuid"], newIncidentUUID)
	}
}

func TestHandleAlertUnlink_409_NonCorrelated(t *testing.T) {
	testhelpers.NewGlobalSQLiteDB(t, &database.Incident{}, &database.Alert{})

	incUUID := "inc-" + uuid.New().String()
	alertUUID := seedUnlinkTestAlert(t, incUUID, false)

	svc := &unlinkSkillService{
		unlinkFn: func(ctx context.Context, auuid string) (string, error) {
			return "", services.ErrAlertNotCorrelated
		},
	}

	h := NewAPIHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	mux := http.NewServeMux()
	h.SetupRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/alerts/"+alertUUID+"/unlink", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAlertUnlink_404_NotFound(t *testing.T) {
	testhelpers.NewGlobalSQLiteDB(t, &database.Incident{}, &database.Alert{})

	svc := &unlinkSkillService{}

	h := NewAPIHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	mux := http.NewServeMux()
	h.SetupRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/alerts/nonexistent-uuid/unlink", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}
