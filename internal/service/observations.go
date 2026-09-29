package service

import (
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/audit"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

func (s *Service) AppendObservation(
	id string,
	input ObservationInput,
) (domain.FermentationRun, error) {
	recordedAt := s.clock.Now()
	var updated domain.FermentationRun
	var beforeVersion int64
	err := s.store.Execute(func(snapshot *domain.Snapshot) error {
		run, ok := snapshot.Runs[id]
		if !ok {
			return domain.NewError(domain.CodeRunNotFound, "run was not found")
		}
		now := s.clock.Now()
		observation := domain.GravityObservation{
			RunID:        run.ID,
			Sequence:     input.Sequence,
			Gravity:      input.Gravity,
			TemperatureC: input.TemperatureC,
			ObservedAt:   input.ObservedAt.UTC(),
			Note:         input.Note,
			ReceivedAt:   now,
		}
		beforeVersion = run.Version
		if err := run.AddObservation(observation); err != nil {
			return err
		}
		snapshot.Runs[id] = run.Clone()
		updated = run.Clone()
		return nil
	}, s.auditCommit(audit.OpRunAppendObservation, recordedAt, func() []audit.Target {
		return []audit.Target{{
			Kind:          audit.KindRun,
			ID:            id,
			BeforeVersion: &beforeVersion,
			AfterVersion:  updated.Version,
			Summary:       runSummary(updated),
		}}
	}))
	if err != nil {
		return domain.FermentationRun{}, err
	}
	return updated, nil
}

func (s *Service) BeginConditioning(
	id string,
	input VersionInput,
) (domain.FermentationRun, error) {
	recordedAt := s.clock.Now()
	var updated domain.FermentationRun
	var beforeVersion int64
	err := s.store.Execute(func(snapshot *domain.Snapshot) error {
		run, ok := snapshot.Runs[id]
		if !ok {
			return domain.NewError(domain.CodeRunNotFound, "run was not found")
		}
		if err := s.checkExpected(run.Version, input.ExpectedVersion); err != nil {
			return err
		}
		beforeVersion = run.Version
		if err := run.BeginConditioning(s.clock.Now()); err != nil {
			return err
		}
		snapshot.Runs[id] = run.Clone()
		updated = run.Clone()
		return nil
	}, s.auditCommit(audit.OpRunBeginConditioning, recordedAt, func() []audit.Target {
		return []audit.Target{{
			Kind:          audit.KindRun,
			ID:            id,
			BeforeVersion: &beforeVersion,
			AfterVersion:  updated.Version,
			Summary:       runSummary(updated),
		}}
	}))
	if err != nil {
		return domain.FermentationRun{}, err
	}
	return updated, nil
}

func (s *Service) CompleteRun(
	id string,
	input VersionInput,
) (domain.FermentationRun, domain.Vessel, error) {
	recordedAt := s.clock.Now()
	var updatedRun domain.FermentationRun
	var updatedVessel domain.Vessel
	var beforeRunVersion, beforeVesselVersion int64
	err := s.store.Execute(func(snapshot *domain.Snapshot) error {
		run, ok := snapshot.Runs[id]
		if !ok {
			return domain.NewError(domain.CodeRunNotFound, "run was not found")
		}
		if err := s.checkExpected(run.Version, input.ExpectedVersion); err != nil {
			return err
		}
		formula, ok := snapshot.Formulas[run.FormulaID]
		if !ok {
			return domain.NewError(domain.CodeFormulaNotFound, "attached formula was not found")
		}
		beforeRunVersion = run.Version
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
		beforeVesselVersion = vessel.Version
		if err := vessel.StartCleaning(run.ID, s.clock.Now()); err != nil {
			return err
		}
		snapshot.Runs[id] = run.Clone()
		snapshot.Vessels[vessel.ID] = vessel
		updatedRun = run.Clone()
		updatedVessel = vessel
		return nil
	}, s.auditCommit(audit.OpRunComplete, recordedAt, func() []audit.Target {
		return []audit.Target{
			{
				Kind:          audit.KindRun,
				ID:            id,
				BeforeVersion: &beforeRunVersion,
				AfterVersion:  updatedRun.Version,
				Summary:       runSummary(updatedRun),
			},
			{
				Kind:          audit.KindVessel,
				ID:            updatedVessel.ID,
				BeforeVersion: &beforeVesselVersion,
				AfterVersion:  updatedVessel.Version,
				Summary:       vesselSummary(updatedVessel),
			},
		}
	}))
	if err != nil {
		return domain.FermentationRun{}, domain.Vessel{}, err
	}
	return updatedRun, updatedVessel, nil
}
