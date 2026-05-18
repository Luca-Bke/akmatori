package handlers

import (
	"testing"

	"github.com/akmatori/akmatori/internal/database"
	"github.com/akmatori/akmatori/internal/services"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupListenerChannelsDB stands up an in-memory sqlite DB with the channels
// schema applied and the global database.DB pointed at it. The teardown
// restores the prior handle so this fixture composes with sibling tests.
func setupListenerChannelsDB(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	prevDB := database.DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&database.Integration{},
		&database.Channel{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	database.DB = db
	return db, func() { database.DB = prevDB }
}

// seedListenerChannel inserts an enabled Integration + Channel pair with
// can_listen=true and returns the Channel for assertions.
func seedListenerChannel(t *testing.T, db *gorm.DB, externalID, displayName, extractionPrompt string, processHuman bool) *database.Channel {
	t.Helper()
	integ := &database.Integration{
		UUID:     uuid.New().String(),
		Provider: database.MessagingProviderSlack,
		Name:     "Slack",
		Enabled:  true,
	}
	if err := db.Create(integ).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	ch := &database.Channel{
		UUID:                 uuid.New().String(),
		IntegrationID:        integ.ID,
		ExternalID:           externalID,
		DisplayName:          displayName,
		CanListen:            true,
		ExtractionPrompt:     extractionPrompt,
		ProcessHumanMessages: processHuman,
		Enabled:              true,
	}
	if err := db.Create(ch).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	return ch
}

// TestSlackHandler_LoadListenerChannels_FromChannelsTable verifies the
// post-Task-6 listener loader sources its map exclusively from the channels
// table. A can_listen=true row appears in the map keyed by ExternalID; the
// channel's ExtractionPrompt and ProcessHumanMessages travel into the in-memory
// record so handleAlertChannelMessage can read them without re-querying.
func TestSlackHandler_LoadListenerChannels_FromChannelsTable(t *testing.T) {
	db, cleanup := setupListenerChannelsDB(t)
	defer cleanup()

	seeded := seedListenerChannel(t, db, "C_LISTENER", "#monitoring",
		"extract host and severity", true)

	h := NewSlackHandler(nil, nil, nil, nil, nil)
	h.SetChannelService(services.NewChannelService())

	if err := h.LoadListenerChannels(); err != nil {
		t.Fatalf("LoadListenerChannels returned error: %v", err)
	}

	got, ok := h.isAlertChannel("C_LISTENER")
	if !ok {
		t.Fatal("C_LISTENER should be loaded as a listener channel")
	}
	if got.UUID != seeded.UUID {
		t.Errorf("listener channel UUID = %q, want %q", got.UUID, seeded.UUID)
	}
	if got.ExtractionPrompt != "extract host and severity" {
		t.Errorf("ExtractionPrompt = %q, want %q", got.ExtractionPrompt, "extract host and severity")
	}
	if !got.ProcessHumanMessages {
		t.Error("ProcessHumanMessages = false, want true")
	}
}

// TestSlackHandler_LoadListenerChannels_FiltersByCanListen verifies channels
// with can_listen=false are not loaded — they are post-only destinations.
func TestSlackHandler_LoadListenerChannels_FiltersByCanListen(t *testing.T) {
	db, cleanup := setupListenerChannelsDB(t)
	defer cleanup()

	// Post-only channel (no can_listen).
	integ := &database.Integration{
		UUID:     uuid.New().String(),
		Provider: database.MessagingProviderSlack,
		Name:     "Slack",
		Enabled:  true,
	}
	if err := db.Create(integ).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	postOnly := &database.Channel{
		UUID:          uuid.New().String(),
		IntegrationID: integ.ID,
		ExternalID:    "C_POST_ONLY",
		DisplayName:   "#outbound",
		CanPost:       true,
		CanListen:     false,
		Enabled:       true,
	}
	if err := db.Create(postOnly).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	h := NewSlackHandler(nil, nil, nil, nil, nil)
	h.SetChannelService(services.NewChannelService())

	if err := h.LoadListenerChannels(); err != nil {
		t.Fatalf("LoadListenerChannels returned error: %v", err)
	}

	if _, ok := h.isAlertChannel("C_POST_ONLY"); ok {
		t.Error("post-only channel must not appear in listener map")
	}
}

// TestSlackHandler_LoadListenerChannels_SkipsDisabledIntegration verifies a
// listener channel whose parent integration is disabled does not get loaded —
// the integration toggle is the operator's emergency stop. We create the row
// enabled then flip the column via UPDATE because GORM v2 omits zero-value
// bools from INSERT, so the gorm:"default:true" tag would otherwise restore
// the field to true on create.
func TestSlackHandler_LoadListenerChannels_SkipsDisabledIntegration(t *testing.T) {
	db, cleanup := setupListenerChannelsDB(t)
	defer cleanup()

	integ := &database.Integration{
		UUID:     uuid.New().String(),
		Provider: database.MessagingProviderSlack,
		Name:     "Slack",
		Enabled:  true,
	}
	if err := db.Create(integ).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	if err := db.Model(integ).Update("enabled", false).Error; err != nil {
		t.Fatalf("disable integration: %v", err)
	}
	ch := &database.Channel{
		UUID:          uuid.New().String(),
		IntegrationID: integ.ID,
		ExternalID:    "C_INACTIVE",
		DisplayName:   "#inactive",
		CanListen:     true,
		Enabled:       true,
	}
	if err := db.Create(ch).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	h := NewSlackHandler(nil, nil, nil, nil, nil)
	h.SetChannelService(services.NewChannelService())

	if err := h.LoadListenerChannels(); err != nil {
		t.Fatalf("LoadListenerChannels returned error: %v", err)
	}

	if _, ok := h.isAlertChannel("C_INACTIVE"); ok {
		t.Error("channel under disabled integration must not be loaded")
	}
}

// TestSlackHandler_LoadListenerChannels_NoChannelService verifies graceful
// degradation when ChannelManager is not wired. The pre-Task-6 alias must
// behave identically.
func TestSlackHandler_LoadListenerChannels_NoChannelService(t *testing.T) {
	h := NewSlackHandler(nil, nil, nil, nil, nil)
	if err := h.LoadListenerChannels(); err != nil {
		t.Errorf("LoadListenerChannels with nil service should not error: %v", err)
	}
}

// TestSlackHandler_ReloadListenerChannels_PicksUpNewRows verifies that
// re-running the loader picks up new Channel rows added since the last load —
// this is the Reload path the API handler triggers after Channel CRUD.
func TestSlackHandler_ReloadListenerChannels_PicksUpNewRows(t *testing.T) {
	db, cleanup := setupListenerChannelsDB(t)
	defer cleanup()

	h := NewSlackHandler(nil, nil, nil, nil, nil)
	h.SetChannelService(services.NewChannelService())

	if err := h.LoadListenerChannels(); err != nil {
		t.Fatalf("initial LoadListenerChannels: %v", err)
	}
	if _, ok := h.isAlertChannel("C_NEW"); ok {
		t.Fatal("C_NEW should not exist before seeding")
	}

	seedListenerChannel(t, db, "C_NEW", "#new", "", false)

	h.ReloadListenerChannels()
	if _, ok := h.isAlertChannel("C_NEW"); !ok {
		t.Error("ReloadListenerChannels should pick up newly inserted Channel")
	}
}
