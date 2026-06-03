package adapters

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/akmatori/akmatori/internal/alerts"
	"github.com/akmatori/akmatori/internal/database"
)

// ZabbixAdapter handles Zabbix webhooks
type ZabbixAdapter struct {
	alerts.BaseAdapter
}

// NewZabbixAdapter creates a new Zabbix adapter
func NewZabbixAdapter() *ZabbixAdapter {
	return &ZabbixAdapter{
		BaseAdapter: alerts.BaseAdapter{SourceType: "zabbix"},
	}
}

// ZabbixPayload represents the webhook payload from Zabbix
type ZabbixPayload struct {
	EventTime         string `json:"event_time"`
	AlertName         string `json:"alert_name"`
	Severity          string `json:"severity"`
	Priority          string `json:"priority"`
	MetricName        string `json:"metric_name"`
	MetricValue       string `json:"metric_value"`
	TriggerExpression string `json:"trigger_expression"`
	PendingDuration   string `json:"pending_duration"`
	EventID           string `json:"event_id"`
	Hardware          string `json:"hardware"`
	EventStatus       string `json:"event_status"`
	RunbookURL        string `json:"runbook_url"`
}

// ValidateWebhookSecret validates the Zabbix webhook secret header
func (a *ZabbixAdapter) ValidateWebhookSecret(r *http.Request, instance *database.AlertSourceInstance) error {
	if instance.WebhookSecret == "" {
		return nil // No secret configured, allow request
	}

	secret := r.Header.Get("X-Zabbix-Secret")
	if secret != instance.WebhookSecret {
		return fmt.Errorf("invalid webhook secret")
	}

	return nil
}

// ParsePayload parses Zabbix webhook payload into normalized alerts
func (a *ZabbixAdapter) ParsePayload(body []byte, instance *database.AlertSourceInstance) ([]alerts.NormalizedAlert, error) {
	var payload ZabbixPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("failed to parse zabbix payload: %w", err)
	}

	// Capture all raw fields including ones not in ZabbixPayload struct
	var rawFields map[string]interface{}
	if err := json.Unmarshal(body, &rawFields); err != nil {
		rawFields = nil
	}

	// Get field mappings (use instance override or defaults)
	mappings := alerts.MergeMappings(a.GetDefaultMappings(), instance.FieldMappings)

	n := a.parseAlert(payload, rawFields, mappings)
	return []alerts.NormalizedAlert{n}, nil
}

func (a *ZabbixAdapter) parseAlert(payload ZabbixPayload, rawFields map[string]interface{}, mappings database.JSONB) alerts.NormalizedAlert {
	// Start with all raw fields from the original webhook payload
	// This preserves any extra fields not defined in ZabbixPayload struct
	payloadMap := make(map[string]interface{})
	for k, v := range rawFields {
		payloadMap[k] = v
	}
	// Overlay known struct fields to ensure consistent types
	payloadMap["event_time"] = payload.EventTime
	payloadMap["alert_name"] = payload.AlertName
	payloadMap["severity"] = payload.Severity
	payloadMap["priority"] = payload.Priority
	payloadMap["metric_name"] = payload.MetricName
	payloadMap["metric_value"] = payload.MetricValue
	payloadMap["trigger_expression"] = payload.TriggerExpression
	payloadMap["pending_duration"] = payload.PendingDuration
	payloadMap["event_id"] = payload.EventID
	payloadMap["hardware"] = payload.Hardware
	payloadMap["event_status"] = payload.EventStatus
	payloadMap["runbook_url"] = payload.RunbookURL

	alertName := alerts.ExtractString(payloadMap, getMapping(mappings, "alert_name"))
	if alertName == "" {
		alertName = payload.AlertName
	}

	severityText := alerts.ExtractString(payloadMap, getMapping(mappings, "severity"))
	if severityText == "" {
		severityText = payload.Priority
	}
	severity := alerts.NormalizeSeverity(severityText, alerts.DefaultSeverityMapping)

	statusText := alerts.ExtractString(payloadMap, getMapping(mappings, "status"))
	if statusText == "" {
		statusText = payload.EventStatus
	}
	status := alerts.NormalizeStatus(statusText)

	summary := alerts.ExtractString(payloadMap, getMapping(mappings, "summary"))
	if summary == "" {
		summary = payload.TriggerExpression
	}

	targetHost := alerts.ExtractString(payloadMap, getMapping(mappings, "target_host"))
	if targetHost == "" {
		targetHost = payload.Hardware
	}

	metricName := alerts.ExtractString(payloadMap, getMapping(mappings, "metric_name"))
	if metricName == "" {
		metricName = payload.MetricName
	}

	metricValue := alerts.ExtractString(payloadMap, getMapping(mappings, "metric_value"))
	if metricValue == "" {
		metricValue = payload.MetricValue
	}

	runbookURL := alerts.ExtractString(payloadMap, getMapping(mappings, "runbook_url"))
	if runbookURL == "" {
		runbookURL = payload.RunbookURL
	}

	sourceAlertID := alerts.ExtractString(payloadMap, getMapping(mappings, "source_alert_id"))
	if sourceAlertID == "" {
		sourceAlertID = payload.EventID
	}

	// Parse event time
	var startedAt *time.Time
	startedAtText := alerts.ExtractString(payloadMap, getMapping(mappings, "started_at"))
	if startedAtText == "" {
		startedAtText = payload.EventTime
	}
	if startedAtText != "" {
		if t, err := time.Parse("2006-01-02 15:04:05", startedAtText); err == nil {
			startedAt = &t
		} else if t, err := time.Parse(time.RFC3339, startedAtText); err == nil {
			startedAt = &t
		}
	}

	// Build target labels
	targetLabels := map[string]string{
		"hardware":           targetHost,
		"trigger_expression": payload.TriggerExpression,
		"pending_duration":   payload.PendingDuration,
	}

	return alerts.NormalizedAlert{
		AlertName:         alertName,
		Severity:          severity,
		Status:            status,
		Summary:           summary,
		Description:       fmt.Sprintf("Metric: %s = %s\nTrigger: %s", metricName, metricValue, summary),
		TargetHost:        targetHost,
		TargetService:     "",
		TargetLabels:      targetLabels,
		MetricName:        metricName,
		MetricValue:       metricValue,
		RunbookURL:        runbookURL,
		StartedAt:         startedAt,
		SourceAlertID:     sourceAlertID,
		SourceFingerprint: sourceAlertID,
		RawPayload:        payloadMap,
	}
}

// GetDefaultMappings returns the default field mappings for Zabbix
func (a *ZabbixAdapter) GetDefaultMappings() database.JSONB {
	return database.JSONB{
		"alert_name":      "alert_name",
		"severity":        "priority",
		"status":          "event_status",
		"summary":         "trigger_expression",
		"target_host":     "hardware",
		"metric_name":     "metric_name",
		"metric_value":    "metric_value",
		"runbook_url":     "runbook_url",
		"source_alert_id": "event_id",
		"started_at":      "event_time",
	}
}
