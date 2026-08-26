package ledger

import "errors"

var (
	ErrNotFound  = errors.New("ledger record not found")
	ErrInvalid   = errors.New("invalid ledger input")
	ErrConflict  = errors.New("immutable ledger conflict")
	ErrCorrupt   = errors.New("ledger corruption")
	ErrClosed    = errors.New("ledger store closed")
	ErrRetryable = errors.New("retryable ledger operation")
)
