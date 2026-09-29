package audit

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testTarget(id string, before, after int64) Target {
	target := Target{
		Kind:         KindFormula,
		ID:           id,
		AfterVersion: after,
		Summary:      "state=draft version=1",
	}
	if before > 0 {
		target.BeforeVersion = &before
	}
	return target
}

func TestAppendAssignsContiguousSequencesAndQueries(t *testing.T) {
	log := openTestLog(t)
	at := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)

	if err := log.Append(OpFormulaCreate, at, testTarget("formula_1", 0, 1)); err != nil {
		t.Fatalf("append first entry: %v", err)
	}
	if err := log.Append(OpFormulaApprove, at.Add(time.Minute),
		testTarget("formula_1", 2, 3),
		testTarget("formula_2", 0, 1),
	); err != nil {
		t.Fatalf("append batch: %v", err)
	}

	entries := log.Query(Filter{})
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	for index, entry := range entries {
		if entry.Sequence != int64(index+1) {
			t.Fatalf("entry %d has sequence %d, want %d", index, entry.Sequence, index+1)
		}
	}
	if entries[0].BeforeVersion != nil {
		t.Fatalf("create entry must not carry a before version")
	}
	if got := entries[1].BeforeVersion; got == nil || *got != 2 {
		t.Fatalf("update entry must record before version 2, got %v", got)
	}
	if entries[1].RecordedAt != entries[2].RecordedAt {
		t.Fatalf("entries of one batch must share the recorded time")
	}

	byID := log.Query(Filter{TargetID: "formula_2"})
	if len(byID) != 1 || byID[0].TargetID != "formula_2" {
		t.Fatalf("target id filter returned %v", byID)
	}
	byKind := log.Query(Filter{TargetKind: KindFormula, Operation: OpFormulaApprove})
	if len(byKind) != 2 {
		t.Fatalf("operation filter expected 2 entries, got %d", len(byKind))
	}
	window := log.Query(Filter{
		From: ptrTime(at.Add(30 * time.Second)),
		To:   ptrTime(at.Add(90 * time.Second)),
	})
	if len(window) != 2 {
		t.Fatalf("time window expected 2 entries, got %d", len(window))
	}
}

func TestRestartPreservesOrderAndSequence(t *testing.T) {
	dataDir := t.TempDir()
	at := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)

	first := mustOpen(t, dataDir)
	if err := first.Append(OpVesselCreate, at,
		Target{Kind: KindVessel, ID: "vessel_1", AfterVersion: 1, Summary: "state=available version=1"},
	); err != nil {
		t.Fatalf("append before restart: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first log: %v", err)
	}

	reopened := mustOpen(t, dataDir)
	defer reopened.Close()
	entries := reopened.Query(Filter{})
	if len(entries) != 1 || entries[0].Sequence != 1 || entries[0].TargetID != "vessel_1" {
		t.Fatalf("reopened log did not restore prior entry: %v", entries)
	}
	if err := reopened.Append(OpVesselRetire, at.Add(time.Hour),
		testTargetVessel("vessel_1", 1, 2),
	); err != nil {
		t.Fatalf("append after restart: %v", err)
	}
	entries = reopened.Query(Filter{})
	if len(entries) != 2 || entries[1].Sequence != 2 {
		t.Fatalf("sequence did not continue after restart: %v", entries)
	}

	// Persisted file must hold one JSON object per line, in sequence order.
	raw, err := os.ReadFile(filepath.Join(dataDir, logFileName))
	if err != nil {
		t.Fatalf("read persisted log: %v", err)
	}
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	lines := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "{") || strings.Count(line, "{") != 1 {
			t.Fatalf("persisted line is not a single flat JSON object: %q", line)
		}
		lines++
	}
	if lines != 2 {
		t.Fatalf("expected 2 persisted lines, got %d", lines)
	}
}

func TestConcurrentAppendsDoNotInterleaveOrSkip(t *testing.T) {
	log := openTestLog(t)
	at := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)

	const goroutines = 16
	const perGoroutine = 40
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			id := fmt.Sprintf("formula_%02d", g)
			for i := 0; i < perGoroutine; i++ {
				if err := log.Append(OpFormulaApprove, at.Add(time.Duration(g*perGoroutine+i)*time.Second),
					testTarget(id, int64(i+1), int64(i+2)),
				); err != nil {
					errs <- err
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent append failed: %v", err)
	}

	entries := log.Query(Filter{})
	want := goroutines * perGoroutine
	if len(entries) != want {
		t.Fatalf("expected %d entries, got %d", want, len(entries))
	}
	seen := make(map[int64]bool, want)
	for _, entry := range entries {
		if seen[entry.Sequence] {
			t.Fatalf("duplicate sequence %d", entry.Sequence)
		}
		seen[entry.Sequence] = true
	}
	for sequence := int64(1); sequence <= int64(want); sequence++ {
		if !seen[sequence] {
			t.Fatalf("missing sequence %d", sequence)
		}
	}

	// Reopening must reconcile exactly the same contiguous trail.
	if err := log.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened := mustOpen(t, filepath.Dir(log.path))
	defer reopened.Close()
	if len(reopened.Query(Filter{})) != want {
		t.Fatalf("restarted log lost entries")
	}
}

func TestInvalidEntriesAreRejectedBeforeWriting(t *testing.T) {
	log := openTestLog(t)
	at := time.Now().UTC()

	cases := []struct {
		name    string
		targets []Target
	}{
		{"unknown kind", []Target{{Kind: "recipe", ID: "x", AfterVersion: 1}}},
		{"empty id", []Target{{Kind: KindRun, ID: "  ", AfterVersion: 1}}},
		{"non-positive after version", []Target{{Kind: KindRun, ID: "run_1", AfterVersion: 0}}},
		{"before not below after", []Target{{
			Kind: KindRun, ID: "run_1", BeforeVersion: ptrInt64(3), AfterVersion: 3,
		}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := log.Append(OpRunAbort, at, tc.targets...)
			if err == nil {
				t.Fatalf("expected rejection")
			}
		})
	}
	if entries := log.Query(Filter{}); len(entries) != 0 {
		t.Fatalf("rejected appends must leave no entries, got %d", len(entries))
	}
}

func TestCorruptTrailFailsClosed(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, logFileName)
	if err := os.WriteFile(path, []byte("{not json\n"), 0o644); err != nil {
		t.Fatalf("seed corrupt log: %v", err)
	}
	if _, err := Open(dataDir); err == nil {
		t.Fatalf("opening a corrupt trail must fail")
	}

	// A sequence gap is also rejected rather than silently continuing.
	if err := os.WriteFile(path, []byte(
		`{"sequence":1,"operation":"run_create","target_kind":"run","target_id":"run_1","after_version":1,"recorded_at":"2026-09-29T10:00:00Z","summary":""}`+"\n"+
			`{"sequence":3,"operation":"run_abort","target_kind":"run","target_id":"run_1","before_version":1,"after_version":2,"recorded_at":"2026-09-29T10:01:00Z","summary":""}`+"\n",
	), 0o644); err != nil {
		t.Fatalf("seed gapped log: %v", err)
	}
	if _, err := Open(dataDir); err == nil {
		t.Fatalf("opening a gapped trail must fail")
	}
}

func TestAppendAfterCloseFailsWithoutMutation(t *testing.T) {
	log := openTestLog(t)
	if err := log.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	err := log.Append(OpFormulaCreate, time.Now().UTC(), testTarget("formula_1", 0, 1))
	if err == nil {
		t.Fatalf("append after close must fail")
	}
	if entries := log.Query(Filter{}); len(entries) != 0 {
		t.Fatalf("closed log must not gain entries")
	}
}

func openTestLog(t *testing.T) *Log {
	t.Helper()
	log := mustOpen(t, t.TempDir())
	t.Cleanup(func() { _ = log.Close() })
	return log
}

func mustOpen(t *testing.T, dataDir string) *Log {
	t.Helper()
	log, err := Open(dataDir)
	if err != nil {
		t.Fatalf("open audit log: %v", err)
	}
	return log
}

func testTargetVessel(id string, before, after int64) Target {
	target := testTarget(id, before, after)
	target.Kind = KindVessel
	return target
}

func ptrTime(value time.Time) *time.Time {
	return &value
}

func ptrInt64(value int64) *int64 {
	return &value
}
