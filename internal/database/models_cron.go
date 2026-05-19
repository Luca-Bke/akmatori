package database

import "time"

// CronJobMode controls how a CronJob fires: a single one-shot LLM call, or a
// full agent investigation that creates an Incident.
type CronJobMode string

const (
	CronJobModeOneshot CronJobMode = "oneshot"
	CronJobModeAgent   CronJobMode = "agent"
)

// IsValidCronJobMode reports whether the given mode string is a known cron job
// execution mode.
func IsValidCronJobMode(mode string) bool {
	switch CronJobMode(mode) {
	case CronJobModeOneshot, CronJobModeAgent:
		return true
	}
	return false
}

// CronJobRunStatus is the recorded status of the last cron tick.
const (
	CronJobRunStatusOK    = "ok"
	CronJobRunStatusError = "error"
)

// CronJob represents a scheduled task. Each job runs on its own cron schedule
// (parsed via robfig/cron/v3), executes either a one-shot LLM call or a full
// agent investigation, and posts results to a Channel.
//
// MVP uses global agent settings (skills, tool allowlist, LLM settings);
// per-cron overrides are a follow-up.
type CronJob struct {
	ID            uint        `gorm:"primaryKey" json:"id"`
	UUID          string      `gorm:"uniqueIndex;size:36;not null" json:"uuid"`
	Name          string      `gorm:"uniqueIndex;size:128;not null" json:"name"`
	Description   string      `gorm:"type:text" json:"description"`
	Schedule      string      `gorm:"size:128;not null" json:"schedule"`
	Prompt        string      `gorm:"type:text;not null" json:"prompt"`
	Mode          CronJobMode `gorm:"type:varchar(16);not null;default:'oneshot'" json:"mode"`
	ChannelID     *uint       `gorm:"index" json:"channel_id"`
	Enabled       bool        `gorm:"default:true" json:"enabled"`
	LastRunAt     *time.Time  `json:"last_run_at,omitempty"`
	LastRunStatus string      `gorm:"size:16" json:"last_run_status"`
	LastRunError  string      `gorm:"type:text" json:"last_run_error"`
	NextRunAt     *time.Time  `json:"next_run_at,omitempty"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`

	Channel *Channel `gorm:"foreignKey:ChannelID" json:"channel,omitempty"`
}

func (CronJob) TableName() string {
	return "cron_jobs"
}
