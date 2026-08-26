package ledger

import (
	"fmt"
	"time"
)

const (
	MaxCapsuleBytes           = 1 << 20
	MaxEnvelopeBytes          = 4096
	MaxEnvelopesPerCapsule    = 64
	MaxWitnesses              = 32
	MaxCheckpointPayloadBytes = 64 << 10
	MaxSignedCheckpointBytes  = 128 << 10
	MaxWitnessReceiptBytes    = 2 << 20
	MaxIdentifierBytes        = 191
	MaxReasonBytes            = 4096
	DefaultScanLimit          = 100
	MaxScanLimit              = 1000
)

// ValidateIdentifier enforces the portable identifier subset shared by every backend.
func ValidateIdentifier(value string) error {
	if len(value) == 0 || len(value) > MaxIdentifierBytes {
		return fmt.Errorf("%w: identifier length", ErrInvalid)
	}
	for _, character := range []byte(value) {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == ':' || character == '/' || character == '-' {
			continue
		}
		return fmt.Errorf("%w: identifier character", ErrInvalid)
	}
	return nil
}

// NormalizeTime applies the microsecond UTC precision shared with MySQL 8.
func NormalizeTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC().Truncate(time.Microsecond)
}

// CapsuleID is a verified lowercase SHA-256 identifier.
type CapsuleID string

// EnvelopeDigest identifies exact Producer Envelope bytes.
type EnvelopeDigest string

// Finding preserves an upstream structured verification finding.
type Finding struct {
	Code     string `json:"code"`
	Detail   string `json:"detail"`
	Severity string `json:"severity"`
	Check    *int   `json:"check,omitempty"`
}

// VerificationResult preserves the complete AAC verification outcome.
type VerificationResult struct {
	OK        bool              `json:"ok"`
	Findings  []Finding         `json:"findings,omitempty"`
	Assurance map[string]string `json:"assurance,omitempty"`
	CapsuleID CapsuleID         `json:"capsule_id,omitempty"`
}

// Clone returns a fully independent verification result.
func (v VerificationResult) Clone() VerificationResult {
	v.Findings = cloneFindings(v.Findings)
	if v.Assurance != nil {
		copyMap := make(map[string]string, len(v.Assurance))
		for key, value := range v.Assurance {
			copyMap[key] = value
		}
		v.Assurance = copyMap
	}
	return v
}

// EnvelopeVerification preserves envelope findings and signer evidence.
type EnvelopeVerification struct {
	OK        bool      `json:"ok"`
	Findings  []Finding `json:"findings,omitempty"`
	PublicKey []byte    `json:"public_key,omitempty"`
}

// Clone returns fully independent envelope verification evidence.
func (v EnvelopeVerification) Clone() EnvelopeVerification {
	v.Findings = cloneFindings(v.Findings)
	v.PublicKey = append([]byte(nil), v.PublicKey...)
	return v
}

// Envelope is an immutable Producer Envelope association.
type Envelope struct {
	Digest       EnvelopeDigest       `json:"digest"`
	Bytes        []byte               `json:"bytes"`
	Verification EnvelopeVerification `json:"verification"`
	AddedAt      time.Time            `json:"added_at"`
}

// Clone returns a fully independent Envelope value.
func (e Envelope) Clone() Envelope {
	e.Bytes = append([]byte(nil), e.Bytes...)
	e.Verification = e.Verification.Clone()
	return e
}

// Record is one gaplessly sequenced immutable Capsule.
type Record struct {
	Seq          uint64             `json:"seq"`
	CapsuleID    CapsuleID          `json:"capsule_id"`
	Capsule      []byte             `json:"capsule"`
	Envelopes    []Envelope         `json:"envelopes,omitempty"`
	Verification VerificationResult `json:"verification"`
	ParentID     CapsuleID          `json:"parent_capsule_id,omitempty"`
	AppendedAt   time.Time          `json:"appended_at"`
}

// Clone returns a fully independent Record value.
func (r Record) Clone() Record {
	r.Capsule = append([]byte(nil), r.Capsule...)
	r.Verification = r.Verification.Clone()
	if len(r.Envelopes) == 0 {
		r.Envelopes = nil
	} else {
		copyEnvelopes := make([]Envelope, len(r.Envelopes))
		for index := range r.Envelopes {
			copyEnvelopes[index] = r.Envelopes[index].Clone()
		}
		r.Envelopes = copyEnvelopes
	}
	return r
}

func cloneFindings(input []Finding) []Finding {
	if len(input) == 0 {
		return nil
	}
	output := append([]Finding(nil), input...)
	for index := range output {
		if output[index].Check != nil {
			value := *output[index].Check
			output[index].Check = &value
		}
	}
	return output
}

// ChainGap identifies a Capsule whose declared parent is absent.
type ChainGap struct {
	Seq       uint64    `json:"seq"`
	CapsuleID CapsuleID `json:"capsule_id"`
	ParentID  CapsuleID `json:"parent_capsule_id"`
}

// LogEntry is the narrow ordered CLL input projection.
type LogEntry struct {
	Seq        uint64    `json:"seq"`
	CapsuleID  CapsuleID `json:"capsule_id"`
	AppendedAt time.Time `json:"appended_at"`
}

// RecordVerification attributes an audit result to stored identity.
type RecordVerification struct {
	Seq       uint64             `json:"seq"`
	CapsuleID CapsuleID          `json:"capsule_id"`
	Result    VerificationResult `json:"result"`
	Error     string             `json:"error,omitempty"`
}

// AppendOutcome distinguishes insertion from exact idempotency.
type AppendOutcome string

const (
	AppendInserted   AppendOutcome = "inserted"
	AppendIdempotent AppendOutcome = "idempotent"
)

// AddOutcome distinguishes envelope insertion from exact idempotency.
type AddOutcome string

const (
	EnvelopeInserted   AddOutcome = "inserted"
	EnvelopeIdempotent AddOutcome = "idempotent"
)

// AppendInput is the already-verified atomic Store append.
type AppendInput struct {
	CapsuleID    CapsuleID
	Capsule      []byte
	Envelopes    []Envelope
	Verification VerificationResult
	ParentID     CapsuleID
	AppendedAt   time.Time
}

// EnvelopeInput is an already-verified later envelope association.
type EnvelopeInput struct {
	CapsuleID CapsuleID
	Envelope  Envelope
}

// Normalized returns an append with portable persisted timestamp precision.
func (in AppendInput) Normalized() AppendInput {
	in.AppendedAt = NormalizeTime(in.AppendedAt)
	if len(in.Envelopes) > 0 {
		in.Envelopes = append([]Envelope(nil), in.Envelopes...)
		for index := range in.Envelopes {
			in.Envelopes[index].AddedAt = NormalizeTime(in.Envelopes[index].AddedAt)
		}
	}
	return in
}

// Normalized returns an envelope association with portable timestamp precision.
func (in EnvelopeInput) Normalized() EnvelopeInput {
	in.Envelope.AddedAt = NormalizeTime(in.Envelope.AddedAt)
	return in
}

// Validate checks a verified append before a backend allocates a sequence.
func (in AppendInput) Validate() error {
	if !validID(in.CapsuleID) || len(in.Capsule) == 0 || len(in.Capsule) > MaxCapsuleBytes || in.AppendedAt.IsZero() {
		return fmt.Errorf("%w: invalid append identity, size, or timestamp", ErrInvalid)
	}
	if in.ParentID != "" && !validID(in.ParentID) {
		return fmt.Errorf("%w: invalid parent capsule id", ErrInvalid)
	}
	if in.Verification.CapsuleID != "" && in.Verification.CapsuleID != in.CapsuleID {
		return fmt.Errorf("%w: verification capsule id mismatch", ErrInvalid)
	}
	seen := make(map[EnvelopeDigest]struct{}, len(in.Envelopes))
	if len(in.Envelopes) > MaxEnvelopesPerCapsule {
		return fmt.Errorf("%w: too many initial envelopes", ErrInvalid)
	}
	for _, envelope := range in.Envelopes {
		if err := envelope.validate(); err != nil {
			return err
		}
		if _, exists := seen[envelope.Digest]; exists {
			return fmt.Errorf("%w: duplicate initial envelope", ErrInvalid)
		}
		seen[envelope.Digest] = struct{}{}
	}
	return nil
}

// Validate checks a later envelope association before durable storage.
func (in EnvelopeInput) Validate() error {
	if !validID(in.CapsuleID) {
		return fmt.Errorf("%w: invalid capsule id", ErrInvalid)
	}
	return in.Envelope.validate()
}

func (e Envelope) validate() error {
	if len(e.Bytes) == 0 || len(e.Bytes) > MaxEnvelopeBytes || e.AddedAt.IsZero() || e.Digest != digestEnvelope(e.Bytes) {
		return fmt.Errorf("%w: invalid envelope digest, size, or timestamp", ErrInvalid)
	}
	return nil
}
