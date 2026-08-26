package anchor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/ethanyzhang/capsule-ledger-go/ledger"
)

// Submitter sends a signed checkpoint to one witness.
type Submitter interface {
	Submit(context.Context, []byte) (Receipt, error)
}

// Verifier validates a receipt without trusting the submitting server.
type Verifier interface {
	Verify([]byte, Receipt) error
}

// DeliveryConfig controls one witness's independent retry loop.
type DeliveryConfig struct {
	WitnessID    string
	PollInterval time.Duration
	BaseBackoff  time.Duration
	MaxBackoff   time.Duration
	Jitter       bool
}

// DefaultDeliveryConfig returns bounded exponential retry defaults.
func DefaultDeliveryConfig(witnessID string) DeliveryConfig {
	return DeliveryConfig{WitnessID: witnessID, PollInterval: time.Minute, BaseBackoff: time.Second, MaxBackoff: 15 * time.Minute, Jitter: true}
}

// DeliveryRunner delivers the oldest durable checkpoint for one witness.
type DeliveryRunner struct {
	config   DeliveryConfig
	store    ledger.CLLStore
	client   Submitter
	verifier Verifier
	notify   chan struct{}
}

// NewDeliveryRunner validates dependencies without starting background work.
func NewDeliveryRunner(config DeliveryConfig, store ledger.CLLStore, client Submitter, verifier Verifier) (*DeliveryRunner, error) {
	if ledger.ValidateIdentifier(config.WitnessID) != nil || config.PollInterval <= 0 || config.BaseBackoff <= 0 || config.MaxBackoff < config.BaseBackoff || store == nil || client == nil || verifier == nil {
		return nil, fmt.Errorf("%w: invalid witness delivery configuration", ledger.ErrInvalid)
	}
	return &DeliveryRunner{config: config, store: store, client: client, verifier: verifier, notify: make(chan struct{}, 1)}, nil
}

func (r *DeliveryRunner) Notify() {
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

func (r *DeliveryRunner) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.config.PollInterval)
	defer ticker.Stop()
	for {
		for {
			changed, err := r.RunOnce(ctx, time.Now().UTC())
			if err != nil {
				if errors.Is(err, ledger.ErrRetryable) {
					break
				}
				return err
			}
			if !changed {
				break
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.notify:
		case <-ticker.C:
		}
	}
}

// RunOnce persists at most one eligible delivery attempt. Its boolean is true
// when an attempt result was committed, including retryable and terminal
// failures, not only verified delivery.
func (r *DeliveryRunner) RunOnce(ctx context.Context, now time.Time) (bool, error) {
	now = ledger.NormalizeTime(now)
	pending, err := r.store.PendingWitnesses(ctx, r.config.WitnessID, 1)
	if err != nil {
		return false, err
	}
	if len(pending) == 0 {
		return false, nil
	}
	item := pending[0]
	if !item.NextAttemptAt.IsZero() && now.Before(item.NextAttemptAt) {
		return false, nil
	}
	receipt, submitErr := r.client.Submit(ctx, item.Checkpoint.SignedStatement)
	if submitErr != nil {
		state := ledger.WitnessPermanentFailure
		next := time.Time{}
		if IsContinuityConflict(submitErr) {
			state = ledger.WitnessContinuityConflict
		} else if IsRetryable(submitErr) {
			state = ledger.WitnessRetryable
			next = now.Add(r.backoff(item.Attempts))
		}
		result := ledger.WitnessResult{WitnessID: r.config.WitnessID, MMRSize: item.Checkpoint.MMRSize, State: state, AttemptedAt: now, NextAttemptAt: next, Error: boundedText(submitErr.Error())}
		return true, r.store.CommitWitness(ctx, result)
	}
	if err := r.verifier.Verify(item.Checkpoint.SignedStatement, receipt); err != nil {
		return true, r.store.CommitWitness(ctx, ledger.WitnessResult{WitnessID: r.config.WitnessID, MMRSize: item.Checkpoint.MMRSize, State: ledger.WitnessPermanentFailure, AttemptedAt: now, Error: boundedText(err.Error())})
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return false, err
	}
	return true, r.store.CommitWitness(ctx, ledger.WitnessResult{WitnessID: r.config.WitnessID, MMRSize: item.Checkpoint.MMRSize, State: ledger.WitnessVerified, Receipt: raw, AttemptedAt: now})
}

func (r *DeliveryRunner) backoff(attempts uint64) time.Duration {
	delay := r.config.BaseBackoff
	for count := uint64(0); count < attempts && delay < r.config.MaxBackoff/2; count++ {
		delay *= 2
	}
	if delay > r.config.MaxBackoff {
		delay = r.config.MaxBackoff
	}
	if r.config.Jitter {
		delay = time.Duration(rand.Int64N(int64(delay) + 1))
	}
	return delay
}
