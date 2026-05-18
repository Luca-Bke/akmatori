package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/akmatori/akmatori/internal/api"
	"github.com/akmatori/akmatori/internal/database"
	"github.com/akmatori/akmatori/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupAlertSourceAPIHandler(t *testing.T) (*APIHandler, *services.AlertService) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	if err := db.AutoMigrate(&database.AlertSourceType{}, &database.AlertSourceInstance{}); err != nil {
		t.Fatalf("migrate alert source tables: %v", err)
	}
	database.DB = db

	service := services.NewAlertService()
	handler := NewAPIHandler(nil, nil, nil, service, nil, nil, nil, nil, nil, nil, nil)
	return handler, service
}

func performAlertSourceRequest(t *testing.T, handler http.HandlerFunc, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()

	var reqBody *bytes.Reader
	if body == nil {
		reqBody = bytes.NewReader(nil)
	} else if raw, ok := body.(string); ok {
		reqBody = bytes.NewReader([]byte(raw))
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reqBody = bytes.NewReader(encoded)
	}

	req := httptest.NewRequest(method, path, reqBody)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)
	return w
}

func requireAlertSourceAPIError(t *testing.T, w *httptest.ResponseRecorder, status int, want string) {
	t.Helper()

	if w.Code != status {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, status, w.Body.String())
	}
	var got api.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode error response: %v; body: %s", err, w.Body.String())
	}
	if !strings.Contains(got.Error, want) {
		t.Fatalf("error = %q, want substring %q", got.Error, want)
	}
}

func requireReload(t *testing.T, ch <-chan struct{}, operation string) {
	t.Helper()

	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("%s did not trigger alert channel reload", operation)
	}
}

func TestAPIHandler_HandleAlertSourceTypes(t *testing.T) {
	handler, service := setupAlertSourceAPIHandler(t)
	if _, err := service.CreateAlertSourceType("custom_webhook", "Custom Webhook", "custom alerts", database.JSONB{"alert_name": "title"}, "X-Custom-Secret"); err != nil {
		t.Fatalf("seed alert source type: %v", err)
	}

	w := performAlertSourceRequest(t, handler.handleAlertSourceTypes, http.MethodGet, "/api/alert-source-types", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var types []database.AlertSourceType
	if err := json.Unmarshal(w.Body.Bytes(), &types); err != nil {
		t.Fatalf("decode source types: %v", err)
	}
	if len(types) != 1 || types[0].Name != "custom_webhook" {
		t.Fatalf("source types = %+v, want one custom_webhook type", types)
	}

	w = performAlertSourceRequest(t, handler.handleAlertSourceTypes, http.MethodPost, "/api/alert-source-types", nil)
	requireAlertSourceAPIError(t, w, http.StatusMethodNotAllowed, "Method not allowed")
}

func TestAPIHandler_HandleAlertSources_CreateValidationAndConflict(t *testing.T) {
	handler, service := setupAlertSourceAPIHandler(t)
	if _, err := service.CreateAlertSourceType("custom_webhook", "Custom Webhook", "custom alerts", database.JSONB{}, "X-Custom-Secret"); err != nil {
		t.Fatalf("seed custom_webhook source type: %v", err)
	}
	if _, err := service.CreateAlertSourceType("slack_channel", "Slack Channel", "Slack channel alerts", database.JSONB{}, ""); err != nil {
		t.Fatalf("seed slack_channel source type: %v", err)
	}
	reloads := make(chan struct{}, 4)
	handler.SetAlertChannelReloader(func() { reloads <- struct{}{} })

	w := performAlertSourceRequest(t, handler.handleAlertSources, http.MethodPost, "/api/alert-sources", "{")
	requireAlertSourceAPIError(t, w, http.StatusBadRequest, "invalid JSON in request body")

	w = performAlertSourceRequest(t, handler.handleAlertSources, http.MethodPost, "/api/alert-sources", api.CreateAlertSourceRequest{
		SourceTypeName: " ",
		Name:           "production alerts",
	})
	requireAlertSourceAPIError(t, w, http.StatusBadRequest, "source_type_name and name are required")

	w = performAlertSourceRequest(t, handler.handleAlertSources, http.MethodPost, "/api/alert-sources", api.CreateAlertSourceRequest{
		SourceTypeName: "slack_channel",
		Name:           "Slack alerts",
		Settings:       database.JSONB{"slack_channel_id": "   "},
	})
	requireAlertSourceAPIError(t, w, http.StatusBadRequest, "slack_channel_id is required")

	create := api.CreateAlertSourceRequest{
		SourceTypeName: " custom_webhook ",
		Name:           " Production alerts ",
		Description:    "Primary webhook",
		WebhookSecret:  "secret",
		FieldMappings:  database.JSONB{"severity": "priority"},
		Settings:       database.JSONB{"region": "eu"},
	}
	w = performAlertSourceRequest(t, handler.handleAlertSources, http.MethodPost, "/api/alert-sources", create)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
	requireReload(t, reloads, "create alert source")
	var created database.AlertSourceInstance
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created source: %v", err)
	}
	if created.Name != "Production alerts" {
		t.Fatalf("created name = %q, want trimmed Production alerts", created.Name)
	}

	w = performAlertSourceRequest(t, handler.handleAlertSources, http.MethodPost, "/api/alert-sources", create)
	requireAlertSourceAPIError(t, w, http.StatusConflict, "already exists")
}

func TestAPIHandler_HandleAlertSourceByUUID_UpdateAndDelete(t *testing.T) {
	handler, service := setupAlertSourceAPIHandler(t)
	if _, err := service.CreateAlertSourceType("slack_channel", "Slack Channel", "Slack channel alerts", database.JSONB{}, ""); err != nil {
		t.Fatalf("seed slack_channel source type: %v", err)
	}
	instance, err := service.CreateInstance("slack_channel", "Slack alerts", "", "", nil, database.JSONB{"slack_channel_id": "COLD"})
	if err != nil {
		t.Fatalf("seed alert source instance: %v", err)
	}
	reloads := make(chan struct{}, 4)
	handler.SetAlertChannelReloader(func() { reloads <- struct{}{} })
	path := "/api/alert-sources/" + instance.UUID

	w := performAlertSourceRequest(t, handler.handleAlertSourceByUUID, http.MethodGet, path, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	emptyName := "   "
	w = performAlertSourceRequest(t, handler.handleAlertSourceByUUID, http.MethodPut, path, api.UpdateAlertSourceRequest{Name: &emptyName})
	requireAlertSourceAPIError(t, w, http.StatusBadRequest, "name cannot be empty")

	badSettings := database.JSONB{"slack_channel_id": ""}
	w = performAlertSourceRequest(t, handler.handleAlertSourceByUUID, http.MethodPut, path, api.UpdateAlertSourceRequest{Settings: &badSettings})
	requireAlertSourceAPIError(t, w, http.StatusBadRequest, "slack_channel_id is required")

	newName := "Updated Slack alerts"
	goodSettings := database.JSONB{"slack_channel_id": "CNEW"}
	w = performAlertSourceRequest(t, handler.handleAlertSourceByUUID, http.MethodPut, path, api.UpdateAlertSourceRequest{
		Name:     &newName,
		Settings: &goodSettings,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	requireReload(t, reloads, "update alert source")
	var updated database.AlertSourceInstance
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode updated source: %v", err)
	}
	if updated.Name != newName || updated.Settings["slack_channel_id"] != "CNEW" {
		t.Fatalf("updated source = %+v, want name %q and channel CNEW", updated, newName)
	}

	w = performAlertSourceRequest(t, handler.handleAlertSourceByUUID, http.MethodDelete, path, nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204; body: %s", w.Code, w.Body.String())
	}
	requireReload(t, reloads, "delete alert source")

	w = performAlertSourceRequest(t, handler.handleAlertSourceByUUID, http.MethodGet, path, nil)
	requireAlertSourceAPIError(t, w, http.StatusNotFound, "Alert source not found")
}
