// Package memory provides the memory.search and memory.get gateway tools that
// proxy QMD's qmd.query / qmd.get against the cross-incident memory collection.
//
// Design constraints (decided in the implementation plan):
//   - QMD-only: when QMD is unreachable, callers receive a clean error rather
//     than a filesystem fallback. mcp-gateway intentionally does not mount
//     /akmatori/memory.
//   - Always available: the memory namespace is registered as a proxy
//     namespace so calls bypass the per-incident allowlist. This matches the
//     intent that any agent should be able to recall memory at any time.
package memory

import (
	"context"
	"fmt"
	"strings"

	"github.com/akmatori/mcp-gateway/internal/mcp"
	"github.com/akmatori/mcp-gateway/internal/mcpproxy"
)

// CollectionName is the QMD collection that memory.search / memory.get target.
// Must match the entry in qmd/qmd-config.yml.
const CollectionName = "memories"

// MemoryRoot is the on-disk root for memory files (relative to QMD's mount).
// memory.get rejects paths that escape this root.
const MemoryRoot = "/akmatori/memory"

// Tool wires the gateway tools to the running QMD proxy connection.
type Tool struct {
	proxy *mcpproxy.ProxyHandler
}

// New constructs a Tool that calls QMD via the supplied proxy handler.
// The proxy must already have QMD registered as a system MCP server.
func New(proxy *mcpproxy.ProxyHandler) *Tool {
	return &Tool{proxy: proxy}
}

// Register adds memory.search and memory.get to the MCP server. Returns the
// list of registered tool names so the caller can mark the namespace as a
// proxy (allowlist-bypass) namespace.
func (t *Tool) Register(server *mcp.Server) []string {
	server.RegisterTool(searchToolDef(), t.search)
	server.RegisterTool(getToolDef(), t.get)
	return []string{"memory.search", "memory.get"}
}

func searchToolDef() mcp.Tool {
	return mcp.Tool{
		Name: "memory.search",
		Description: "Search cross-incident memory (hosts, recurring patterns, tool quirks, operator feedback). " +
			"Wraps QMD's qmd.query against the 'memories' collection so results never include unrelated runbooks. " +
			"Use scope='global' for everyone-applicable facts or a skill name for skill-specific knowledge.",
		InputSchema: mcp.InputSchema{
			Type: "object",
			Properties: map[string]mcp.Property{
				"query": {Type: "string", Description: "Lex/keyword query passed through to QMD."},
				"scope": {Type: "string", Description: "Optional scope filter: 'global' or a skill name."},
				"type":  {Type: "string", Description: "Optional memory type: host, incident_pattern, tool_quirk, feedback."},
				"limit": {Type: "integer", Description: "Maximum result count (default 5)."},
			},
			Required: []string{"query"},
		},
	}
}

func getToolDef() mcp.Tool {
	return mcp.Tool{
		Name: "memory.get",
		Description: "Retrieve the full body of a memory file by its filesystem path. Path must live under " +
			MemoryRoot + " — paths with '..' or escapes are rejected. Wraps QMD's qmd.get for consistent retrieval.",
		InputSchema: mcp.InputSchema{
			Type: "object",
			Properties: map[string]mcp.Property{
				"file":  {Type: "string", Description: "Absolute path under " + MemoryRoot + " (as returned by memory.search)."},
				"lines": {Type: "integer", Description: "Optional line cap. Defaults to QMD's window."},
			},
			Required: []string{"file"},
		},
	}
}

// search proxies to qmd.query with a collection filter so results stay scoped
// to the memories collection. Optional scope/type filters are added to the
// query string itself when supplied — QMD treats them as additional lex
// terms, which is good enough for the small per-scope corpora we expect.
func (t *Tool) search(ctx context.Context, _ string, args map[string]interface{}) (interface{}, error) {
	if t.proxy == nil {
		return nil, errMemoryUnavailable
	}

	query, _ := args["query"].(string)
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("memory.search: 'query' is required")
	}

	scope, _ := args["scope"].(string)
	memType, _ := args["type"].(string)
	limit := readIntDefault(args, "limit", 5)
	if limit <= 0 {
		limit = 5
	}

	// Compose the lex query. We append scope/type as additional keywords
	// rather than relying on QMD-side metadata filters because the
	// frontmatter is already inlined into the file body, so lex hits work.
	lex := query
	if scope != "" {
		lex = lex + " scope:" + scope
	}
	if memType != "" {
		lex = lex + " type:" + memType
	}

	qmdArgs := map[string]interface{}{
		"collection": CollectionName,
		"searches": []map[string]interface{}{
			{"type": "lex", "query": lex},
		},
		"limit": limit,
	}

	result, err := t.proxy.CallTool(ctx, "qmd.query", qmdArgs)
	if err != nil {
		return nil, fmt.Errorf("memory.search: %w (QMD unavailable?)", err)
	}
	return extractText(result), nil
}

// get proxies to qmd.get after sandboxing the path inside MemoryRoot. The
// validation here is defense-in-depth: QMD itself only exposes files under
// its configured collection root, but rejecting bad paths early surfaces a
// clearer error than QMD's "file not found".
func (t *Tool) get(ctx context.Context, _ string, args map[string]interface{}) (interface{}, error) {
	// Path validation runs first so callers always get a precise error
	// (traversal/outside-root) before we even consult the proxy.
	file, _ := args["file"].(string)
	if file == "" {
		return nil, fmt.Errorf("memory.get: 'file' is required")
	}
	if err := validateMemoryPath(file); err != nil {
		return nil, err
	}
	if t.proxy == nil {
		return nil, errMemoryUnavailable
	}
	qmdArgs := map[string]interface{}{
		"file": file,
	}
	if lines, ok := args["lines"]; ok {
		qmdArgs["lines"] = lines
	}
	result, err := t.proxy.CallTool(ctx, "qmd.get", qmdArgs)
	if err != nil {
		return nil, fmt.Errorf("memory.get: %w (QMD unavailable?)", err)
	}
	return extractText(result), nil
}

// validateMemoryPath sandboxes the file argument before forwarding it to QMD.
// memory.get is contractually scoped to memory documents, so the validation
// must reject paths that resolve outside the memories collection — including
// relative paths that point to other collections (e.g. "runbooks/foo.md")
// or other roots.
//
// Accepted forms:
//
//	Absolute:                "/akmatori/memory/global/1-foo.md"
//	QMD collection-relative: "memories/global/1-foo.md"
//
// Rejected:
//
//	Empty, paths containing ".." (any), absolute paths outside MemoryRoot,
//	relative paths that don't begin with the memories collection prefix
//	(e.g. "runbooks/foo.md", "global/1-foo.md", "/etc/passwd").
func validateMemoryPath(p string) error {
	clean := strings.TrimSpace(p)
	if clean == "" {
		return fmt.Errorf("memory.get: path cannot be empty")
	}
	if strings.Contains(clean, "..") {
		return fmt.Errorf("memory.get: path traversal rejected: %q", p)
	}
	if strings.HasPrefix(clean, "/") {
		if !strings.HasPrefix(clean, MemoryRoot+"/") {
			return fmt.Errorf("memory.get: absolute path must live under %s, got %q", MemoryRoot, p)
		}
		return nil
	}
	// Relative path: must be a memories-collection path. QMD returns
	// collection-prefixed paths (e.g. "memories/global/1-foo.md"); accepting
	// anything else would let memory.get reach into the runbooks collection.
	if !strings.HasPrefix(clean, CollectionName+"/") {
		return fmt.Errorf("memory.get: relative path must start with %q/ — got %q (memory.get is scoped to the memories collection)", CollectionName, p)
	}
	return nil
}

// extractText flattens a CallToolResult to a single string. Multi-content
// results are concatenated with newlines so the agent sees one coherent
// response. Empty results yield an empty string (which the agent treats as
// "no matches").
func extractText(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	if len(result.Content) == 1 {
		return result.Content[0].ExtractText()
	}
	var parts []string
	for _, c := range result.Content {
		if t := c.ExtractText(); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n")
}

func readIntDefault(args map[string]interface{}, key string, def int) int {
	v, ok := args[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return def
}

// errMemoryUnavailable is returned when memory tools are invoked before the
// proxy handler has been wired up. In production this only happens during
// startup / shutdown windows.
var errMemoryUnavailable = fmt.Errorf("memory tools unavailable: QMD proxy not configured")
