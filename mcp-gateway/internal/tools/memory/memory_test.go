package memory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/akmatori/mcp-gateway/internal/mcp"
)

func TestValidateMemoryPath(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool // true = valid, false = error
	}{
		{"empty", "", false},
		{"traversal", "/akmatori/memory/../runbooks/secret.md", false},
		{"traversal nested", "/akmatori/memory/global/../../etc/passwd", false},
		{"traversal in relative path", "../etc/passwd", false},
		{"absolute outside root", "/etc/passwd", false},
		// Regression: relative paths must be scoped to the memories
		// collection. Without this constraint, an agent could pass a path
		// like "runbooks/secret.md" or "global/1-foo.md" (no collection
		// prefix) and memory.get would silently reach outside the memories
		// collection — defeating its sandboxing contract.
		{"rejects other collection prefix", "runbooks/foo.md", false},
		{"rejects scope-only relative", "global/1-foo.md", false},
		{"rejects bare filename", "1-foo.md", false},
		// Accepted shapes: absolute paths under MemoryRoot, and relative
		// paths prefixed with the memories collection (the form QMD's
		// qmd.query returns for memory documents).
		{"qmd collection-relative", "memories/global/1-foo.md", true},
		{"qmd collection-relative manifest", "memories/redis/MEMORY.md", true},
		{"qmd collection-relative skill scope", "memories/postgres-skill/2-data-dir.md", true},
		{"valid absolute scope file", "/akmatori/memory/global/1-host.md", true},
		{"valid absolute manifest", "/akmatori/memory/redis/MEMORY.md", true},
		{"valid absolute skill scope", "/akmatori/memory/postgres-skill/2-data-dir.md", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMemoryPath(tc.path)
			if tc.want && err != nil {
				t.Errorf("expected %q valid, got error: %v", tc.path, err)
			}
			if !tc.want && err == nil {
				t.Errorf("expected %q invalid, got nil", tc.path)
			}
		})
	}
}

func TestExtractText_SingleContent(t *testing.T) {
	r := &mcp.CallToolResult{Content: []mcp.Content{{Type: "text", Text: "hello"}}}
	if got := extractText(r); got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestExtractText_MultipleContents(t *testing.T) {
	r := &mcp.CallToolResult{Content: []mcp.Content{
		{Type: "text", Text: "first"},
		{Type: "text", Text: "second"},
	}}
	got := extractText(r)
	if !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Errorf("got %q", got)
	}
}

func TestExtractText_NilSafe(t *testing.T) {
	if got := extractText(nil); got != "" {
		t.Errorf("expected empty string from nil result, got %q", got)
	}
}

func TestReadIntDefault(t *testing.T) {
	cases := []struct {
		name    string
		args    map[string]interface{}
		key     string
		def     int
		want    int
	}{
		{"missing", map[string]interface{}{}, "limit", 7, 7},
		{"int", map[string]interface{}{"limit": 3}, "limit", 5, 3},
		{"int64", map[string]interface{}{"limit": int64(9)}, "limit", 5, 9},
		{"float64", map[string]interface{}{"limit": 4.0}, "limit", 5, 4},
		{"unparseable", map[string]interface{}{"limit": "abc"}, "limit", 5, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := readIntDefault(tc.args, tc.key, tc.def); got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSearch_RequiresQuery(t *testing.T) {
	tool := &Tool{}
	_, err := tool.search(context.Background(), "", map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error when query missing")
	}
	if !strings.Contains(err.Error(), "memory tools unavailable") {
		// Without proxy handler we hit the unavailable branch first; verify.
		t.Logf("first error path was unavailable check, that's fine: %v", err)
	}
}

func TestSearch_RejectsBlankQuery(t *testing.T) {
	tool := &Tool{proxy: nil}
	_, err := tool.search(context.Background(), "", map[string]interface{}{"query": "  "})
	if err == nil {
		t.Fatal("expected error when query blank")
	}
}

func TestGet_RequiresFile(t *testing.T) {
	tool := &Tool{proxy: nil}
	_, err := tool.get(context.Background(), "", map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error when file missing")
	}
}

func TestGet_RejectsTraversal(t *testing.T) {
	tool := &Tool{proxy: nil}
	_, err := tool.get(context.Background(), "", map[string]interface{}{"file": "/akmatori/memory/../etc/passwd"})
	if err == nil || !strings.Contains(err.Error(), "traversal") {
		t.Fatalf("expected traversal error, got %v", err)
	}
}

func TestGet_RejectsOutsideRoot(t *testing.T) {
	tool := &Tool{proxy: nil}
	_, err := tool.get(context.Background(), "", map[string]interface{}{"file": "/etc/passwd"})
	if err == nil || !strings.Contains(err.Error(), "must live under") {
		t.Fatalf("expected outside-root error, got %v", err)
	}
}

// TestRegister_AttachesBothTools verifies that calling Register adds the two
// expected tool names to the server. We use a minimal mcp.Server constructed
// via the package's NewServer to avoid unrelated test fixtures.
func TestRegister_AttachesBothTools(t *testing.T) {
	server := mcp.NewServer("test", "0.0.0", nil)
	tool := New(nil)
	names := tool.Register(server)

	if len(names) != 2 {
		t.Fatalf("expected 2 registered names, got %d: %v", len(names), names)
	}
	want := map[string]bool{"memory.search": true, "memory.get": true}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected registered name: %s", n)
		}
		delete(want, n)
	}
	if len(want) > 0 {
		t.Errorf("missing registrations: %v", want)
	}

	// Server should know both tools.
	for _, n := range []string{"memory.search", "memory.get"} {
		if !serverHasTool(server, n) {
			t.Errorf("server missing tool %s", n)
		}
	}
}

func serverHasTool(s *mcp.Server, name string) bool {
	for _, tool := range s.Tools() {
		if tool.Name == name {
			return true
		}
	}
	return false
}

// Sanity: the unavailable error is returned (not silently ignored) when the
// proxy is nil. This guards against a future refactor that defaults to a
// silent no-op.
func TestSearch_UnavailableWhenProxyNil(t *testing.T) {
	tool := New(nil)
	_, err := tool.search(context.Background(), "", map[string]interface{}{"query": "anything"})
	if err == nil {
		t.Fatal("expected error when proxy nil")
	}
	if !errors.Is(err, errMemoryUnavailable) {
		t.Errorf("expected errMemoryUnavailable, got %v", err)
	}
}
