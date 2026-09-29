package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testRecord(sequenceHint int) Record {
	return Record{
		Operation:     OperationVesselCreate,
		TargetKind:    KindVessel,
		TargetID:      "vessel_test",
		VersionBefore: 0,
		VersionAfter:  1,
		RecordedAt:    time.Date(2026, 9, 29, 10, 0, sequenceHint, 0, time.UTC),
		Summary:       "vessel created; state=available",
	}
}

func TestAppendAssignsContiguousSequence(t *testing.T) {
	log, _ := openTestLog(t)

	for want := int64(1); want <= 5; want++ {
		entry, err := log.Append(testRecord(int(want)))
		if err != nil {
			t.Fatalf("append %d: %v", want, err)
		}
		if entry.Sequence != want {
			t.Fatalf("sequence = %d, want %d", entry.Sequence, want)
		}
	}

	entries := log.Query(Query{})
	if len(entries) != 5 {
		t.Fatalf("query returned %d entries, want 5", len(entries))
	}
	for i, entry := range entries {
		if entry.Sequence != int64(i+1) {
			t.Fatalf("entry %d has sequence %d", i, entry.Sequence)
		}
		if entry.RecordedAt.Location() != time.UTC {
			t.Fatalf("recorded time not normalized to UTC")
		}
	}
}

func TestConcurrentAppendsDoNotInterleaveOrSkip(t *testing.T) {
	log, dir := openTestLog(t)

	const writers = 32
	const perWriter = 25
	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				if _, err := log.Append(testRecord(i)); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent append failed: %v", err)
	}

	want := int64(writers * perWriter)
	entries := log.Query(Query{})
	if int64(len(entries)) != want {
		t.Fatalf("entries = %d, want %d", len(entries), want)
	}

	// In-memory view: every sequence from 1..want appears exactly once.
	seen := make(map[int64]bool, want)
	for _, entry := range entries {
		if seen[entry.Sequence] {
			t.Fatalf("duplicate sequence %d", entry.Sequence)
		}
		seen[entry.Sequence] = true
	}
	for sequence := int64(1); sequence <= want; sequence++ {
		if !seen[sequence] {
			t.Fatalf("missing sequence %d", sequence)
		}
	}

	// On-disk view: one complete JSON object per line in sequence order, so
	// nothing was interleaved or torn at the byte level.
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != int(want) {
		t.Fatalf("log lines = %d, want %d", len(lines), want)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	for expected := int64(1); expected <= want; expected++ {
		var entry Entry
		if err := decoder.Decode(&entry); err != nil {
			t.Fatalf("line %d is not a complete entry: %v", expected, err)
		}
		if entry.Sequence != expected {
			t.Fatalf("on-disk sequence = %d, want %d", entry.Sequence, expected)
		}
	}
}

func TestSequenceAndAttributionSurviveRestart(t *testing.T) {
	dir := t.TempDir()

	first, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := first.Append(testRecord(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	entry, err := reopened.Append(testRecord(3))
	if err != nil {
		t.Fatalf("append after restart: %v", err)
	}
	if entry.Sequence != 4 {
		t.Fatalf("sequence after restart = %d, want 4", entry.Sequence)
	}

	entries := reopened.Query(Query{})
	if len(entries) != 4 {
		t.Fatalf("replayed entries = %d, want 4", len(entries))
	}
	for i, entry := range entries {
		if entry.Sequence != int64(i+1) {
			t.Fatalf("replayed sequence mismatch at %d: %d", i, entry.Sequence)
		}
		if entry.TargetID != "vessel_test" {
			t.Fatalf("target attribution lost on restart: %q", entry.TargetID)
		}
	}
}

func TestNonContiguousLogStopsStartup(t *testing.T) {
	dir := t.TempDir()
	log, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := log.Append(testRecord(0)); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, FileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Rewrite the sole entry to claim sequence 2, breaking contiguity.
	var entry Entry
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatal(err)
	}
	entry.Sequence = 2
	tampered, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(tampered, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(dir); err == nil {
		t.Fatal("expected startup to reject a non-contiguous log")
	}
}

func TestQueryFilters(t *testing.T) {
	log, _ := openTestLog(t)

	base := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	records := []Record{
		{OperationVesselCreate, KindVessel, "vessel_a", 0, 1, base, "vessel a"},
		{OperationRunCreate, KindRun, "run_a", 0, 1, base.Add(time.Minute), "run a"},
		{OperationRunReserve, KindRun, "run_a", 1, 2, base.Add(2 * time.Minute), "reserve"},
		{OperationRunComplete, KindRun, "run_b", 4, 5, base.Add(3 * time.Minute), "complete"},
	}
	for _, record := range records {
		if _, err := log.Append(record); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name  string
		query Query
		want  int
	}{
		{"no filter", Query{}, 4},
		{"target id", Query{TargetID: "run_a"}, 2},
		{"operation", Query{Operation: OperationRunCreate}, 1},
		{"kind", Query{TargetKind: KindRun}, 3},
		{"from inclusive", Query{From: pointer(base)}, 4},
		{"from later", Query{From: pointer(base.Add(2 * time.Minute))}, 2},
		{"to inclusive", Query{To: pointer(base.Add(time.Minute))}, 2},
		{"window", Query{From: pointer(base.Add(time.Minute)), To: pointer(base.Add(2 * time.Minute))}, 2},
		{"combined", Query{TargetID: "run_a", Operation: OperationRunReserve}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := log.Query(tc.query)
			if len(got) != tc.want {
				t.Fatalf("Query(%s) returned %d entries, want %d", tc.name, len(got), tc.want)
			}
			for i := 1; i < len(got); i++ {
				if got[i-1].Sequence > got[i].Sequence {
					t.Fatalf("Query(%s) did not preserve sequence order", tc.name)
				}
			}
		})
	}
}

func TestQueryReturnsCopies(t *testing.T) {
	log, _ := openTestLog(t)
	if _, err := log.Append(testRecord(0)); err != nil {
		t.Fatal(err)
	}
	first := log.Query(Query{})
	first[0].Summary = "mutated by caller"
	again := log.Query(Query{})
	if again[0].Summary != "vessel created; state=available" {
		t.Fatal("caller was able to mutate log-owned entries")
	}
}

func TestAppendRejectsInvalidRecords(t *testing.T) {
	log, _ := openTestLog(t)
	record := testRecord(0)
	record.Summary = ""
	if _, err := log.Append(record); err == nil {
		t.Fatal("expected invalid record to be rejected")
	}
	record = testRecord(0)
	record.TargetID = ""
	if _, err := log.Append(record); err == nil {
		t.Fatal("expected record without target id to be rejected")
	}
	if entries := log.Query(Query{}); len(entries) != 0 {
		t.Fatalf("rejected records must not be persisted, got %d", len(entries))
	}
}

func TestAppendFailsAfterClose(t *testing.T) {
	log, _ := openTestLog(t)
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := log.Append(testRecord(0)); err == nil {
		t.Fatal("append after close must fail")
	}
}

// openTestLog opens a log in a fresh temporary directory and returns the
// directory too, so tests can inspect the on-disk file. The directory is
// cleaned up automatically by the testing package.
func openTestLog(t *testing.T) (*Log, string) {
	t.Helper()
	dir := t.TempDir()
	log, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log, dir
}

func pointer(value time.Time) *time.Time { return &value }
