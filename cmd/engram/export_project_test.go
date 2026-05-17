package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
)

// seedObsAndGetSyncID creates a session + observation and returns the observation sync_id.
func seedObsAndGetSyncID(t *testing.T, cfg store.Config, sessionID, project, title string) string {
	t.Helper()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	if err := s.CreateSession(sessionID, project, "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: sessionID,
		Type:      "decision",
		Title:     title,
		Content:   "content for " + title,
		Project:   project,
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	return obs.SyncID
}

// TestProjectScopedExportExcludesOtherProjects verifies S1.1:
// --project flag filters to only observations/sessions for that project.
func TestProjectScopedExportExcludesOtherProjects(t *testing.T) {
	workDir := t.TempDir()
	withCwd(t, workDir)
	cfg := testConfig(t)
	stubExitWithPanic(t)

	seedObsAndGetSyncID(t, cfg, "sess-alpha", "alpha", "Alpha observation")
	seedObsAndGetSyncID(t, cfg, "sess-beta", "beta", "Beta observation")

	outFile := filepath.Join(workDir, "alpha-export.json")
	withArgs(t, "engram", "export", "--project", "alpha", outFile)
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdExport(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("export --project alpha failed: panic=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stdout, "Exported to") {
		t.Fatalf("unexpected stdout: %q", stdout)
	}

	raw, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read export file: %v", err)
	}
	var data store.ExportData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("parse export: %v", err)
	}

	for _, obs := range data.Observations {
		proj := ""
		if obs.Project != nil {
			proj = *obs.Project
		}
		if proj != "alpha" {
			t.Errorf("observation project = %q, want %q", proj, "alpha")
		}
	}
	for _, sess := range data.Sessions {
		if sess.Project != "alpha" {
			t.Errorf("session project = %q, want %q", sess.Project, "alpha")
		}
	}
	if len(data.Observations) == 0 {
		t.Error("expected at least one observation in scoped export, got none")
	}
}

// TestUnscopedExportPreservesBackwardCompat verifies S1.2:
// engram export (no flag) includes all projects and is structurally identical to pre-patch.
func TestUnscopedExportPreservesBackwardCompat(t *testing.T) {
	workDir := t.TempDir()
	withCwd(t, workDir)
	cfg := testConfig(t)
	stubExitWithPanic(t)

	seedObsAndGetSyncID(t, cfg, "sess-p1", "projectone", "Obs P1")
	seedObsAndGetSyncID(t, cfg, "sess-p2", "projecttwo", "Obs P2")

	outFile := filepath.Join(workDir, "all-export.json")
	withArgs(t, "engram", "export", outFile)
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdExport(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("unscoped export failed: panic=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stdout, "Exported to") {
		t.Fatalf("unexpected stdout: %q", stdout)
	}

	raw, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	var data store.ExportData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("parse export: %v", err)
	}

	projects := map[string]bool{}
	for _, obs := range data.Observations {
		if obs.Project != nil {
			projects[*obs.Project] = true
		}
	}
	if !projects["projectone"] || !projects["projecttwo"] {
		t.Errorf("unscoped export missing projects; found: %v", projects)
	}
}

// TestScopedExportIncludesMemoryRelationsArray verifies S1.3:
// scoped export always includes the memory_relations key (may be empty slice).
func TestScopedExportIncludesMemoryRelationsArray(t *testing.T) {
	workDir := t.TempDir()
	withCwd(t, workDir)
	cfg := testConfig(t)
	stubExitWithPanic(t)

	syncIDa := seedObsAndGetSyncID(t, cfg, "sess-rel", "relproject", "Obs A")
	syncIDb := seedObsAndGetSyncID(t, cfg, "sess-rel", "relproject", "Obs B")

	// Create a relation between A and B.
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	_, err = s.SaveRelation(store.SaveRelationParams{
		SyncID:   "rel-testrelation01",
		SourceID: syncIDa,
		TargetID: syncIDb,
	})
	s.Close()
	if err != nil {
		t.Fatalf("SaveRelation: %v", err)
	}

	outFile := filepath.Join(workDir, "rel-export.json")
	withArgs(t, "engram", "export", "--project", "relproject", outFile)
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdExport(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("export failed: panic=%v stderr=%q", recovered, stderr)
	}

	raw, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	var data store.ExportData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("parse export: %v", err)
	}

	// memory_relations must be present and contain our relation.
	if len(data.MemoryRelations) == 0 {
		t.Fatal("expected memory_relations to be non-empty after SaveRelation")
	}
	found := false
	for _, r := range data.MemoryRelations {
		if r.SyncID == "rel-testrelation01" {
			found = true
			if r.SourceID != syncIDa {
				t.Errorf("relation source_id = %q, want %q", r.SourceID, syncIDa)
			}
			if r.TargetID != syncIDb {
				t.Errorf("relation target_id = %q, want %q", r.TargetID, syncIDb)
			}
		}
	}
	if !found {
		t.Error("expected rel-testrelation01 in memory_relations, not found")
	}
}

// TestRoundTripPreservesSupersedeChainsViaRelations verifies S1.4 / S4.3:
// export → import to fresh store → relations preserved by sync_id.
func TestRoundTripPreservesSupersedeChainsViaRelations(t *testing.T) {
	workDir := t.TempDir()
	withCwd(t, workDir)
	cfg := testConfig(t)
	stubExitWithPanic(t)

	syncIDa := seedObsAndGetSyncID(t, cfg, "sess-rt", "rtproject", "Obs-RT-A")
	syncIDb := seedObsAndGetSyncID(t, cfg, "sess-rt", "rtproject", "Obs-RT-B")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	_, err = s.SaveRelation(store.SaveRelationParams{
		SyncID:   "rel-roundtrip001",
		SourceID: syncIDa,
		TargetID: syncIDb,
	})
	s.Close()
	if err != nil {
		t.Fatalf("SaveRelation: %v", err)
	}

	// Export with --project flag.
	outFile := filepath.Join(workDir, "rt-export.json")
	withArgs(t, "engram", "export", "--project", "rtproject", outFile)
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdExport(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("export failed: panic=%v stderr=%q", recovered, stderr)
	}

	// Import into a fresh store.
	freshCfg := testConfig(t)
	withArgs(t, "engram", "import", outFile)
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdImport(freshCfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("import failed: panic=%v stderr=%q stdout=%q", recovered, stderr, stdout)
	}
	if !strings.Contains(stdout, "Imported from") {
		t.Fatalf("unexpected import stdout: %q", stdout)
	}

	// Verify relation is in the fresh store.
	fresh, err := store.New(freshCfg)
	if err != nil {
		t.Fatalf("fresh store.New: %v", err)
	}
	defer fresh.Close()

	rel, err := fresh.GetRelation("rel-roundtrip001")
	if err != nil {
		t.Fatalf("GetRelation after import: %v", err)
	}
	if rel.SourceID != syncIDa {
		t.Errorf("restored relation source_id = %q, want %q", rel.SourceID, syncIDa)
	}
	if rel.TargetID != syncIDb {
		t.Errorf("restored relation target_id = %q, want %q", rel.TargetID, syncIDb)
	}
}

// TestScopedExportEmptyRelationsKeyAlwaysPresent verifies F1 regression:
// when no relations exist for the project, "memory_relations" key must still be
// present in the JSON output (not absent due to omitempty) and must be an empty array.
func TestScopedExportEmptyRelationsKeyAlwaysPresent(t *testing.T) {
	workDir := t.TempDir()
	withCwd(t, workDir)
	cfg := testConfig(t)
	stubExitWithPanic(t)

	seedObsAndGetSyncID(t, cfg, "sess-nrel", "nrelproject", "NoRelObs")

	outFile := filepath.Join(workDir, "nrel-export.json")
	withArgs(t, "engram", "export", "--project", "nrelproject", outFile)
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdExport(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("export failed: panic=%v stderr=%q", recovered, stderr)
	}

	raw, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read export file: %v", err)
	}

	// Check JSON key presence via raw map — not struct (struct always has field).
	var rawMap map[string]any
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		t.Fatalf("parse export as raw map: %v", err)
	}
	val, ok := rawMap["memory_relations"]
	if !ok {
		t.Fatal("memory_relations key must be present in JSON even when empty (omitempty removed)")
	}
	arr, isSlice := val.([]any)
	if !isSlice {
		t.Fatalf("memory_relations must be a JSON array, got %T", val)
	}
	if len(arr) != 0 {
		t.Errorf("expected empty memory_relations array, got %d entries", len(arr))
	}
}

// TestIdempotentReImportNoObservationDuplicates verifies F3+F4 regression:
// importing the same file twice must not increase the observation count.
func TestIdempotentReImportNoObservationDuplicates(t *testing.T) {
	workDir := t.TempDir()
	withCwd(t, workDir)
	cfg := testConfig(t)
	stubExitWithPanic(t)

	seedObsAndGetSyncID(t, cfg, "sess-obsdup", "dupproject", "Dup-A")

	outFile := filepath.Join(workDir, "dup-export.json")
	withArgs(t, "engram", "export", "--project", "dupproject", outFile)
	_, _, recovered := captureOutputAndRecover(t, func() { cmdExport(cfg) })
	if recovered != nil {
		t.Fatalf("export failed: panic=%v", recovered)
	}

	freshCfg := testConfig(t)

	// First import.
	withArgs(t, "engram", "import", outFile)
	stdout1, stderr1, rec1 := captureOutputAndRecover(t, func() { cmdImport(freshCfg) })
	if rec1 != nil || stderr1 != "" {
		t.Fatalf("first import failed: panic=%v stderr=%q stdout=%q", rec1, stderr1, stdout1)
	}
	if !strings.Contains(stdout1, "Observations:    1") {
		t.Errorf("first import should insert 1 observation, stdout=%q", stdout1)
	}

	// Second import — same file, must be a no-op for observations.
	withArgs(t, "engram", "import", outFile)
	stdout2, stderr2, rec2 := captureOutputAndRecover(t, func() { cmdImport(freshCfg) })
	if rec2 != nil || stderr2 != "" {
		t.Fatalf("second import failed: panic=%v stderr=%q stdout=%q", rec2, stderr2, stdout2)
	}
	if !strings.Contains(stdout2, "Observations:    0") {
		t.Errorf("second import should insert 0 observations (idempotent), stdout=%q", stdout2)
	}
}

// TestIdempotentReImportNoRelationDuplicates verifies S4.2:
// importing the same file twice must not increase the relation count.
func TestIdempotentReImportNoRelationDuplicates(t *testing.T) {
	workDir := t.TempDir()
	withCwd(t, workDir)
	cfg := testConfig(t)
	stubExitWithPanic(t)

	syncIDa := seedObsAndGetSyncID(t, cfg, "sess-idem", "idemproject", "Idem-A")
	syncIDb := seedObsAndGetSyncID(t, cfg, "sess-idem", "idemproject", "Idem-B")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	_, err = s.SaveRelation(store.SaveRelationParams{
		SyncID:   "rel-idempotent01",
		SourceID: syncIDa,
		TargetID: syncIDb,
	})
	s.Close()
	if err != nil {
		t.Fatalf("SaveRelation: %v", err)
	}

	outFile := filepath.Join(workDir, "idem-export.json")
	withArgs(t, "engram", "export", "--project", "idemproject", outFile)
	_, _, recovered := captureOutputAndRecover(t, func() { cmdExport(cfg) })
	if recovered != nil {
		t.Fatalf("export failed: panic=%v", recovered)
	}

	freshCfg := testConfig(t)

	// First import.
	withArgs(t, "engram", "import", outFile)
	stdout1, stderr1, rec1 := captureOutputAndRecover(t, func() { cmdImport(freshCfg) })
	if rec1 != nil || stderr1 != "" {
		t.Fatalf("first import failed: panic=%v stderr=%q stdout=%q", rec1, stderr1, stdout1)
	}

	// Second import — same file.
	withArgs(t, "engram", "import", outFile)
	stdout2, stderr2, rec2 := captureOutputAndRecover(t, func() { cmdImport(freshCfg) })
	if rec2 != nil || stderr2 != "" {
		t.Fatalf("second import failed: panic=%v stderr=%q stdout=%q", rec2, stderr2, stdout2)
	}

	// Verify no duplicates by checking that MemoryRelations: 0 on second import
	// (INSERT OR IGNORE means 0 rows affected the second time).
	if !strings.Contains(stdout2, "MemoryRelations: 0") {
		t.Errorf("second import should insert 0 relations (idempotent), stdout=%q", stdout2)
	}

	// Verify relation still exists and is correct.
	fresh, err := store.New(freshCfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer fresh.Close()

	rel, err := fresh.GetRelation("rel-idempotent01")
	if err != nil {
		t.Fatalf("GetRelation: %v", err)
	}
	if rel.SyncID != "rel-idempotent01" {
		t.Errorf("unexpected relation sync_id: %q", rel.SyncID)
	}
}

// TestCrossProjectRelationNotExported verifies Engram H1 fix:
// a relation whose endpoints belong to different projects must NOT appear in a
// scoped export for either project (AND logic, not OR).
func TestCrossProjectRelationNotExported(t *testing.T) {
	workDir := t.TempDir()
	withCwd(t, workDir)
	cfg := testConfig(t)
	stubExitWithPanic(t)

	// Two observations in different projects.
	syncX := seedObsAndGetSyncID(t, cfg, "sess-cross-x", "projectX", "Obs-X")
	syncY := seedObsAndGetSyncID(t, cfg, "sess-cross-y", "projectY", "Obs-Y")

	// Relation bridges the two projects.
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	_, err = s.SaveRelation(store.SaveRelationParams{
		SyncID:   "rel-cross-project01",
		SourceID: syncX,
		TargetID: syncY,
	})
	s.Close()
	if err != nil {
		t.Fatalf("SaveRelation: %v", err)
	}

	// Export scoped to projectX — bridging relation must NOT appear.
	outFile := filepath.Join(workDir, "x-export.json")
	withArgs(t, "engram", "export", "--project", "projectX", outFile)
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdExport(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("export --project projectX failed: panic=%v stderr=%q", recovered, stderr)
	}

	raw, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	var data store.ExportData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("parse export: %v", err)
	}

	for _, r := range data.MemoryRelations {
		if r.SyncID == "rel-cross-project01" {
			t.Errorf("cross-project relation %q must NOT appear in projectX scoped export", r.SyncID)
		}
	}
}

// TestProjectFlagEqualsForm verifies Engram H2 fix:
// --project=value (equals form) must be parsed identically to --project value.
func TestProjectFlagEqualsForm(t *testing.T) {
	workDir := t.TempDir()
	withCwd(t, workDir)
	cfg := testConfig(t)
	stubExitWithPanic(t)

	seedObsAndGetSyncID(t, cfg, "sess-eq", "eqproject", "Obs-Eq")

	outFile := filepath.Join(workDir, "eq-export.json")
	// Use --project=eqproject (equals form) — previously fell through to positional arg.
	withArgs(t, "engram", "export", "--project=eqproject", outFile)
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdExport(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("export --project=eqproject failed: panic=%v stderr=%q", recovered, stderr)
	}

	raw, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	var data store.ExportData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("parse export: %v", err)
	}

	if len(data.Observations) == 0 {
		t.Fatal("--project=eqproject must export observations; got none (project was not parsed)")
	}
	for _, obs := range data.Observations {
		proj := ""
		if obs.Project != nil {
			proj = *obs.Project
		}
		if proj != "eqproject" {
			t.Errorf("observation project = %q, want eqproject", proj)
		}
	}
}

// TestNullEndpointRelationInScopedExport verifies NEW-1 fix:
// A relation with NULL source_id and an in-project target must appear in a scoped export.
// Before the fix, NULL IN (subquery) evaluated to NULL (not FALSE), silently excluding the row.
func TestNullEndpointRelationInScopedExport(t *testing.T) {
	workDir := t.TempDir()
	withCwd(t, workDir)
	cfg := testConfig(t)
	stubExitWithPanic(t)

	const scopeProject = "nullendpointproject"

	// Seed a target observation inside the scoped project.
	syncIDtarget := seedObsAndGetSyncID(t, cfg, "sess-nullep", scopeProject, "NullEP-Target")

	// Insert a relation with a truly NULL source_id using raw SQL.
	// SaveRelation stores '' as empty string, not NULL; we need a real NULL to exercise the bug.
	rawDB, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("open raw DB for seed: %v", err)
	}
	_, err = rawDB.Exec(`
		INSERT INTO memory_relations (sync_id, source_id, target_id, relation, judgment_status, created_at, updated_at)
		VALUES ('rel-nullep-source01', NULL, ?, 'pending', 'pending', datetime('now'), datetime('now'))
	`, syncIDtarget)
	rawDB.Close()
	if err != nil {
		t.Fatalf("seed NULL source_id relation: %v", err)
	}

	// Export scoped to the project.
	outFile := filepath.Join(workDir, "nullep-export.json")
	withArgs(t, "engram", "export", "--project", scopeProject, outFile)
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdExport(cfg) })
	if recovered != nil {
		t.Fatalf("export panicked: %v", recovered)
	}
	if stderr != "" {
		t.Fatalf("export stderr: %q", stderr)
	}

	// Parse the export and assert the null-endpoint relation IS present.
	raw, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read export file: %v", err)
	}
	var data struct {
		MemoryRelations []struct {
			SyncID string `json:"sync_id"`
		} `json:"memory_relations"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("unmarshal export: %v", err)
	}
	found := false
	for _, r := range data.MemoryRelations {
		if r.SyncID == "rel-nullep-source01" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("relation rel-nullep-source01 (NULL source_id, in-project target) missing from scoped export — NULL IN (subquery) NULL semantics bug may still be present")
	}
}

// TestNullSourceIDRoundTrip verifies Engram L1 fix:
// a relation with NULL source_id must survive export→import with source_id still NULL,
// not coerced to an empty string that breaks IS NULL queries.
func TestNullSourceIDRoundTrip(t *testing.T) {
	workDir := t.TempDir()
	withCwd(t, workDir)
	cfg := testConfig(t)
	stubExitWithPanic(t)

	// Seed a target observation so the relation has a valid target endpoint.
	syncIDtarget := seedObsAndGetSyncID(t, cfg, "sess-null", "nullproject", "Obs-Null-Target")

	// Insert a relation with an explicit empty source_id (NULL equivalent after NULLIF).
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	_, err = s.SaveRelation(store.SaveRelationParams{
		SyncID:   "rel-null-source01",
		SourceID: "", // explicit empty → NULL in DB via schema
		TargetID: syncIDtarget,
	})
	s.Close()
	if err != nil {
		t.Fatalf("SaveRelation: %v", err)
	}

	// Export (unscoped so null-source relation is included regardless of scope logic).
	outFile := filepath.Join(workDir, "null-export.json")
	withArgs(t, "engram", "export", outFile)
	_, _, recovered := captureOutputAndRecover(t, func() { cmdExport(cfg) })
	if recovered != nil {
		t.Fatalf("export failed: panic=%v", recovered)
	}

	// Import into a fresh store.
	freshCfg := testConfig(t)
	withArgs(t, "engram", "import", outFile)
	stdout, stderr, rec := captureOutputAndRecover(t, func() { cmdImport(freshCfg) })
	if rec != nil || stderr != "" {
		t.Fatalf("import failed: panic=%v stderr=%q stdout=%q", rec, stderr, stdout)
	}

	// Verify the relation's source_id in the fresh store is still empty/null (not a literal "").
	fresh, err := store.New(freshCfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer fresh.Close()

	rel, err := fresh.GetRelation("rel-null-source01")
	if err != nil {
		t.Fatalf("GetRelation after import: %v", err)
	}
	// TargetID must survive the round-trip intact.
	if rel.TargetID != syncIDtarget {
		t.Errorf("target_id after round-trip = %q, want %q", rel.TargetID, syncIDtarget)
	}

	// Strengthen L1 assertion: verify the DB column is truly NULL, not an empty string.
	// GetRelation uses ifnull(source_id,'') so it cannot distinguish NULL from ''.
	// Raw SQL on the freshStore DB is the only reliable way.
	fresh.Close()
	rawDB, err := sql.Open("sqlite", filepath.Join(freshCfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("open raw DB: %v", err)
	}
	defer rawDB.Close()

	var nullCount int
	err = rawDB.QueryRow(
		"SELECT count(*) FROM memory_relations WHERE source_id IS NULL AND sync_id = ?",
		"rel-null-source01",
	).Scan(&nullCount)
	if err != nil {
		t.Fatalf("raw SQL query: %v", err)
	}
	if nullCount != 1 {
		t.Errorf("imported relation source_id: want NULL in DB (count=1), got count=%d — NULLIF fix may be reverted", nullCount)
	}
}
