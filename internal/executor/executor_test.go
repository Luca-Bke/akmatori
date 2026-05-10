package executor

import (
	"strings"
	"testing"
)

// TestPrependGuidance_ScopesRunbookSearchToRunbooksCollection guards against
// the runbook-search guidance regressing to an unscoped qmd.query. With the
// memories collection now indexed by QMD, an unscoped search would surface
// memory documents during the "search runbooks first" workflow and the
// agent might fetch/follow them as runbooks.
func TestPrependGuidance_ScopesRunbookSearchToRunbooksCollection(t *testing.T) {
	out := PrependGuidance("test task")
	for _, want := range []string{
		`gateway_call("qmd.query"`,
		`"collection": "runbooks"`,
		`"type": "lex"`,
		`gateway_call("qmd.get"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("PrependGuidance() missing %q\nfull output:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "test task") {
		t.Errorf("PrependGuidance() should append the user task, got:\n%s", out)
	}
}
