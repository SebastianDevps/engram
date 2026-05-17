package main

import (
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
