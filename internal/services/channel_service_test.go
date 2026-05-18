package services

import (
	"errors"
	"testing"

	"github.com/akmatori/akmatori/internal/database"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupChannelServiceTest builds an in-memory sqlite DB with the channels
// schema applied and returns a ChannelService bound to it. The DB also has the
// partial-unique index installed so the per-integration default-post
// invariant is exercised end-to-end.
func setupChannelServiceTest(t *testing.T) (*ChannelService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&database.AlertSourceType{},
		&database.AlertSourceInstance{},
		&database.Integration{},
		&database.Channel{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	// Mirror the production partial-unique index so default-post conflicts
	// surface here just like they would in postgres.
	if err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_channels_default_post_per_integration ON channels (integration_id) WHERE is_default_post = true").Error; err != nil {
		t.Fatalf("partial index: %v", err)
	}
	return newChannelServiceWithDB(db), db
}

func seedSlackIntegration(t *testing.T, db *gorm.DB) *database.Integration {
	t.Helper()
	row := &database.Integration{
		UUID:     uuid.New().String(),
		Provider: database.MessagingProviderSlack,
		Name:     "Slack",
		Enabled:  true,
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("seed integration: %v", err)
	}
	return row
}

func TestChannelService_CreateIntegration_RejectsUnknownProvider(t *testing.T) {
	svc, _ := setupChannelServiceTest(t)

	_, err := svc.CreateIntegration(database.MessagingProvider("discord"), "Discord", nil, true)
	if err == nil {
		t.Fatal("CreateIntegration with unknown provider error = nil, want error")
	}
}

func TestChannelService_CreateIntegration_AssignsUUID(t *testing.T) {
	svc, _ := setupChannelServiceTest(t)

	got, err := svc.CreateIntegration(database.MessagingProviderSlack, "Slack Prod", database.JSONB{"bot_token": "x"}, true)
	if err != nil {
		t.Fatalf("CreateIntegration error = %v", err)
	}
	if got.UUID == "" {
		t.Errorf("CreateIntegration UUID is empty, want auto-generated UUID")
	}
	if got.Provider != database.MessagingProviderSlack {
		t.Errorf("CreateIntegration provider = %q, want slack", got.Provider)
	}
}

func TestChannelService_CreateChannel_DefaultsDisplayNameToExternalID(t *testing.T) {
	svc, db := setupChannelServiceTest(t)
	integration := seedSlackIntegration(t, db)

	got, err := svc.CreateChannel(&database.Channel{
		IntegrationID: integration.ID,
		ExternalID:    "C-incidents",
		CanPost:       true,
	})
	if err != nil {
		t.Fatalf("CreateChannel error = %v", err)
	}
	if got.DisplayName != "C-incidents" {
		t.Errorf("CreateChannel DisplayName = %q, want external_id fallback", got.DisplayName)
	}
}

func TestChannelService_CreateChannel_RejectsEmptyExternalID(t *testing.T) {
	svc, db := setupChannelServiceTest(t)
	integration := seedSlackIntegration(t, db)

	if _, err := svc.CreateChannel(&database.Channel{
		IntegrationID: integration.ID,
		ExternalID:    "   ",
		CanPost:       true,
	}); err == nil {
		t.Errorf("CreateChannel with blank external_id error = nil, want error")
	}
}

func TestChannelService_CreateChannel_RejectsSecondDefaultPerProvider(t *testing.T) {
	svc, db := setupChannelServiceTest(t)
	integration := seedSlackIntegration(t, db)
	if _, err := svc.CreateChannel(&database.Channel{
		IntegrationID: integration.ID,
		ExternalID:    "C-default",
		CanPost:       true,
		IsDefaultPost: true,
		Enabled:       true,
	}); err != nil {
		t.Fatalf("seed default channel: %v", err)
	}

	// A second integration of the same provider must not be allowed to also
	// host a default channel — the cross-integration invariant.
	second := &database.Integration{
		UUID:     uuid.New().String(),
		Provider: database.MessagingProviderSlack,
		Name:     "Slack Backup",
		Enabled:  true,
	}
	if err := db.Create(second).Error; err != nil {
		t.Fatalf("seed second integration: %v", err)
	}

	_, err := svc.CreateChannel(&database.Channel{
		IntegrationID: second.ID,
		ExternalID:    "C-second-default",
		CanPost:       true,
		IsDefaultPost: true,
		Enabled:       true,
	})
	if !errors.Is(err, ErrDuplicateDefaultPost) {
		t.Errorf("CreateChannel second default error = %v, want ErrDuplicateDefaultPost", err)
	}
}

func TestChannelService_CreateChannel_RejectsSecondDefaultSameIntegration(t *testing.T) {
	svc, db := setupChannelServiceTest(t)
	integration := seedSlackIntegration(t, db)
	if _, err := svc.CreateChannel(&database.Channel{
		IntegrationID: integration.ID,
		ExternalID:    "C-default",
		CanPost:       true,
		IsDefaultPost: true,
		Enabled:       true,
	}); err != nil {
		t.Fatalf("seed default channel: %v", err)
	}

	// Adding a second default on the same integration is blocked by the
	// service guard before it ever reaches the DB partial-unique index.
	_, err := svc.CreateChannel(&database.Channel{
		IntegrationID: integration.ID,
		ExternalID:    "C-another-default",
		CanPost:       true,
		IsDefaultPost: true,
		Enabled:       true,
	})
	if !errors.Is(err, ErrDuplicateDefaultPost) {
		t.Errorf("CreateChannel duplicate same-integration default error = %v, want ErrDuplicateDefaultPost", err)
	}
}

func TestChannelService_UpdateChannel_AllowsSelfReSaveAsDefault(t *testing.T) {
	svc, db := setupChannelServiceTest(t)
	integration := seedSlackIntegration(t, db)
	channel, err := svc.CreateChannel(&database.Channel{
		IntegrationID: integration.ID,
		ExternalID:    "C-default",
		CanPost:       true,
		IsDefaultPost: true,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("seed default channel: %v", err)
	}

	yes := true
	updated, err := svc.UpdateChannel(channel.UUID, ChannelUpdate{IsDefaultPost: &yes})
	if err != nil {
		t.Fatalf("UpdateChannel re-save as default error = %v, want nil", err)
	}
	if !updated.IsDefaultPost {
		t.Errorf("UpdateChannel re-save as default IsDefaultPost = false, want true")
	}
}

func TestChannelService_ResolveDefault_ReturnsConfiguredChannel(t *testing.T) {
	svc, db := setupChannelServiceTest(t)
	integration := seedSlackIntegration(t, db)
	created, err := svc.CreateChannel(&database.Channel{
		IntegrationID: integration.ID,
		ExternalID:    "C-default",
		CanPost:       true,
		IsDefaultPost: true,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("seed default channel: %v", err)
	}

	got, err := svc.ResolveDefault(database.MessagingProviderSlack)
	if err != nil {
		t.Fatalf("ResolveDefault error = %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("ResolveDefault returned channel id %d, want %d", got.ID, created.ID)
	}
	if got.Integration.Provider != database.MessagingProviderSlack {
		t.Errorf("ResolveDefault preloaded integration provider = %q, want slack", got.Integration.Provider)
	}
}

func TestChannelService_ResolveDefault_NoDefault_ReturnsErrChannelNotFound(t *testing.T) {
	svc, db := setupChannelServiceTest(t)
	integration := seedSlackIntegration(t, db)
	if _, err := svc.CreateChannel(&database.Channel{
		IntegrationID: integration.ID,
		ExternalID:    "C-other",
		CanPost:       true,
		Enabled:       true,
	}); err != nil {
		t.Fatalf("seed channel: %v", err)
	}

	_, err := svc.ResolveDefault(database.MessagingProviderSlack)
	if !errors.Is(err, ErrChannelNotFound) {
		t.Errorf("ResolveDefault error = %v, want ErrChannelNotFound", err)
	}
}

func TestChannelService_ResolveForAlertSource_PrefersExplicitChannel(t *testing.T) {
	svc, db := setupChannelServiceTest(t)
	integration := seedSlackIntegration(t, db)
	defaultChan, err := svc.CreateChannel(&database.Channel{
		IntegrationID: integration.ID,
		ExternalID:    "C-default",
		CanPost:       true,
		IsDefaultPost: true,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("seed default: %v", err)
	}
	explicit, err := svc.CreateChannel(&database.Channel{
		IntegrationID: integration.ID,
		ExternalID:    "C-explicit",
		CanPost:       true,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("seed explicit: %v", err)
	}

	asi := &database.AlertSourceInstance{NotificationChannelID: &explicit.ID}
	got, err := svc.ResolveForAlertSource(asi, database.MessagingProviderSlack)
	if err != nil {
		t.Fatalf("ResolveForAlertSource(explicit) error = %v", err)
	}
	if got.ID != explicit.ID {
		t.Errorf("ResolveForAlertSource returned id %d, want explicit id %d (default was %d)", got.ID, explicit.ID, defaultChan.ID)
	}
}

func TestChannelService_ResolveForAlertSource_FallsBackToDefault(t *testing.T) {
	svc, db := setupChannelServiceTest(t)
	integration := seedSlackIntegration(t, db)
	defaultChan, err := svc.CreateChannel(&database.Channel{
		IntegrationID: integration.ID,
		ExternalID:    "C-default",
		CanPost:       true,
		IsDefaultPost: true,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("seed default: %v", err)
	}

	asi := &database.AlertSourceInstance{}
	got, err := svc.ResolveForAlertSource(asi, database.MessagingProviderSlack)
	if err != nil {
		t.Fatalf("ResolveForAlertSource(no explicit) error = %v", err)
	}
	if got.ID != defaultChan.ID {
		t.Errorf("ResolveForAlertSource fallback returned id %d, want default id %d", got.ID, defaultChan.ID)
	}
}

func TestChannelService_ResolveForAlertSource_StaleFKFallsBackToDefault(t *testing.T) {
	svc, db := setupChannelServiceTest(t)
	integration := seedSlackIntegration(t, db)
	defaultChan, err := svc.CreateChannel(&database.Channel{
		IntegrationID: integration.ID,
		ExternalID:    "C-default",
		CanPost:       true,
		IsDefaultPost: true,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("seed default: %v", err)
	}

	staleID := defaultChan.ID + 9999
	asi := &database.AlertSourceInstance{NotificationChannelID: &staleID}

	got, err := svc.ResolveForAlertSource(asi, database.MessagingProviderSlack)
	if err != nil {
		t.Fatalf("ResolveForAlertSource(stale fk) error = %v", err)
	}
	if got.ID != defaultChan.ID {
		t.Errorf("ResolveForAlertSource stale fk returned id %d, want default id %d", got.ID, defaultChan.ID)
	}
}

func TestChannelService_ListChannels_FilterByCanListen(t *testing.T) {
	svc, db := setupChannelServiceTest(t)
	integration := seedSlackIntegration(t, db)
	if _, err := svc.CreateChannel(&database.Channel{
		IntegrationID: integration.ID,
		ExternalID:    "C-listener",
		CanListen:     true,
		Enabled:       true,
	}); err != nil {
		t.Fatalf("seed listener: %v", err)
	}
	if _, err := svc.CreateChannel(&database.Channel{
		IntegrationID: integration.ID,
		ExternalID:    "C-poster",
		CanPost:       true,
		Enabled:       true,
	}); err != nil {
		t.Fatalf("seed poster: %v", err)
	}

	yes := true
	rows, err := svc.ListChannels(ListChannelsFilter{CanListen: &yes})
	if err != nil {
		t.Fatalf("ListChannels error = %v", err)
	}
	if len(rows) != 1 || rows[0].ExternalID != "C-listener" {
		t.Errorf("ListChannels CanListen=true returned %+v, want exactly the listener row", rows)
	}
}

func TestChannelService_DeleteIntegration_CascadesChannels(t *testing.T) {
	svc, db := setupChannelServiceTest(t)
	integration := seedSlackIntegration(t, db)
	if _, err := svc.CreateChannel(&database.Channel{
		IntegrationID: integration.ID,
		ExternalID:    "C-x",
		CanPost:       true,
	}); err != nil {
		t.Fatalf("seed channel: %v", err)
	}

	if err := svc.DeleteIntegration(integration.UUID); err != nil {
		t.Fatalf("DeleteIntegration error = %v", err)
	}
	var remaining int64
	db.Model(&database.Channel{}).Where("integration_id = ?", integration.ID).Count(&remaining)
	if remaining != 0 {
		t.Errorf("DeleteIntegration left %d channels behind, want 0", remaining)
	}
}
