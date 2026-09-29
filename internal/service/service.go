package service

import (
	"strings"
	"sync"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/audit"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/clock"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
)

type Service struct {
	store *storage.Store
	clock clock.Clock
	audit *audit.Log
	// writeMu serializes the business commit and the audit append as one
	// unit. The store already serializes state writes, but a second mutex
	// here ensures that the audit log observes successful writes in the same
	// order they were committed, so concurrent requests can never produce
	// audit entries whose sequence order disagrees with state revision order.
	writeMu sync.Mutex
}

func New(store *storage.Store, timeSource clock.Clock, auditLog *audit.Log) *Service {
	if timeSource == nil {
		timeSource = clock.System{}
	}
	return &Service{store: store, clock: timeSource, audit: auditLog}
}

// commit runs mutator against the store and, on success, builds and appends
// one audit record. buildRecord runs after the state commit succeeds but
// before the lock is released, reading the versions captured by mutator.
// Audit failure is logged but does not change the successful result of the
// business write. Holding writeMu across both steps keeps audit order aligned
// with commit order under concurrency.
func (s *Service) commit(
	mutator func(*domain.Snapshot) error,
	buildRecord func() audit.Record,
) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.store.Execute(mutator); err != nil {
		return err
	}
	s.recordAudit(buildRecord())
	return nil
}

func (s *Service) Revision() int64 {
	return s.store.Revision()
}

func (s *Service) checkExpected(current int64, expected *int64) error {
	if expected != nil && current != *expected {
		return domain.NewError(domain.CodeRevisionConflict,
			"the aggregate version does not match X-Expected-Version")
	}
	return nil
}

func generateEntityID(prefix string) (string, error) {
	return domain.NewID(prefix)
}

func generatedRunCode() (string, error) {
	value, err := domain.NewID("run")
	if err != nil {
		return "", err
	}
	suffix := strings.ToUpper(strings.TrimPrefix(value, "run_"))
	return "RUN-" + suffix, nil
}
