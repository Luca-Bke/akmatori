package handlers

import (
	"testing"

	"github.com/akmatori/akmatori/internal/database"
	"github.com/akmatori/akmatori/internal/services"
	"github.com/google/uuid"
)

// Silent-listener gating: a listener Channel with can_post=false must never be
// written back to. incidentThreadPostable is the seam used by the webhook
// correlation path to decide whether a "Recurring alert" note may go into a
// matched incident's thread; the listener investigation flow branches on
// channel.CanPost directly.

func TestAlertHandler_IncidentThreadPostable_SilentListenerChannel(t *testing.T) {
	db, cleanup := setupChannelRoutingDB(t)
	defer cleanup()

	integ, _, _ := seedIntegrationWithChannels(t, db)

	silent := &database.Channel{
		UUID:          uuid.New().String(),
		IntegrationID: integ.ID,
		ExternalID:    "C_SILENT",
		DisplayName:   "#silent-alerts",
		CanListen:     true,
		CanPost:       false,
		Enabled:       true,
	}
	if err := db.Create(silent).Error; err != nil {
		t.Fatalf("create silent channel: %v", err)
	}

	h := NewAlertHandler(nil, nil, nil, nil, nil, nil, nil)
	h.SetChannelService(services.NewChannelService())

	incident := &database.Incident{
		SourceKind: database.IncidentSourceKindAlert,
		SourceUUID: silent.UUID,
	}
	if h.incidentThreadPostable(incident) {
		t.Error("incidentThreadPostable = true for can_post=false listener channel, want false")
	}
}

func TestAlertHandler_IncidentThreadPostable_PostableListenerChannel(t *testing.T) {
	db, cleanup := setupChannelRoutingDB(t)
	defer cleanup()

	_, defaultCh, _ := seedIntegrationWithChannels(t, db)
	_ = db

	h := NewAlertHandler(nil, nil, nil, nil, nil, nil, nil)
	h.SetChannelService(services.NewChannelService())

	incident := &database.Incident{
		SourceKind: database.IncidentSourceKindAlert,
		SourceUUID: defaultCh.UUID,
	}
	if !h.incidentThreadPostable(incident) {
		t.Error("incidentThreadPostable = false for can_post=true channel, want true")
	}
}

func TestAlertHandler_IncidentThreadPostable_NonChannelSourceDefaultsTrue(t *testing.T) {
	_, cleanup := setupChannelRoutingDB(t)
	defer cleanup()

	h := NewAlertHandler(nil, nil, nil, nil, nil, nil, nil)
	h.SetChannelService(services.NewChannelService())

	// Webhook alert sources carry the AlertSourceInstance UUID in
	// source_uuid — no channel row resolves, so posting stays allowed.
	incident := &database.Incident{
		SourceKind: database.IncidentSourceKindAlert,
		SourceUUID: uuid.New().String(),
	}
	if !h.incidentThreadPostable(incident) {
		t.Error("incidentThreadPostable = false for non-channel source UUID, want true")
	}
}

func TestAlertHandler_IncidentThreadPostable_NilGuards(t *testing.T) {
	h := NewAlertHandler(nil, nil, nil, nil, nil, nil, nil)

	// No channel service wired → default postable (webhook/legacy paths).
	incident := &database.Incident{SourceUUID: uuid.New().String()}
	if !h.incidentThreadPostable(incident) {
		t.Error("incidentThreadPostable = false with nil channelService, want true")
	}
	if !h.incidentThreadPostable(nil) {
		t.Error("incidentThreadPostable = false for nil incident, want true")
	}

	h.SetChannelService(services.NewChannelService())
	if !h.incidentThreadPostable(&database.Incident{}) {
		t.Error("incidentThreadPostable = false for empty source UUID, want true")
	}
}
