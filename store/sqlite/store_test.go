package sqlite

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ethanyzhang/cll-go/internal/storetest"
	"github.com/ethanyzhang/cll-go/ledger"
	"github.com/stretchr/testify/require"
)

func TestStoreContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contract.db")
	open := func(t *testing.T, logID string) ledger.Store {
		store, err := Open(path, logID)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Close()) })
		return store
	}
	storetest.Run(t, open)
	storetest.RunAdmission(t, open)
}

func TestTwoSQLiteHandlesSerializeSequenceAllocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.db")
	first, err := Open(path, "shared-log")
	require.NoError(t, err)
	second, err := Open(path, "shared-log")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, first.Close()); require.NoError(t, second.Close()) })
	stores := []*Store{first, second}
	var wait sync.WaitGroup
	errors := make(chan error, 16)
	for number := 1; number <= 16; number++ {
		number := number
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _, err := stores[number%2].Append(t.Context(), ledger.AppendInput{CapsuleID: ledger.CapsuleID(fmt.Sprintf("%064x", number)), Capsule: []byte(fmt.Sprintf("capsule-%d", number)), Authenticity: ledger.AuthenticityUnsigned, AppendedAt: time.Now().UTC()})
			errors <- err
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	entries, err := first.ScanIDs(t.Context(), 0, 20)
	require.NoError(t, err)
	require.Len(t, entries, 16)
	for index, entry := range entries {
		require.Equal(t, uint64(index+1), entry.Seq)
	}
}

func TestCLLStoreContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cll.db")
	storetest.RunCLL(t, func(t *testing.T, logID string) ledger.CLLStore {
		store, err := Open(path, logID)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Close()) })
		return store
	})
}

func TestRebaselineContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rebaseline.db")
	storetest.RunRebaseline(t, func(t *testing.T, logID string) storetest.RebaselineStore {
		store, err := Open(path, logID)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Close()) })
		return store
	})
}

func TestStoreLifecycleAndLogIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	first, err := Open(path, "first")
	require.NoError(t, err)
	input := ledger.AppendInput{
		CapsuleID:    ledger.CapsuleID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		Capsule:      []byte(`{"v":4}`),
		Authenticity: ledger.AuthenticityUnsigned,
		ParentID:     ledger.CapsuleID("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
		AppendedAt:   time.Date(2026, 8, 25, 1, 2, 3, 0, time.UTC),
	}
	record, outcome, err := first.Append(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, ledger.AppendInserted, outcome)
	require.Equal(t, uint64(1), record.Seq)

	record, outcome, err = first.Append(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, ledger.AppendIdempotent, outcome)

	envelope := ledger.Envelope{Digest: "4c503ca67761e5c4aaecfe996244c25d8c0b40902d1085c85b4468bd567548c6", Bytes: []byte("envelope"), AddedAt: input.AppendedAt}
	_, addOutcome, err := first.AddEnvelope(t.Context(), ledger.EnvelopeInput{CapsuleID: input.CapsuleID, Envelope: envelope})
	require.NoError(t, err)
	require.Equal(t, ledger.EnvelopeInserted, addOutcome)
	gaps, err := first.FindChainGaps(t.Context())
	require.NoError(t, err)
	require.Len(t, gaps, 1)
	require.NoError(t, first.Close())

	reopened, err := Open(path, "first")
	require.NoError(t, err)
	restored, err := reopened.Get(t.Context(), input.CapsuleID)
	require.NoError(t, err)
	require.Equal(t, input.Capsule, restored.Capsule)
	require.Len(t, restored.Envelopes, 1)
	entries, err := reopened.ScanIDs(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Equal(t, []ledger.LogEntry{{Seq: 1, CapsuleID: input.CapsuleID, AppendedAt: input.AppendedAt}}, entries)
	require.NoError(t, reopened.Close())

	second, err := Open(path, "second")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.Close()) })
	_, err = second.Get(t.Context(), input.CapsuleID)
	require.ErrorIs(t, err, ledger.ErrNotFound)
}

func TestOpenRejectsVersionTwoSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "version.db")
	store, err := Open(path, "versioned")
	require.NoError(t, err)
	require.NoError(t, store.Close())

	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE schema_metadata SET version=2 WHERE singleton=1`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = Open(path, "versioned")
	require.ErrorIs(t, err, ledger.ErrCorrupt)
}

func TestGetRejectsSignedRecordWithStrippedEnvelope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stripped.db")
	store, err := Open(path, "stripped")
	require.NoError(t, err)
	now := time.Now().UTC()
	envelopeBytes := []byte("signed-envelope")
	digest := sha256.Sum256(envelopeBytes)
	id := ledger.CapsuleID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_, _, err = store.Append(t.Context(), ledger.AppendInput{
		CapsuleID: id, Capsule: []byte(`{"v":4}`), Authenticity: ledger.AuthenticitySigned, AppendedAt: now,
		Envelopes: []ledger.Envelope{{Digest: ledger.EnvelopeDigest(hex.EncodeToString(digest[:])), Bytes: envelopeBytes, Verification: ledger.EnvelopeVerification{OK: true, PublicKey: make([]byte, 32)}, AddedAt: now}},
	})
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), `DELETE FROM envelopes WHERE log_id=? AND capsule_id=?`, "stripped", id)
	require.NoError(t, err)
	_, err = store.Get(t.Context(), id)
	require.ErrorIs(t, err, ledger.ErrCorrupt)
	require.ErrorContains(t, err, "signed record has no verified envelope")
	require.NoError(t, store.Close())
}
