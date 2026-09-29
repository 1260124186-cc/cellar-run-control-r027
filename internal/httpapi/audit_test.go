package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/audit"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/clock"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/service"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
)

type auditHTTPClient struct {
	baseURL string
	t       *testing.T
}

func (c *auditHTTPClient) do(method, path string, body any, expectedVersion *int64) (int, []byte) {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("encode body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		c.t.Fatalf("new request: %v", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if expectedVersion != nil {
		request.Header.Set("X-Expected-Version", fmt.Sprintf("%d", *expectedVersion))
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		c.t.Fatalf("perform %s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		c.t.Fatalf("read response: %v", err)
	}
	return response.StatusCode, raw
}

type auditEntryView struct {
	Sequence      int64     `json:"sequence"`
	Operation     string    `json:"operation"`
	TargetKind    string    `json:"target_kind"`
	TargetID      string    `json:"target_id"`
	BeforeVersion *int64    `json:"before_version"`
	AfterVersion  int64     `json:"after_version"`
	RecordedAt    time.Time `json:"recorded_at"`
	Summary       string    `json:"summary"`
}

func newAuditServer(t *testing.T) (*httptest.Server, string, *clock.Fixed) {
	t.Helper()
	dataDir := t.TempDir()
	store, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	auditLog, err := audit.Open(dataDir)
	if err != nil {
		t.Fatalf("open audit log: %v", err)
	}
	t.Cleanup(func() { _ = auditLog.Close() })
	fixed := clock.NewFixed(time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC))
	app := service.New(store, fixed, auditLog)
	server := httptest.NewServer(NewHandler(app))
	t.Cleanup(server.Close)
	return server, dataDir, fixed
}

func createApprovedFormula(t *testing.T, c *auditHTTPClient, name string) string {
	t.Helper()
	status, body := c.do("POST", "/v1/formulas", map[string]any{
		"name":                    name,
		"style":                   "Ale",
		"target_original_gravity": 1.050,
		"target_final_gravity":    1.010,
		"max_fermentation_temp_c": 25,
		"minimum_days":            7,
		"maximum_days":            14,
	}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create formula: status %d body %s", status, body)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode formula: %v", err)
	}
	status, body = c.do("PUT", "/v1/formulas/"+created.ID+"/steps", map[string]any{
		"steps": []map[string]any{{
			"sequence":      1,
			"label":         "Ferment",
			"temperature_c": 20,
			"hold_minutes":  1440,
		}},
	}, ptr64(1))
	if status != http.StatusOK {
		t.Fatalf("set steps: status %d body %s", status, body)
	}
	status, _ = c.do("POST", "/v1/formulas/"+created.ID+"/approve", nil, ptr64(2))
	if status != http.StatusOK {
		t.Fatalf("approve formula: status %d body %s", status, body)
	}
	return created.ID
}

func queryAudit(t *testing.T, c *auditHTTPClient, query string) []auditEntryView {
	t.Helper()
	status, body := c.do("GET", "/v1/audit-entries"+query, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("query audit entries %q: status %d body %s", query, status, body)
	}
	var response struct {
		Entries []auditEntryView `json:"entries"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode audit entries: %v", err)
	}
	return response.Entries
}

func TestAuditEndToEnd(t *testing.T) {
	server, _, _ := newAuditServer(t)
	c := &auditHTTPClient{baseURL: server.URL, t: t}

	formulaID := createApprovedFormula(t, c, "Audit Pale")

	// Failed writes (unknown target) must not produce audit entries.
	status, _ := c.do("POST", "/v1/formulas/formula_missing/approve", nil, nil)
	if status != http.StatusNotFound {
		t.Fatalf("expected 404 for missing formula, got %d", status)
	}

	entries := queryAudit(t, c, "")
	operations := make([]string, 0, len(entries))
	for _, entry := range entries {
		operations = append(operations, entry.Operation)
	}
	wantOps := []string{"formula_create", "formula_set_steps", "formula_approve"}
	if len(operations) != len(wantOps) {
		t.Fatalf("expected %d entries, got %d (%v)", len(wantOps), len(operations), operations)
	}
	for index, want := range wantOps {
		if operations[index] != want {
			t.Fatalf("entry %d operation = %q, want %q", index, operations[index], want)
		}
		if entries[index].Sequence != int64(index+1) {
			t.Fatalf("entry %d sequence = %d", index, entries[index].Sequence)
		}
	}
	if entries[0].BeforeVersion != nil || entries[0].AfterVersion != 1 {
		t.Fatalf("create entry versions wrong: %+v", entries[0])
	}
	if entries[2].BeforeVersion == nil || *entries[2].BeforeVersion != 2 ||
		entries[2].AfterVersion != 3 {
		t.Fatalf("approve entry versions wrong: %+v", entries[2])
	}

	// Filters: by target, operation, and time range.
	byTarget := queryAudit(t, c, "?target_id="+formulaID)
	if len(byTarget) != 3 {
		t.Fatalf("target filter expected 3 entries, got %d", len(byTarget))
	}
	byOperation := queryAudit(t, c, "?operation=formula_approve")
	if len(byOperation) != 1 || byOperation[0].TargetID != formulaID {
		t.Fatalf("operation filter returned %v", byOperation)
	}
	byWindow := queryAudit(t, c,
		"?from=2026-09-29T11:00:00Z&to=2026-09-29T13:00:00Z")
	if len(byWindow) != 3 {
		t.Fatalf("time range filter expected 3 entries, got %d", len(byWindow))
	}
	byEmptyWindow := queryAudit(t, c,
		"?from=2026-09-28T00:00:00Z&to=2026-09-28T23:59:59Z")
	if len(byEmptyWindow) != 0 {
		t.Fatalf("outside time range expected 0 entries, got %d", len(byEmptyWindow))
	}

	// Invalid filters are rejected as stable 400 errors.
	for _, query := range []string{
		"?target_kind=recipe",
		"?operation=formula_delete",
		"?from=not-a-time",
		"?from=2026-09-29T13:00:00Z&to=2026-09-29T11:00:00Z",
	} {
		status, _ := c.do("GET", "/v1/audit-entries"+query, nil, nil)
		if status != http.StatusBadRequest {
			t.Fatalf("query %q expected 400, got %d", query, status)
		}
	}
}

func TestAuditExcludesSensitiveRequestFields(t *testing.T) {
	server, dataDir, _ := newAuditServer(t)
	c := &auditHTTPClient{baseURL: server.URL, t: t}
	formulaID := createApprovedFormula(t, c, "Secret Stout")

	status, body := c.do("POST", "/v1/vessels", map[string]any{
		"code":            "VES-SECRET-1",
		"capacity_liters": 500,
	}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create vessel: %d %s", status, body)
	}
	vesselID := decodeVesselID(t, body)

	status, body = c.do("POST", "/v1/runs", map[string]any{
		"code":                  "RUN-SECRET-1",
		"formula_id":            formulaID,
		"planned_volume_liters": 200,
		"notes":                 "supersecret-operator-note",
	}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create run: %d %s", status, body)
	}
	var run struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}
	if err := json.Unmarshal(body, &run); err != nil {
		t.Fatalf("decode run: %v", err)
	}

	reserveStatus, reserveBody := c.do("POST", "/v1/runs/"+run.ID+"/reserve",
		map[string]any{"vessel_id": vesselID}, ptr64(run.Version))
	if reserveStatus != http.StatusOK {
		t.Fatalf("reserve run: %d %s", reserveStatus, reserveBody)
	}
	var reserveView struct {
		Run struct {
			Version int64 `json:"version"`
		} `json:"run"`
	}
	if err := json.Unmarshal(reserveBody, &reserveView); err != nil {
		t.Fatalf("decode reserve response: %v", err)
	}
	if status, _ := c.do("POST", "/v1/runs/"+run.ID+"/start", nil,
		ptr64(reserveView.Run.Version)); status != http.StatusOK {
		t.Fatalf("start run: %d", status)
	}

	observedAt := time.Date(2026, 9, 29, 12, 0, 30, 0, time.UTC)
	status, body = c.do("POST", "/v1/runs/"+run.ID+"/observations", map[string]any{
		"sequence":      1,
		"gravity":       1.045,
		"temperature_c": 20,
		"observed_at":   observedAt.Format(time.RFC3339),
		"note":          "supersecret-observation-note",
	}, nil)
	if status != http.StatusOK {
		t.Fatalf("append observation: %d %s", status, body)
	}

	// Nothing request-specific may appear in the persisted trail: no notes,
	// names, measured values, or vessel/run codes.
	raw, err := os.ReadFile(filepath.Join(dataDir, "audit.jsonl"))
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	content := string(raw)
	for _, secret := range []string{
		"supersecret",
		"Secret Stout",
		"SECRET",
		"1.045",
	} {
		if strings.Contains(content, secret) {
			t.Fatalf("audit log leaked request data %q: %s", secret, content)
		}
	}
	if !strings.Contains(content, `"operation":"run_append_observation"`) {
		t.Fatalf("audit log missing observation entry: %s", content)
	}
}

func decodeVesselID(t *testing.T, raw []byte) string {
	t.Helper()
	var vessel struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &vessel); err != nil {
		t.Fatalf("decode vessel: %v", err)
	}
	return vessel.ID
}

func TestAuditSurvivesRestart(t *testing.T) {
	server, dataDir, fixed := newAuditServer(t)
	c := &auditHTTPClient{baseURL: server.URL, t: t}
	formulaID := createApprovedFormula(t, c, "Restart Red")

	before := queryAudit(t, c, "?target_id="+formulaID)
	if len(before) != 3 || before[2].Sequence != 3 {
		t.Fatalf("unexpected entries before restart: %v", before)
	}

	// Simulate a process restart against the same data directory.
	server.Close()
	store, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	auditLog, err := audit.Open(dataDir)
	if err != nil {
		t.Fatalf("reopen audit log: %v", err)
	}
	t.Cleanup(func() { _ = auditLog.Close() })
	app := service.New(store, fixed, auditLog)
	restarted := httptest.NewServer(NewHandler(app))
	t.Cleanup(restarted.Close)
	c = &auditHTTPClient{baseURL: restarted.URL, t: t}

	after := queryAudit(t, c, "?target_id="+formulaID)
	if len(after) != 3 {
		t.Fatalf("entries did not survive restart: %v", after)
	}

	// A new write must continue the sequence and remain attributable.
	status, _ := c.do("POST", "/v1/vessels", map[string]any{
		"code":            "VES-RESTART-1",
		"capacity_liters": 300,
	}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create vessel after restart: %d", status)
	}
	all := queryAudit(t, c, "")
	if len(all) != 4 || all[3].Sequence != 4 || all[3].Operation != "vessel_create" {
		t.Fatalf("sequence did not continue after restart: %v", all)
	}
}

func TestConcurrentWritesKeepContiguousAuditTrail(t *testing.T) {
	server, _, _ := newAuditServer(t)
	c := &auditHTTPClient{baseURL: server.URL, t: t}

	const writers = 12
	const perWriter = 10
	var wg sync.WaitGroup
	failures := make(chan string, writers*perWriter)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				status, body := c.do("POST", "/v1/vessels", map[string]any{
					"code":            fmt.Sprintf("VES-%02d-%02d", w, i),
					"capacity_liters": 100,
				}, nil)
				if status != http.StatusCreated {
					failures <- fmt.Sprintf("writer %d item %d: status %d body %s",
						w, i, status, body)
				}
			}
		}(w)
	}
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Fatalf("concurrent write failed: %s", failure)
	}

	entries := queryAudit(t, c, "?target_kind=vessel")
	want := writers * perWriter
	if len(entries) != want {
		t.Fatalf("expected %d vessel audit entries, got %d", want, len(entries))
	}
	seen := make(map[int64]bool, want)
	for _, entry := range entries {
		if entry.Operation != "vessel_create" || entry.BeforeVersion != nil ||
			entry.AfterVersion != 1 {
			t.Fatalf("unexpected vessel entry: %+v", entry)
		}
		if seen[entry.Sequence] {
			t.Fatalf("duplicate audit sequence %d", entry.Sequence)
		}
		seen[entry.Sequence] = true
	}
	for sequence := int64(1); sequence <= int64(want); sequence++ {
		if !seen[sequence] {
			t.Fatalf("missing audit sequence %d", sequence)
		}
	}
}

func ptr64(value int64) *int64 {
	return &value
}
