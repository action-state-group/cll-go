package ledger

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	aacverify "github.com/action-state-group/agent-action-capsule/go/verify"
)

// Clock supplies append timestamps for deterministic hosts and tests.
type Clock func() time.Time

// Service verifies input before delegating exact bytes to a Store.
type Service struct {
	store    Store
	verifier Verifier
	now      Clock
}

// New constructs a ledger Service without starting background work.
func New(store Store, verifier Verifier, clock Clock) (*Service, error) {
	if store == nil || verifier == nil {
		return nil, fmt.Errorf("%w: store and verifier are required", ErrInvalid)
	}
	if clock == nil {
		clock = time.Now
	}
	return &Service{store: store, verifier: verifier, now: clock}, nil
}

func (s *Service) Append(ctx context.Context, capsule []byte, envelopes ...[]byte) (Record, error) {
	if len(envelopes) > MaxEnvelopesPerCapsule {
		return Record{}, fmt.Errorf("%w: too many envelopes", ErrInvalid)
	}
	verification, id, err := s.verifier.VerifyCapsule(capsule)
	if err != nil {
		return Record{}, err
	}
	parentID, err := capsuleParent(capsule)
	if err != nil {
		return Record{}, err
	}
	appendedAt := NormalizeTime(s.now())
	verifiedEnvelopes := make([]Envelope, 0, len(envelopes))
	seen := make(map[EnvelopeDigest][]byte, len(envelopes))
	for _, raw := range envelopes {
		verification, err := s.verifier.VerifyEnvelope(id, raw)
		if err != nil {
			return Record{}, err
		}
		digest := digestEnvelope(raw)
		if previous, exists := seen[digest]; exists {
			if !bytes.Equal(previous, raw) {
				return Record{}, fmt.Errorf("%w: envelope digest collision", ErrConflict)
			}
			continue
		}
		seen[digest] = cloneBytes(raw)
		verifiedEnvelopes = append(verifiedEnvelopes, Envelope{
			Digest: digest, Bytes: cloneBytes(raw), Verification: verification,
			AddedAt: appendedAt,
		})
	}
	record, _, err := s.store.Append(ctx, AppendInput{
		CapsuleID: id, Capsule: cloneBytes(capsule), Envelopes: verifiedEnvelopes,
		Verification: verification, ParentID: parentID, AppendedAt: appendedAt,
	})
	return record, err
}

func (s *Service) AddEnvelope(ctx context.Context, id CapsuleID, raw []byte) (Envelope, error) {
	if !validID(id) {
		return Envelope{}, fmt.Errorf("%w: malformed capsule id", ErrInvalid)
	}
	verification, err := s.verifier.VerifyEnvelope(id, raw)
	if err != nil {
		return Envelope{}, err
	}
	envelope, _, err := s.store.AddEnvelope(ctx, EnvelopeInput{
		CapsuleID: id,
		Envelope:  Envelope{Digest: digestEnvelope(raw), Bytes: cloneBytes(raw), Verification: verification, AddedAt: NormalizeTime(s.now())},
	})
	return envelope, err
}

func (s *Service) Get(ctx context.Context, id CapsuleID) (Record, error) {
	if !validID(id) {
		return Record{}, fmt.Errorf("%w: malformed capsule id", ErrInvalid)
	}
	return s.store.Get(ctx, id)
}

func (s *Service) Scan(ctx context.Context, after uint64, limit int) ([]Record, error) {
	if limit == 0 {
		limit = DefaultScanLimit
	}
	if limit < 0 || limit > MaxScanLimit {
		return nil, fmt.Errorf("%w: scan limit %d", ErrInvalid, limit)
	}
	return s.store.Scan(ctx, after, limit)
}

func (s *Service) FindChainGaps(ctx context.Context) ([]ChainGap, error) {
	return s.store.FindChainGaps(ctx)
}

func (s *Service) Audit(ctx context.Context, maxRecords int) ([]RecordVerification, error) {
	if maxRecords <= 0 || maxRecords > MaxScanLimit {
		return nil, fmt.Errorf("%w: audit bound %d", ErrInvalid, maxRecords)
	}
	auditor, ok := s.verifier.(AuditVerifier)
	if !ok {
		return nil, fmt.Errorf("%w: verifier does not support store audit", ErrInvalid)
	}
	records, err := s.store.Scan(ctx, 0, maxRecords+1)
	if err != nil {
		return nil, err
	}
	if len(records) > maxRecords {
		return nil, fmt.Errorf("%w: ledger exceeds audit bound %d", ErrInvalid, maxRecords)
	}
	return auditor.VerifyRecords(records), nil
}

func capsuleParent(data []byte) (CapsuleID, error) {
	decoded, err := aacverify.DecodeCapsuleJSON(data)
	if err != nil {
		return "", fmt.Errorf("%w: decode capsule parent: %v", ErrInvalid, err)
	}
	capsule, ok := decoded.(map[string]any)
	if !ok {
		return "", fmt.Errorf("%w: capsule root must be an object", ErrInvalid)
	}
	chain, ok := capsule["chain"].(map[string]any)
	if !ok {
		return "", nil
	}
	value, ok := chain["parent_capsule_id"]
	if !ok {
		return "", nil
	}
	parent, ok := value.(string)
	if !ok || !validID(CapsuleID(parent)) {
		return "", fmt.Errorf("%w: malformed parent capsule id", ErrInvalid)
	}
	return CapsuleID(parent), nil
}

func validID(id CapsuleID) bool {
	if len(id) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(string(id))
	return err == nil && len(decoded) == sha256.Size && string(id) == string(bytes.ToLower([]byte(id)))
}

func digestEnvelope(data []byte) EnvelopeDigest {
	digest := sha256.Sum256(data)
	return EnvelopeDigest(hex.EncodeToString(digest[:]))
}

func cloneBytes(input []byte) []byte {
	return append([]byte(nil), input...)
}
