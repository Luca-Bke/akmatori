package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/akmatori/akmatori/internal/api"
	"github.com/akmatori/akmatori/internal/database"
	"github.com/akmatori/akmatori/internal/services"
)

// CreateIntegrationRequest is the request body for POST /api/integrations.
// Credentials are stored verbatim as JSONB; their shape is provider-specific.
type CreateIntegrationRequest struct {
	Provider    string         `json:"provider"`
	Name        string         `json:"name"`
	Credentials database.JSONB `json:"credentials,omitempty"`
	Enabled     *bool          `json:"enabled,omitempty"`
}

// UpdateIntegrationRequest is the request body for PUT /api/integrations/{uuid}.
// Provider is intentionally immutable on update — operators must delete and
// re-create when switching backends so credential shape stays consistent.
type UpdateIntegrationRequest struct {
	Name        *string         `json:"name,omitempty"`
	Credentials *database.JSONB `json:"credentials,omitempty"`
	Enabled     *bool           `json:"enabled,omitempty"`
}

// handleIntegrations dispatches GET /api/integrations and POST /api/integrations.
func (h *APIHandler) handleIntegrations(w http.ResponseWriter, r *http.Request) {
	if h.channelService == nil {
		api.RespondError(w, http.StatusServiceUnavailable, "Channel service is not configured")
		return
	}

	switch r.Method {
	case http.MethodGet:
		rows, err := h.channelService.ListIntegrations()
		if err != nil {
			api.RespondError(w, http.StatusInternalServerError, "Failed to list integrations")
			return
		}
		api.RespondJSON(w, http.StatusOK, rows)

	case http.MethodPost:
		var req CreateIntegrationRequest
		if err := api.DecodeJSON(r, &req); err != nil {
			api.RespondError(w, http.StatusBadRequest, err.Error())
			return
		}

		provider := strings.TrimSpace(req.Provider)
		if provider == "" {
			api.RespondError(w, http.StatusBadRequest, "provider is required")
			return
		}
		if !h.isProviderKnown(database.MessagingProvider(provider)) {
			api.RespondError(w, http.StatusBadRequest, "provider is not a known messaging provider")
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			api.RespondError(w, http.StatusBadRequest, "name is required")
			return
		}
		enabled := true
		if req.Enabled != nil {
			enabled = *req.Enabled
		}

		row, err := h.channelService.CreateIntegration(database.MessagingProvider(provider), req.Name, req.Credentials, enabled)
		if err != nil {
			api.RespondError(w, integrationErrStatus(err), err.Error())
			return
		}
		api.RespondJSON(w, http.StatusCreated, row)

	default:
		api.RespondError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// handleIntegrationByUUID dispatches GET/PUT/DELETE /api/integrations/{uuid}.
func (h *APIHandler) handleIntegrationByUUID(w http.ResponseWriter, r *http.Request) {
	if h.channelService == nil {
		api.RespondError(w, http.StatusServiceUnavailable, "Channel service is not configured")
		return
	}

	uuid := strings.TrimPrefix(r.URL.Path, "/api/integrations/")
	if uuid == "" || strings.Contains(uuid, "/") {
		api.RespondError(w, http.StatusBadRequest, "Invalid integration UUID")
		return
	}

	switch r.Method {
	case http.MethodGet:
		row, err := h.channelService.GetIntegrationByUUID(uuid)
		if err != nil {
			api.RespondError(w, integrationErrStatus(err), err.Error())
			return
		}
		api.RespondJSON(w, http.StatusOK, row)

	case http.MethodPut:
		var req UpdateIntegrationRequest
		if err := api.DecodeJSON(r, &req); err != nil {
			api.RespondError(w, http.StatusBadRequest, err.Error())
			return
		}
		var creds database.JSONB
		if req.Credentials != nil {
			creds = database.JSONB(*req.Credentials)
		}
		row, err := h.channelService.UpdateIntegration(uuid, req.Name, creds, req.Enabled)
		if err != nil {
			api.RespondError(w, integrationErrStatus(err), err.Error())
			return
		}
		api.RespondJSON(w, http.StatusOK, row)

	case http.MethodDelete:
		if err := h.channelService.DeleteIntegration(uuid); err != nil {
			api.RespondError(w, integrationErrStatus(err), err.Error())
			return
		}
		api.RespondNoContent(w)

	default:
		api.RespondError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// isProviderKnown reports whether the supplied provider identifier is one the
// API will accept on create. When a ProviderRegistry is wired we ask the
// registry (so a registered telegram stub would be acceptable while an
// unregistered provider is rejected); otherwise we fall back to the model
// whitelist so unit tests without a registry still validate.
func (h *APIHandler) isProviderKnown(p database.MessagingProvider) bool {
	if h.providerRegistry != nil {
		if _, err := h.providerRegistry.Get(p); err == nil {
			return true
		}
		// Even when the registry lacks the provider, accept any value the
		// model layer recognises so operators can pre-configure a Telegram
		// integration before the runtime provider lands.
	}
	return database.IsValidMessagingProvider(string(p))
}

// integrationErrStatus translates ChannelService errors into HTTP status codes.
func integrationErrStatus(err error) int {
	switch {
	case errors.Is(err, services.ErrIntegrationNotFound):
		return http.StatusNotFound
	case errors.Is(err, services.ErrChannelNotFound):
		return http.StatusNotFound
	case errors.Is(err, services.ErrDuplicateDefaultPost):
		return http.StatusConflict
	default:
		// Validation errors from the service layer carry plain-text messages
		// (e.g. "integration name cannot be empty"); surface them as 400 so
		// the UI can render the message directly.
		if isClientError(err) {
			return http.StatusBadRequest
		}
		return http.StatusInternalServerError
	}
}

// isClientError reports whether the error message looks like a user-facing
// validation failure rather than an unexpected backend error. The service
// layer wraps DB errors with "create integration: ..." / "update channel: ..."
// prefixes; anything that lacks such a prefix is treated as a 400.
func isClientError(err error) bool {
	msg := err.Error()
	prefixes := []string{
		"create integration: ",
		"update integration: ",
		"delete integration",
		"create channel: ",
		"update channel: ",
		"delete channel",
		"list channels: ",
		"list integrations: ",
		"get integration ",
		"get channel ",
		"resolve ",
		"load integration ",
		"count existing ",
		"reload integration after update",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(msg, p) {
			return false
		}
	}
	return true
}
