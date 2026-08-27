package mysql

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/ethanyzhang/capsule-ledger-go/internal/storetest"
	"github.com/ethanyzhang/capsule-ledger-go/ledger"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	containerMysql "github.com/testcontainers/testcontainers-go/modules/mysql"
)

func TestStoreContract(t *testing.T) {
	dsn, terminate := startMySQL(t)
	t.Cleanup(terminate)
	storetest.Run(t, func(t *testing.T, logID string) ledger.Store {
		store, err := Open(t.Context(), dsn, logID)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Close()) })
		return store
	})
}

func TestCLLStoreContract(t *testing.T) {
	dsn, terminate := startMySQL(t)
	t.Cleanup(terminate)
	storetest.RunCLL(t, func(t *testing.T, logID string) ledger.CLLStore {
		store, err := Open(t.Context(), dsn, logID)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Close()) })
		return store
	})
}

func TestRebaselineContract(t *testing.T) {
	dsn, terminate := startMySQL(t)
	t.Cleanup(terminate)
	storetest.RunRebaseline(t, func(t *testing.T, logID string) storetest.RebaselineStore {
		store, err := Open(t.Context(), dsn, logID)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Close()) })
		return store
	})
}

func TestStoreLifecycleAndLogIsolation(t *testing.T) {
	dsn, terminate := startMySQL(t)
	t.Cleanup(terminate)
	first, err := Open(t.Context(), dsn, "first")
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

	_, outcome, err = first.Append(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, ledger.AppendIdempotent, outcome)

	envelope := ledger.Envelope{Digest: "4c503ca67761e5c4aaecfe996244c25d8c0b40902d1085c85b4468bd567548c6", Bytes: []byte("envelope"), AddedAt: input.AppendedAt}
	_, addOutcome, err := first.AddEnvelope(t.Context(), ledger.EnvelopeInput{CapsuleID: input.CapsuleID, Envelope: envelope})
	require.NoError(t, err)
	require.Equal(t, ledger.EnvelopeInserted, addOutcome)
	entries, err := first.ScanIDs(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Equal(t, []ledger.LogEntry{{Seq: 1, CapsuleID: input.CapsuleID, AppendedAt: input.AppendedAt}}, entries)
	gaps, err := first.FindChainGaps(t.Context())
	require.NoError(t, err)
	require.Len(t, gaps, 1)
	require.NoError(t, first.Close())

	second, err := Open(t.Context(), dsn, "second")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.Close()) })
	_, err = second.Get(t.Context(), input.CapsuleID)
	require.ErrorIs(t, err, ledger.ErrNotFound)
	require.NoError(t, second.Close())

	db, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `UPDATE schema_metadata SET version=1 WHERE singleton=1`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	_, err = Open(t.Context(), dsn, "first")
	require.ErrorIs(t, err, ledger.ErrCorrupt)
}

func startMySQL(t *testing.T) (string, func()) {
	t.Helper()
	if os.Getenv("CI") == "" {
		testcontainers.SkipIfProviderIsNotHealthy(t)
	}
	container, err := containerMysql.Run(t.Context(), "mysql:8.0.36",
		containerMysql.WithDatabase("ledger"),
		containerMysql.WithUsername("ledger"),
		containerMysql.WithPassword("password"),
	)
	require.NoError(t, err)
	terminate := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		require.NoError(t, container.Terminate(ctx))
	}
	return container.MustConnectionString(t.Context()), terminate
}

func TestClassifyRetryableMySQLErrors(t *testing.T) {
	for _, number := range []uint16{1205, 1213} {
		err := classify(&mysqlDriver.MySQLError{Number: number, Message: "retry"})
		require.ErrorIs(t, err, ledger.ErrRetryable)
	}
}
