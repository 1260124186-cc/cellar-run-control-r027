package service

import (
	"fmt"
	"log"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/audit"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

// recordAudit appends entries for a write that already succeeded. Audit
// failures are logged but never change the outcome of the business write.
func (s *Service) recordAudit(
	operation audit.Operation,
	at time.Time,
	targets ...audit.Target,
) {
	if s.audit == nil || len(targets) == 0 {
		return
	}
	if err := s.audit.Append(operation, at, targets...); err != nil {
		log.Printf("audit: could not record %s: %v", operation, err)
	}
}

// auditCommit prepares the post-commit callback handed to the store. It runs
// while the store write lock is held, immediately after the business snapshot
// is durably committed, which makes the audit sequence order identical to the
// business commit order even under concurrent writes. Targets are built lazily
// inside the callback so they read the aggregates and version numbers produced
// by the committed mutation instead of their pre-commit zero values.
func (s *Service) auditCommit(
	operation audit.Operation,
	at time.Time,
	buildTargets func() []audit.Target,
) func() {
	if s.audit == nil || buildTargets == nil {
		return nil
	}
	return func() {
		s.recordAudit(operation, at, buildTargets()...)
	}
}

// QueryAudit returns audit entries matching the filter in stable sequence
// order. The result is read-only.
func (s *Service) QueryAudit(filter audit.Filter) ([]audit.Entry, error) {
	if s.audit == nil {
		return []audit.Entry{}, nil
	}
	return s.audit.Query(filter), nil
}

// The summaries below describe only the resulting aggregate state. Request
// payload fields such as notes, names, or measured values are deliberately
// excluded so the audit trail never carries sensitive request data.

func formulaSummary(formula domain.Formula) string {
	return fmt.Sprintf("state=%s version=%d steps=%d",
		formula.State, formula.Version, len(formula.Steps))
}

func vesselSummary(vessel domain.Vessel) string {
	return fmt.Sprintf("state=%s version=%d", vessel.State, vessel.Version)
}

func runSummary(run domain.FermentationRun) string {
	return fmt.Sprintf("state=%s version=%d observations=%d",
		run.State, run.Version, len(run.Observations))
}
