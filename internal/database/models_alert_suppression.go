package database

import "time"

// AlertSuppressionLog records each suppression decision made by the AI gate so
// operators can audit why alerts were suppressed without investigation.
type AlertSuppressionLog struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	SourceUUID    string    `gorm:"type:varchar(36);index;not null" json:"source_uuid"`
	AlertName     string    `gorm:"type:varchar(255);not null" json:"alert_name"`
	TargetHost    string    `gorm:"type:varchar(255)" json:"target_host"`
	IncidentUUID  string    `gorm:"type:varchar(36);index;not null" json:"incident_uuid"`
	SignatureName string    `gorm:"type:varchar(255)" json:"signature_name"`
	Confidence    float64   `gorm:"not null" json:"confidence"`
	Reasoning     string    `gorm:"type:text" json:"reasoning"`
	CreatedAt     time.Time `json:"created_at"`
}

func (AlertSuppressionLog) TableName() string {
	return "alert_suppression_logs"
}
