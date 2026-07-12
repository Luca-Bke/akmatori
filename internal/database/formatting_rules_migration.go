package database

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// migrateGlobalFormattingToRule converts a legacy enabled global
// FormattingSettings row into a single catch-all FormattingRule so that
// deployments upgrading from the singleton-formatter era keep formatting
// their responses. Runs only when the formatting_rules table is empty, so
// it is idempotent and never touches operator-managed rules. Deployments
// with formatting disabled get no rule — same raw-output behaviour as
// before the upgrade.
func migrateGlobalFormattingToRule(db *gorm.DB) error {
	// Reset GORM session state: earlier migration steps on the pinned
	// migration connection can leak statement state (table name, clauses)
	// into subsequent queries — without this the settings lookup below can
	// resolve against the wrong table.
	db = db.Session(&gorm.Session{NewDB: true})

	var ruleCount int64
	if err := db.Model(&FormattingRule{}).Count(&ruleCount).Error; err != nil {
		return fmt.Errorf("count formatting rules: %w", err)
	}
	if ruleCount > 0 {
		return nil
	}

	var settings FormattingSettings
	if err := db.Where("singleton_key = ?", "default").First(&settings).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return fmt.Errorf("load formatting settings: %w", err)
	}
	if !settings.Enabled {
		return nil
	}

	// The legacy default prompt carries field-specific guidance that predates
	// the schema-instruction injection; store blank so the rule inherits the
	// modern DefaultFormattingPrompt, matching the old formatter's substitution.
	systemPrompt := strings.TrimSpace(settings.SystemPrompt)
	if IsLegacyDefaultFormattingPrompt(systemPrompt) {
		systemPrompt = ""
	}

	rule := FormattingRule{
		UUID:                uuid.New().String(),
		Name:                "Global default (migrated)",
		Enabled:             true,
		Position:            0,
		SystemPrompt:        systemPrompt,
		OutputSchemaExample: strings.TrimSpace(settings.OutputSchemaExample),
		MaxTokens:           settings.MaxTokens,
		Temperature:         settings.Temperature,
	}
	if err := db.Create(&rule).Error; err != nil {
		return fmt.Errorf("create migrated formatting rule: %w", err)
	}
	slog.Info("migrated global formatting settings into catch-all formatting rule", "rule_uuid", rule.UUID)
	return nil
}
