package handlers

import (
	"net/http"

	"github.com/akmatori/akmatori/internal/api"
)

// handleSlackSettings returns 410 Gone for any access to /api/settings/slack.
// The endpoint was retired by the unified-channels rollout; operators should
// configure Slack via /api/integrations + /api/channels.
func (h *APIHandler) handleSlackSettings(w http.ResponseWriter, r *http.Request) {
	api.RespondError(w, http.StatusGone, "/api/settings/slack has been removed; use /api/integrations and /api/channels")
}

// maskToken masks a token for display, showing only last 4 characters.
func maskToken(token string) string {
	if token == "" {
		return ""
	}
	if len(token) <= 4 {
		return "****"
	}
	return "****" + token[len(token)-4:]
}
