package service

import (
	"sort"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/audit"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

func (s *Service) CreateRun(input CreateRunInput) (domain.FermentationRun, error) {
	now := s.clock.Now()
	code := domain.NormalizeRunCode(input.Code)
	if code == "" {
		generated, err := generatedRunCode()
		if err != nil {
			return domain.FermentationRun{}, domain.NewError(domain.CodeStorage, err.Error())
		}
		code = generated
	}
	run := domain.FermentationRun{
		Code:           code,
		FormulaID:      input.FormulaID,
		PlannedVolumeL: input.PlannedVolumeL,
		Notes:          input.Notes,
		State:          domain.RunPlanned,
		Observations:   []domain.GravityObservation{},
		Version:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := run.Validate(); err != nil {
		return domain.FermentationRun{}, err
	}
	id, err := generateEntityID("run")
	if err != nil {
		return domain.FermentationRun{}, domain.NewError(domain.CodeStorage, err.Error())
	}
	run.ID = id

	err = s.store.Execute(func(snapshot *domain.Snapshot) error {
		formula, ok := snapshot.Formulas[run.FormulaID]
		if !ok {
			return domain.NewError(domain.CodeFormulaNotFound, "formula was not found")
		}
		if formula.State != domain.FormulaApproved {
			return domain.NewError(domain.CodeFormulaState,
				"run requires an approved formula")
		}
		if _, exists := snapshot.RunIndex[run.Code]; exists {
			return domain.NewError(domain.CodeRunConflict,
				"a run with the same code already exists")
		}
		snapshot.Runs[run.ID] = run.Clone()
		snapshot.RunIndex[run.Code] = run.ID
		return nil
	}, s.auditCommit(audit.OpRunCreate, now, func() []audit.Target {
		return []audit.Target{{
			Kind:         audit.KindRun,
			ID:           run.ID,
			AfterVersion: run.Version,
			Summary:      runSummary(run),
		}}
	}))
	if err != nil {
		return domain.FermentationRun{}, err
	}
	return run.Clone(), nil
}

func (s *Service) ReserveRun(
	id string,
	input ReserveRunInput,
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
		vessel, ok := snapshot.Vessels[input.VesselID]
		if !ok {
			return domain.NewError(domain.CodeVesselNotFound, "vessel was not found")
		}
		if run.PlannedVolumeL > vessel.CapacityL {
			return domain.FieldError(domain.CodeInvalidInput,
				"planned_volume_liters", "exceeds vessel capacity")
		}
		beforeRunVersion = run.Version
		beforeVesselVersion = vessel.Version
		if err := vessel.Reserve(run.ID, s.clock.Now()); err != nil {
			return err
		}
		if err := run.ReserveVessel(vessel.ID, s.clock.Now()); err != nil {
			return err
		}
		snapshot.Runs[id] = run.Clone()
		snapshot.Vessels[vessel.ID] = vessel
		updatedRun = run.Clone()
		updatedVessel = vessel
		return nil
	}, s.auditCommit(audit.OpRunReserve, recordedAt, func() []audit.Target {
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

func (s *Service) StartRun(
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
		if run.VesselID == nil {
			return domain.NewError(domain.CodeRunState, "run has no vessel")
		}
		vessel, ok := snapshot.Vessels[*run.VesselID]
		if !ok {
			return domain.NewError(domain.CodeVesselNotFound, "vessel was not found")
		}
		beforeRunVersion = run.Version
		beforeVesselVersion = vessel.Version
		if err := vessel.StartRun(run.ID, s.clock.Now()); err != nil {
			return err
		}
		if err := run.StartFermentation(s.clock.Now()); err != nil {
			return err
		}
		snapshot.Runs[id] = run.Clone()
		snapshot.Vessels[vessel.ID] = vessel
		updatedRun = run.Clone()
		updatedVessel = vessel
		return nil
	}, s.auditCommit(audit.OpRunStart, recordedAt, func() []audit.Target {
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

func (s *Service) AbortRun(
	id string,
	input VersionInput,
) (domain.FermentationRun, domain.Vessel, error) {
	recordedAt := s.clock.Now()
	var updatedRun domain.FermentationRun
	var updatedVessel domain.Vessel
	var beforeRunVersion, beforeVesselVersion int64
	vesselTouched := false
	err := s.store.Execute(func(snapshot *domain.Snapshot) error {
		run, ok := snapshot.Runs[id]
		if !ok {
			return domain.NewError(domain.CodeRunNotFound, "run was not found")
		}
		if err := s.checkExpected(run.Version, input.ExpectedVersion); err != nil {
			return err
		}
		hadStarted := run.HadStarted()
		beforeRunVersion = run.Version
		if err := run.Abort(s.clock.Now()); err != nil {
			return err
		}
		if run.VesselID != nil {
			vessel, ok := snapshot.Vessels[*run.VesselID]
			if !ok {
				return domain.NewError(domain.CodeVesselNotFound, "vessel was not found")
			}
			beforeVesselVersion = vessel.Version
			vesselTouched = true
			if hadStarted {
				if err := vessel.StartCleaning(run.ID, s.clock.Now()); err != nil {
					return err
				}
			} else if err := vessel.ReleaseReservation(run.ID, s.clock.Now()); err != nil {
				return err
			}
			snapshot.Vessels[vessel.ID] = vessel
			updatedVessel = vessel
		}
		snapshot.Runs[id] = run.Clone()
		updatedRun = run.Clone()
		return nil
	}, s.auditCommit(audit.OpRunAbort, recordedAt, func() []audit.Target {
		targets := []audit.Target{{
			Kind:          audit.KindRun,
			ID:            id,
			BeforeVersion: &beforeRunVersion,
			AfterVersion:  updatedRun.Version,
			Summary:       runSummary(updatedRun),
		}}
		if vesselTouched {
			targets = append(targets, audit.Target{
				Kind:          audit.KindVessel,
				ID:            updatedVessel.ID,
				BeforeVersion: &beforeVesselVersion,
				AfterVersion:  updatedVessel.Version,
				Summary:       vesselSummary(updatedVessel),
			})
		}
		return targets
	}))
	if err != nil {
		return domain.FermentationRun{}, domain.Vessel{}, err
	}
	return updatedRun, updatedVessel, nil
}

func (s *Service) GetRun(id string) (domain.FermentationRun, error) {
	var found domain.FermentationRun
	err := s.store.View(func(snapshot domain.Snapshot) error {
		run, ok := snapshot.Runs[id]
		if !ok {
			return domain.NewError(domain.CodeRunNotFound, "run was not found")
		}
		found = run.Clone()
		return nil
	})
	if err != nil {
		return domain.FermentationRun{}, err
	}
	return found, nil
}

func (s *Service) ListRuns() ([]domain.FermentationRun, error) {
	var runs []domain.FermentationRun
	err := s.store.View(func(snapshot domain.Snapshot) error {
		runs = make([]domain.FermentationRun, 0, len(snapshot.Runs))
		for _, run := range snapshot.Runs {
			runs = append(runs, run.Clone())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].Code == runs[j].Code {
			return runs[i].ID < runs[j].ID
		}
		return runs[i].Code < runs[j].Code
	})
	return runs, nil
}
