package cll

import (
	"context"
	"time"
)

// Entry is one application-neutral value in local log order. Seq is a dense,
// 1-based sequence. Value is an exact 32-byte record identity committed as an
// MMR leaf. AppendedAt must be non-zero because checkpoint age cadence depends
// on it.
type Entry struct {
	Seq        uint64
	Value      []byte
	AppendedAt time.Time
}

// Clone returns an entry whose value cannot alias source-owned memory.
func (e Entry) Clone() Entry {
	e.Value = append([]byte(nil), e.Value...)
	return e
}

// Source exposes a bounded projection of local log entries ordered by dense,
// 1-based Seq values.
type Source interface {
	ScanEntries(ctx context.Context, afterSeq uint64, limit int) ([]Entry, error)
}
