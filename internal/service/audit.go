package service

import (
	"log"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/audit"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

// recordAudit appends one entry for a successful business write. Audit
// failures are reported on the process log but never change the outcome of
// the business write itself.
func (s *Service) recordAudit(record audit.Record) {
	if s.audit == nil {
		return
	}
	if _, err := s.audit.Append(record); err != nil {
		log.Printf("audit: could not record %s for %s: %v",
			record.Operation, record.TargetID, err)
	}
}

// AuditEntries returns the audit entries matching the query filters in
// stable sequence order.
func (s *Service) AuditEntries(query audit.Query) ([]audit.Entry, error) {
	if s.audit == nil {
		return nil, domain.NewError(domain.CodeStorage, "audit log is not configured")
	}
	return s.audit.Query(query), nil
}
