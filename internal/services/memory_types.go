package services

// Memory type constants — single source of truth used by validation,
// prompt rendering, and the extractor's JSON schema.
const (
	MemoryTypeHost            = "host"
	MemoryTypeIncidentPattern = "incident_pattern"
	MemoryTypeToolQuirk       = "tool_quirk"
	MemoryTypeFeedback        = "feedback"
)

// MemoryScopeGlobal is the reserved scope name for memories visible to every
// incident (injected into the incident-manager AGENTS.md). Any other scope
// value is treated as a skill name.
const MemoryScopeGlobal = "global"

// MemoryCreatedByAgent / MemoryCreatedByOperator are the only allowed values
// for Memory.CreatedBy. Empty is also valid (legacy/manual rows).
const (
	MemoryCreatedByAgent    = "agent"
	MemoryCreatedByOperator = "operator"
)

// Body / description / name caps. These are enforced by the service layer.
//
// MemoryNameMaxLen is bounded BELOW the typical filesystem NAME_MAX of 255
// bytes. The on-disk memory file is named `<id>-<name>.md`, so a 255-char
// name combined with a multi-digit ID and the ".md" suffix would push the
// final filename past NAME_MAX and make os.WriteFile fail with ENAMETOOLONG
// — leaving the DB row in place while every subsequent SyncMemoryFiles
// call kept failing on the same path. 200 reserves >40 bytes of headroom
// for the longest realistic ID + ".md" + safety margin.
const (
	MemoryNameMaxLen        = 200
	MemoryDescriptionMaxLen = 500
	MemoryBodyMaxBytes      = 8 * 1024
)

// AllMemoryTypes returns all valid memory type values in canonical order.
// Used for prompt rendering and validation.
func AllMemoryTypes() []string {
	return []string{
		MemoryTypeHost,
		MemoryTypeIncidentPattern,
		MemoryTypeToolQuirk,
		MemoryTypeFeedback,
	}
}

// ValidMemoryType reports whether s is one of the four canonical memory types.
func ValidMemoryType(s string) bool {
	switch s {
	case MemoryTypeHost, MemoryTypeIncidentPattern, MemoryTypeToolQuirk, MemoryTypeFeedback:
		return true
	}
	return false
}
