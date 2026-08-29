package ledger

import (
	"errors"
	"fmt"
)

var (
	ErrNotFound  = errors.New("ledger record not found")
	ErrInvalid   = errors.New("invalid ledger input")
	ErrConflict  = errors.New("immutable ledger conflict")
	ErrCorrupt   = errors.New("ledger corruption")
	ErrClosed    = errors.New("ledger store closed")
	ErrRetryable = errors.New("retryable ledger operation")
	// ErrAdmission is invalid caller input with a narrower classification for
	// hosts that want to report an admission-policy rejection distinctly.
	ErrAdmission = fmt.Errorf("%w: ledger admission rejected", ErrInvalid)
)
