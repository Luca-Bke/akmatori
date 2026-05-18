package handlers

import (
	"log/slog"
	"net/http"

	"github.com/akmatori/akmatori/internal/api"
	"github.com/akmatori/akmatori/internal/database"
)

// handleSlackSettings handles GET /api/settings/slack and PUT /api/settings/slack.
//
// Deprecated: once the new Integrations + Channels infrastructure is wired
// (channelService non-nil), this endpoint returns a 308 Permanent Redirect to
// /api/integrations so clients migrate. The legacy CRUD path is kept as a
// fallback for deployments that have not yet provisioned a ChannelManager;
// Task 10 of the unified-channels plan removes the redirect entirely (or
// downgrades it to 410 Gone).
func (h *APIHandler) handleSlackSettings(w http.ResponseWriter, r *http.Request) {
	if h.channelService != nil {
		w.Header().Set("Location", "/api/integrations")
		// 308 preserves method + body so clients that follow the redirect
		// can re-issue the request shape against the new endpoint; in
		// practice the body shape differs, but the redirect is the
		// machine-readable signal to migrate.
		http.Error(w, "Use /api/integrations — /api/settings/slack is deprecated", http.StatusPermanentRedirect)
		return
	}

	db := database.GetDB()

	switch r.Method {
	case http.MethodGet:
		var settings database.SlackSettings
		if err := db.First(&settings).Error; err != nil {
			api.RespondError(w, http.StatusNotFound, "Settings not found")
			return
		}
		response := map[string]interface{}{
			"id":             settings.ID,
			"bot_token":      maskToken(settings.BotToken),
			"signing_secret": maskToken(settings.SigningSecret),
			"app_token":      maskToken(settings.AppToken),
			"alerts_channel": settings.AlertsChannel,
			"enabled":        settings.Enabled,
			"is_configured":  settings.IsConfigured(),
			"created_at":     settings.CreatedAt,
			"updated_at":     settings.UpdatedAt,
		}
		api.RespondJSON(w, http.StatusOK, response)

	case http.MethodPut:
		var req api.UpdateSlackSettingsRequest
		if err := api.DecodeJSON(r, &req); err != nil {
			api.RespondError(w, http.StatusBadRequest, err.Error())
			return
		}

		var settings database.SlackSettings
		if err := db.First(&settings).Error; err != nil {
			api.RespondError(w, http.StatusNotFound, "Settings not found")
			return
		}

		updates := make(map[string]interface{})
		if req.BotToken != nil {
			updates["bot_token"] = *req.BotToken
		}
		if req.SigningSecret != nil {
			updates["signing_secret"] = *req.SigningSecret
		}
		if req.AppToken != nil {
			updates["app_token"] = *req.AppToken
		}
		if req.AlertsChannel != nil {
			updates["alerts_channel"] = *req.AlertsChannel
		}
		if req.Enabled != nil {
			updates["enabled"] = *req.Enabled
		}

		if err := db.Model(&settings).Updates(updates).Error; err != nil {
			api.RespondError(w, http.StatusInternalServerError, "Failed to update settings")
			return
		}

		if h.slackManager != nil {
			h.slackManager.TriggerReload()
			slog.Info("Slack settings updated, triggering hot-reload")
		}

		db.First(&settings)
		response := map[string]interface{}{
			"id":             settings.ID,
			"bot_token":      maskToken(settings.BotToken),
			"signing_secret": maskToken(settings.SigningSecret),
			"app_token":      maskToken(settings.AppToken),
			"alerts_channel": settings.AlertsChannel,
			"enabled":        settings.Enabled,
			"is_configured":  settings.IsConfigured(),
			"created_at":     settings.CreatedAt,
			"updated_at":     settings.UpdatedAt,
		}
		api.RespondJSON(w, http.StatusOK, response)

	default:
		api.RespondError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// maskToken masks a token for display, showing only last 4 characters
func maskToken(token string) string {
	if token == "" {
		return ""
	}
	if len(token) <= 4 {
		return "****"
	}
	return "****" + token[len(token)-4:]
}
