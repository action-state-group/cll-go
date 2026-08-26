package checkpoint

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/action-state-group/agent-action-capsule/go/canonical"
	"github.com/ethanyzhang/capsule-ledger-go/ledger"
	"github.com/veraison/go-cose"
)

const maxPayloadBytes = 64 << 10

// Payload is the anchor-compatible canonical checkpoint claim set.
type Payload struct {
	LogID     string
	KeyID     string
	MMRSize   uint64
	Root      string
	PrevSize  uint64
	PrevRoot  string
	Timestamp time.Time
}

// ParsePayload accepts only this profile's exact canonical JSON projection.
func ParsePayload(data []byte) (Payload, error) {
	if len(data) == 0 || len(data) > maxPayloadBytes {
		return Payload{}, fmt.Errorf("checkpoint payload size is invalid")
	}
	var wire struct {
		ArtifactType string `json:"artifact_type"`
		KeyID        string `json:"key_id"`
		Kind         string `json:"kind"`
		LogID        string `json:"log_id"`
		MMRRoot      string `json:"mmr_root"`
		MMRSize      uint64 `json:"mmr_size"`
		PrevRoot     string `json:"prev_root"`
		PrevSize     uint64 `json:"prev_size"`
		Root         string `json:"root"`
		Timestamp    string `json:"timestamp"`
		Version      uint64 `json:"v"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return Payload{}, fmt.Errorf("decode checkpoint payload: %w", err)
	}
	if wire.ArtifactType != "mmr-checkpoint" || wire.Kind != "mmr_checkpoint" || wire.Version != 1 || wire.Root != wire.MMRRoot {
		return Payload{}, fmt.Errorf("checkpoint profile fields are invalid")
	}
	timestamp, err := time.Parse(time.RFC3339Nano, wire.Timestamp)
	if err != nil {
		return Payload{}, fmt.Errorf("checkpoint timestamp is invalid: %w", err)
	}
	payload := Payload{LogID: wire.LogID, KeyID: wire.KeyID, MMRSize: wire.MMRSize, Root: wire.Root, PrevSize: wire.PrevSize, PrevRoot: wire.PrevRoot, Timestamp: timestamp}
	canonical, err := payload.CanonicalJSON()
	if err != nil || !bytes.Equal(canonical, data) {
		return Payload{}, fmt.Errorf("checkpoint payload is not canonical")
	}
	return payload, nil
}

func (p Payload) CanonicalJSON() ([]byte, error) {
	if ledger.ValidateIdentifier(p.LogID) != nil || ledger.ValidateIdentifier(p.KeyID) != nil || p.MMRSize == 0 || len(p.Root) != 64 || p.Timestamp.IsZero() {
		return nil, fmt.Errorf("invalid checkpoint payload")
	}
	if p.PrevSize >= p.MMRSize || (p.PrevSize == 0) != (p.PrevRoot == "") {
		return nil, fmt.Errorf("invalid checkpoint predecessor")
	}
	if _, err := hex.DecodeString(p.Root); err != nil || p.Root != string(bytes.ToLower([]byte(p.Root))) {
		return nil, fmt.Errorf("invalid checkpoint root")
	}
	if p.PrevRoot != "" {
		if decoded, err := hex.DecodeString(p.PrevRoot); err != nil || len(decoded) != 32 || p.PrevRoot != string(bytes.ToLower([]byte(p.PrevRoot))) {
			return nil, fmt.Errorf("invalid previous checkpoint root")
		}
	}
	return canonical.JCS(map[string]any{
		"artifact_type": "mmr-checkpoint",
		"key_id":        p.KeyID,
		"kind":          "mmr_checkpoint",
		"log_id":        p.LogID,
		"mmr_root":      p.Root,
		"mmr_size":      json.Number(strconv.FormatUint(p.MMRSize, 10)),
		"prev_root":     p.PrevRoot,
		"prev_size":     json.Number(strconv.FormatUint(p.PrevSize, 10)),
		"root":          p.Root,
		"timestamp":     p.Timestamp.UTC().Format(time.RFC3339Nano),
		"v":             json.Number("1"),
	})
}

// Ed25519Signer emits tagged COSE_Sign1 checkpoint statements.
type Ed25519Signer struct {
	keyID   string
	private ed25519.PrivateKey
	public  ed25519.PublicKey
}

func NewEd25519Signer(keyID string, private ed25519.PrivateKey) (*Ed25519Signer, error) {
	if ledger.ValidateIdentifier(keyID) != nil || len(private) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("key id and Ed25519 private key are required")
	}
	public, ok := private.Public().(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("derive Ed25519 public key")
	}
	return &Ed25519Signer{keyID: keyID, private: append(ed25519.PrivateKey(nil), private...), public: append(ed25519.PublicKey(nil), public...)}, nil
}

func (s *Ed25519Signer) KeyID() string { return s.keyID }

func (s *Ed25519Signer) SignCheckpoint(ctx context.Context, payload []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	signer, err := cose.NewSigner(cose.AlgorithmEdDSA, s.private)
	if err != nil {
		return nil, fmt.Errorf("create COSE signer: %w", err)
	}
	message := cose.NewSign1Message()
	message.Payload = append([]byte(nil), payload...)
	message.Headers.Protected.SetAlgorithm(cose.AlgorithmEdDSA)
	if err := message.Sign(rand.Reader, nil, signer); err != nil {
		return nil, fmt.Errorf("sign checkpoint: %w", err)
	}
	encoded, err := message.MarshalCBOR()
	if err != nil {
		return nil, fmt.Errorf("encode checkpoint COSE_Sign1: %w", err)
	}
	if err := s.VerifyCheckpoint(payload, encoded); err != nil {
		return nil, fmt.Errorf("verify newly signed checkpoint: %w", err)
	}
	return encoded, nil
}

func (s *Ed25519Signer) VerifyCheckpoint(payload, statement []byte) error {
	var message cose.Sign1Message
	if err := message.UnmarshalCBOR(statement); err != nil {
		return fmt.Errorf("decode checkpoint COSE_Sign1: %w", err)
	}
	if !bytes.Equal(message.Payload, payload) {
		return fmt.Errorf("checkpoint payload mismatch")
	}
	if len(message.Headers.Unprotected) != 0 {
		return fmt.Errorf("checkpoint unprotected headers must be empty")
	}
	algorithm, err := message.Headers.Protected.Algorithm()
	if err != nil || algorithm != cose.AlgorithmEdDSA || len(message.Headers.Protected) != 1 {
		return fmt.Errorf("checkpoint protected headers must contain only EdDSA")
	}
	verifier, err := cose.NewVerifier(cose.AlgorithmEdDSA, s.public)
	if err != nil {
		return fmt.Errorf("create COSE verifier: %w", err)
	}
	if err := message.Verify(nil, verifier); err != nil {
		return fmt.Errorf("verify checkpoint signature: %w", err)
	}
	return nil
}

// Config defines entry, age, and overdue checkpoint thresholds.
type Config struct {
	CadenceEntries uint64
	CadenceAge     time.Duration
	MaxLagEntries  uint64
}

// DefaultConfig returns 100 entries, 15 minutes, and over-200 overdue.
func DefaultConfig() Config {
	return Config{CadenceEntries: 100, CadenceAge: 15 * time.Minute, MaxLagEntries: 200}
}

func (c Config) Validate() error {
	if c.CadenceEntries == 0 || c.CadenceAge <= 0 || c.MaxLagEntries < c.CadenceEntries {
		return fmt.Errorf("invalid checkpoint cadence")
	}
	return nil
}

func (c Config) Due(entries uint64, firstUncheckpointed, now time.Time) bool {
	if entries == 0 {
		return false
	}
	return entries >= c.CadenceEntries || !firstUncheckpointed.IsZero() && now.Sub(firstUncheckpointed) >= c.CadenceAge
}

func (c Config) Overdue(entries uint64) bool { return entries > c.MaxLagEntries }
