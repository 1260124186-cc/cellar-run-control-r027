package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// FileName is the append-only audit log inside the configured data directory.
const FileName = "audit.log"

// Log appends audit entries to a local JSON-lines file. A single mutex
// serializes sequence assignment and the file write, so concurrent appends
// cannot interleave bytes or skip sequence numbers. Entries are persisted
// before they become visible to queries, and the persisted sequence is
// replayed on startup so order and target attribution survive restarts.
type Log struct {
	mu       sync.RWMutex
	file     *os.File
	entries  []Entry
	sequence int64
	closed   bool
	// failed marks a write or sync error. Once set, every later Append is
	// rejected without touching the file, which prevents appending behind
	// a partially written line and corrupting the on-disk sequence.
	failed error
}

// Open loads the existing audit log in dataDir (if any) and prepares it for
// appending. A corrupt or non-contiguous log stops startup instead of being
// silently truncated.
func Open(dataDir string) (*Log, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create audit directory: %w", err)
	}
	path := filepath.Join(dataDir, FileName)
	entries, err := readEntries(path)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open audit log: %w", err)
	}
	log := &Log{file: file, entries: entries}
	if len(entries) > 0 {
		log.sequence = entries[len(entries)-1].Sequence
	}
	return log, nil
}

func readEntries(path string) ([]Entry, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read audit log: %w", err)
	}
	defer file.Close()

	entries := []Entry{}
	decoder := json.NewDecoder(file)
	for {
		var entry Entry
		err := decoder.Decode(&entry)
		if errors.Is(err, io.EOF) {
			return entries, nil
		}
		if err != nil {
			return nil, fmt.Errorf("decode audit log: %w", err)
		}
		expected := int64(len(entries) + 1)
		if entry.Sequence != expected {
			return nil, fmt.Errorf("audit log entry %d breaks the contiguous sequence",
				entry.Sequence)
		}
		entries = append(entries, entry)
	}
}

// Append assigns the next sequence number and durably writes one entry. The
// entry becomes visible to queries only after the write and its fsync
// succeed. A write error poisons the log: subsequent appends fail closed so a
// torn line can never be followed by another record. Append failures are
// reported to the caller and never alter the outcome of a business write,
// because service code treats them as non-fatal.
func (l *Log) Append(record Record) (Entry, error) {
	if err := record.validate(); err != nil {
		return Entry{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed != nil {
		return Entry{}, fmt.Errorf("audit log previously failed (%v); refusing to append", l.failed)
	}
	if l.closed {
		return Entry{}, errors.New("audit log is closed")
	}
	entry := Entry{
		Sequence:      l.sequence + 1,
		Operation:     record.Operation,
		TargetKind:    record.TargetKind,
		TargetID:      record.TargetID,
		VersionBefore: record.VersionBefore,
		VersionAfter:  record.VersionAfter,
		RecordedAt:    record.RecordedAt.UTC(),
		Summary:       record.Summary,
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return Entry{}, fmt.Errorf("encode audit entry: %w", err)
	}
	if _, err := l.file.Write(append(line, '\n')); err != nil {
		l.failed = err
		return Entry{}, fmt.Errorf("append audit entry: %w", err)
	}
	if err := l.file.Sync(); err != nil {
		l.failed = err
		return Entry{}, fmt.Errorf("commit audit entry: %w", err)
	}
	l.entries = append(l.entries, entry)
	l.sequence = entry.Sequence
	return entry, nil
}

// Query returns copies of the entries matching the filter in stable sequence
// order. It never shares the log-owned slice or entries with callers, so the
// read-only interface cannot mutate recorded history.
func (l *Log) Query(query Query) []Entry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	matched := make([]Entry, 0)
	for _, entry := range l.entries {
		if query.matches(entry) {
			matched = append(matched, entry)
		}
	}
	return matched
}

// Close flushes and closes the underlying file. Appending after Close fails.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	if err := l.file.Close(); err != nil {
		return fmt.Errorf("close audit log: %w", err)
	}
	return nil
}
