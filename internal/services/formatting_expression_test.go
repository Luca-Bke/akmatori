package services

import (
	"strings"
	"testing"

	"github.com/akmatori/akmatori/internal/database"
)

func TestEvalMatchExpression_Comparisons(t *testing.T) {
	flow := FormatFlow{
		SourceKind:  "alert",
		TriggerUUID: "trig-1",
		ChannelUUID: "chan-1",
		LastSkill:   "netbox",
	}

	cases := []struct {
		expr string
		want bool
	}{
		{`source_kind == "alert"`, true},
		{`source_kind == "cron"`, false},
		{`source_kind != "cron"`, true},
		{`trigger == "trig-1"`, true},
		{`channel == "chan-1"`, true},
		{`skill == "netbox"`, true},
		{`last_skill == "netbox"`, true}, // alias
		{`skill == 'netbox'`, true},      // single quotes
		{`SKILL == "netbox"`, true},      // case-insensitive field
		{`skill = "netbox"`, true},       // single-equals typo tolerance
		{`skill == " netbox "`, true},    // value trimming
		{`skill == ""`, false},
		{`channel == ""`, false},
	}
	for _, tc := range cases {
		got, err := EvalMatchExpression(tc.expr, flow)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tc.expr, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s = %v, want %v", tc.expr, got, tc.want)
		}
	}
}

func TestEvalMatchExpression_BooleanLogic(t *testing.T) {
	flow := FormatFlow{SourceKind: "alert", ChannelUUID: "chan-1", LastSkill: "netbox"}

	cases := []struct {
		expr string
		want bool
	}{
		{`source_kind == "alert" && channel == "chan-1"`, true},
		{`source_kind == "alert" && channel == "other"`, false},
		{`source_kind == "cron" || skill == "netbox"`, true},
		{`source_kind == "cron" || skill == "grafana"`, false},
		{`!(source_kind == "cron")`, true},
		{`!(source_kind == "alert")`, false},
		{`not (source_kind == "cron")`, true},
		{`source_kind == "alert" AND (channel == "x" OR skill == "netbox")`, true},
		{`source_kind == "alert" and channel == "chan-1" or skill == "none"`, true},
		// Precedence: AND binds tighter than OR.
		{`skill == "none" || source_kind == "alert" && channel == "chan-1"`, true},
		{`(skill == "none" || source_kind == "alert") && channel == "none"`, false},
		{`!skill == "none" && source_kind == "alert"`, true}, // ! applies to the comparison
	}
	for _, tc := range cases {
		got, err := EvalMatchExpression(tc.expr, flow)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tc.expr, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s = %v, want %v", tc.expr, got, tc.want)
		}
	}
}

func TestValidateMatchExpression_Errors(t *testing.T) {
	cases := []struct {
		expr    string
		wantMsg string
	}{
		{`bogus == "x"`, "unknown field"},
		{`skill "netbox"`, "expected == or !="},
		{`skill == netbox`, "must be quoted"},
		{`skill == "netbox`, "unterminated string"},
		{`(skill == "netbox"`, "missing closing parenthesis"},
		{`skill == "a" && `, "expected a condition"},
		{`skill == "a" skill == "b"`, "unexpected"},
		{`&& skill == "a"`, "expected a field name"},
		{`!= "a"`, "expected a field name"},
		{`or`, "unknown field"},
	}
	for _, tc := range cases {
		err := ValidateMatchExpression(tc.expr)
		if err == nil {
			t.Errorf("%s: expected error containing %q, got nil", tc.expr, tc.wantMsg)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantMsg) {
			t.Errorf("%s: error %q does not contain %q", tc.expr, err.Error(), tc.wantMsg)
		}
		if !strings.Contains(err.Error(), "position") {
			t.Errorf("%s: error %q missing position info", tc.expr, err.Error())
		}
	}
}

func TestValidateMatchExpression_EmptyIsValid(t *testing.T) {
	if err := ValidateMatchExpression(""); err != nil {
		t.Errorf("empty expression must be valid, got %v", err)
	}
	if err := ValidateMatchExpression("   "); err != nil {
		t.Errorf("whitespace expression must be valid, got %v", err)
	}
}

func TestMatchFormattingRule_ExpressionRules(t *testing.T) {
	flow := FormatFlow{SourceKind: "alert", ChannelUUID: "chan-1", LastSkill: "netbox"}

	exprRule := rule("expr", 0, true, nil)
	exprRule.MatchExpression = `source_kind == "alert" && (channel == "chan-1" || skill == "grafana")`
	fallback := rule("catch-all", 1, true, nil)

	got := MatchFormattingRule([]database.FormattingRule{exprRule, fallback}, flow)
	if got == nil || got.Name != "expr" {
		t.Errorf("expected expression rule to match, got %v", got)
	}

	// Expression that doesn't match falls through to the next rule.
	exprRule.MatchExpression = `source_kind == "cron"`
	got = MatchFormattingRule([]database.FormattingRule{exprRule, fallback}, flow)
	if got == nil || got.Name != "catch-all" {
		t.Errorf("expected fall-through to catch-all, got %v", got)
	}

	// Invalid stored expression fails safe: rule skipped, no panic.
	exprRule.MatchExpression = `skill == broken`
	got = MatchFormattingRule([]database.FormattingRule{exprRule, fallback}, flow)
	if got == nil || got.Name != "catch-all" {
		t.Errorf("expected invalid-expression rule to be skipped, got %v", got)
	}

	// Expression takes precedence over (stale) simple fields on the same row.
	exprRule.MatchExpression = `skill == "netbox"`
	exprRule.MatchSourceKind = "cron" // would not match; must be ignored
	got = MatchFormattingRule([]database.FormattingRule{exprRule, fallback}, flow)
	if got == nil || got.Name != "expr" {
		t.Errorf("expected expression to override simple fields, got %v", got)
	}
}
