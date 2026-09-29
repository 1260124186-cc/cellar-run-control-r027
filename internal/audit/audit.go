// Package audit keeps a local append-only trail of successful business
// writes. Each entry records the operation type, the target aggregate, the
// aggregate versions before and after the write, the request time, and a
// result summary. Summaries must never contain request-supplied free text
// such as run notes or observation notes; only generated identifiers,
// lifecycle states, versions, and computed numbers are recorded.
package audit

import (
	"errors"
	"time"
)

// Operation identifies the business write that produced an audit entry.
type Operation string

const (
	OperationFormulaCreate        Operation = "formula.create"
	OperationFormulaSetSteps      Operation = "formula.set_steps"
	OperationFormulaApprove       Operation = "formula.approve"
	OperationFormulaRetire        Operation = "formula.retire"
	OperationVesselCreate         Operation = "vessel.create"
	OperationVesselFinishCleaning Operation = "vessel.finish_cleaning"
	OperationVesselRetire         Operation = "vessel.retire"
	OperationRunCreate            Operation = "run.create"
	OperationRunReserve           Operation = "run.reserve"
	OperationRunStart             Operation = "run.start"
	OperationRunAbort             Operation = "run.abort"
	OperationRunObservationAppend Operation = "run.append_observation"
	OperationRunConditioningBegin Operation = "run.begin_conditioning"
	OperationRunComplete          Operation = "run.complete"
)

// Kind identifies the aggregate an audit entry belongs to.
type Kind string

const (
	KindFormula Kind = "formula"
	KindVessel  Kind = "vessel"
	KindRun     Kind = "run"
)

var validOperations = map[Operation]struct{}{
	OperationFormulaCreate:        {},
	OperationFormulaSetSteps:      {},
	OperationFormulaApprove:       {},
	OperationFormulaRetire:        {},
	OperationVesselCreate:         {},
	OperationVesselFinishCleaning: {},
	OperationVesselRetire:         {},
	OperationRunCreate:            {},
	OperationRunReserve:           {},
	OperationRunStart:             {},
	OperationRunAbort:             {},
	OperationRunObservationAppend: {},
	OperationRunConditioningBegin: {},
	OperationRunComplete:          {},
}

var validKinds = map[Kind]struct{}{
	KindFormula: {},
	KindVessel:  {},
	KindRun:     {},
}

// Valid reports whether the operation is one the service can emit.
func (o Operation) Valid() bool {
	_, ok := validOperations[o]
	return ok
}

// Valid reports whether the kind is one the service can attribute entries to.
func (k Kind) Valid() bool {
	_, ok := validKinds[k]
	return ok
}

// Record describes one successful business write before the log assigns a
// sequence number.
type Record struct {
	Operation     Operation
	TargetKind    Kind
	TargetID      string
	VersionBefore int64
	VersionAfter  int64
	RecordedAt    time.Time
	Summary       string
}

func (r Record) validate() error {
	switch {
	case !r.Operation.Valid():
		return errors.New("audit record operation is unknown")
	case !r.TargetKind.Valid():
		return errors.New("audit record target kind is unknown")
	case r.TargetID == "":
		return errors.New("audit record target id is required")
	case r.VersionBefore < 0 || r.VersionAfter < 1:
		return errors.New("audit record versions are out of range")
	case r.RecordedAt.IsZero():
		return errors.New("audit record time is required")
	case r.Summary == "":
		return errors.New("audit record summary is required")
	}
	return nil
}

// Entry is one immutable line in the audit log.
type Entry struct {
	Sequence      int64     `json:"sequence"`
	Operation     Operation `json:"operation"`
	TargetKind    Kind      `json:"target_kind"`
	TargetID      string    `json:"target_id"`
	VersionBefore int64     `json:"version_before"`
	VersionAfter  int64     `json:"version_after"`
	RecordedAt    time.Time `json:"recorded_at"`
	Summary       string    `json:"summary"`
}

// Query filters audit entries. Empty fields match everything and the time
// range is inclusive on both ends.
type Query struct {
	TargetKind Kind
	TargetID   string
	Operation  Operation
	From       *time.Time
	To         *time.Time
}

func (q Query) matches(entry Entry) bool {
	if q.TargetKind != "" && entry.TargetKind != q.TargetKind {
		return false
	}
	if q.TargetID != "" && entry.TargetID != q.TargetID {
		return false
	}
	if q.Operation != "" && entry.Operation != q.Operation {
		return false
	}
	if q.From != nil && entry.RecordedAt.Before(q.From.UTC()) {
		return false
	}
	if q.To != nil && entry.RecordedAt.After(q.To.UTC()) {
		return false
	}
	return true
}
