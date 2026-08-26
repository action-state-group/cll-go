package sqlite

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ethanyzhang/capsule-ledger-go/internal/storetest"
	"github.com/ethanyzhang/capsule-ledger-go/ledger"
	"github.com/stretchr/testify/require"
)

func TestStoreContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contract.db")
	storetest.Run(t, func(t *testing.T, logID string) ledger.Store {
		store, err := Open(path, logID)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Close()) })
		return store
	})
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
			_, _, err := stores[number%2].Append(t.Context(), ledger.AppendInput{CapsuleID: ledger.CapsuleID(fmt.Sprintf("%064x", number)), Capsule: []byte(fmt.Sprintf("capsule-%d", number)), AppendedAt: time.Now().UTC()})
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
		CapsuleID:  ledger.CapsuleID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		Capsule:    []byte(`{"v":4}`),
		ParentID:   ledger.CapsuleID("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
		AppendedAt: time.Date(2026, 8, 25, 1, 2, 3, 0, time.UTC),
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

func TestOpenRejectsUnknownSchemaVersion(t *testing.T) {
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
