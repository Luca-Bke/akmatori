package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/akmatori/akmatori/internal/api"
	"github.com/akmatori/akmatori/internal/database"
	"github.com/akmatori/akmatori/internal/services"
)

// CreateChannelRequest is the request body for POST /api/channels. The
// integration is selected by UUID so callers do not have to expose internal
// integer IDs to the UI; ExternalID is the provider-specific channel handle.
type CreateChannelRequest struct {
	IntegrationUUID      string `json:"integration_uuid"`
	ExternalID           string `json:"external_id"`
	DisplayName          string `json:"display_name,omitempty"`
	CanPost              bool   `json:"can_post"`
	CanListen            bool   `json:"can_listen"`
	IsDefaultPost        bool   `json:"is_default_post,omitempty"`
	ExtractionPrompt     string `json:"extraction_prompt,omitempty"`
	ProcessHumanMessages bool   `json:"process_human_messages,omitempty"`
	Enabled              *bool  `json:"enabled,omitempty"`
}

// UpdateChannelRequest is the request body for PUT /api/channels/{uuid}. Every
// field is optional so the UI can submit partial patches; IntegrationUUID is
// not present because re-parenting a channel between integrations would change
// its addressing semantics (delete + recreate instead).
type UpdateChannelRequest struct {
	ExternalID           *string `json:"external_id,omitempty"`
	DisplayName          *string `json:"display_name,omitempty"`
	CanPost              *bool   `json:"can_post,omitempty"`
	CanListen            *bool   `json:"can_listen,omitempty"`
	IsDefaultPost        *bool   `json:"is_default_post,omitempty"`
	ExtractionPrompt     *string `json:"extraction_prompt,omitempty"`
	ProcessHumanMessages *bool   `json:"process_human_messages,omitempty"`
	Enabled              *bool   `json:"enabled,omitempty"`
}

// handleChannels dispatches GET /api/channels and POST /api/channels.
func (h *APIHandler) handleChannels(w http.ResponseWriter, r *http.Request) {
	if h.channelService == nil {
		api.RespondError(w, http.StatusServiceUnavailable, "Channel service is not configured")
		return
	}

	switch r.Method {
	case http.MethodGet:
		filter, err := parseChannelFilter(r)
		if err != nil {
			api.RespondError(w, http.StatusBadRequest, err.Error())
			return
		}
		rows, err := h.channelService.ListChannels(filter)
		if err != nil {
			api.RespondError(w, integrationErrStatus(err), err.Error())
			return
		}
		api.RespondJSON(w, http.StatusOK, rows)

	case http.MethodPost:
		var req CreateChannelRequest
		if err := api.DecodeJSON(r, &req); err != nil {
			api.RespondError(w, http.StatusBadRequest, err.Error())
			return
		}
		if strings.TrimSpace(req.IntegrationUUID) == "" {
			api.RespondError(w, http.StatusBadRequest, "integration_uuid is required")
			return
		}
		if strings.TrimSpace(req.ExternalID) == "" {
			api.RespondError(w, http.StatusBadRequest, "external_id is required")
			return
		}

		integration, err := h.channelService.GetIntegrationByUUID(req.IntegrationUUID)
		if err != nil {
			api.RespondError(w, integrationErrStatus(err), err.Error())
			return
		}

		// Provider-specific create-time validation. Slack channel IDs and
		// names always have non-empty external IDs; the service-layer guard
		// already enforces non-empty, but Slack-channel handles also must
		// not be pure whitespace nor contain commas (which would break
		// downstream multi-channel parsing in the legacy code path).
		if integration.Provider == database.MessagingProviderSlack {
			if strings.ContainsAny(req.ExternalID, " ,") {
				api.RespondError(w, http.StatusBadRequest, "slack external_id must not contain spaces or commas")
				return
			}
		}

		enabled := true
		if req.Enabled != nil {
			enabled = *req.Enabled
		}

		ch := &database.Channel{
			IntegrationID:        integration.ID,
			ExternalID:           req.ExternalID,
			DisplayName:          req.DisplayName,
			CanPost:              req.CanPost,
			CanListen:            req.CanListen,
			IsDefaultPost:        req.IsDefaultPost,
			ExtractionPrompt:     req.ExtractionPrompt,
			ProcessHumanMessages: req.ProcessHumanMessages,
			Enabled:              enabled,
		}

		row, err := h.channelService.CreateChannel(ch)
		if err != nil {
			api.RespondError(w, integrationErrStatus(err), err.Error())
			return
		}
		// Reload Slack listener mappings so new can_listen channels become
		// active immediately. Safe to call even when the channel only posts;
		// the loader filters by can_listen on its own.
		h.reloadAlertChannels()
		api.RespondJSON(w, http.StatusCreated, row)

	default:
		api.RespondError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// handleChannelByUUID dispatches GET/PUT/DELETE /api/channels/{uuid}.
func (h *APIHandler) handleChannelByUUID(w http.ResponseWriter, r *http.Request) {
	if h.channelService == nil {
		api.RespondError(w, http.StatusServiceUnavailable, "Channel service is not configured")
		return
	}

	uuid := strings.TrimPrefix(r.URL.Path, "/api/channels/")
	if uuid == "" || strings.Contains(uuid, "/") {
		api.RespondError(w, http.StatusBadRequest, "Invalid channel UUID")
		return
	}

	switch r.Method {
	case http.MethodGet:
		row, err := h.channelService.GetChannelByUUID(uuid)
		if err != nil {
			api.RespondError(w, integrationErrStatus(err), err.Error())
			return
		}
		api.RespondJSON(w, http.StatusOK, row)

	case http.MethodPut:
		var req UpdateChannelRequest
		if err := api.DecodeJSON(r, &req); err != nil {
			api.RespondError(w, http.StatusBadRequest, err.Error())
			return
		}
		patch := services.ChannelUpdate{
			ExternalID:           req.ExternalID,
			DisplayName:          req.DisplayName,
			CanPost:              req.CanPost,
			CanListen:            req.CanListen,
			IsDefaultPost:        req.IsDefaultPost,
			ExtractionPrompt:     req.ExtractionPrompt,
			ProcessHumanMessages: req.ProcessHumanMessages,
			Enabled:              req.Enabled,
		}
		row, err := h.channelService.UpdateChannel(uuid, patch)
		if err != nil {
			api.RespondError(w, integrationErrStatus(err), err.Error())
			return
		}
		h.reloadAlertChannels()
		api.RespondJSON(w, http.StatusOK, row)

	case http.MethodDelete:
		if err := h.channelService.DeleteChannel(uuid); err != nil {
			api.RespondError(w, integrationErrStatus(err), err.Error())
			return
		}
		h.reloadAlertChannels()
		api.RespondNoContent(w)

	default:
		api.RespondError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// parseChannelFilter reads ListChannels filter parameters from a request URL.
// Booleans use strconv.ParseBool semantics so "true"/"false"/"1"/"0" all work;
// invalid values surface as a 400 rather than being silently ignored.
func parseChannelFilter(r *http.Request) (services.ListChannelsFilter, error) {
	q := r.URL.Query()
	filter := services.ListChannelsFilter{
		IntegrationUUID: strings.TrimSpace(q.Get("integration_uuid")),
	}
	if raw := q.Get("can_post"); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return filter, &validationError{field: "can_post", reason: "must be a boolean"}
		}
		filter.CanPost = &v
	}
	if raw := q.Get("can_listen"); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return filter, &validationError{field: "can_listen", reason: "must be a boolean"}
		}
		filter.CanListen = &v
	}
	return filter, nil
}

// validationError is a small typed error used to keep parse-time errors
// distinguishable from service-layer ones; the message is fully user-facing.
type validationError struct {
	field  string
	reason string
}

func (e *validationError) Error() string {
	return e.field + " " + e.reason
}
