package mysql

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/ethanyzhang/capsule-ledger-go/ledger"
	"github.com/ethanyzhang/capsule-ledger-go/mmr"
	mysqlDriver "github.com/go-sql-driver/mysql"
)

const schemaVersion = 3

const schema = `
CREATE TABLE IF NOT EXISTS schema_metadata (
  singleton TINYINT UNSIGNED PRIMARY KEY,
  version INT UNSIGNED NOT NULL,
  CONSTRAINT schema_metadata_singleton CHECK (singleton = 1)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS ledger_metadata (
  log_id VARCHAR(191) PRIMARY KEY,
  next_seq BIGINT UNSIGNED NOT NULL,
  indexed_seq BIGINT UNSIGNED NOT NULL DEFAULT 0,
  first_uncheckpointed DATETIME(6) NULL
  ,active BOOLEAN NOT NULL DEFAULT TRUE
  ,force_checkpoint BOOLEAN NOT NULL DEFAULT FALSE
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS capsules (
  log_id VARCHAR(191) NOT NULL,
  seq BIGINT UNSIGNED NOT NULL,
  capsule_id CHAR(64) NOT NULL,
  capsule LONGBLOB NOT NULL,
  authenticity VARCHAR(8) NOT NULL,
  verification JSON NOT NULL,
  parent_id CHAR(64) NOT NULL,
  appended_at DATETIME(6) NOT NULL,
  PRIMARY KEY (log_id, seq),
  UNIQUE KEY capsules_id (log_id, capsule_id),
  KEY capsules_parent (log_id, parent_id),
  CONSTRAINT capsules_log FOREIGN KEY (log_id) REFERENCES ledger_metadata(log_id),
  CONSTRAINT capsules_authenticity CHECK (authenticity IN ('unsigned','signed'))
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS envelopes (
  log_id VARCHAR(191) NOT NULL,
  capsule_id CHAR(64) NOT NULL,
  digest CHAR(64) NOT NULL,
  envelope BLOB NOT NULL,
  verification JSON NOT NULL,
  added_at DATETIME(6) NOT NULL,
  PRIMARY KEY (log_id, capsule_id, digest),
  CONSTRAINT envelopes_capsule FOREIGN KEY (log_id, capsule_id)
    REFERENCES capsules(log_id, capsule_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS mmr_nodes (
  log_id VARCHAR(191) NOT NULL, position BIGINT UNSIGNED NOT NULL, hash BINARY(32) NOT NULL,
  PRIMARY KEY (log_id, position), CONSTRAINT mmr_nodes_log FOREIGN KEY (log_id) REFERENCES ledger_metadata(log_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS checkpoints (
  log_id VARCHAR(191) NOT NULL, mmr_size BIGINT UNSIGNED NOT NULL, indexed_seq BIGINT UNSIGNED NOT NULL,
  root CHAR(64) NOT NULL, payload BLOB NOT NULL, signed_checkpoint BLOB NOT NULL, created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (log_id, mmr_size), CONSTRAINT checkpoints_log FOREIGN KEY (log_id) REFERENCES ledger_metadata(log_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS witness_deliveries (
  log_id VARCHAR(191) NOT NULL, witness_id VARCHAR(191) NOT NULL, mmr_size BIGINT UNSIGNED NOT NULL,
  state VARCHAR(32) NOT NULL, attempts BIGINT UNSIGNED NOT NULL DEFAULT 0,
  attempted_at DATETIME(6) NULL, next_attempt_at DATETIME(6) NULL,
  last_error TEXT NOT NULL, receipt BLOB NULL,
  PRIMARY KEY (log_id, witness_id, mmr_size),
  CONSTRAINT witness_checkpoint FOREIGN KEY (log_id, mmr_size) REFERENCES checkpoints(log_id, mmr_size)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS rebaselines (
  migration_id VARCHAR(191) PRIMARY KEY, old_log_id VARCHAR(191) NOT NULL,
  new_log_id VARCHAR(191) NOT NULL UNIQUE, reason TEXT NOT NULL,
  migrated_at DATETIME(6) NOT NULL, last_witnessed_size BIGINT UNSIGNED NOT NULL
) ENGINE=InnoDB;
`

// Store persists log state in MySQL 8.
type Store struct {
	mu     sync.RWMutex
	db     *sql.DB
	logID  string
	closed bool
}

// Open connects to MySQL, initializes the first-release schema, and binds logID.
func Open(ctx context.Context, dsn, logID string) (*Store, error) {
	if dsn == "" || ledger.ValidateIdentifier(logID) != nil {
		return nil, fmt.Errorf("%w: DSN and log id are required", ledger.ErrInvalid)
	}
	config, err := mysqlDriver.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse MySQL DSN: %w", err)
	}
	config.MultiStatements = true
	config.ParseTime = true
	config.Loc = time.UTC
	db, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("open MySQL store: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("ping MySQL store: %w", err), db.Close())
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return nil, errors.Join(fmt.Errorf("initialize MySQL store: %w", err), db.Close())
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_metadata(singleton,version) VALUES(1,?) ON DUPLICATE KEY UPDATE singleton=VALUES(singleton)`, schemaVersion); err != nil {
		return nil, errors.Join(fmt.Errorf("initialize MySQL schema version: %w", err), db.Close())
	}
	var version uint64
	if err := db.QueryRowContext(ctx, `SELECT version FROM schema_metadata WHERE singleton=1`).Scan(&version); err != nil || version != schemaVersion {
		return nil, errors.Join(fmt.Errorf("unsupported MySQL schema version %d: %w", version, ledger.ErrCorrupt), err, db.Close())
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO ledger_metadata(log_id,next_seq) VALUES(?,1) ON DUPLICATE KEY UPDATE log_id=VALUES(log_id)`, logID); err != nil {
		return nil, errors.Join(fmt.Errorf("initialize MySQL log: %w", err), db.Close())
	}
	var active bool
	if err := db.QueryRowContext(ctx, `SELECT active FROM ledger_metadata WHERE log_id=?`, logID).Scan(&active); err != nil || !active {
		return nil, errors.Join(fmt.Errorf("open inactive MySQL log: %w", ledger.ErrInvalid), err, db.Close())
	}
	return &Store{db: db, logID: logID}, nil
}

func (s *Store) Append(ctx context.Context, in ledger.AppendInput) (_ ledger.Record, _ ledger.AppendOutcome, finalErr error) {
	defer func() { finalErr = classify(finalErr) }()
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
	var seq uint64
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT next_seq,active FROM ledger_metadata WHERE log_id=? FOR UPDATE`, s.logID).Scan(&seq, &active); err != nil {
		return ledger.Record{}, "", err
	}
	if !active {
		return ledger.Record{}, "", ledger.ErrConflict
	}
	if existing, getErr := get(ctx, tx, s.logID, in.CapsuleID); getErr == nil {
		if existing.Authenticity != in.Authenticity || !bytes.Equal(existing.Capsule, in.Capsule) || !sameEnvelopes(existing.Envelopes, in.Envelopes) {
			return ledger.Record{}, "", ledger.ErrConflict
		}
		return existing, ledger.AppendIdempotent, nil
	} else if !errors.Is(getErr, ledger.ErrNotFound) {
		return ledger.Record{}, "", getErr
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO capsules(log_id,seq,capsule_id,capsule,authenticity,verification,parent_id,appended_at) VALUES(?,?,?,?,?,?,?,?)`,
		s.logID, seq, in.CapsuleID, in.Capsule, in.Authenticity, verification, in.ParentID, in.AppendedAt.UTC()); err != nil {
		return ledger.Record{}, "", err
	}
	for _, envelope := range in.Envelopes {
		if err := insertEnvelope(ctx, tx, s.logID, in.CapsuleID, envelope); err != nil {
			return ledger.Record{}, "", err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ledger_metadata SET next_seq=next_seq+1 WHERE log_id=?`, s.logID); err != nil {
		return ledger.Record{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return ledger.Record{}, "", err
	}
	record := ledger.Record{Seq: seq, CapsuleID: in.CapsuleID, Capsule: clone(in.Capsule), Authenticity: in.Authenticity, Envelopes: cloneEnvelopes(in.Envelopes), Verification: in.Verification.Clone(), ParentID: in.ParentID, AppendedAt: ledger.NormalizeTime(in.AppendedAt)}
	return record.Clone(), ledger.AppendInserted, nil
}

func (s *Store) AddEnvelope(ctx context.Context, in ledger.EnvelopeInput) (_ ledger.Envelope, _ ledger.AddOutcome, finalErr error) {
	defer func() { finalErr = classify(finalErr) }()
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
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT active FROM ledger_metadata WHERE log_id=? FOR UPDATE`, s.logID).Scan(&active); err != nil {
		return ledger.Envelope{}, "", err
	}
	if !active {
		return ledger.Envelope{}, "", ledger.ErrConflict
	}
	var present int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM capsules WHERE log_id=? AND capsule_id=? FOR UPDATE`, s.logID, in.CapsuleID).Scan(&present); errors.Is(err, sql.ErrNoRows) {
		return ledger.Envelope{}, "", ledger.ErrNotFound
	} else if err != nil {
		return ledger.Envelope{}, "", err
	}
	var stored ledger.Envelope
	var verification []byte
	err = tx.QueryRowContext(ctx, `SELECT digest,envelope,verification,added_at FROM envelopes WHERE log_id=? AND capsule_id=? AND digest=?`, s.logID, in.CapsuleID, in.Envelope.Digest).
		Scan(&stored.Digest, &stored.Bytes, &verification, &stored.AddedAt)
	if err == nil {
		if !bytes.Equal(stored.Bytes, in.Envelope.Bytes) {
			return ledger.Envelope{}, "", ledger.ErrConflict
		}
		if err := json.Unmarshal(verification, &stored.Verification); err != nil {
			return ledger.Envelope{}, "", fmt.Errorf("decode stored envelope verification: %w", ledger.ErrCorrupt)
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
	_, err = tx.ExecContext(ctx, `INSERT INTO envelopes(log_id,capsule_id,digest,envelope,verification,added_at) VALUES(?,?,?,?,?,?)`, logID, id, envelope.Digest, envelope.Bytes, verification, envelope.AddedAt.UTC())
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
	err := q.QueryRowContext(ctx, `SELECT seq,capsule_id,capsule,authenticity,verification,parent_id,appended_at FROM capsules WHERE log_id=? AND capsule_id=?`, logID, id).
		Scan(&record.Seq, &record.CapsuleID, &record.Capsule, &record.Authenticity, &verification, &parent, &record.AppendedAt)
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
	rows, err := q.QueryContext(ctx, `SELECT digest,envelope,verification,added_at FROM envelopes WHERE log_id=? AND capsule_id=? ORDER BY digest`, logID, id)
	if err != nil {
		return ledger.Record{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var envelope ledger.Envelope
		var encoded []byte
		if err := rows.Scan(&envelope.Digest, &envelope.Bytes, &encoded, &envelope.AddedAt); err != nil {
			return ledger.Record{}, err
		}
		if err := json.Unmarshal(encoded, &envelope.Verification); err != nil {
			return ledger.Record{}, fmt.Errorf("decode envelope verification: %w", err)
		}
		record.Envelopes = append(record.Envelopes, envelope)
	}
	if err := rows.Err(); err != nil {
		return ledger.Record{}, err
	}
	if err := record.Validate(); err != nil {
		return ledger.Record{}, fmt.Errorf("decode stored record: %w: %v", ledger.ErrCorrupt, err)
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
	var ids []ledger.CapsuleID
	for rows.Next() {
		var id ledger.CapsuleID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
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
		if err := rows.Scan(&entry.Seq, &entry.CapsuleID, &entry.AppendedAt); err != nil {
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
	var first sql.NullTime
	if err := s.db.QueryRowContext(ctx, `SELECT indexed_seq,first_uncheckpointed,force_checkpoint FROM ledger_metadata WHERE log_id=?`, s.logID).Scan(&state.IndexedSeq, &first, &state.ForceCheckpoint); err != nil {
		return ledger.CLLState{}, err
	}
	if first.Valid {
		state.FirstUncheckpointed = first.Time.UTC()
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
	rows, err = s.db.QueryContext(ctx, `SELECT indexed_seq,mmr_size,root,payload,signed_checkpoint,created_at FROM checkpoints WHERE log_id=? ORDER BY mmr_size`, s.logID)
	if err != nil {
		return ledger.CLLState{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var cp ledger.CheckpointRecord
		if err := rows.Scan(&cp.IndexedSeq, &cp.MMRSize, &cp.Root, &cp.Payload, &cp.SignedCheckpoint, &cp.CreatedAt); err != nil {
			return ledger.CLLState{}, err
		}
		cp.CreatedAt = cp.CreatedAt.UTC()
		state.Checkpoints = append(state.Checkpoints, cp)
	}
	return state, rows.Err()
}

func (s *Store) CommitCLL(ctx context.Context, mutation ledger.CLLMutation) (finalErr error) {
	defer func() { finalErr = classify(finalErr) }()
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
	var first sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT indexed_seq,first_uncheckpointed,active FROM ledger_metadata WHERE log_id=? FOR UPDATE`, s.logID).Scan(&indexed, &first, &active); err != nil {
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
	var firstValue any
	if !mutation.FirstUncheckpointed.IsZero() {
		firstValue = mutation.FirstUncheckpointed.UTC()
	}
	force := true
	if mutation.Checkpoint != nil {
		force = false
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ledger_metadata SET indexed_seq=?,first_uncheckpointed=?,force_checkpoint=IF(force_checkpoint,?,FALSE) WHERE log_id=?`, mutation.IndexedSeq, firstValue, force, s.logID); err != nil {
		return err
	}
	if mutation.Checkpoint != nil {
		cp := mutation.Checkpoint
		if cp.MMRSize != count+uint64(len(mutation.Nodes)) {
			return ledger.ErrInvalid
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO checkpoints(log_id,mmr_size,indexed_seq,root,payload,signed_checkpoint,created_at) VALUES(?,?,?,?,?,?,?)`, s.logID, cp.MMRSize, cp.IndexedSeq, cp.Root, cp.Payload, cp.SignedCheckpoint, cp.CreatedAt.UTC()); err != nil {
			return err
		}
		for _, id := range mutation.WitnessIDs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO witness_deliveries(log_id,witness_id,mmr_size,state,last_error) VALUES(?,?,?,?,?)`, s.logID, id, cp.MMRSize, ledger.WitnessPending, ""); err != nil {
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
	rows, err := s.db.QueryContext(ctx, `SELECT d.state,d.attempts,d.next_attempt_at,d.last_error,c.indexed_seq,c.mmr_size,c.root,c.payload,c.signed_checkpoint,c.created_at FROM witness_deliveries d JOIN checkpoints c ON c.log_id=d.log_id AND c.mmr_size=d.mmr_size WHERE d.log_id=? AND d.witness_id=? AND d.state IN (?,?) AND NOT EXISTS(SELECT 1 FROM witness_deliveries blocked WHERE blocked.log_id=d.log_id AND blocked.witness_id=d.witness_id AND blocked.state=?) ORDER BY d.mmr_size LIMIT ?`, s.logID, witnessID, ledger.WitnessPending, ledger.WitnessRetryable, ledger.WitnessContinuityConflict, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ledger.PendingWitness
	for rows.Next() {
		item := ledger.PendingWitness{WitnessID: witnessID}
		var next sql.NullTime
		if err := rows.Scan(&item.State, &item.Attempts, &next, &item.LastError, &item.Checkpoint.IndexedSeq, &item.Checkpoint.MMRSize, &item.Checkpoint.Root, &item.Checkpoint.Payload, &item.Checkpoint.SignedCheckpoint, &item.Checkpoint.CreatedAt); err != nil {
			return nil, err
		}
		if next.Valid {
			item.NextAttemptAt = next.Time.UTC()
		}
		item.Checkpoint.CreatedAt = item.Checkpoint.CreatedAt.UTC()
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) CommitWitness(ctx context.Context, result ledger.WitnessResult) (finalErr error) {
	defer func() { finalErr = classify(finalErr) }()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ledger.ErrClosed
	}
	result = result.Normalized()
	if err := result.Validate(); err != nil {
		return err
	}
	var next any
	if !result.NextAttemptAt.IsZero() {
		next = result.NextAttemptAt.UTC()
	}
	var attempted any
	if !result.AttemptedAt.IsZero() {
		attempted = result.AttemptedAt.UTC()
	}
	res, err := s.db.ExecContext(ctx, `UPDATE witness_deliveries SET state=?,attempts=attempts+1,attempted_at=?,next_attempt_at=?,last_error=?,receipt=? WHERE log_id=? AND witness_id=? AND mmr_size=? AND state IN (?,?) AND EXISTS(SELECT 1 FROM ledger_metadata m WHERE m.log_id=? AND m.active=TRUE)`, result.State, attempted, next, result.Error, result.Receipt, s.logID, result.WitnessID, result.MMRSize, ledger.WitnessPending, ledger.WitnessRetryable, s.logID)
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
	var attempted, next sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT d.state,d.attempts,d.attempted_at,d.next_attempt_at,d.last_error,d.receipt,c.indexed_seq,c.mmr_size,c.root,c.payload,c.signed_checkpoint,c.created_at FROM witness_deliveries d JOIN checkpoints c ON c.log_id=d.log_id AND c.mmr_size=d.mmr_size WHERE d.log_id=? AND d.witness_id=? AND d.mmr_size=?`, s.logID, witnessID, mmrSize).Scan(&item.State, &item.Attempts, &attempted, &next, &item.LastError, &item.Receipt, &item.Checkpoint.IndexedSeq, &item.Checkpoint.MMRSize, &item.Checkpoint.Root, &item.Checkpoint.Payload, &item.Checkpoint.SignedCheckpoint, &item.Checkpoint.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ledger.PendingWitness{}, ledger.ErrNotFound
	}
	if err != nil {
		return ledger.PendingWitness{}, err
	}
	if attempted.Valid {
		item.LastAttemptAt = attempted.Time.UTC()
	}
	if next.Valid {
		item.NextAttemptAt = next.Time.UTC()
	}
	item.Checkpoint.CreatedAt = item.Checkpoint.CreatedAt.UTC()
	return item, nil
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
	defer func() { finalErr = classify(finalErr) }()
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
	var first sql.NullTime
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT next_seq,indexed_seq,first_uncheckpointed,active FROM ledger_metadata WHERE log_id=? FOR UPDATE`, s.logID).Scan(&next, &indexed, &first, &active); err != nil {
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
	var firstValue any
	if first.Valid {
		firstValue = first.Time.UTC()
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ledger_metadata(log_id,next_seq,indexed_seq,first_uncheckpointed,active,force_checkpoint) VALUES(?,?,?,?,TRUE,TRUE)`, input.NewLogID, next, indexed, firstValue); err != nil {
		return ledger.RebaselineRecord{}, err
	}
	for _, statement := range []string{`INSERT INTO capsules(log_id,seq,capsule_id,capsule,authenticity,verification,parent_id,appended_at) SELECT ?,seq,capsule_id,capsule,authenticity,verification,parent_id,appended_at FROM capsules WHERE log_id=?`, `INSERT INTO envelopes SELECT ?,capsule_id,digest,envelope,verification,added_at FROM envelopes WHERE log_id=?`, `INSERT INTO mmr_nodes SELECT ?,position,hash FROM mmr_nodes WHERE log_id=?`} {
		if _, err := tx.ExecContext(ctx, statement, input.NewLogID, s.logID); err != nil {
			return ledger.RebaselineRecord{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ledger_metadata SET active=FALSE WHERE log_id=?`, s.logID); err != nil {
		return ledger.RebaselineRecord{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO rebaselines(migration_id,old_log_id,new_log_id,reason,migrated_at,last_witnessed_size) VALUES(?,?,?,?,?,?)`, record.MigrationID, record.OldLogID, record.NewLogID, record.Reason, record.At, record.LastWitnessedSize); err != nil {
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
		err := s.db.QueryRowContext(ctx, `SELECT old_log_id,new_log_id,reason,migrated_at,migration_id,last_witnessed_size FROM rebaselines WHERE new_log_id=?`, current).
			Scan(&record.OldLogID, &record.NewLogID, &record.Reason, &record.At, &record.MigrationID, &record.LastWitnessedSize)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, err
		}
		record.At = ledger.NormalizeTime(record.At)
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

func classify(err error) error {
	var mysqlErr *mysqlDriver.MySQLError
	if errors.As(err, &mysqlErr) {
		switch mysqlErr.Number {
		case 1062:
			return ledger.ErrConflict
		case 1205, 1213:
			return fmt.Errorf("%w: MySQL error %d", ledger.ErrRetryable, mysqlErr.Number)
		}
	}
	return err
}

func sameEnvelopes(left, right []ledger.Envelope) bool {
	if len(right) > len(left) {
		return false
	}
	values := make(map[ledger.EnvelopeDigest][]byte, len(left))
	for _, envelope := range left {
		values[envelope.Digest] = envelope.Bytes
	}
	for _, envelope := range right {
		stored, exists := values[envelope.Digest]
		if !exists || !bytes.Equal(stored, envelope.Bytes) {
			return false
		}
	}
	return true
}

func clone(input []byte) []byte { return append([]byte(nil), input...) }
func cloneEnvelope(envelope ledger.Envelope) ledger.Envelope {
	envelope.AddedAt = ledger.NormalizeTime(envelope.AddedAt)
	return envelope.Clone()
}
func cloneEnvelopes(input []ledger.Envelope) []ledger.Envelope {
	if len(input) == 0 {
		return nil
	}
	result := make([]ledger.Envelope, len(input))
	for index := range input {
		result[index] = cloneEnvelope(input[index])
	}
	return result
}
