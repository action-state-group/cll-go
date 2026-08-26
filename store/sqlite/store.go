package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ethanyzhang/capsule-ledger-go/ledger"
	"github.com/ethanyzhang/capsule-ledger-go/mmr"
	sqliteDriver "modernc.org/sqlite"
)

const schema = `
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;
PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS schema_metadata (
  singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
  version INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS ledger_metadata (
  log_id TEXT PRIMARY KEY,
  next_seq INTEGER NOT NULL CHECK (next_seq > 0),
  indexed_seq INTEGER NOT NULL DEFAULT 0,
  first_uncheckpointed TEXT NOT NULL DEFAULT ''
  ,active INTEGER NOT NULL DEFAULT 1
  ,force_checkpoint INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS capsules (
  log_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  capsule_id TEXT NOT NULL,
  capsule BLOB NOT NULL,
  verification BLOB NOT NULL,
  parent_id TEXT NOT NULL,
  appended_at TEXT NOT NULL,
  PRIMARY KEY (log_id, seq),
  UNIQUE (log_id, capsule_id),
  FOREIGN KEY (log_id) REFERENCES ledger_metadata(log_id)
);
CREATE TABLE IF NOT EXISTS envelopes (
  log_id TEXT NOT NULL,
  capsule_id TEXT NOT NULL,
  digest TEXT NOT NULL,
  envelope BLOB NOT NULL,
  verification BLOB NOT NULL,
  added_at TEXT NOT NULL,
  PRIMARY KEY (log_id, capsule_id, digest),
  FOREIGN KEY (log_id, capsule_id) REFERENCES capsules(log_id, capsule_id)
);
CREATE INDEX IF NOT EXISTS capsules_parent ON capsules(log_id, parent_id);
CREATE TABLE IF NOT EXISTS mmr_nodes (
  log_id TEXT NOT NULL, position INTEGER NOT NULL, hash BLOB NOT NULL,
  PRIMARY KEY (log_id, position), FOREIGN KEY (log_id) REFERENCES ledger_metadata(log_id)
);
CREATE TABLE IF NOT EXISTS checkpoints (
  log_id TEXT NOT NULL, mmr_size INTEGER NOT NULL, indexed_seq INTEGER NOT NULL,
  root TEXT NOT NULL, payload BLOB NOT NULL, signed_statement BLOB NOT NULL, created_at TEXT NOT NULL,
  PRIMARY KEY (log_id, mmr_size), FOREIGN KEY (log_id) REFERENCES ledger_metadata(log_id)
);
CREATE TABLE IF NOT EXISTS witness_deliveries (
  log_id TEXT NOT NULL, witness_id TEXT NOT NULL, mmr_size INTEGER NOT NULL,
  state TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, next_attempt_at TEXT NOT NULL DEFAULT '',
  attempted_at TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '', receipt BLOB,
  PRIMARY KEY (log_id, witness_id, mmr_size),
  FOREIGN KEY (log_id, mmr_size) REFERENCES checkpoints(log_id, mmr_size)
);
CREATE TABLE IF NOT EXISTS rebaselines (
  migration_id TEXT PRIMARY KEY, old_log_id TEXT NOT NULL, new_log_id TEXT NOT NULL UNIQUE,
  reason TEXT NOT NULL, migrated_at TEXT NOT NULL, last_witnessed_size INTEGER NOT NULL
);
`

// Store persists log state in an embedded SQLite database.
type Store struct {
	mu     sync.RWMutex
	db     *sql.DB
	logID  string
	closed bool
}

// Open opens or creates a SQLite database bound to logID.
func Open(path, logID string) (*Store, error) {
	if path == "" || ledger.ValidateIdentifier(logID) != nil {
		return nil, fmt.Errorf("%w: path and log id are required", ledger.ErrInvalid)
	}
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	dsn := path + separator + "_journal_mode=wal&_foreign_keys=on&_busy_timeout=5000&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite store: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return nil, errors.Join(fmt.Errorf("initialize SQLite store: %w", err), db.Close())
	}
	if _, err := db.Exec(`INSERT INTO schema_metadata(singleton,version) VALUES(1,1) ON CONFLICT(singleton) DO NOTHING`); err != nil {
		return nil, errors.Join(fmt.Errorf("initialize SQLite schema version: %w", err), db.Close())
	}
	var version int
	if err := db.QueryRow(`SELECT version FROM schema_metadata WHERE singleton=1`).Scan(&version); err != nil || version != 1 {
		return nil, errors.Join(fmt.Errorf("unsupported SQLite schema version %d: %w", version, ledger.ErrCorrupt), err, db.Close())
	}
	if _, err := db.Exec(`INSERT INTO ledger_metadata(log_id, next_seq) VALUES (?, 1) ON CONFLICT(log_id) DO NOTHING`, logID); err != nil {
		return nil, errors.Join(fmt.Errorf("initialize SQLite log: %w", err), db.Close())
	}
	var active bool
	if err := db.QueryRow(`SELECT active FROM ledger_metadata WHERE log_id=?`, logID).Scan(&active); err != nil || !active {
		return nil, errors.Join(fmt.Errorf("open inactive SQLite log: %w", ledger.ErrInvalid), err, db.Close())
	}
	return &Store{db: db, logID: logID}, nil
}

func (s *Store) Append(ctx context.Context, in ledger.AppendInput) (_ ledger.Record, _ ledger.AppendOutcome, finalErr error) {
	defer func() { finalErr = classifySQLite(finalErr) }()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ledger.Record{}, "", ledger.ErrClosed
	}
	in = in.Normalized()
	if err := in.Validate(); err != nil {
		return ledger.Record{}, "", err
	}
	verification, err := json.Marshal(in.Verification)
	if err != nil {
		return ledger.Record{}, "", fmt.Errorf("encode verification: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ledger.Record{}, "", err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureActive(ctx, tx, s.logID); err != nil {
		return ledger.Record{}, "", err
	}
	if existing, getErr := get(ctx, tx, s.logID, in.CapsuleID); getErr == nil {
		if !bytes.Equal(existing.Capsule, in.Capsule) || !sameEnvelopes(existing.Envelopes, in.Envelopes) {
			return ledger.Record{}, "", ledger.ErrConflict
		}
		return existing, ledger.AppendIdempotent, nil
	} else if !errors.Is(getErr, ledger.ErrNotFound) {
		return ledger.Record{}, "", getErr
	}
	var seq uint64
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT next_seq,active FROM ledger_metadata WHERE log_id = ?`, s.logID).Scan(&seq, &active); err != nil {
		return ledger.Record{}, "", err
	}
	if !active {
		return ledger.Record{}, "", ledger.ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO capsules(log_id,seq,capsule_id,capsule,verification,parent_id,appended_at) VALUES(?,?,?,?,?,?,?)`,
		s.logID, seq, in.CapsuleID, in.Capsule, verification, in.ParentID, in.AppendedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return ledger.Record{}, "", err
	}
	for _, envelope := range in.Envelopes {
		if err := insertEnvelope(ctx, tx, s.logID, in.CapsuleID, envelope); err != nil {
			return ledger.Record{}, "", err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ledger_metadata SET next_seq = next_seq + 1 WHERE log_id = ?`, s.logID); err != nil {
		return ledger.Record{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return ledger.Record{}, "", err
	}
	record := ledger.Record{Seq: seq, CapsuleID: in.CapsuleID, Capsule: clone(in.Capsule), Envelopes: cloneEnvelopes(in.Envelopes), Verification: in.Verification.Clone(), ParentID: in.ParentID, AppendedAt: ledger.NormalizeTime(in.AppendedAt)}
	return record.Clone(), ledger.AppendInserted, nil
}

func (s *Store) AddEnvelope(ctx context.Context, in ledger.EnvelopeInput) (_ ledger.Envelope, _ ledger.AddOutcome, finalErr error) {
	defer func() { finalErr = classifySQLite(finalErr) }()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ledger.Envelope{}, "", ledger.ErrClosed
	}
	in = in.Normalized()
	if err := in.Validate(); err != nil {
		return ledger.Envelope{}, "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ledger.Envelope{}, "", err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureActive(ctx, tx, s.logID); err != nil {
		return ledger.Envelope{}, "", err
	}
	var present int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM capsules WHERE log_id=? AND capsule_id=?`, s.logID, in.CapsuleID).Scan(&present); errors.Is(err, sql.ErrNoRows) {
		return ledger.Envelope{}, "", ledger.ErrNotFound
	} else if err != nil {
		return ledger.Envelope{}, "", err
	}
	var stored ledger.Envelope
	var verification []byte
	var addedAt string
	err = tx.QueryRowContext(ctx, `SELECT digest,envelope,verification,added_at FROM envelopes WHERE log_id=? AND capsule_id=? AND digest=?`, s.logID, in.CapsuleID, in.Envelope.Digest).
		Scan(&stored.Digest, &stored.Bytes, &verification, &addedAt)
	if err == nil {
		if !bytes.Equal(stored.Bytes, in.Envelope.Bytes) {
			return ledger.Envelope{}, "", ledger.ErrConflict
		}
		if err := json.Unmarshal(verification, &stored.Verification); err != nil {
			return ledger.Envelope{}, "", fmt.Errorf("decode stored envelope verification: %w", ledger.ErrCorrupt)
		}
		stored.AddedAt, err = time.Parse(time.RFC3339Nano, addedAt)
		if err != nil {
			return ledger.Envelope{}, "", fmt.Errorf("decode stored envelope timestamp: %w", ledger.ErrCorrupt)
		}
		return cloneEnvelope(stored), ledger.EnvelopeIdempotent, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ledger.Envelope{}, "", err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM envelopes WHERE log_id=? AND capsule_id=?`, s.logID, in.CapsuleID).Scan(&count); err != nil {
		return ledger.Envelope{}, "", err
	}
	if count >= ledger.MaxEnvelopesPerCapsule {
		return ledger.Envelope{}, "", fmt.Errorf("%w: envelope limit reached", ledger.ErrInvalid)
	}
	if err := insertEnvelope(ctx, tx, s.logID, in.CapsuleID, in.Envelope); err != nil {
		return ledger.Envelope{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return ledger.Envelope{}, "", err
	}
	return cloneEnvelope(in.Envelope), ledger.EnvelopeInserted, nil
}

func insertEnvelope(ctx context.Context, tx *sql.Tx, logID string, id ledger.CapsuleID, envelope ledger.Envelope) error {
	envelope.AddedAt = ledger.NormalizeTime(envelope.AddedAt)
	verification, err := json.Marshal(envelope.Verification)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO envelopes(log_id,capsule_id,digest,envelope,verification,added_at) VALUES(?,?,?,?,?,?)`, logID, id, envelope.Digest, envelope.Bytes, verification, envelope.AddedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) Get(ctx context.Context, id ledger.CapsuleID) (ledger.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ledger.Record{}, ledger.ErrClosed
	}
	return get(ctx, s.db, s.logID, id)
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func get(ctx context.Context, q queryer, logID string, id ledger.CapsuleID) (ledger.Record, error) {
	var record ledger.Record
	var verification []byte
	var parent string
	var appended string
	err := q.QueryRowContext(ctx, `SELECT seq,capsule_id,capsule,verification,parent_id,appended_at FROM capsules WHERE log_id=? AND capsule_id=?`, logID, id).Scan(&record.Seq, &record.CapsuleID, &record.Capsule, &verification, &parent, &appended)
	if errors.Is(err, sql.ErrNoRows) {
		return ledger.Record{}, ledger.ErrNotFound
	}
	if err != nil {
		return ledger.Record{}, err
	}
	record.ParentID = ledger.CapsuleID(parent)
	if err := json.Unmarshal(verification, &record.Verification); err != nil {
		return ledger.Record{}, fmt.Errorf("decode verification: %w", err)
	}
	if record.AppendedAt, err = time.Parse(time.RFC3339Nano, appended); err != nil {
		return ledger.Record{}, err
	}
	rows, err := q.QueryContext(ctx, `SELECT digest,envelope,verification,added_at FROM envelopes WHERE log_id=? AND capsule_id=? ORDER BY digest`, logID, id)
	if err != nil {
		return ledger.Record{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var envelope ledger.Envelope
		var encoded []byte
		var added string
		if err := rows.Scan(&envelope.Digest, &envelope.Bytes, &encoded, &added); err != nil {
			return ledger.Record{}, err
		}
		if err := json.Unmarshal(encoded, &envelope.Verification); err != nil {
			return ledger.Record{}, err
		}
		if envelope.AddedAt, err = time.Parse(time.RFC3339Nano, added); err != nil {
			return ledger.Record{}, err
		}
		record.Envelopes = append(record.Envelopes, envelope)
	}
	if err := rows.Err(); err != nil {
		return ledger.Record{}, err
	}
	return record, nil
}

func (s *Store) Scan(ctx context.Context, after uint64, limit int) ([]ledger.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, ledger.ErrClosed
	}
	if limit <= 0 || limit > ledger.MaxScanLimit {
		return nil, ledger.ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT capsule_id FROM capsules WHERE log_id=? AND seq>? ORDER BY seq LIMIT ?`, s.logID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []ledger.CapsuleID
	for rows.Next() {
		var id ledger.CapsuleID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	result := make([]ledger.Record, 0, len(ids))
	for _, id := range ids {
		record, err := get(ctx, s.db, s.logID, id)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, nil
}

func (s *Store) ScanIDs(ctx context.Context, after uint64, limit int) ([]ledger.LogEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, ledger.ErrClosed
	}
	if limit <= 0 || limit > ledger.MaxScanLimit {
		return nil, ledger.ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT seq,capsule_id,appended_at FROM capsules WHERE log_id=? AND seq>? ORDER BY seq LIMIT ?`, s.logID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []ledger.LogEntry
	for rows.Next() {
		var entry ledger.LogEntry
		var appended string
		if err := rows.Scan(&entry.Seq, &entry.CapsuleID, &appended); err != nil {
			return nil, err
		}
		entry.AppendedAt, err = time.Parse(time.RFC3339Nano, appended)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *Store) FindChainGaps(ctx context.Context) ([]ledger.ChainGap, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, ledger.ErrClosed
	}
	rows, err := s.db.QueryContext(ctx, `SELECT c.seq,c.capsule_id,c.parent_id FROM capsules c LEFT JOIN capsules p ON p.log_id=c.log_id AND p.capsule_id=c.parent_id WHERE c.log_id=? AND c.parent_id<>'' AND p.capsule_id IS NULL ORDER BY c.seq`, s.logID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var gaps []ledger.ChainGap
	for rows.Next() {
		var gap ledger.ChainGap
		if err := rows.Scan(&gap.Seq, &gap.CapsuleID, &gap.ParentID); err != nil {
			return nil, err
		}
		gaps = append(gaps, gap)
	}
	return gaps, rows.Err()
}

func (s *Store) LoadCLL(ctx context.Context) (ledger.CLLState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ledger.CLLState{}, ledger.ErrClosed
	}
	state := ledger.CLLState{LogID: s.logID}
	var first string
	if err := s.db.QueryRowContext(ctx, `SELECT indexed_seq,first_uncheckpointed,force_checkpoint FROM ledger_metadata WHERE log_id=?`, s.logID).Scan(&state.IndexedSeq, &first, &state.ForceCheckpoint); err != nil {
		return ledger.CLLState{}, err
	}
	var err error
	if first != "" {
		state.FirstUncheckpointed, err = time.Parse(time.RFC3339Nano, first)
		if err != nil {
			return ledger.CLLState{}, err
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT position,hash FROM mmr_nodes WHERE log_id=? ORDER BY position`, s.logID)
	if err != nil {
		return ledger.CLLState{}, err
	}
	for rows.Next() {
		var node ledger.MMRNode
		if err := rows.Scan(&node.Position, &node.Hash); err != nil {
			rows.Close()
			return ledger.CLLState{}, err
		}
		state.Nodes = append(state.Nodes, node)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ledger.CLLState{}, err
	}
	if err := rows.Close(); err != nil {
		return ledger.CLLState{}, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT indexed_seq,mmr_size,root,payload,signed_statement,created_at FROM checkpoints WHERE log_id=? ORDER BY mmr_size`, s.logID)
	if err != nil {
		return ledger.CLLState{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var cp ledger.CheckpointRecord
		var created string
		if err := rows.Scan(&cp.IndexedSeq, &cp.MMRSize, &cp.Root, &cp.Payload, &cp.SignedStatement, &created); err != nil {
			return ledger.CLLState{}, err
		}
		cp.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return ledger.CLLState{}, err
		}
		state.Checkpoints = append(state.Checkpoints, cp)
	}
	return state, rows.Err()
}

func (s *Store) CommitCLL(ctx context.Context, mutation ledger.CLLMutation) (finalErr error) {
	defer func() { finalErr = classifySQLite(finalErr) }()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ledger.ErrClosed
	}
	mutation = mutation.Normalized()
	if err := mutation.Validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var indexed uint64
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT indexed_seq,active FROM ledger_metadata WHERE log_id=?`, s.logID).Scan(&indexed, &active); err != nil {
		return err
	}
	if !active {
		return ledger.ErrConflict
	}
	if indexed != mutation.ExpectedIndexedSeq || mutation.IndexedSeq < indexed {
		return ledger.ErrConflict
	}
	var count uint64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mmr_nodes WHERE log_id=?`, s.logID).Scan(&count); err != nil {
		return err
	}
	leaves, complete := mmr.LeafCount(count + uint64(len(mutation.Nodes)))
	if !complete || leaves != mutation.IndexedSeq {
		return fmt.Errorf("%w: MMR size does not match indexed sequence", ledger.ErrInvalid)
	}
	for index, node := range mutation.Nodes {
		if node.Position != count+uint64(index) {
			return ledger.ErrInvalid
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO mmr_nodes(log_id,position,hash) VALUES(?,?,?)`, s.logID, node.Position, node.Hash); err != nil {
			return err
		}
	}
	first := ""
	if !mutation.FirstUncheckpointed.IsZero() {
		first = mutation.FirstUncheckpointed.UTC().Format(time.RFC3339Nano)
	}
	force := 1
	if mutation.Checkpoint != nil {
		force = 0
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ledger_metadata SET indexed_seq=?,first_uncheckpointed=?,force_checkpoint=CASE WHEN force_checkpoint=1 THEN ? ELSE 0 END WHERE log_id=?`, mutation.IndexedSeq, first, force, s.logID); err != nil {
		return err
	}
	if mutation.Checkpoint != nil {
		cp := mutation.Checkpoint
		if cp.MMRSize != count+uint64(len(mutation.Nodes)) {
			return ledger.ErrInvalid
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO checkpoints(log_id,mmr_size,indexed_seq,root,payload,signed_statement,created_at) VALUES(?,?,?,?,?,?,?)`, s.logID, cp.MMRSize, cp.IndexedSeq, cp.Root, cp.Payload, cp.SignedStatement, cp.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		for _, id := range mutation.WitnessIDs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO witness_deliveries(log_id,witness_id,mmr_size,state) VALUES(?,?,?,?)`, s.logID, id, cp.MMRSize, ledger.WitnessPending); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) PendingWitnesses(ctx context.Context, witnessID string, limit int) ([]ledger.PendingWitness, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, ledger.ErrClosed
	}
	if ledger.ValidateIdentifier(witnessID) != nil || limit <= 0 || limit > ledger.MaxScanLimit {
		return nil, ledger.ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT d.state,d.attempts,d.next_attempt_at,d.last_error,c.indexed_seq,c.mmr_size,c.root,c.payload,c.signed_statement,c.created_at FROM witness_deliveries d JOIN checkpoints c ON c.log_id=d.log_id AND c.mmr_size=d.mmr_size WHERE d.log_id=? AND d.witness_id=? AND d.state IN (?,?) AND NOT EXISTS(SELECT 1 FROM witness_deliveries blocked WHERE blocked.log_id=d.log_id AND blocked.witness_id=d.witness_id AND blocked.state=?) ORDER BY d.mmr_size LIMIT ?`, s.logID, witnessID, ledger.WitnessPending, ledger.WitnessRetryable, ledger.WitnessContinuityConflict, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ledger.PendingWitness
	for rows.Next() {
		item := ledger.PendingWitness{WitnessID: witnessID}
		var next, created string
		if err := rows.Scan(&item.State, &item.Attempts, &next, &item.LastError, &item.Checkpoint.IndexedSeq, &item.Checkpoint.MMRSize, &item.Checkpoint.Root, &item.Checkpoint.Payload, &item.Checkpoint.SignedStatement, &created); err != nil {
			return nil, err
		}
		if next != "" {
			item.NextAttemptAt, err = time.Parse(time.RFC3339Nano, next)
			if err != nil {
				return nil, err
			}
		}
		item.Checkpoint.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) CommitWitness(ctx context.Context, result ledger.WitnessResult) (finalErr error) {
	defer func() { finalErr = classifySQLite(finalErr) }()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ledger.ErrClosed
	}
	result = result.Normalized()
	if err := result.Validate(); err != nil {
		return err
	}
	next := ""
	if !result.NextAttemptAt.IsZero() {
		next = result.NextAttemptAt.UTC().Format(time.RFC3339Nano)
	}
	attempted := ""
	if !result.AttemptedAt.IsZero() {
		attempted = result.AttemptedAt.UTC().Format(time.RFC3339Nano)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE witness_deliveries SET state=?,attempts=attempts+1,attempted_at=?,next_attempt_at=?,last_error=?,receipt=? WHERE log_id=? AND witness_id=? AND mmr_size=? AND state IN (?,?) AND EXISTS(SELECT 1 FROM ledger_metadata m WHERE m.log_id=? AND m.active=1)`, result.State, attempted, next, result.Error, result.Receipt, s.logID, result.WitnessID, result.MMRSize, ledger.WitnessPending, ledger.WitnessRetryable, s.logID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return s.classifyWitnessUpdate(ctx, result.WitnessID, result.MMRSize)
	}
	return nil
}

func (s *Store) classifyWitnessUpdate(ctx context.Context, witnessID string, mmrSize uint64) error {
	var state ledger.WitnessState
	err := s.db.QueryRowContext(ctx, `SELECT state FROM witness_deliveries WHERE log_id=? AND witness_id=? AND mmr_size=?`, s.logID, witnessID, mmrSize).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return ledger.ErrNotFound
	}
	if err != nil {
		return err
	}
	return ledger.ErrConflict
}

func (s *Store) GetWitness(ctx context.Context, witnessID string, mmrSize uint64) (ledger.PendingWitness, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ledger.PendingWitness{}, ledger.ErrClosed
	}
	if ledger.ValidateIdentifier(witnessID) != nil || mmrSize == 0 {
		return ledger.PendingWitness{}, ledger.ErrInvalid
	}
	item := ledger.PendingWitness{WitnessID: witnessID}
	var attempted, next, created string
	err := s.db.QueryRowContext(ctx, `SELECT d.state,d.attempts,d.attempted_at,d.next_attempt_at,d.last_error,d.receipt,c.indexed_seq,c.mmr_size,c.root,c.payload,c.signed_statement,c.created_at FROM witness_deliveries d JOIN checkpoints c ON c.log_id=d.log_id AND c.mmr_size=d.mmr_size WHERE d.log_id=? AND d.witness_id=? AND d.mmr_size=?`, s.logID, witnessID, mmrSize).Scan(&item.State, &item.Attempts, &attempted, &next, &item.LastError, &item.Receipt, &item.Checkpoint.IndexedSeq, &item.Checkpoint.MMRSize, &item.Checkpoint.Root, &item.Checkpoint.Payload, &item.Checkpoint.SignedStatement, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return ledger.PendingWitness{}, ledger.ErrNotFound
	}
	if err != nil {
		return ledger.PendingWitness{}, err
	}
	if attempted != "" {
		item.LastAttemptAt, err = time.Parse(time.RFC3339Nano, attempted)
		if err != nil {
			return ledger.PendingWitness{}, err
		}
	}
	if next != "" {
		item.NextAttemptAt, err = time.Parse(time.RFC3339Nano, next)
		if err != nil {
			return ledger.PendingWitness{}, err
		}
	}
	item.Checkpoint.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	return item, err
}

func classifySQLite(err error) error {
	if err == nil {
		return nil
	}
	var sqliteErr *sqliteDriver.Error
	if errors.As(err, &sqliteErr) {
		switch sqliteErr.Code() & 0xff {
		case 5, 6:
			return fmt.Errorf("%w: SQLite error %d", ledger.ErrRetryable, sqliteErr.Code())
		}
	}
	if bytes.Contains([]byte(err.Error()), []byte("UNIQUE constraint failed")) {
		return ledger.ErrConflict
	}
	return err
}

func ensureActive(ctx context.Context, tx *sql.Tx, logID string) error {
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT active FROM ledger_metadata WHERE log_id=?`, logID).Scan(&active); err != nil {
		return err
	}
	if !active {
		return ledger.ErrConflict
	}
	return nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.db.Close()
}

func (s *Store) Rebaseline(ctx context.Context, input ledger.RebaselineInput) (_ ledger.RebaselineRecord, finalErr error) {
	defer func() { finalErr = classifySQLite(finalErr) }()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ledger.RebaselineRecord{}, ledger.ErrClosed
	}
	input = input.Normalized()
	if input.Validate() != nil || input.NewLogID == s.logID {
		return ledger.RebaselineRecord{}, ledger.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ledger.RebaselineRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM ledger_metadata WHERE log_id=?`, input.NewLogID).Scan(&exists)
	if err == nil {
		return ledger.RebaselineRecord{}, ledger.ErrInvalid
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ledger.RebaselineRecord{}, err
	}
	var next, indexed uint64
	var first string
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT next_seq,indexed_seq,first_uncheckpointed,active FROM ledger_metadata WHERE log_id=?`, s.logID).Scan(&next, &indexed, &first, &active); err != nil {
		return ledger.RebaselineRecord{}, err
	}
	if !active {
		return ledger.RebaselineRecord{}, ledger.ErrConflict
	}
	var conflicts int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM witness_deliveries WHERE log_id=? AND state=?`, s.logID, ledger.WitnessContinuityConflict).Scan(&conflicts); err != nil {
		return ledger.RebaselineRecord{}, err
	}
	if conflicts == 0 {
		return ledger.RebaselineRecord{}, fmt.Errorf("%w: rebaseline requires a continuity conflict", ledger.ErrInvalid)
	}
	record := ledger.RebaselineRecord{OldLogID: s.logID, NewLogID: input.NewLogID, Reason: input.Reason, At: input.At.UTC(), MigrationID: input.MigrationID}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(mmr_size),0) FROM witness_deliveries WHERE log_id=? AND state=?`, s.logID, ledger.WitnessVerified).Scan(&record.LastWitnessedSize); err != nil {
		return ledger.RebaselineRecord{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ledger_metadata(log_id,next_seq,indexed_seq,first_uncheckpointed,active,force_checkpoint) VALUES(?,?,?,?,1,1)`, input.NewLogID, next, indexed, first); err != nil {
		return ledger.RebaselineRecord{}, err
	}
	for _, statement := range []string{`INSERT INTO capsules SELECT ?,seq,capsule_id,capsule,verification,parent_id,appended_at FROM capsules WHERE log_id=?`, `INSERT INTO envelopes SELECT ?,capsule_id,digest,envelope,verification,added_at FROM envelopes WHERE log_id=?`, `INSERT INTO mmr_nodes SELECT ?,position,hash FROM mmr_nodes WHERE log_id=?`} {
		if _, err := tx.ExecContext(ctx, statement, input.NewLogID, s.logID); err != nil {
			return ledger.RebaselineRecord{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ledger_metadata SET active=0 WHERE log_id=?`, s.logID); err != nil {
		return ledger.RebaselineRecord{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO rebaselines(migration_id,old_log_id,new_log_id,reason,migrated_at,last_witnessed_size) VALUES(?,?,?,?,?,?)`, record.MigrationID, record.OldLogID, record.NewLogID, record.Reason, record.At.Format(time.RFC3339Nano), record.LastWitnessedSize); err != nil {
		return ledger.RebaselineRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return ledger.RebaselineRecord{}, err
	}
	s.logID = input.NewLogID
	return record, nil
}

// Rebaselines returns the active log's durable migration lineage.
func (s *Store) Rebaselines(ctx context.Context, limit int) ([]ledger.RebaselineRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, ledger.ErrClosed
	}
	if limit <= 0 || limit > ledger.MaxScanLimit {
		return nil, ledger.ErrInvalid
	}
	current := s.logID
	history := make([]ledger.RebaselineRecord, 0, limit)
	seen := make(map[string]struct{}, limit)
	for len(history) < limit {
		if _, exists := seen[current]; exists {
			return nil, fmt.Errorf("%w: cyclic rebaseline lineage", ledger.ErrCorrupt)
		}
		seen[current] = struct{}{}
		var record ledger.RebaselineRecord
		var at string
		err := s.db.QueryRowContext(ctx, `SELECT old_log_id,new_log_id,reason,migrated_at,migration_id,last_witnessed_size FROM rebaselines WHERE new_log_id=?`, current).
			Scan(&record.OldLogID, &record.NewLogID, &record.Reason, &at, &record.MigrationID, &record.LastWitnessedSize)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, err
		}
		record.At, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid rebaseline timestamp", ledger.ErrCorrupt)
		}
		var witnessed uint64
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(mmr_size),0) FROM witness_deliveries WHERE log_id=? AND state=?`, record.OldLogID, ledger.WitnessVerified).Scan(&witnessed); err != nil {
			return nil, err
		}
		if witnessed != record.LastWitnessedSize {
			return nil, fmt.Errorf("%w: rebaseline witnessed size mismatch", ledger.ErrCorrupt)
		}
		history = append(history, record)
		current = record.OldLogID
	}
	slices.Reverse(history)
	return history, nil
}

func sameEnvelopes(left, right []ledger.Envelope) bool {
	if len(right) > len(left) {
		return false
	}
	values := make(map[ledger.EnvelopeDigest][]byte, len(left))
	for _, e := range left {
		values[e.Digest] = e.Bytes
	}
	for _, e := range right {
		stored, exists := values[e.Digest]
		if !exists || !bytes.Equal(stored, e.Bytes) {
			return false
		}
	}
	return true
}
func clone(input []byte) []byte { return append([]byte(nil), input...) }
func cloneEnvelope(e ledger.Envelope) ledger.Envelope {
	e.AddedAt = ledger.NormalizeTime(e.AddedAt)
	return e.Clone()
}
func cloneEnvelopes(in []ledger.Envelope) []ledger.Envelope {
	if len(in) == 0 {
		return nil
	}
	out := make([]ledger.Envelope, len(in))
	for i := range in {
		out[i] = cloneEnvelope(in[i])
	}
	return out
}
