package checkpoint

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/action-state-group/agent-action-capsule/go/canonical"
	"github.com/ethanyzhang/capsule-ledger-go/ledger"
)

// MaxEncodedBytes bounds canonical checkpoint signing bodies and signed records.
const MaxEncodedBytes = 64 << 10

// Payload is the canonical checkpoint signing body shared with capsule-emit.
type Payload struct {
	LogID     string
	KeyID     string
	MMRSize   uint64
	Root      string
	PrevSize  uint64
	PrevRoot  string
	Timestamp time.Time
}

// recordWire is the single JSON projection shared by signing-body and signed
// checkpoint parsing. Signature is empty for the signing body.
type recordWire struct {
	Version   uint64 `json:"v"`
	Kind      string `json:"kind"`
	LogID     string `json:"log_id"`
	MMRSize   uint64 `json:"mmr_size"`
	Root      string `json:"root"`
	PrevSize  uint64 `json:"prev_size"`
	PrevRoot  string `json:"prev_root"`
	KeyID     string `json:"key_id"`
	Timestamp string `json:"timestamp"`
	Signature string `json:"signature,omitempty"`
}

// ParsePayload accepts only this profile's exact canonical JSON projection.
func ParsePayload(data []byte) (Payload, error) {
	if len(data) == 0 || len(data) > MaxEncodedBytes {
		return Payload{}, fmt.Errorf("checkpoint payload size is invalid")
	}
	var wire recordWire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return Payload{}, fmt.Errorf("decode checkpoint payload: %w", err)
	}
	if wire.Kind != "mmr_checkpoint" || wire.Version != 1 {
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
	fields, err := p.canonicalFields()
	if err != nil {
		return nil, err
	}
	return canonical.JCS(fields)
}

func (p Payload) canonicalFields() (map[string]any, error) {
	key, keyErr := hex.DecodeString(p.KeyID)
	if ledger.ValidateIdentifier(p.LogID) != nil || keyErr != nil || len(key) != ed25519.PublicKeySize || hex.EncodeToString(key) != p.KeyID || p.MMRSize == 0 || len(p.Root) != 64 || p.Timestamp.IsZero() {
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
	return map[string]any{
		"key_id":    p.KeyID,
		"kind":      "mmr_checkpoint",
		"log_id":    p.LogID,
		"mmr_size":  json.Number(strconv.FormatUint(p.MMRSize, 10)),
		"prev_root": p.PrevRoot,
		"prev_size": json.Number(strconv.FormatUint(p.PrevSize, 10)),
		"root":      p.Root,
		"timestamp": p.Timestamp.UTC().Format(time.RFC3339Nano),
		"v":         json.Number("1"),
	}, nil
}

// Digest returns the SHA-256 digest of the canonical signing body.
func (p Payload) Digest() ([sha256.Size]byte, error) {
	body, err := p.CanonicalJSON()
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(body), nil
}

// DigestHex returns Digest as lowercase hexadecimal text.
func (p Payload) DigestHex() (string, error) {
	digest, err := p.Digest()
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(digest[:]), nil
}

// Record is the exact signed CheckpointRecord accepted by POST /v1/checkpoint.
type Record struct {
	Version   uint64    `json:"v"`
	Kind      string    `json:"kind"`
	LogID     string    `json:"log_id"`
	MMRSize   uint64    `json:"mmr_size"`
	Root      string    `json:"root"`
	PrevSize  uint64    `json:"prev_size"`
	PrevRoot  string    `json:"prev_root"`
	KeyID     string    `json:"key_id"`
	Timestamp time.Time `json:"timestamp"`
	Signature string    `json:"signature"`
}

// Payload returns the signature-free projection covered by Record.Signature.
func (r Record) Payload() Payload {
	return Payload{LogID: r.LogID, KeyID: r.KeyID, MMRSize: r.MMRSize, Root: r.Root, PrevSize: r.PrevSize, PrevRoot: r.PrevRoot, Timestamp: r.Timestamp}
}

// CanonicalJSON returns the exact checkpoint-only witness request body.
func (r Record) CanonicalJSON() ([]byte, error) {
	if r.Version != 1 || r.Kind != "mmr_checkpoint" {
		return nil, fmt.Errorf("checkpoint profile fields are invalid")
	}
	fields, err := r.Payload().canonicalFields()
	if err != nil {
		return nil, err
	}
	signature, err := hex.DecodeString(r.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || hex.EncodeToString(signature) != r.Signature {
		return nil, fmt.Errorf("invalid checkpoint signature")
	}
	fields["signature"] = r.Signature
	return canonical.JCS(fields)
}

// ParseRecord accepts only this profile's exact canonical signed JSON.
func ParseRecord(data []byte) (Record, error) {
	if len(data) == 0 || len(data) > MaxEncodedBytes {
		return Record{}, fmt.Errorf("signed checkpoint size is invalid")
	}
	var wire recordWire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return Record{}, fmt.Errorf("decode signed checkpoint: %w", err)
	}
	timestamp, err := time.Parse(time.RFC3339Nano, wire.Timestamp)
	if err != nil {
		return Record{}, fmt.Errorf("checkpoint timestamp is invalid: %w", err)
	}
	record := Record{Version: wire.Version, Kind: wire.Kind, LogID: wire.LogID, MMRSize: wire.MMRSize, Root: wire.Root, PrevSize: wire.PrevSize, PrevRoot: wire.PrevRoot, KeyID: wire.KeyID, Timestamp: timestamp, Signature: wire.Signature}
	canonical, err := record.CanonicalJSON()
	if err != nil || !bytes.Equal(canonical, data) {
		return Record{}, fmt.Errorf("signed checkpoint is not canonical")
	}
	return record, nil
}

// EntryHash returns the CT-log entry hash used by the checkpoint-only witness.
func (r Record) EntryHash() ([]byte, error) {
	digest, err := r.Payload().Digest()
	if err != nil {
		return nil, err
	}
	entry := sha256.Sum256(digest[:])
	return entry[:], nil
}

// VerifySignature checks the self-contained Ed25519 checkpoint signature.
func (r Record) VerifySignature() error {
	public, err := hex.DecodeString(r.KeyID)
	if err != nil || len(public) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid checkpoint public key")
	}
	signature, err := hex.DecodeString(r.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("invalid checkpoint signature")
	}
	digestHex, err := r.Payload().DigestHex()
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(public), []byte(digestHex), signature) {
		return fmt.Errorf("verify checkpoint signature")
	}
	return nil
}

// Ed25519Signer emits checkpoint records whose key_id is the raw public key.
type Ed25519Signer struct {
	private ed25519.PrivateKey
	public  ed25519.PublicKey
}

func NewEd25519Signer(private ed25519.PrivateKey) (*Ed25519Signer, error) {
	if len(private) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("Ed25519 private key is required")
	}
	public, ok := private.Public().(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("derive Ed25519 public key")
	}
	return &Ed25519Signer{private: append(ed25519.PrivateKey(nil), private...), public: append(ed25519.PublicKey(nil), public...)}, nil
}

func (s *Ed25519Signer) KeyID() string { return hex.EncodeToString(s.public) }

func (s *Ed25519Signer) SignCheckpoint(ctx context.Context, payload []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parsed, err := ParsePayload(payload)
	if err != nil || parsed.KeyID != s.KeyID() {
		return nil, fmt.Errorf("checkpoint payload key does not match signer")
	}
	digestHex, err := parsed.DigestHex()
	if err != nil {
		return nil, err
	}
	record := Record{Version: 1, Kind: "mmr_checkpoint", LogID: parsed.LogID, MMRSize: parsed.MMRSize, Root: parsed.Root, PrevSize: parsed.PrevSize, PrevRoot: parsed.PrevRoot, KeyID: parsed.KeyID, Timestamp: parsed.Timestamp, Signature: hex.EncodeToString(ed25519.Sign(s.private, []byte(digestHex)))}
	encoded, err := record.CanonicalJSON()
	if err != nil {
		return nil, fmt.Errorf("encode signed checkpoint: %w", err)
	}
	if err := s.VerifyCheckpoint(payload, encoded); err != nil {
		return nil, fmt.Errorf("verify newly signed checkpoint: %w", err)
	}
	return encoded, nil
}

func (s *Ed25519Signer) VerifyCheckpoint(payload, statement []byte) error {
	parsedPayload, err := ParsePayload(payload)
	if err != nil {
		return err
	}
	record, err := ParseRecord(statement)
	if err != nil {
		return err
	}
	if record.Payload() != parsedPayload || record.KeyID != s.KeyID() {
		return fmt.Errorf("checkpoint payload mismatch")
	}
	return record.VerifySignature()
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
