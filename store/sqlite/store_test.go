package sqlite

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/action-state-group/cll-go/cll"
	"github.com/action-state-group/cll-go/internal/storetest"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestContractAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cll.sqlite")
	store, err := Open(path, "default")
	require.NoError(t, err)
	storetest.Run(t, store)
	reopened, err := Open(path, "default")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	entries, err := reopened.ScanEntries(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, entries, 2)
}

func TestCrossHandleDenseSequences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cll.sqlite")
	first, err := Open(path, "shared")
	require.NoError(t, err)
	second, err := Open(path, "shared")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, first.Close())
		require.NoError(t, second.Close())
	})
	storetest.CrossHandle(t, first, second)
}

func TestReadUsesStableSnapshotAcrossExternalWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.sqlite")
	store, err := Open(path, "snapshot")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	external, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, external.Close()) })
	_, err = external.Exec("PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000")
	require.NoError(t, err)

	err = store.withRead(t.Context(), func(q queryer) error {
		var before int
		if err := q.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM cll_nodes WHERE log_id=?", "snapshot").Scan(&before); err != nil {
			return err
		}
		require.Zero(t, before)
		if _, err := external.ExecContext(t.Context(), "INSERT INTO cll_nodes(log_id,position,node) VALUES(?,?,?)", "snapshot", 0, bytes.Repeat([]byte{1}, cll.EntryBytes)); err != nil {
			return err
		}
		var during int
		if err := q.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM cll_nodes WHERE log_id=?", "snapshot").Scan(&during); err != nil {
			return err
		}
		require.Zero(t, during)
		return nil
	})
	require.NoError(t, err)

	var after int
	require.NoError(t, external.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM cll_nodes WHERE log_id=?", "snapshot").Scan(&after))
	require.Equal(t, 1, after)
}

func TestSchemaAndLegacyDetection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cll.sqlite")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE ledger_metadata(id INTEGER)")
	require.NoError(t, err)
	require.NoError(t, db.Close())
	store, err := Open(path, "default")
	require.Nil(t, store)
	require.ErrorIs(t, err, cll.ErrCorrupt)

	genericPath := filepath.Join(t.TempDir(), "generic.sqlite")
	db, err = sql.Open("sqlite", genericPath)
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE schema_metadata(singleton INTEGER, version INTEGER); CREATE TABLE capsules(id TEXT)")
	require.NoError(t, err)
	require.NoError(t, db.Close())
	store, err = Open(genericPath, "default")
	require.NoError(t, err)
	require.NoError(t, store.Close())
}
