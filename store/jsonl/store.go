package jsonl

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"

	"github.com/ethanyzhang/capsule-ledger-go/ledger"
	"github.com/ethanyzhang/capsule-ledger-go/mmr"
)

const (
	journalName          = "ledger.jsonl"
	maxJournalEventBytes = 4 << 20
	schemaVersion        = 3
)

type event struct {
	Version    int                      `json:"version"`
	Type       string                   `json:"type"`
	LogID      string                   `json:"log_id,omitempty"`
	Record     *ledger.Record           `json:"record,omitempty"`
	CapsuleID  ledger.CapsuleID         `json:"capsule_id,omitempty"`
	Envelope   *ledger.Envelope         `json:"envelope,omitempty"`
	CLL        *ledger.CLLMutation      `json:"cll,omitempty"`
	Witness    *ledger.WitnessResult    `json:"witness,omitempty"`
	Rebaseline *ledger.RebaselineRecord `json:"rebaseline,omitempty"`
}

// Store persists one active log in an append-only JSONL journal.
type Store struct {
	mu             sync.RWMutex
	logID          string
	requestedLogID string
	journal        *os.File
	lockFile       *os.File
	records        map[ledger.CapsuleID]ledger.Record
	order          []ledger.CapsuleID
	closed         bool
	initialized    bool
	cll            ledger.CLLState
	witnesses      map[string]ledger.PendingWitness
	knownLogIDs    map[string]struct{}
	migrationIDs   map[string]struct{}
	rebaselines    []ledger.RebaselineRecord
}

// Open opens or creates the journal and acquires its exclusive writer lock.
func Open(root, logID string) (*Store, error) {
	if root == "" || ledger.ValidateIdentifier(logID) != nil {
		return nil, fmt.Errorf("%w: root and log id are required", ledger.ErrInvalid)
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("create JSONL root: %w", err)
	}
	lockFile, err := os.OpenFile(filepath.Join(root, ".writer.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open writer lock: %w", err)
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		closeErr := lockFile.Close()
		return nil, errors.Join(fmt.Errorf("JSONL store already has a writer: %w", err), closeErr)
	}
	journal, err := os.OpenFile(filepath.Join(root, journalName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		unlockErr := unlockAndClose(lockFile)
		return nil, errors.Join(fmt.Errorf("open journal: %w", err), unlockErr)
	}
	store := &Store{
		requestedLogID: logID, journal: journal, lockFile: lockFile,
		records:      make(map[ledger.CapsuleID]ledger.Record),
		witnesses:    make(map[string]ledger.PendingWitness),
		knownLogIDs:  make(map[string]struct{}),
		migrationIDs: make(map[string]struct{}),
	}
	if err := store.replay(); err != nil {
		closeErr := store.closeFiles()
		return nil, errors.Join(err, closeErr)
	}
	if !store.initialized {
		entry := event{Version: schemaVersion, Type: "log.init", LogID: logID}
		if err := store.appendEvent(entry); err != nil {
			closeErr := store.closeFiles()
			return nil, errors.Join(err, closeErr)
		}
		store.initialized = true
		store.logID = logID
		store.cll.LogID = logID
		store.knownLogIDs[logID] = struct{}{}
	}
	if store.logID != logID {
		closeErr := store.closeFiles()
		return nil, errors.Join(fmt.Errorf("journal active log id %q does not match requested log id %q", store.logID, logID), closeErr)
	}
	return store, nil
}

func (s *Store) replay() error {
	if _, err := s.journal.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek journal: %w", err)
	}
	reader := bufio.NewReaderSize(s.journal, maxJournalEventBytes+1)
	var validOffset int64
	for {
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			return fmt.Errorf("%w: journal event exceeds %d bytes", ledger.ErrCorrupt, maxJournalEventBytes)
		}
		if errors.Is(err, io.EOF) {
			if len(line) > 0 {
				if truncateErr := s.journal.Truncate(validOffset); truncateErr != nil {
					return fmt.Errorf("truncate torn journal tail: %w", truncateErr)
				}
			}
			break
		}
		if err != nil {
			return fmt.Errorf("read journal: %w", err)
		}
		var entry event
		if err := json.Unmarshal(bytes.TrimSuffix(line, []byte{'\n'}), &entry); err != nil {
			return fmt.Errorf("%w: invalid journal event at offset %d: %v", ledger.ErrCorrupt, validOffset, err)
		}
		if err := s.apply(entry); err != nil {
			return fmt.Errorf("%w: event at offset %d: %v", ledger.ErrCorrupt, validOffset, err)
		}
		validOffset += int64(len(line))
	}
	_, err := s.journal.Seek(0, io.SeekEnd)
	return err
}

func (s *Store) apply(entry event) error {
	if entry.Version != schemaVersion {
		return fmt.Errorf("%w: unsupported event version %d", ledger.ErrCorrupt, entry.Version)
	}
	switch entry.Type {
	case "log.init":
		if s.initialized || entry.LogID == "" {
			return fmt.Errorf("invalid log initialization")
		}
		s.initialized = true
		s.logID = entry.LogID
		s.cll.LogID = entry.LogID
		s.knownLogIDs[entry.LogID] = struct{}{}
	case "capsule.append":
		if !s.initialized {
			return fmt.Errorf("capsule precedes log initialization")
		}
		if entry.Record == nil || entry.Record.Seq != uint64(len(s.order)+1) {
			return fmt.Errorf("non-contiguous capsule append")
		}
		if err := entry.Record.Validate(); err != nil {
			return fmt.Errorf("invalid capsule record: %w", err)
		}
		if _, exists := s.records[entry.Record.CapsuleID]; exists {
			return fmt.Errorf("duplicate capsule id")
		}
		s.records[entry.Record.CapsuleID] = cloneRecord(*entry.Record)
		s.order = append(s.order, entry.Record.CapsuleID)
	case "envelope.add":
		if entry.Envelope == nil {
			return fmt.Errorf("missing envelope")
		}
		record, exists := s.records[entry.CapsuleID]
		if !exists {
			return fmt.Errorf("envelope references missing capsule")
		}
		for _, existing := range record.Envelopes {
			if existing.Digest == entry.Envelope.Digest {
				return fmt.Errorf("duplicate envelope event")
			}
		}
		record.Envelopes = append(record.Envelopes, cloneEnvelope(*entry.Envelope))
		s.records[entry.CapsuleID] = record
	case "cll.commit":
		if entry.CLL == nil {
			return fmt.Errorf("missing CLL mutation")
		}
		if err := s.applyCLL(*entry.CLL); err != nil {
			return err
		}
	case "witness.commit":
		if entry.Witness == nil {
			return fmt.Errorf("missing witness result")
		}
		if err := s.applyWitness(*entry.Witness); err != nil {
			return err
		}
	case "log.rebaseline":
		if entry.Rebaseline == nil || entry.Rebaseline.OldLogID != s.logID {
			return fmt.Errorf("invalid log rebaseline")
		}
		input := ledger.RebaselineInput{NewLogID: entry.Rebaseline.NewLogID, Reason: entry.Rebaseline.Reason, At: entry.Rebaseline.At, MigrationID: entry.Rebaseline.MigrationID}
		if input.Validate() != nil {
			return fmt.Errorf("invalid log rebaseline metadata")
		}
		if _, exists := s.knownLogIDs[entry.Rebaseline.NewLogID]; exists {
			return fmt.Errorf("reused log id")
		}
		if _, exists := s.migrationIDs[entry.Rebaseline.MigrationID]; exists {
			return fmt.Errorf("reused migration id")
		}
		if entry.Rebaseline.LastWitnessedSize != s.lastWitnessedSize() {
			return fmt.Errorf("invalid rebaseline witnessed size")
		}
		s.applyRebaseline(*entry.Rebaseline)
	default:
		return fmt.Errorf("unknown event type %q", entry.Type)
	}
	return nil
}

func (s *Store) LoadCLL(ctx context.Context) (ledger.CLLState, error) {
	if err := ctx.Err(); err != nil {
		return ledger.CLLState{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ledger.CLLState{}, ledger.ErrClosed
	}
	return cloneCLL(s.cll), nil
}

func (s *Store) CommitCLL(ctx context.Context, mutation ledger.CLLMutation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	mutation = mutation.Normalized()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ledger.ErrClosed
	}
	if err := s.validateCLL(mutation); err != nil {
		return err
	}
	entry := event{Version: schemaVersion, Type: "cll.commit", CLL: &mutation}
	if err := s.appendEvent(entry); err != nil {
		return err
	}
	return s.applyCLL(mutation)
}

func (s *Store) validateCLL(mutation ledger.CLLMutation) error {
	if err := mutation.Validate(); err != nil {
		return err
	}
	if mutation.ExpectedIndexedSeq != s.cll.IndexedSeq || mutation.IndexedSeq < mutation.ExpectedIndexedSeq {
		return ledger.ErrConflict
	}
	leaves, complete := mmr.LeafCount(uint64(len(s.cll.Nodes) + len(mutation.Nodes)))
	if !complete || leaves != mutation.IndexedSeq {
		return fmt.Errorf("%w: MMR size does not match indexed sequence", ledger.ErrInvalid)
	}
	for index, node := range mutation.Nodes {
		if node.Position != uint64(len(s.cll.Nodes)+index) {
			return ledger.ErrInvalid
		}
	}
	if mutation.Checkpoint != nil && mutation.Checkpoint.MMRSize != uint64(len(s.cll.Nodes)+len(mutation.Nodes)) {
		return ledger.ErrInvalid
	}
	if mutation.Checkpoint != nil {
		for _, witnessID := range mutation.WitnessIDs {
			if _, exists := s.witnesses[witnessKey(witnessID, mutation.Checkpoint.MMRSize)]; exists {
				return ledger.ErrConflict
			}
		}
	}
	return nil
}

func (s *Store) Rebaseline(ctx context.Context, input ledger.RebaselineInput) (ledger.RebaselineRecord, error) {
	if err := ctx.Err(); err != nil {
		return ledger.RebaselineRecord{}, err
	}
	input = input.Normalized()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ledger.RebaselineRecord{}, ledger.ErrClosed
	}
	if input.Validate() != nil || input.NewLogID == s.logID {
		return ledger.RebaselineRecord{}, ledger.ErrInvalid
	}
	if _, exists := s.knownLogIDs[input.NewLogID]; exists {
		return ledger.RebaselineRecord{}, ledger.ErrInvalid
	}
	if _, exists := s.migrationIDs[input.MigrationID]; exists {
		return ledger.RebaselineRecord{}, ledger.ErrConflict
	}
	continuityConflict := false
	for _, item := range s.witnesses {
		if item.State == ledger.WitnessContinuityConflict {
			continuityConflict = true
			break
		}
	}
	if !continuityConflict {
		return ledger.RebaselineRecord{}, fmt.Errorf("%w: rebaseline requires a continuity conflict", ledger.ErrInvalid)
	}
	record := ledger.RebaselineRecord{OldLogID: s.logID, NewLogID: input.NewLogID, Reason: input.Reason, At: input.At.UTC(), MigrationID: input.MigrationID}
	record.LastWitnessedSize = s.lastWitnessedSize()
	entry := event{Version: schemaVersion, Type: "log.rebaseline", Rebaseline: &record}
	if err := s.appendEvent(entry); err != nil {
		return ledger.RebaselineRecord{}, err
	}
	s.applyRebaseline(record)
	return record, nil
}

func (s *Store) lastWitnessedSize() uint64 {
	var size uint64
	for _, item := range s.witnesses {
		if item.State == ledger.WitnessVerified && item.Checkpoint.MMRSize > size {
			size = item.Checkpoint.MMRSize
		}
	}
	return size
}

func (s *Store) applyRebaseline(record ledger.RebaselineRecord) {
	s.rebaselines = append(s.rebaselines, record)
	s.migrationIDs[record.MigrationID] = struct{}{}
	s.knownLogIDs[record.NewLogID] = struct{}{}
	s.logID = record.NewLogID
	s.requestedLogID = record.NewLogID
	s.cll.LogID = record.NewLogID
	s.cll.Checkpoints = nil
	s.cll.ForceCheckpoint = true
	s.witnesses = make(map[string]ledger.PendingWitness)
}

// Rebaselines returns the durable migration history in chronological order.
func (s *Store) Rebaselines(ctx context.Context, limit int) ([]ledger.RebaselineRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, ledger.ErrClosed
	}
	if limit <= 0 || limit > ledger.MaxScanLimit {
		return nil, ledger.ErrInvalid
	}
	start := 0
	if len(s.rebaselines) > limit {
		start = len(s.rebaselines) - limit
	}
	return append([]ledger.RebaselineRecord(nil), s.rebaselines[start:]...), nil
}

func (s *Store) applyCLL(mutation ledger.CLLMutation) error {
	if err := s.validateCLL(mutation); err != nil {
		return err
	}
	s.cll.IndexedSeq = mutation.IndexedSeq
	s.cll.FirstUncheckpointed = mutation.FirstUncheckpointed
	for _, node := range mutation.Nodes {
		s.cll.Nodes = append(s.cll.Nodes, ledger.MMRNode{Position: node.Position, Hash: clone(node.Hash)})
	}
	if mutation.Checkpoint != nil {
		s.cll.ForceCheckpoint = false
		checkpoint := cloneCheckpoint(*mutation.Checkpoint)
		s.cll.Checkpoints = append(s.cll.Checkpoints, checkpoint)
		for _, witnessID := range mutation.WitnessIDs {
			key := witnessKey(witnessID, checkpoint.MMRSize)
			s.witnesses[key] = ledger.PendingWitness{WitnessID: witnessID, Checkpoint: checkpoint, State: ledger.WitnessPending}
		}
	}
	return nil
}

func (s *Store) PendingWitnesses(ctx context.Context, witnessID string, limit int) ([]ledger.PendingWitness, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ledger.ValidateIdentifier(witnessID) != nil || limit <= 0 || limit > ledger.MaxScanLimit {
		return nil, ledger.ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, ledger.ErrClosed
	}
	for _, item := range s.witnesses {
		if item.WitnessID == witnessID && item.State == ledger.WitnessContinuityConflict {
			return []ledger.PendingWitness{}, nil
		}
	}
	var pending []ledger.PendingWitness
	for _, item := range s.witnesses {
		if item.WitnessID == witnessID && (item.State == ledger.WitnessPending || item.State == ledger.WitnessRetryable) {
			item.Checkpoint = cloneCheckpoint(item.Checkpoint)
			pending = append(pending, item)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].Checkpoint.MMRSize < pending[j].Checkpoint.MMRSize })
	if len(pending) > limit {
		pending = pending[:limit]
	}
	return pending, nil
}

func (s *Store) CommitWitness(ctx context.Context, result ledger.WitnessResult) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	result = result.Normalized()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ledger.ErrClosed
	}
	if err := s.validateWitness(result); err != nil {
		return err
	}
	entry := event{Version: schemaVersion, Type: "witness.commit", Witness: &result}
	if err := s.appendEvent(entry); err != nil {
		return err
	}
	return s.applyWitness(result)
}

func (s *Store) GetWitness(ctx context.Context, witnessID string, mmrSize uint64) (ledger.PendingWitness, error) {
	if err := ctx.Err(); err != nil {
		return ledger.PendingWitness{}, err
	}
	if ledger.ValidateIdentifier(witnessID) != nil || mmrSize == 0 {
		return ledger.PendingWitness{}, ledger.ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ledger.PendingWitness{}, ledger.ErrClosed
	}
	item, ok := s.witnesses[witnessKey(witnessID, mmrSize)]
	if !ok {
		return ledger.PendingWitness{}, ledger.ErrNotFound
	}
	item.Checkpoint = cloneCheckpoint(item.Checkpoint)
	item.Receipt = clone(item.Receipt)
	return item, nil
}

func (s *Store) validateWitness(result ledger.WitnessResult) error {
	if err := result.Validate(); err != nil {
		return err
	}
	item, exists := s.witnesses[witnessKey(result.WitnessID, result.MMRSize)]
	if !exists {
		return ledger.ErrNotFound
	}
	if item.State != ledger.WitnessPending && item.State != ledger.WitnessRetryable {
		return ledger.ErrConflict
	}
	return nil
}

func (s *Store) applyWitness(result ledger.WitnessResult) error {
	if err := s.validateWitness(result); err != nil {
		return err
	}
	key := witnessKey(result.WitnessID, result.MMRSize)
	item := s.witnesses[key]
	item.State = result.State
	item.Attempts++
	item.LastAttemptAt = result.AttemptedAt
	item.NextAttemptAt = result.NextAttemptAt
	item.LastError = result.Error
	item.Receipt = clone(result.Receipt)
	s.witnesses[key] = item
	return nil
}

func witnessKey(id string, size uint64) string { return fmt.Sprintf("%s:%d", id, size) }
func cloneCheckpoint(in ledger.CheckpointRecord) ledger.CheckpointRecord {
	in.Payload = clone(in.Payload)
	in.SignedCheckpoint = clone(in.SignedCheckpoint)
	return in
}
func cloneCLL(in ledger.CLLState) ledger.CLLState {
	in.Nodes = append([]ledger.MMRNode(nil), in.Nodes...)
	for i := range in.Nodes {
		in.Nodes[i].Hash = clone(in.Nodes[i].Hash)
	}
	in.Checkpoints = append([]ledger.CheckpointRecord(nil), in.Checkpoints...)
	for i := range in.Checkpoints {
		in.Checkpoints[i] = cloneCheckpoint(in.Checkpoints[i])
	}
	return in
}

func (s *Store) appendEvent(entry event) error {
	encoded, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode journal event: %w", err)
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maxJournalEventBytes {
		return fmt.Errorf("%w: journal event exceeds %d bytes", ledger.ErrInvalid, maxJournalEventBytes)
	}
	start, err := s.journal.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("seek journal end: %w", err)
	}
	written := 0
	for written < len(encoded) {
		n, writeErr := s.journal.Write(encoded[written:])
		written += n
		if writeErr != nil {
			return s.rollbackEvent(start, fmt.Errorf("write journal event: %w", writeErr))
		}
		if n == 0 {
			return s.rollbackEvent(start, fmt.Errorf("write journal event: %w", io.ErrShortWrite))
		}
	}
	if err := s.journal.Sync(); err != nil {
		return s.rollbackEvent(start, fmt.Errorf("sync journal event: %w", err))
	}
	return nil
}

func (s *Store) rollbackEvent(start int64, cause error) error {
	truncateErr := s.journal.Truncate(start)
	_, seekErr := s.journal.Seek(start, io.SeekStart)
	syncErr := s.journal.Sync()
	rollbackErr := errors.Join(truncateErr, seekErr, syncErr)
	if rollbackErr == nil {
		return cause
	}
	s.closed = true
	return errors.Join(cause, fmt.Errorf("%w: journal rollback failed", ledger.ErrCorrupt), rollbackErr, s.closeFiles())
}

func (s *Store) Append(ctx context.Context, input ledger.AppendInput) (ledger.Record, ledger.AppendOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ledger.Record{}, "", err
	}
	input = input.Normalized()
	if err := input.Validate(); err != nil {
		return ledger.Record{}, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ledger.Record{}, "", ledger.ErrClosed
	}
	if existing, ok := s.records[input.CapsuleID]; ok {
		if existing.Authenticity != input.Authenticity || !bytes.Equal(existing.Capsule, input.Capsule) || !sameEnvelopes(existing.Envelopes, input.Envelopes) {
			return ledger.Record{}, "", ledger.ErrConflict
		}
		return cloneRecord(existing), ledger.AppendIdempotent, nil
	}
	record := ledger.Record{
		Seq: uint64(len(s.order) + 1), CapsuleID: input.CapsuleID,
		Capsule: clone(input.Capsule), Authenticity: input.Authenticity, Envelopes: cloneEnvelopes(input.Envelopes),
		Verification: input.Verification, ParentID: input.ParentID, AppendedAt: ledger.NormalizeTime(input.AppendedAt),
	}
	entry := event{Version: schemaVersion, Type: "capsule.append", Record: &record}
	if err := s.appendEvent(entry); err != nil {
		return ledger.Record{}, "", err
	}
	s.records[record.CapsuleID] = cloneRecord(record)
	s.order = append(s.order, record.CapsuleID)
	return cloneRecord(record), ledger.AppendInserted, nil
}

func (s *Store) AddEnvelope(ctx context.Context, input ledger.EnvelopeInput) (ledger.Envelope, ledger.AddOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ledger.Envelope{}, "", err
	}
	input = input.Normalized()
	if err := input.Validate(); err != nil {
		return ledger.Envelope{}, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ledger.Envelope{}, "", ledger.ErrClosed
	}
	record, ok := s.records[input.CapsuleID]
	if !ok {
		return ledger.Envelope{}, "", ledger.ErrNotFound
	}
	for _, existing := range record.Envelopes {
		if existing.Digest == input.Envelope.Digest {
			if !bytes.Equal(existing.Bytes, input.Envelope.Bytes) {
				return ledger.Envelope{}, "", ledger.ErrConflict
			}
			return cloneEnvelope(existing), ledger.EnvelopeIdempotent, nil
		}
	}
	if len(record.Envelopes) >= ledger.MaxEnvelopesPerCapsule {
		return ledger.Envelope{}, "", fmt.Errorf("%w: envelope limit reached", ledger.ErrInvalid)
	}
	envelope := cloneEnvelope(input.Envelope)
	entry := event{Version: schemaVersion, Type: "envelope.add", CapsuleID: input.CapsuleID, Envelope: &envelope}
	if err := s.appendEvent(entry); err != nil {
		return ledger.Envelope{}, "", err
	}
	record.Envelopes = append(record.Envelopes, envelope)
	s.records[input.CapsuleID] = record
	return cloneEnvelope(envelope), ledger.EnvelopeInserted, nil
}

func (s *Store) Get(ctx context.Context, id ledger.CapsuleID) (ledger.Record, error) {
	if err := ctx.Err(); err != nil {
		return ledger.Record{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ledger.Record{}, ledger.ErrClosed
	}
	record, ok := s.records[id]
	if !ok {
		return ledger.Record{}, ledger.ErrNotFound
	}
	return cloneRecord(record), nil
}

func (s *Store) Scan(ctx context.Context, after uint64, limit int) ([]ledger.Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, ledger.ErrClosed
	}
	if limit <= 0 || limit > ledger.MaxScanLimit {
		return nil, ledger.ErrInvalid
	}
	if after > uint64(len(s.order)) {
		return []ledger.Record{}, nil
	}
	end := int(after) + limit
	if end > len(s.order) {
		end = len(s.order)
	}
	result := make([]ledger.Record, 0, end-int(after))
	for _, id := range s.order[int(after):end] {
		result = append(result, cloneRecord(s.records[id]))
	}
	return result, nil
}

func (s *Store) ScanIDs(ctx context.Context, after uint64, limit int) ([]ledger.LogEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, ledger.ErrClosed
	}
	if limit <= 0 || limit > ledger.MaxScanLimit {
		return nil, ledger.ErrInvalid
	}
	if after > uint64(len(s.order)) {
		return []ledger.LogEntry{}, nil
	}
	end := int(after) + limit
	if end > len(s.order) {
		end = len(s.order)
	}
	result := make([]ledger.LogEntry, 0, end-int(after))
	for _, id := range s.order[int(after):end] {
		record := s.records[id]
		result = append(result, ledger.LogEntry{Seq: record.Seq, CapsuleID: id, AppendedAt: record.AppendedAt})
	}
	return result, nil
}

func (s *Store) FindChainGaps(ctx context.Context) ([]ledger.ChainGap, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, ledger.ErrClosed
	}
	var gaps []ledger.ChainGap
	for _, id := range s.order {
		record := s.records[id]
		if record.ParentID == "" {
			continue
		}
		if _, exists := s.records[record.ParentID]; !exists {
			gaps = append(gaps, ledger.ChainGap{Seq: record.Seq, CapsuleID: id, ParentID: record.ParentID})
		}
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].Seq < gaps[j].Seq })
	return gaps, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.closeFiles()
}

func (s *Store) closeFiles() error {
	var journalErr error
	if s.journal != nil {
		journalErr = s.journal.Close()
	}
	return errors.Join(journalErr, unlockAndClose(s.lockFile))
}

func unlockAndClose(file *os.File) error {
	if file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return errors.Join(unlockErr, file.Close())
}

func sameEnvelopes(left, right []ledger.Envelope) bool {
	if len(right) > len(left) {
		return false
	}
	byDigest := make(map[ledger.EnvelopeDigest][]byte, len(left))
	for _, envelope := range left {
		byDigest[envelope.Digest] = envelope.Bytes
	}
	for _, envelope := range right {
		stored, exists := byDigest[envelope.Digest]
		if !exists || !bytes.Equal(stored, envelope.Bytes) {
			return false
		}
	}
	return true
}

func cloneRecord(input ledger.Record) ledger.Record {
	input.AppendedAt = ledger.NormalizeTime(input.AppendedAt)
	return input.Clone()
}

func cloneEnvelopes(input []ledger.Envelope) []ledger.Envelope {
	if len(input) == 0 {
		return nil
	}
	result := make([]ledger.Envelope, len(input))
	for index, envelope := range input {
		result[index] = cloneEnvelope(envelope)
	}
	return result
}

func cloneEnvelope(input ledger.Envelope) ledger.Envelope {
	input.AddedAt = ledger.NormalizeTime(input.AddedAt)
	return input.Clone()
}

func clone(input []byte) []byte {
	return append([]byte(nil), input...)
}
