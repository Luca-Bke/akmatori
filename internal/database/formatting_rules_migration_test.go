package database

import (
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupFormattingRulesMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	if err := db.AutoMigrate(&FormattingSettings{}, &FormattingRule{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

func TestMigrateGlobalFormattingToRule_NoSettingsRow_IsNoop(t *testing.T) {
	db := setupFormattingRulesMigrationDB(t)

	if err := migrateGlobalFormattingToRule(db); err != nil {
		t.Fatalf("migrateGlobalFormattingToRule: %v", err)
	}

	var count int64
	db.Model(&FormattingRule{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 rules, got %d", count)
	}
}

func TestMigrateGlobalFormattingToRule_DisabledSettings_IsNoop(t *testing.T) {
	db := setupFormattingRulesMigrationDB(t)
	settings := DefaultFormattingSettings()
	if err := db.Create(settings).Error; err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	if err := migrateGlobalFormattingToRule(db); err != nil {
		t.Fatalf("migrateGlobalFormattingToRule: %v", err)
	}

	var count int64
	db.Model(&FormattingRule{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 rules for disabled settings, got %d", count)
	}
}

func TestMigrateGlobalFormattingToRule_EnabledSettings_SeedsCatchAll(t *testing.T) {
	db := setupFormattingRulesMigrationDB(t)
	settings := &FormattingSettings{
		SingletonKey:        "default",
		Enabled:             true,
		SystemPrompt:        "custom operator prompt",
		OutputSchemaExample: `{"headline": "example"}`,
		MaxTokens:           2000,
		Temperature:         0.5,
	}
	if err := db.Create(settings).Error; err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	if err := migrateGlobalFormattingToRule(db); err != nil {
		t.Fatalf("migrateGlobalFormattingToRule: %v", err)
	}

	var rules []FormattingRule
	if err := db.Find(&rules).Error; err != nil {
		t.Fatalf("load rules: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 migrated rule, got %d", len(rules))
	}
	rule := rules[0]
	if !rule.Enabled {
		t.Error("migrated rule should be enabled")
	}
	if rule.MatchSourceKind != "" || rule.MatchSourceUUID != "" || rule.MatchChannelUUID != "" || rule.MatchLastSkill != "" {
		t.Errorf("migrated rule must be catch-all, got %+v", rule)
	}
	if rule.SystemPrompt != "custom operator prompt" {
		t.Errorf("system prompt = %q", rule.SystemPrompt)
	}
	if rule.OutputSchemaExample != `{"headline": "example"}` {
		t.Errorf("schema example = %q", rule.OutputSchemaExample)
	}
	if rule.MaxTokens != 2000 || rule.Temperature != 0.5 {
		t.Errorf("max_tokens/temperature = %d/%v", rule.MaxTokens, rule.Temperature)
	}
	if rule.UUID == "" {
		t.Error("migrated rule must carry a UUID")
	}

	// Idempotent: second run does not duplicate.
	if err := migrateGlobalFormattingToRule(db); err != nil {
		t.Fatalf("second run: %v", err)
	}
	var count int64
	db.Model(&FormattingRule{}).Count(&count)
	if count != 1 {
		t.Errorf("expected 1 rule after re-run, got %d", count)
	}
}

func TestMigrateGlobalFormattingToRule_LegacyPromptBlanked(t *testing.T) {
	db := setupFormattingRulesMigrationDB(t)
	settings := &FormattingSettings{
		SingletonKey:        "default",
		Enabled:             true,
		SystemPrompt:        legacyDefaultFormattingPrompt,
		OutputSchemaExample: `{"summary": "x"}`,
		MaxTokens:           1500,
		Temperature:         0.2,
	}
	if err := db.Create(settings).Error; err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	if err := migrateGlobalFormattingToRule(db); err != nil {
		t.Fatalf("migrateGlobalFormattingToRule: %v", err)
	}

	var rule FormattingRule
	if err := db.First(&rule).Error; err != nil {
		t.Fatalf("load rule: %v", err)
	}
	if rule.SystemPrompt != "" {
		t.Errorf("legacy default prompt should be blanked, got %q", rule.SystemPrompt)
	}
}

func TestMigrateGlobalFormattingToRule_ExistingRules_Untouched(t *testing.T) {
	db := setupFormattingRulesMigrationDB(t)
	settings := &FormattingSettings{SingletonKey: "default", Enabled: true}
	if err := db.Create(settings).Error; err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	existing := FormattingRule{UUID: uuid.New().String(), Name: "operator rule", Enabled: true}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("seed rule: %v", err)
	}

	if err := migrateGlobalFormattingToRule(db); err != nil {
		t.Fatalf("migrateGlobalFormattingToRule: %v", err)
	}

	var count int64
	db.Model(&FormattingRule{}).Count(&count)
	if count != 1 {
		t.Errorf("expected existing rule only, got %d rules", count)
	}
}
