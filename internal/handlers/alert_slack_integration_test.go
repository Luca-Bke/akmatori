package handlers

import (
	"context"
	"os"
	"testing"

	internalslack "github.com/akmatori/akmatori/internal/slack"
	"github.com/akmatori/akmatori/internal/database"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupAlertSlackTestDB(t *testing.T) func() {
	t.Helper()

	prevDB := database.DB
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/test.db"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}

	if err := db.AutoMigrate(&database.SlackSettings{}, &database.ProxySettings{}, &database.GeneralSettings{}); err != nil {
		t.Fatalf("migrate sqlite db: %v", err)
	}

	database.DB = db
	return func() {
		database.DB = prevDB
	}
}

func TestResolveBaseURL_DBOverridesEnvAndTrimsSlash(t *testing.T) {
	cleanup := setupAlertSlackTestDB(t)
	defer cleanup()

	t.Setenv("AKMATORI_BASE_URL", "https://env.example.com")

	settings := &database.GeneralSettings{BaseURL: "https://db.example.com/"}
	if err := database.DB.Create(settings).Error; err != nil {
		t.Fatalf("create general settings: %v", err)
	}

	if got := resolveBaseURL(); got != "https://db.example.com" {
		t.Fatalf("resolveBaseURL() = %q, want %q", got, "https://db.example.com")
	}
}

func TestResolveBaseURL_UsesEnvWhenDBBaseURLEmpty(t *testing.T) {
	cleanup := setupAlertSlackTestDB(t)
	defer cleanup()

	t.Setenv("AKMATORI_BASE_URL", "https://env.example.com")

	if err := database.DB.Create(&database.GeneralSettings{}).Error; err != nil {
		t.Fatalf("create empty general settings: %v", err)
	}

	if got := resolveBaseURL(); got != "https://env.example.com" {
		t.Fatalf("resolveBaseURL() = %q, want %q", got, "https://env.example.com")
	}
}

func TestResolveBaseURL_FallsBackToLocalhost(t *testing.T) {
	cleanup := setupAlertSlackTestDB(t)
	defer cleanup()

	if err := os.Unsetenv("AKMATORI_BASE_URL"); err != nil {
		t.Fatalf("unset AKMATORI_BASE_URL: %v", err)
	}

	if got := resolveBaseURL(); got != "http://localhost:3000" {
		t.Fatalf("resolveBaseURL() = %q, want %q", got, "http://localhost:3000")
	}
}

func TestAlertHandler_IsSlackEnabled_DependsOnSettingsAndClient(t *testing.T) {
	cleanup := setupAlertSlackTestDB(t)
	defer cleanup()

	h := &AlertHandler{slackManager: internalslack.NewManager()}

	if got := h.isSlackEnabled(); got {
		t.Fatal("isSlackEnabled() = true with no settings row, want false")
	}

	settings := &database.SlackSettings{
		BotToken:      "xoxb-test-token",
		SigningSecret: "signing-secret",
		AppToken:      "xapp-test-token",
		AlertsChannel: "C_ALERTS",
		Enabled:       true,
	}
	if err := database.DB.Create(settings).Error; err != nil {
		t.Fatalf("create slack settings: %v", err)
	}

	if got := h.isSlackEnabled(); got {
		t.Fatal("isSlackEnabled() = true without Slack client, want false")
	}

	if err := h.slackManager.Start(context.Background()); err != nil {
		t.Fatalf("start slack manager: %v", err)
	}
	defer h.slackManager.Stop()

	if got := h.isSlackEnabled(); !got {
		t.Fatal("isSlackEnabled() = false with active settings and initialized client, want true")
	}
}
