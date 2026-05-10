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
//
// It also pins the {lex, vec, hyde} triplet shape so the user-turn reminder
// stays in sync with DefaultIncidentManagerPrompt's runbook-search section:
// a single qmd.query carrying THREE searches[] entries — one per retrieval
// mode (lex/vec/hyde), all three carrying the same natural-language alert
// summary, fused by QMD via RRF, with retry guidance capped at 3 total calls.
func TestPrependGuidance_ScopesRunbookSearchToRunbooksCollection(t *testing.T) {
	out := PrependGuidance("test task")
	for _, want := range []string{
		`gateway_call("qmd.query"`,
		`"collections": ["runbooks"]`,
		`"type": "lex"`,
		`"type": "vec"`,
		`"type": "hyde"`,
		`gateway_call("qmd.get"`,
		"Cap total qmd.query calls at 3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("PrependGuidance() missing %q\nfull output:\n%s", want, out)
		}
	}

	if !strings.Contains(out, "test task") {
		t.Errorf("PrependGuidance() should append the user task, got:\n%s", out)
	}
}
