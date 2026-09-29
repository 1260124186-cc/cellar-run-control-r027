package service

import (
	"strings"
	"testing"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/audit"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/clock"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
)

func newTestService(t *testing.T) (*Service, *audit.Log) {
	t.Helper()
	dir := t.TempDir()
	store, err := storage.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	auditLog, err := audit.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = auditLog.Close() })
	fixed := clock.NewFixed(time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC))
	return New(store, fixed, auditLog), auditLog
}

func validFormulaInput() CreateFormulaInput {
	return CreateFormulaInput{
		Name:                  "Test Formula",
		Style:                 "Ale",
		TargetOriginalGravity: 1.050,
		TargetFinalGravity:    1.010,
		MaxFermentationTempC:  22,
		MinimumDays:           7,
		MaximumDays:           14,
	}
}

func TestSuccessfulWritesAreAudited(t *testing.T) {
	app, auditLog := newTestService(t)

	formula, err := app.CreateFormula(validFormulaInput())
	if err != nil {
		t.Fatalf("create formula: %v", err)
	}
	vessel, err := app.CreateVessel(CreateVesselInput{Code: "VES-1", CapacityL: 100})
	if err != nil {
		t.Fatalf("create vessel: %v", err)
	}

	entries := auditLog.Query(audit.Query{})
	if len(entries) != 2 {
		t.Fatalf("expected 2 audit entries, got %d", len(entries))
	}

	formulaEntry := entries[0]
	if formulaEntry.Operation != audit.OperationFormulaCreate {
		t.Fatalf("operation = %q", formulaEntry.Operation)
	}
	if formulaEntry.TargetKind != audit.KindFormula || formulaEntry.TargetID != formula.ID {
		t.Fatalf("formula entry attribution = %s/%s", formulaEntry.TargetKind, formulaEntry.TargetID)
	}
	if formulaEntry.VersionBefore != 0 || formulaEntry.VersionAfter != 1 {
		t.Fatalf("versions = %d->%d, want 0->1",
			formulaEntry.VersionBefore, formulaEntry.VersionAfter)
	}
	if formulaEntry.RecordedAt.Location() != time.UTC {
		t.Fatalf("recorded_at not UTC")
	}

	vesselEntry := entries[1]
	if vesselEntry.Operation != audit.OperationVesselCreate ||
		vesselEntry.TargetID != vessel.ID ||
		vesselEntry.VersionAfter != 1 {
		t.Fatalf("unexpected vessel entry: %+v", vesselEntry)
	}
}

func TestFailedBusinessWritesLeaveNoAuditEntry(t *testing.T) {
	app, auditLog := newTestService(t)

	input := validFormulaInput()
	if _, err := app.CreateFormula(input); err != nil {
		t.Fatal(err)
	}
	// Duplicate normalized name fails the business write.
	input.Name = "  test   formula "
	if _, err := app.CreateFormula(input); err == nil {
		t.Fatal("expected duplicate name conflict")
	}
	if entries := auditLog.Query(audit.Query{}); len(entries) != 1 {
		t.Fatalf("failed write must not be audited, got %d entries", len(entries))
	}
}

func TestSensitiveFreeTextNeverReachesAuditSummary(t *testing.T) {
	app, auditLog := newTestService(t)

	const secret = "SECRET-NOTE-VALUE-123"

	formula, err := app.CreateFormula(validFormulaInput())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.SetFormulaSteps(formula.ID, SetFormulaStepsInput{
		Steps: []domain.FormulaStep{{
			Sequence:     1,
			Label:        "Ferment",
			TemperatureC: 20,
			HoldMinutes:  60,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ApproveFormula(formula.ID, VersionInput{}); err != nil {
		t.Fatal(err)
	}

	vessel, err := app.CreateVessel(CreateVesselInput{Code: "VES-SECRET", CapacityL: 100})
	if err != nil {
		t.Fatalf("create vessel: %v", err)
	}
	run, err := app.CreateRun(CreateRunInput{
		Code:           "RUN-SECRET-1",
		FormulaID:      formula.ID,
		PlannedVolumeL: 50,
		Notes:          secret,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := app.ReserveRun(run.ID, ReserveRunInput{VesselID: vessel.ID}); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if _, _, err := app.StartRun(run.ID, VersionInput{}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := app.AppendObservation(run.ID, ObservationInput{
		Sequence:     1,
		Gravity:      1.045,
		TemperatureC: 20,
		ObservedAt:   time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC),
		Note:         secret,
	}); err != nil {
		t.Fatalf("observation: %v", err)
	}

	for _, entry := range auditLog.Query(audit.Query{}) {
		if strings.Contains(entry.Summary, secret) {
			t.Fatalf("audit summary for %s leaked request free text: %q",
				entry.Operation, entry.Summary)
		}
	}
}

func TestAuditFailureDoesNotChangeBusinessSuccess(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	auditLog, err := audit.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Close the audit log so every append fails; the business writes must
	// still succeed and remain readable.
	if err := auditLog.Close(); err != nil {
		t.Fatal(err)
	}
	app := New(store, clock.NewFixed(time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC)), auditLog)

	vessel, err := app.CreateVessel(CreateVesselInput{Code: "VES-OK", CapacityL: 200})
	if err != nil {
		t.Fatalf("business create must succeed with a broken audit log, got %v", err)
	}
	got, err := app.GetVessel(vessel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != vessel.ID || got.State != domain.VesselAvailable {
		t.Fatalf("persisted vessel mismatch: %+v", got)
	}
}

func TestAuditEntriesQueryByTarget(t *testing.T) {
	app, auditLog := newTestService(t)

	first, err := app.CreateFormula(validFormulaInput())
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.CreateVessel(CreateVesselInput{Code: "VES-Q", CapacityL: 50})
	if err != nil {
		t.Fatal(err)
	}

	byFormula := auditLog.Query(audit.Query{TargetKind: audit.KindFormula})
	if len(byFormula) != 1 || byFormula[0].TargetID != first.ID {
		t.Fatalf("kind filter returned %+v", byFormula)
	}
	byID := auditLog.Query(audit.Query{TargetID: second.ID})
	if len(byID) != 1 || byID[0].TargetKind != audit.KindVessel {
		t.Fatalf("id filter returned %+v", byID)
	}
}
