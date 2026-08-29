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

// Config controls application vocabulary extensions and deterministic time.
// AAC format-4 verification itself is mandatory and is not replaceable by the
// host.
type Config struct {
	// RegistryExtensions adds application vocabulary under the registry names
	// in registry_baseline.go, for example "effect.type". Only true values are
	// added; extensions cannot remove baseline values. New copies the map.
	RegistryExtensions map[string]map[string]bool
	Clock              Clock
}

// Service verifies input before delegating exact bytes to a Store.
type Service struct {
	store    Store
	verifier AACVerifier
	now      Clock
}

// New constructs a ledger Service with mandatory AAC format-4 verification
// and without starting background work.
func New(store Store, config Config) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: store is required", ErrInvalid)
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	return &Service{
		store:    store,
		verifier: NewAACVerifier(config.RegistryExtensions),
		now:      config.Clock,
	}, nil
}

// Append admits one Capsule under the caller's explicit authenticity mode.
// Signed admission requires at least one valid Producer Envelope; unsigned
// admission rejects envelopes rather than inferring or silently changing mode.
func (s *Service) Append(ctx context.Context, mode AdmissionMode, capsule []byte, envelopes ...[]byte) (Record, error) {
	if mode != AdmissionUnsigned && mode != AdmissionSigned {
		return Record{}, fmt.Errorf("%w: admission mode must be unsigned or signed", ErrInvalid)
	}
	if len(envelopes) > MaxEnvelopesPerCapsule {
		return Record{}, fmt.Errorf("%w: too many envelopes", ErrInvalid)
	}
	if mode == AdmissionUnsigned && len(envelopes) != 0 {
		return Record{}, fmt.Errorf("%w: unsigned admission does not permit Producer Envelopes", ErrAdmission)
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
	candidates := make([]envelopeCandidate, 0, len(envelopes)+1)
	for _, raw := range envelopes {
		candidates = append(candidates, envelopeCandidate{raw: raw})
	}
	if mode == AdmissionSigned {
		if embedded, present := embeddedProducerEnvelope(capsule); present {
			candidates = append(candidates, embedded)
		}
	}
	verifiedEnvelopes := make([]Envelope, 0, len(candidates))
	seen := make(map[EnvelopeDigest][]byte, len(candidates))
	var firstCandidateErr error
	for _, candidate := range candidates {
		raw := candidate.raw
		envelopeVerification, verifyErr := s.verifier.VerifyEnvelope(id, raw)
		if verifyErr != nil {
			if firstCandidateErr == nil {
				firstCandidateErr = verifyErr
			}
			continue
		}
		if candidate.requiredPublicKey != nil && !bytes.Equal(candidate.requiredPublicKey, envelopeVerification.PublicKey) {
			if firstCandidateErr == nil {
				firstCandidateErr = fmt.Errorf("embedded key_id does not match the authenticated Producer Envelope key")
			}
			continue
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
			Digest: digest, Bytes: cloneBytes(raw), Verification: envelopeVerification,
			AddedAt: appendedAt,
		})
	}
	if mode == AdmissionSigned && len(verifiedEnvelopes) == 0 {
		if firstCandidateErr != nil {
			return Record{}, fmt.Errorf("%w: no Producer Envelope verifies against the recomputed Capsule ID: %v", ErrAdmission, firstCandidateErr)
		}
		return Record{}, fmt.Errorf("%w: no Producer Envelope verifies against the recomputed Capsule ID", ErrAdmission)
	}
	if len(verifiedEnvelopes) > MaxEnvelopesPerCapsule {
		return Record{}, fmt.Errorf("%w: too many verified envelopes", ErrInvalid)
	}
	authenticity := AuthenticityUnsigned
	if mode == AdmissionSigned {
		authenticity = AuthenticitySigned
	}
	record, _, err := s.store.Append(ctx, AppendInput{
		CapsuleID: id, Capsule: cloneBytes(capsule), Authenticity: authenticity, Envelopes: verifiedEnvelopes,
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
	records, err := s.store.Scan(ctx, 0, maxRecords)
	if err != nil {
		return nil, err
	}
	if len(records) == maxRecords {
		extra, err := s.store.ScanIDs(ctx, records[len(records)-1].Seq, 1)
		if err != nil {
			return nil, err
		}
		if len(extra) > 0 {
			return nil, fmt.Errorf("%w: ledger exceeds audit bound %d", ErrInvalid, maxRecords)
		}
	}
	return s.verifier.VerifyRecords(records), nil
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

type envelopeCandidate struct {
	raw               []byte
	requiredPublicKey []byte
}

// embeddedProducerEnvelope extracts capsule-emit's local-only envelope fields.
// Their presence is evidence only; the caller's explicit admission mode still
// decides whether they are considered.
func embeddedProducerEnvelope(data []byte) (envelopeCandidate, bool) {
	decoded, err := aacverify.DecodeCapsuleJSON(data)
	if err != nil {
		return envelopeCandidate{}, false
	}
	capsule, ok := decoded.(map[string]any)
	if !ok {
		return envelopeCandidate{}, false
	}
	signatureHex, signatureOK := capsule["signature"].(string)
	keyIDHex, keyIDOK := capsule["key_id"].(string)
	if !signatureOK || !keyIDOK {
		return envelopeCandidate{}, false
	}
	signature, signatureErr := hex.DecodeString(signatureHex)
	keyID, keyIDErr := hex.DecodeString(keyIDHex)
	if signatureErr != nil || keyIDErr != nil {
		return envelopeCandidate{}, false
	}
	return envelopeCandidate{raw: signature, requiredPublicKey: keyID}, true
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
