package database

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// channelsDefaultPostIndexName is the name of the partial-unique index that
// enforces at most one Channel.IsDefaultPost=true per Integration.
const channelsDefaultPostIndexName = "idx_channels_default_post_per_integration"

// ensureChannelsDefaultPartialIndex creates the partial-unique index that
// enforces at most one default-post Channel per Integration. Idempotent
// (uses IF NOT EXISTS); supports postgres and sqlite (the test backend).
func ensureChannelsDefaultPartialIndex(db *gorm.DB) error {
	// Both postgres and sqlite support `CREATE UNIQUE INDEX ... WHERE ...`.
	// `IF NOT EXISTS` works on both.
	stmt := fmt.Sprintf(
		"CREATE UNIQUE INDEX IF NOT EXISTS %s ON channels (integration_id) WHERE is_default_post = true",
		channelsDefaultPostIndexName,
	)
	if err := db.Exec(stmt).Error; err != nil {
		return fmt.Errorf("create %s: %w", channelsDefaultPostIndexName, err)
	}
	return nil
}

// migrateSlackSettingsToIntegrations performs the read-old → write-new
// backfill from the legacy SlackSettings singleton row into one Integration
// (provider=slack) and one Channel (is_default_post=true) for the configured
// alerts_channel.
//
// Idempotent on re-run: if an integration of provider=slack already exists
// the function returns immediately without touching anything. The legacy
// slack_settings row is left in place to act as a fallback read until the
// cleanup task in Task 10.
func migrateSlackSettingsToIntegrations(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		// Bail out if any Integration row already exists for the slack
		// provider — that's the marker that this migration step has run.
		var existing int64
		if err := tx.Model(&Integration{}).
			Where("provider = ?", MessagingProviderSlack).
			Count(&existing).Error; err != nil {
			return fmt.Errorf("count existing slack integrations: %w", err)
		}
		if existing > 0 {
			return nil
		}

		// Read the legacy slack_settings row (if any). Missing table or
		// missing row are both "nothing to migrate".
		if !tx.Migrator().HasTable(&SlackSettings{}) {
			return nil
		}
		var legacy SlackSettings
		err := tx.First(&legacy).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read legacy slack_settings: %w", err)
		}

		// Only migrate when the operator actually configured Slack (tokens
		// present). An empty default row should not produce a half-filled
		// Integration on first startup.
		if !legacy.IsConfigured() {
			return nil
		}

		credentials := JSONB{
			"bot_token":      legacy.BotToken,
			"signing_secret": legacy.SigningSecret,
			"app_token":      legacy.AppToken,
		}
		integration := &Integration{
			UUID:        uuid.New().String(),
			Provider:    MessagingProviderSlack,
			Name:        "Slack",
			Credentials: credentials,
			Enabled:     legacy.Enabled,
		}
		if err := tx.Create(integration).Error; err != nil {
			return fmt.Errorf("create slack integration: %w", err)
		}

		// Backfill the default outbound channel only when alerts_channel was
		// set — an enabled-but-empty configuration is permitted.
		if legacy.AlertsChannel != "" {
			defaultChannel := &Channel{
				UUID:          uuid.New().String(),
				IntegrationID: integration.ID,
				ExternalID:    legacy.AlertsChannel,
				DisplayName:   legacy.AlertsChannel,
				CanPost:       true,
				CanListen:     false,
				IsDefaultPost: true,
				Enabled:       true,
			}
			if err := tx.Create(defaultChannel).Error; err != nil {
				return fmt.Errorf("create default slack channel: %w", err)
			}
		}

		slog.Info("migrated slack_settings into integrations + default channel", "integration_id", integration.ID)
		return nil
	})
}

// migrateSlackChannelAlertSourcesToChannels converts each existing
// AlertSourceInstance of type "slack_channel" into a Channel row with
// can_listen=true, copying the extraction prompt and process_human_messages
// flag out of the legacy Settings JSONB. The migrated AlertSourceInstance row
// is deleted at the end of the same transaction.
//
// Idempotent: if the slack_channel alert source type does not exist or has no
// active instances, the function returns without changes.
func migrateSlackChannelAlertSourcesToChannels(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var sourceType AlertSourceType
		err := tx.Where("name = ?", "slack_channel").First(&sourceType).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read slack_channel alert_source_type: %w", err)
		}

		var instances []AlertSourceInstance
		if err := tx.Where("alert_source_type_id = ?", sourceType.ID).Find(&instances).Error; err != nil {
			return fmt.Errorf("list slack_channel alert source instances: %w", err)
		}
		if len(instances) == 0 {
			return nil
		}

		// We need a Slack Integration to attach the new Channels to. If the
		// previous migration step did not create one (e.g. operator never
		// configured slack_settings but still had slack_channel rows from a
		// prior dev run), create a placeholder so the listener migration
		// remains complete on its own.
		var integration Integration
		err = tx.Where("provider = ?", MessagingProviderSlack).First(&integration).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			integration = Integration{
				UUID:        uuid.New().String(),
				Provider:    MessagingProviderSlack,
				Name:        "Slack",
				Credentials: JSONB{},
				Enabled:     false,
			}
			if err := tx.Create(&integration).Error; err != nil {
				return fmt.Errorf("create placeholder slack integration: %w", err)
			}
			// GORM v2 omits the zero-value Enabled=false from INSERT, so
			// the column-level `default:true` would otherwise materialize
			// the placeholder as enabled despite having empty credentials.
			// Force it disabled so the listener path's enabled check
			// correctly skips the placeholder until an operator fills in
			// credentials.
			if err := tx.Model(&integration).Update("enabled", false).Error; err != nil {
				return fmt.Errorf("disable placeholder slack integration: %w", err)
			}
		} else if err != nil {
			return fmt.Errorf("lookup slack integration: %w", err)
		}

		for _, inst := range instances {
			externalID, _ := inst.Settings["slack_channel_id"].(string)
			if externalID == "" {
				externalID, _ = inst.Settings["channel_id"].(string)
			}
			if externalID == "" {
				// Without an external channel ID the row is unusable.
				// Skip it but still delete the originating AlertSourceInstance
				// row so the migration is convergent on re-run.
				slog.Warn("slack_channel alert source instance missing channel id, dropping during migration",
					"instance_id", inst.ID, "name", inst.Name)
				if err := tx.Delete(&AlertSourceInstance{}, inst.ID).Error; err != nil {
					return fmt.Errorf("delete unmigrated alert source instance %d: %w", inst.ID, err)
				}
				continue
			}

			// If a Channel with this external id already exists on the
			// integration (e.g. partial prior migration), update its
			// listener fields in place rather than creating a duplicate.
			var existing Channel
			lookup := tx.Where("integration_id = ? AND external_id = ?", integration.ID, externalID).First(&existing)
			extractionPrompt, _ := inst.Settings["extraction_prompt"].(string)
			processHumanMessages, _ := inst.Settings["process_human_messages"].(bool)

			if errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
				channel := &Channel{
					UUID:                 uuid.New().String(),
					IntegrationID:        integration.ID,
					ExternalID:           externalID,
					DisplayName:          fallbackDisplayName(inst.Name, externalID),
					CanPost:              false,
					CanListen:            true,
					IsDefaultPost:        false,
					ExtractionPrompt:     extractionPrompt,
					ProcessHumanMessages: processHumanMessages,
					Enabled:              inst.Enabled,
				}
				if err := tx.Create(channel).Error; err != nil {
					return fmt.Errorf("create listener channel for instance %d: %w", inst.ID, err)
				}
			} else if lookup.Error != nil {
				return fmt.Errorf("lookup existing listener channel: %w", lookup.Error)
			} else {
				updates := map[string]interface{}{
					"can_listen":             true,
					"extraction_prompt":      extractionPrompt,
					"process_human_messages": processHumanMessages,
				}
				if err := tx.Model(&existing).Updates(updates).Error; err != nil {
					return fmt.Errorf("update existing listener channel %d: %w", existing.ID, err)
				}
			}

			if err := tx.Delete(&AlertSourceInstance{}, inst.ID).Error; err != nil {
				return fmt.Errorf("delete migrated alert source instance %d: %w", inst.ID, err)
			}
			slog.Info("migrated slack_channel alert source to channel",
				"instance_id", inst.ID, "external_id", externalID)
		}

		return nil
	})
}

// fallbackDisplayName picks the best available human-readable label for a
// migrated channel: the instance name if it has one, otherwise the external
// channel id.
func fallbackDisplayName(name, externalID string) string {
	if name != "" {
		return name
	}
	return externalID
}

// deprecateSlackChannelAlertSourceType flips the Deprecated flag on the
// "slack_channel" alert_source_types row so it is hidden from the UI/pickers.
// The row itself is preserved so that any historical incident or audit log
// referencing it remains resolvable. Idempotent.
func deprecateSlackChannelAlertSourceType(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var sourceType AlertSourceType
		err := tx.Where("name = ?", "slack_channel").First(&sourceType).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read slack_channel alert_source_type: %w", err)
		}
		if sourceType.Deprecated {
			return nil
		}
		if err := tx.Model(&sourceType).Update("deprecated", true).Error; err != nil {
			return fmt.Errorf("mark slack_channel alert_source_type deprecated: %w", err)
		}
		slog.Info("marked slack_channel alert_source_type as deprecated")
		return nil
	})
}
