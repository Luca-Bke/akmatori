package messaging

import (
	"context"

	"github.com/akmatori/akmatori/internal/database"
)

// TelegramProvider is a placeholder kept so the data model can already record
// telegram-typed Integration / Channel rows. Every method returns
// ErrNotImplemented so the gap is loud rather than silently swallowed.
// Operators see the failure on the next post; the API layer must not crash
// when a telegram channel slips through configuration validation.
type TelegramProvider struct{}

// NewTelegramProvider returns the stub provider. The real implementation will
// replace this file once the Telegram client lands.
func NewTelegramProvider() *TelegramProvider {
	return &TelegramProvider{}
}

func (TelegramProvider) Name() database.MessagingProvider { return database.MessagingProviderTelegram }

func (TelegramProvider) PostMessage(_ context.Context, _ *database.Channel, _ string) (*PostedMessage, error) {
	return nil, ErrNotImplemented
}

func (TelegramProvider) PostThreadReply(_ context.Context, _ *database.Channel, _, _ string) (*PostedMessage, error) {
	return nil, ErrNotImplemented
}

func (TelegramProvider) UpdateMessage(_ context.Context, _ *database.Channel, _, _ string) error {
	return ErrNotImplemented
}
