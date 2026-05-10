package handlers

import (
	"net/http"
	"strings"

	"github.com/akmatori/akmatori/internal/api"
)

// isDuplicateNameErr reports whether err is a database unique-constraint
// violation on the alert source name. Both Postgres (GORM) and SQLite
// (used by tests) surface this via distinctive substrings; we match on the
// same set already used by api_tools.go / api_settings_llm.go so behavior is
// consistent across handlers.
func isDuplicateNameErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "UNIQUE constraint") ||
		strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "already exists")
}

// handleAlertSourceTypes handles GET /api/alert-source-types
func (h *APIHandler) handleAlertSourceTypes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.RespondError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	sourceTypes, err := h.alertService.ListSourceTypes()
	if err != nil {
		api.RespondError(w, http.StatusInternalServerError, "Failed to list source types")
		return
	}

	api.RespondJSON(w, http.StatusOK, sourceTypes)
}

// handleAlertSources handles GET /api/alert-sources and POST /api/alert-sources
func (h *APIHandler) handleAlertSources(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		instances, err := h.alertService.ListInstances()
		if err != nil {
			api.RespondError(w, http.StatusInternalServerError, "Failed to list alert sources")
			return
		}
		api.RespondJSON(w, http.StatusOK, instances)

	case http.MethodPost:
		var req api.CreateAlertSourceRequest
		if err := api.DecodeJSON(r, &req); err != nil {
			api.RespondError(w, http.StatusBadRequest, err.Error())
			return
		}

		req.SourceTypeName = strings.TrimSpace(req.SourceTypeName)
		req.Name = strings.TrimSpace(req.Name)

		if req.SourceTypeName == "" || req.Name == "" {
			api.RespondError(w, http.StatusBadRequest, "source_type_name and name are required")
			return
		}

		if req.SourceTypeName == "slack_channel" {
			channelID, _ := req.Settings["slack_channel_id"].(string)
			if strings.TrimSpace(channelID) == "" {
				api.RespondError(w, http.StatusBadRequest, "slack_channel_id is required in settings for slack_channel source type")
				return
			}
		}

		instance, err := h.alertService.CreateInstance(req.SourceTypeName, req.Name, req.Description, req.WebhookSecret, req.FieldMappings, req.Settings)
		if err != nil {
			if isDuplicateNameErr(err) {
				api.RespondError(w, http.StatusConflict, "An alert source with that name already exists")
				return
			}
			api.RespondError(w, http.StatusInternalServerError, "Failed to create alert source")
			return
		}

		api.RespondJSON(w, http.StatusCreated, instance)
		h.reloadAlertChannels()

	default:
		api.RespondError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// handleAlertSourceByUUID handles GET/PUT/DELETE /api/alert-sources/{uuid}
func (h *APIHandler) handleAlertSourceByUUID(w http.ResponseWriter, r *http.Request) {
	uuid := r.URL.Path[len("/api/alert-sources/"):]
	if uuid == "" {
		api.RespondError(w, http.StatusBadRequest, "UUID is required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		instance, err := h.alertService.GetInstanceByUUID(uuid)
		if err != nil {
			api.RespondError(w, http.StatusNotFound, "Alert source not found")
			return
		}
		api.RespondJSON(w, http.StatusOK, instance)

	case http.MethodPut:
		var req api.UpdateAlertSourceRequest
		if err := api.DecodeJSON(r, &req); err != nil {
			api.RespondError(w, http.StatusBadRequest, err.Error())
			return
		}

		updates := make(map[string]interface{})
		if req.Name != nil {
			trimmed := strings.TrimSpace(*req.Name)
			if trimmed == "" {
				api.RespondError(w, http.StatusBadRequest, "name cannot be empty")
				return
			}
			updates["name"] = trimmed
		}
		if req.Description != nil {
			updates["description"] = *req.Description
		}
		if req.WebhookSecret != nil {
			updates["webhook_secret"] = *req.WebhookSecret
		}
		if req.FieldMappings != nil {
			updates["field_mappings"] = *req.FieldMappings
		}
		if req.Settings != nil {
			updates["settings"] = *req.Settings
		}
		if req.Enabled != nil {
			updates["enabled"] = *req.Enabled
		}

		if req.Settings != nil {
			existing, err := h.alertService.GetInstanceByUUID(uuid)
			if err == nil && existing.AlertSourceType.Name == "slack_channel" {
				channelID, _ := (*req.Settings)["slack_channel_id"].(string)
				if strings.TrimSpace(channelID) == "" {
					api.RespondError(w, http.StatusBadRequest, "slack_channel_id is required in settings for slack_channel source type")
					return
				}
			}
		}

		if err := h.alertService.UpdateInstance(uuid, updates); err != nil {
			if isDuplicateNameErr(err) {
				api.RespondError(w, http.StatusConflict, "An alert source with that name already exists")
				return
			}
			api.RespondError(w, http.StatusInternalServerError, "Failed to update alert source")
			return
		}

		instance, _ := h.alertService.GetInstanceByUUID(uuid)
		api.RespondJSON(w, http.StatusOK, instance)
		h.reloadAlertChannels()

	case http.MethodDelete:
		if err := h.alertService.DeleteInstance(uuid); err != nil {
			api.RespondError(w, http.StatusInternalServerError, "Failed to delete alert source")
			return
		}
		api.RespondNoContent(w)
		h.reloadAlertChannels()

	default:
		api.RespondError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}
