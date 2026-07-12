package handlers

import (
	"net/http"

	"github.com/akmatori/akmatori/internal/api"
)

// handleFormattingSettings returns 410 Gone for any access to
// /api/settings/formatting. The global formatting singleton was replaced by
// ordered per-flow formatting rules (/api/formatting-rules); an enabled
// legacy configuration is auto-migrated into a catch-all rule at startup.
func (h *APIHandler) handleFormattingSettings(w http.ResponseWriter, r *http.Request) {
	api.RespondError(w, http.StatusGone, "/api/settings/formatting has been removed; use /api/formatting-rules")
}
