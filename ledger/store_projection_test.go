package ledger

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProjectCLLEntries(t *testing.T) {
	appendedAt := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	entries, err := ProjectCLLEntries([]LogEntry{{
		Seq:        7,
		CapsuleID:  CapsuleID("11" + "00000000000000000000000000000000000000000000000000000000000000"),
		AppendedAt: appendedAt,
	}})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, uint64(7), entries[0].Seq)
	require.Len(t, entries[0].Value, 32)
	require.Equal(t, byte(0x11), entries[0].Value[0])
	require.Equal(t, appendedAt, entries[0].AppendedAt)
}

func TestProjectCLLEntriesRejectsCorruptCapsuleID(t *testing.T) {
	for _, id := range []CapsuleID{
		CapsuleID("not-hex"),
		CapsuleID("AA" + strings.Repeat("00", 31)),
	} {
		_, err := ProjectCLLEntries([]LogEntry{{CapsuleID: id}})
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrCorrupt))
	}
}
