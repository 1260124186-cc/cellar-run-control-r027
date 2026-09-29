// Package audit keeps a local append-only trail of successful business
// writes. Entries are persisted as JSON lines inside the data directory so
// their order and attribution survive process restarts.
package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const logFileName = "audit.jsonl"

// Operation names the business write that produced an audit entry.
type Operation string

const (
	OpFormulaCreate        Operation = "formula_create"
	OpFormulaSetSteps      Operation = "formula_set_steps"
	OpFormulaApprove       Operation = "formula_approve"
	OpFormulaRetire        Operation = "formula_retire"
	OpVesselCreate         Operation = "vessel_create"
	OpVesselFinishCleaning Operation = "vessel_finish_cleaning"
	OpVesselRetire         Operation = "vessel_retire"
	OpRunCreate            Operation = "run_create"
	OpRunReserve           Operation = "run_reserve"
	OpRunStart             Operation = "run_start"
	OpRunAppendObservation Operation = "run_append_observation"
	OpRunBeginConditioning Operation = "run_begin_conditioning"
	OpRunComplete          Operation = "run_complete"
	OpRunAbort             Operation = "run_abort"
)

// Target kinds identify the aggregate an audit entry belongs to.
const (
	KindFormula = "formula"
	KindVessel  = "vessel"
	KindRun     = "run"
)

func ValidOperation(value string) bool {
	switch Operation(value) {
	case OpFormulaCreate, OpFormulaSetSteps, OpFormulaApprove, OpFormulaRetire,
		OpVesselCreate, OpVesselFinishCleaning, OpVesselRetire,
		OpRunCreate, OpRunReserve, OpRunStart, OpRunAppendObservation,
		OpRunBeginConditioning, OpRunComplete, OpRunAbort:
		return true
	}
	return false
}

func ValidTargetKind(value string) bool {
	switch value {
	case KindFormula, KindVessel, KindRun:
		return true
	}
	return false
}

// Target describes one aggregate affected by a successful write. BeforeVersion
// is nil when the write created the aggregate. Summary carries only
// non-sensitive result state; request payload fields are never recorded.
type Target struct {
	Kind          string
	ID            string
	BeforeVersion *int64
	AfterVersion  int64
	Summary       string
}

// Entry is one immutable audit record. Sequence numbers are contiguous,
// start at 1, and remain stable across restarts.
type Entry struct {
	Sequence      int64     `json:"sequence"`
	Operation     Operation `json:"operation"`
	TargetKind    string    `json:"target_kind"`
	TargetID      string    `json:"target_id"`
	BeforeVersion *int64    `json:"before_version,omitempty"`
	AfterVersion  int64     `json:"after_version"`
	RecordedAt    time.Time `json:"recorded_at"`
	Summary       string    `json:"summary"`
}

func (e Entry) clone() Entry {
	cloned := e
	if e.BeforeVersion != nil {
		value := *e.BeforeVersion
		cloned.BeforeVersion = &value
	}
	return cloned
}

// Filter narrows a read-only query. Empty fields match every entry and the
// time range is inclusive on both ends.
type Filter struct {
	TargetID   string
	TargetKind string
	Operation  Operation
	From       *time.Time
	To         *time.Time
}

func (f Filter) matches(entry Entry) bool {
	if f.TargetID != "" && entry.TargetID != f.TargetID {
		return false
	}
	if f.TargetKind != "" && entry.TargetKind != f.TargetKind {
		return false
	}
	if f.Operation != "" && entry.Operation != f.Operation {
		return false
	}
	if f.From != nil && entry.RecordedAt.Before(*f.From) {
		return false
	}
	if f.To != nil && entry.RecordedAt.After(*f.To) {
		return false
	}
	return true
}

// Log is an append-only audit trail. Appends are serialized behind a mutex
// and written with a single O_APPEND write, so concurrent writers cannot
// interleave entries or skip sequence numbers.
type Log struct {
	mu       sync.RWMutex
	path     string
	file     *os.File
	entries  []Entry
	next     int64
	broken   bool
	brokenBy error
}

// failLocked marks the trail unusable after a short or failed write, when the
// bytes already on disk cannot be reconciled with the in-memory state. The
// process must restart and reconcile from disk before appending again;
// continuing with the next sequence could duplicate or skip entries.
func (l *Log) failLocked(err error) error {
	l.broken = true
	l.brokenBy = err
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
	return err
}

// Open loads any existing trail in the data directory and prepares the log
// for appending. A corrupt trail stops the process before serving, matching
// the snapshot store behavior.
func Open(dataDir string) (*Log, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	log := &Log{
		path: filepath.Join(dataDir, logFileName),
		next: 1,
	}
	if err := log.load(); err != nil {
		return nil, err
	}
	handle, err := os.OpenFile(log.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open audit log: %w", err)
	}
	log.file = handle
	return log, nil
}

func (l *Log) load() error {
	raw, err := os.ReadFile(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read audit log: %w", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var entry Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			return fmt.Errorf("decode audit entry %d: %w", l.next, err)
		}
		if err := validateEntry(entry); err != nil {
			return fmt.Errorf("decode audit entry %d: %w", l.next, err)
		}
		if entry.Sequence != l.next {
			return fmt.Errorf("audit entry sequence %d is not contiguous", entry.Sequence)
		}
		l.entries = append(l.entries, entry)
		l.next++
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read audit log: %w", err)
	}
	return nil
}

func validateEntry(entry Entry) error {
	if entry.Sequence < 1 {
		return fmt.Errorf("sequence must be positive")
	}
	if !ValidOperation(string(entry.Operation)) {
		return fmt.Errorf("unknown operation %q", entry.Operation)
	}
	if !ValidTargetKind(entry.TargetKind) {
		return fmt.Errorf("unknown target kind %q", entry.TargetKind)
	}
	if strings.TrimSpace(entry.TargetID) == "" {
		return fmt.Errorf("target id is required")
	}
	if entry.AfterVersion < 1 {
		return fmt.Errorf("after version must be positive")
	}
	if entry.BeforeVersion != nil && *entry.BeforeVersion >= entry.AfterVersion {
		return fmt.Errorf("before version must be lower than after version")
	}
	if entry.RecordedAt.IsZero() {
		return fmt.Errorf("recorded time is required")
	}
	return nil
}

// Append records one entry per target of a successful operation. The whole
// batch is encoded and committed before it becomes visible to queries; a
// failed append leaves the trail unchanged.
func (l *Log) Append(operation Operation, at time.Time, targets ...Target) error {
	if len(targets) == 0 {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.broken {
		return fmt.Errorf("audit log is broken after a failed write: %w", l.brokenBy)
	}
	if l.file == nil {
		return fmt.Errorf("audit log is closed")
	}

	stamped := at.UTC()
	entries := make([]Entry, 0, len(targets))
	var buffer bytes.Buffer
	sequence := l.next
	for _, target := range targets {
		entry := Entry{
			Sequence:     sequence,
			Operation:    operation,
			TargetKind:   target.Kind,
			TargetID:     target.ID,
			AfterVersion: target.AfterVersion,
			RecordedAt:   stamped,
			Summary:      target.Summary,
		}
		if target.BeforeVersion != nil {
			before := *target.BeforeVersion
			entry.BeforeVersion = &before
		}
		if err := validateEntry(entry); err != nil {
			return fmt.Errorf("record audit entry: %w", err)
		}
		encoded, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("encode audit entry: %w", err)
		}
		buffer.Write(encoded)
		buffer.WriteByte('\n')
		entries = append(entries, entry)
		sequence++
	}
	payload := buffer.Bytes()
	written, err := l.file.Write(payload)
	if err != nil {
		return l.failLocked(fmt.Errorf("append audit log: %w", err))
	}
	// A short write means the file may hold a partial batch; fail closed so a
	// later append cannot reuse or skip the affected sequence numbers.
	if written != len(payload) {
		return l.failLocked(fmt.Errorf("append audit log: short write (%d of %d bytes)",
			written, len(payload)))
	}
	if err := l.file.Sync(); err != nil {
		return l.failLocked(fmt.Errorf("commit audit log: %w", err))
	}
	l.entries = append(l.entries, entries...)
	l.next = sequence
	return nil
}

// Query returns copies of the entries matching the filter in stable
// sequence order.
func (l *Log) Query(filter Filter) []Entry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	matches := make([]Entry, 0, len(l.entries))
	for _, entry := range l.entries {
		if filter.matches(entry) {
			matches = append(matches, entry.clone())
		}
	}
	return matches
}

// Close releases the append handle. The log must not be used afterwards.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// Broken reports whether the trail stopped accepting appends after a failed
// write. A broken process must restart so the trail is reconciled from disk.
func (l *Log) Broken() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.broken
}
