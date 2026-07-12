package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandleFormattingSettings_Gone pins the removal contract: the legacy
// global formatting settings endpoint answers 410 for every method and points
// callers at /api/formatting-rules.
func TestHandleFormattingSettings_Gone(t *testing.T) {
	h := NewAPIHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPost, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/settings/formatting", nil)
			w := httptest.NewRecorder()

			h.handleFormattingSettings(w, req)

			if w.Code != http.StatusGone {
				t.Errorf("expected 410, got %d", w.Code)
			}
		})
	}
}
