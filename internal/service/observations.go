package service

import (
	"fmt"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/audit"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

func (s *Service) AppendObservation(
	id string,
	input ObservationInput,
) (domain.FermentationRun, error) {
	requestedAt := s.clock.Now()
	var updated domain.FermentationRun
	var before int64
	var appended domain.GravityObservation
	err := s.commit(func(snapshot *domain.Snapshot) error {
		run, ok := snapshot.Runs[id]
		if !ok {
			return domain.NewError(domain.CodeRunNotFound, "run was not found")
		}
		before = run.Version
		now := s.clock.Now()
		observation := domain.GravityObservation{
			RunID:        run.ID,
			Sequence:     input.Sequence,
			Gravity:      input.Gravity,
			TemperatureC: input.TemperatureC,
			ObservedAt:   input.ObservedAt.UTC(),
			// Note is request-supplied free text and is intentionally
			// excluded from the audit summary.
			Note:       input.Note,
			ReceivedAt: now,
		}
		if err := run.AddObservation(observation); err != nil {
			return err
		}
		appended = observation
		snapshot.Runs[id] = run.Clone()
		updated = run.Clone()
		return nil
	}, func() audit.Record {
		return audit.Record{
			Operation:     audit.OperationRunObservationAppend,
			TargetKind:    audit.KindRun,
			TargetID:      id,
			VersionBefore: before,
			VersionAfter:  updated.Version,
			RecordedAt:    requestedAt,
			Summary: fmt.Sprintf(
				"observation appended; sequence=%d gravity=%g temperature_c=%g observations=%d",
				appended.Sequence, appended.Gravity, appended.TemperatureC,
				len(updated.Observations)),
		}
	})
	if err != nil {
		return domain.FermentationRun{}, err
	}
	return updated, nil
}

func (s *Service) BeginConditioning(
	id string,
	input VersionInput,
) (domain.FermentationRun, error) {
	requestedAt := s.clock.Now()
	var updated domain.FermentationRun
	var before int64
	err := s.commit(func(snapshot *domain.Snapshot) error {
		run, ok := snapshot.Runs[id]
		if !ok {
			return domain.NewError(domain.CodeRunNotFound, "run was not found")
		}
		before = run.Version
		if err := s.checkExpected(run.Version, input.ExpectedVersion); err != nil {
			return err
		}
		if err := run.BeginConditioning(s.clock.Now()); err != nil {
			return err
		}
		snapshot.Runs[id] = run.Clone()
		updated = run.Clone()
		return nil
	}, func() audit.Record {
		return audit.Record{
			Operation:     audit.OperationRunConditioningBegin,
			TargetKind:    audit.KindRun,
			TargetID:      id,
			VersionBefore: before,
			VersionAfter:  updated.Version,
			RecordedAt:    requestedAt,
			Summary: fmt.Sprintf("conditioning begun; state=%s observations=%d",
				updated.State, len(updated.Observations)),
		}
	})
	if err != nil {
		return domain.FermentationRun{}, err
	}
	return updated, nil
}

func (s *Service) CompleteRun(
	id string,
	input VersionInput,
) (domain.FermentationRun, domain.Vessel, error) {
	requestedAt := s.clock.Now()
	var updatedRun domain.FermentationRun
	var updatedVessel domain.Vessel
	var before int64
	err := s.commit(func(snapshot *domain.Snapshot) error {
		run, ok := snapshot.Runs[id]
		if !ok {
			return domain.NewError(domain.CodeRunNotFound, "run was not found")
		}
		before = run.Version
		if err := s.checkExpected(run.Version, input.ExpectedVersion); err != nil {
			return err
		}
		formula, ok := snapshot.Formulas[run.FormulaID]
		if !ok {
			return domain.NewError(domain.CodeFormulaNotFound, "attached formula was not found")
		}
		if err := run.Complete(formula.TargetOriginalGravity, s.clock.Now()); err != nil {
			return err
		}
		if run.VesselID == nil {
			return domain.NewError(domain.CodeRunState, "run has no vessel")
		}
		vessel, ok := snapshot.Vessels[*run.VesselID]
		if !ok {
			return domain.NewError(domain.CodeVesselNotFound, "vessel was not found")
		}
		if err := vessel.StartCleaning(run.ID, s.clock.Now()); err != nil {
			return err
		}
		snapshot.Runs[id] = run.Clone()
		snapshot.Vessels[vessel.ID] = vessel
		updatedRun = run.Clone()
		updatedVessel = vessel
		return nil
	}, func() audit.Record {
		return audit.Record{
			Operation:     audit.OperationRunComplete,
			TargetKind:    audit.KindRun,
			TargetID:      id,
			VersionBefore: before,
			VersionAfter:  updatedRun.Version,
			RecordedAt:    requestedAt,
			Summary: fmt.Sprintf(
				"run completed; state=%s final_gravity=%g attenuation_percent=%g abv=%g observations=%d vessel_state=%s",
				updatedRun.State,
				updatedRun.Metrics.FinalGravity,
				updatedRun.Metrics.ApparentAttenuation,
				updatedRun.Metrics.EstimatedAlcoholABV,
				updatedRun.Metrics.ObservationCount,
				updatedVessel.State),
		}
	})
	if err != nil {
		return domain.FermentationRun{}, domain.Vessel{}, err
	}
	return updatedRun, updatedVessel, nil
}
