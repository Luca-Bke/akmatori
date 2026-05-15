package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akmatori/akmatori/internal/database"
)

// writeMemoryFile renders a memory-writer-shaped file and drops it on disk.
// Mirrors what the memory-writer subagent emits, so the ingest test exercises
// the same parse path production hits.
func writeMemoryFile(t *testing.T, dir, name, description, memType, scope, incidentUUID, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	var sb strings.Builder
	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "name: %s\n", name)
	fmt.Fprintf(&sb, "description: %s\n", description)
	fmt.Fprintf(&sb, "type: %s\n", memType)
	fmt.Fprintf(&sb, "scope: %s\n", scope)
	if incidentUUID != "" {
		fmt.Fprintf(&sb, "incident_uuid: %s\n", incidentUUID)
	}
	sb.WriteString("created_by: agent\n")
	sb.WriteString("---\n\n")
	fmt.Fprintf(&sb, "# %s\n\n", name)
	fmt.Fprintf(&sb, "%s\n\n", description)
	sb.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		sb.WriteString("\n")
	}
	path := filepath.Join(dir, name+".md")
	if err := os.WriteFile(path, []byte(sb.String()), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestIngestFromDisk_NewFilesCreateRows(t *testing.T) {
	svc := setupMemoryServiceTest(t)
	globalDir := filepath.Join(svc.MemoryDir(), MemoryScopeGlobal)
	writeMemoryFile(t, globalDir, "prod-db-data-dir", "data dir lives on /mnt/data", MemoryTypeHost, MemoryScopeGlobal, "inc-1", "Postgres on prod-db-01 has its data dir on /mnt/data.")
	writeMemoryFile(t, globalDir, "redis-port", "redis runs on 16379", MemoryTypeToolQuirk, MemoryScopeGlobal, "inc-1", "Redis prod cluster listens on 16379, not 6379.")

	if err := svc.IngestFromDisk(context.Background()); err != nil {
		t.Fatalf("IngestFromDisk: %v", err)
	}

	mems, err := svc.ListMemoriesByScope(MemoryScopeGlobal)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(mems) != 2 {
		t.Fatalf("expected 2 ingested memories, got %d: %+v", len(mems), mems)
	}
	byName := map[string]database.Memory{}
	for _, m := range mems {
		byName[m.Name] = m
	}
	if got := byName["prod-db-data-dir"]; got.Type != MemoryTypeHost || got.CreatedBy != MemoryCreatedByAgent {
		t.Errorf("prod-db-data-dir: %+v", got)
	}
	if got := byName["prod-db-data-dir"]; !strings.Contains(got.Body, "prod-db-01") {
		t.Errorf("prod-db-data-dir body lost its content: %q", got.Body)
	}
	if got := byName["redis-port"]; got.IncidentUUID != "inc-1" {
		t.Errorf("redis-port incident_uuid: %q", got.IncidentUUID)
	}
}

func TestIngestFromDisk_RoundTripWithoutBodyDuplication(t *testing.T) {
	// Regression: parseMemoryFile must strip the `# <name>` header and the
	// description echo from the file body, otherwise every ingest cycle
	// (writer → ingest → SyncMemoryFiles → ingest again) would accumulate a
	// duplicate description in Body.
	svc := setupMemoryServiceTest(t)
	dir := filepath.Join(svc.MemoryDir(), MemoryScopeGlobal)
	writeMemoryFile(t, dir, "round-trip", "a single line description", MemoryTypeHost, MemoryScopeGlobal, "", "real body content")

	if err := svc.IngestFromDisk(context.Background()); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	mems, _ := svc.ListMemoriesByScope(MemoryScopeGlobal)
	if len(mems) != 1 {
		t.Fatalf("expected 1, got %d", len(mems))
	}
	if got := mems[0].Body; strings.Contains(got, "# round-trip") {
		t.Errorf("body should not contain the markdown header: %q", got)
	}
	if got := mems[0].Body; strings.HasPrefix(got, "a single line description") {
		t.Errorf("body should not start with the description echo: %q", got)
	}
	if got := mems[0].Body; !strings.Contains(got, "real body content") {
		t.Errorf("body lost its real content: %q", got)
	}
}

func TestIngestFromDisk_ModifiedFilesUpdateByScopeAndName(t *testing.T) {
	svc := setupMemoryServiceTest(t)
	dir := filepath.Join(svc.MemoryDir(), MemoryScopeGlobal)
	writeMemoryFile(t, dir, "updatable", "first description", MemoryTypeHost, MemoryScopeGlobal, "inc-1", "v1 body")

	if err := svc.IngestFromDisk(context.Background()); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	first, _ := svc.ListMemoriesByScope(MemoryScopeGlobal)
	if len(first) != 1 {
		t.Fatalf("expected 1 row after first ingest, got %d", len(first))
	}
	originalID := first[0].ID

	// SyncMemoryFiles renamed the file to <id>-<name>.md and our test file
	// was kept by removeStaleFiles since SyncMemoryFiles only purges
	// unexpected paths within scopeDir. Rewrite the canonical filename so
	// the file format matches what the writer subagent would produce next time.
	canonical := filepath.Join(dir, fmt.Sprintf("%d-updatable.md", originalID))
	if _, err := os.Stat(canonical); err != nil {
		t.Fatalf("expected canonical file %s after sync: %v", canonical, err)
	}
	// Now rewrite the file with new description + body but the same name.
	writeMemoryFile(t, dir, "updatable", "updated description", MemoryTypeHost, MemoryScopeGlobal, "inc-1", "v2 body")

	if err := svc.IngestFromDisk(context.Background()); err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	second, _ := svc.ListMemoriesByScope(MemoryScopeGlobal)
	if len(second) != 1 {
		t.Fatalf("expected 1 row after re-ingest, got %d: %+v", len(second), second)
	}
	if second[0].ID != originalID {
		t.Errorf("upsert changed primary key: was %d, now %d", originalID, second[0].ID)
	}
	if !strings.Contains(second[0].Description, "updated description") {
		t.Errorf("description didn't update: %q", second[0].Description)
	}
	if !strings.Contains(second[0].Body, "v2 body") {
		t.Errorf("body didn't update: %q", second[0].Body)
	}
}

func TestIngestFromDisk_IsIdempotent(t *testing.T) {
	svc := setupMemoryServiceTest(t)
	dir := filepath.Join(svc.MemoryDir(), MemoryScopeGlobal)
	writeMemoryFile(t, dir, "stable", "static description", MemoryTypeFeedback, MemoryScopeGlobal, "inc-1", "stable body")

	if err := svc.IngestFromDisk(context.Background()); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if err := svc.IngestFromDisk(context.Background()); err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if err := svc.IngestFromDisk(context.Background()); err != nil {
		t.Fatalf("third ingest: %v", err)
	}

	mems, _ := svc.ListMemoriesByScope(MemoryScopeGlobal)
	if len(mems) != 1 {
		t.Fatalf("expected idempotent single row, got %d: %+v", len(mems), mems)
	}
	if mems[0].CreatedBy != MemoryCreatedByAgent {
		t.Errorf("created_by should be %q, got %q", MemoryCreatedByAgent, mems[0].CreatedBy)
	}
}

func TestIngestFromDisk_SkipsManifestAndInvalidFiles(t *testing.T) {
	svc := setupMemoryServiceTest(t)
	dir := filepath.Join(svc.MemoryDir(), MemoryScopeGlobal)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Manifest must be skipped silently — it's regenerated by SyncMemoryFiles.
	if err := os.WriteFile(filepath.Join(dir, manifestFile), []byte("# Manifest"), 0644); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}
	// Files without YAML frontmatter must be skipped without erroring.
	if err := os.WriteFile(filepath.Join(dir, "no-frontmatter.md"), []byte("just markdown no frontmatter"), 0644); err != nil {
		t.Fatalf("seed bad file: %v", err)
	}
	// Files with invalid memory type must be skipped.
	bad := "---\nname: bad\ndescription: x\ntype: nope\nscope: global\n---\n\n# bad\n\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "bad-type.md"), []byte(bad), 0644); err != nil {
		t.Fatalf("seed bad-type: %v", err)
	}
	// Files with non-.md suffix must be skipped.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0644); err != nil {
		t.Fatalf("seed txt: %v", err)
	}

	// One good file mixed in to prove the loop doesn't bail on the first error.
	writeMemoryFile(t, dir, "keeper", "good description", MemoryTypeHost, MemoryScopeGlobal, "inc-1", "keep me")

	if err := svc.IngestFromDisk(context.Background()); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	mems, _ := svc.ListMemoriesByScope(MemoryScopeGlobal)
	if len(mems) != 1 || mems[0].Name != "keeper" {
		t.Fatalf("expected only the good file ingested, got %+v", mems)
	}
}

func TestIngestFromDisk_SkipsNonSlugScopeDirs(t *testing.T) {
	// Scope directories must be slug-safe (lowercase a-z/0-9/hyphen). Any
	// other directory under memoryDir is ignored — defense against an
	// operator hand-creating a scope dir that wouldn't pass validation.
	svc := setupMemoryServiceTest(t)
	badScopeDir := filepath.Join(svc.MemoryDir(), "Bad Scope!")
	writeMemoryFile(t, badScopeDir, "should-not-land", "x", MemoryTypeHost, "Bad Scope!", "", "body")

	// And a sibling good scope dir to prove the good path still works.
	goodDir := filepath.Join(svc.MemoryDir(), "redis")
	writeMemoryFile(t, goodDir, "good", "good description", MemoryTypeHost, "redis", "", "good body")

	if err := svc.IngestFromDisk(context.Background()); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	mems, _ := svc.ListMemories("", "")
	if len(mems) != 1 || mems[0].Name != "good" {
		t.Fatalf("expected only good scope to be ingested, got %+v", mems)
	}
}

func TestIngestFromDisk_EmptyDirIsNoOp(t *testing.T) {
	svc := setupMemoryServiceTest(t)
	if err := svc.IngestFromDisk(context.Background()); err != nil {
		t.Fatalf("ingest on empty dir: %v", err)
	}
	mems, _ := svc.ListMemories("", "")
	if len(mems) != 0 {
		t.Errorf("expected no rows on empty ingest, got %+v", mems)
	}
}

func TestIngestFromDisk_MissingDirIsNoOp(t *testing.T) {
	// Regression: if the memory directory hasn't been created yet (fresh
	// install, no incidents completed), ingest must succeed silently rather
	// than erroring out and surfacing as a startup warning.
	svc := setupMemoryServiceTest(t)
	if err := os.RemoveAll(svc.MemoryDir()); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if err := svc.IngestFromDisk(context.Background()); err != nil {
		t.Fatalf("ingest on missing dir: %v", err)
	}
}

func TestParseMemoryFile_HandlesQuotedDescription(t *testing.T) {
	// renderMemoryFile YAML-encodes descriptions that contain colons or
	// quotes. The parser must unwrap them faithfully so a round-trip
	// description is preserved.
	raw := "---\nname: quoted\ndescription: 'prod-db: data dir moved to /mnt/data'\ntype: host\nscope: global\nincident_uuid: inc-x\ncreated_by: agent\n---\n\n# quoted\n\nprod-db: data dir moved to /mnt/data\n\nbody\n"
	mem, err := parseMemoryFile([]byte(raw), MemoryScopeGlobal)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if mem.Description != "prod-db: data dir moved to /mnt/data" {
		t.Errorf("description unwrap failed: %q", mem.Description)
	}
	if mem.IncidentUUID != "inc-x" {
		t.Errorf("incident_uuid: %q", mem.IncidentUUID)
	}
	if mem.Body != "body" {
		t.Errorf("body: %q", mem.Body)
	}
}

func TestParseMemoryFile_RejectsMissingFrontmatter(t *testing.T) {
	if _, err := parseMemoryFile([]byte("no frontmatter here"), MemoryScopeGlobal); err == nil {
		t.Fatal("expected error on missing frontmatter")
	}
	if _, err := parseMemoryFile([]byte("---\nname: x\n"), MemoryScopeGlobal); err == nil {
		t.Fatal("expected error on unclosed frontmatter")
	}
}

func TestParseMemoryFile_RejectsInvalidType(t *testing.T) {
	raw := "---\nname: bad\ndescription: x\ntype: invalid\nscope: global\n---\n\nbody\n"
	if _, err := parseMemoryFile([]byte(raw), MemoryScopeGlobal); err == nil {
		t.Fatal("expected error on invalid type")
	}
}
