package capsuleanchor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/veraison/go-cose"
)

// DefaultMaxResponseBytes bounds an anchor response to one MiB.
const DefaultMaxResponseBytes = 1 << 20

// DefaultRequestTimeout prevents one unavailable witness from hanging its runner.
const DefaultRequestTimeout = 30 * time.Second

// MaxSignedStatementBytes bounds checkpoint parsing and submission.
const MaxSignedStatementBytes = 64 << 10

const (
	// EntryHashSchemeSigStructure is the current malleability-resistant anchor
	// contract that commits the COSE Sig_structure.
	EntryHashSchemeSigStructure = "sig_structure"
	// EntryHashSchemeLegacy is capsule-anchor's explicit migration response for
	// a statement found under its former exact-COSE-bytes entry hash.
	EntryHashSchemeLegacy = "legacy"
	// EntryHashSchemeStatementBytes is the deployed hosted-anchor compatibility
	// profile used locally when the older wire response omits a scheme.
	EntryHashSchemeStatementBytes = "statement_bytes"
)

// CheckpointWitness is capsule-anchor's echoed continuity result.
type CheckpointWitness struct {
	LogID     string `json:"log_id"`
	KeyID     string `json:"key_id"`
	MMRRoot   string `json:"mmr_root"`
	MMRSize   uint64 `json:"mmr_size"`
	PrevSize  uint64 `json:"prev_size"`
	Timestamp string `json:"timestamp"`
	Status    string `json:"status"`
}

// Receipt contains the bounded anchor response needed for offline verification.
type Receipt struct {
	Bytes           []byte
	EntryHash       string
	EntryHashScheme string
	LeafIndex       int64
	TreeSize        int64
	// Checkpoint is zero only for the omitted-field hosted compatibility
	// profile, where the receipt proves inclusion but makes no continuity claim.
	Checkpoint CheckpointWitness
}

// HTTPError preserves the status used for retry classification.
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("anchor returned HTTP %d: %s", e.StatusCode, e.Body)
}
func (e *HTTPError) Retryable() bool {
	return e.StatusCode == http.StatusRequestTimeout || e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}
func (e *HTTPError) ContinuityConflict() bool { return e.StatusCode == http.StatusConflict }

// Client submits signed checkpoints to capsule-anchor.
type Client struct {
	endpoint string
	http     *http.Client
	maxBytes int64
}

// NewClient constructs a redirect-rejecting, response-bounded client.
func NewClient(baseURL string, client *http.Client, maxResponseBytes int64) (*Client, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("base URL must be an HTTP(S) origin or path")
	}
	if maxResponseBytes == 0 {
		maxResponseBytes = DefaultMaxResponseBytes
	}
	if maxResponseBytes < 1 {
		return nil, fmt.Errorf("max response bytes must be positive")
	}
	if client == nil {
		client = http.DefaultClient
	}
	copyClient := *client
	if copyClient.Timeout == 0 {
		copyClient.Timeout = DefaultRequestTimeout
	}
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{endpoint: strings.TrimRight(baseURL, "/") + "/transparency/register-statement", http: &copyClient, maxBytes: maxResponseBytes}, nil
}

func (c *Client) Submit(ctx context.Context, signedStatement []byte) (Receipt, error) {
	if len(signedStatement) == 0 || len(signedStatement) > MaxSignedStatementBytes {
		return Receipt{}, fmt.Errorf("signed statement size is invalid")
	}
	claims, err := decodeCheckpointClaims(signedStatement)
	if err != nil {
		return Receipt{}, err
	}
	body, err := json.Marshal(map[string]string{"signed_statement_b64": base64.StdEncoding.EncodeToString(signedStatement)})
	if err != nil {
		return Receipt{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return Receipt{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return Receipt{}, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, c.maxBytes+1))
	if err != nil {
		return Receipt{}, err
	}
	if int64(len(raw)) > c.maxBytes {
		return Receipt{}, fmt.Errorf("anchor response exceeds %d bytes", c.maxBytes)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Receipt{}, &HTTPError{StatusCode: response.StatusCode, Body: boundedText(strings.TrimSpace(string(raw)))}
	}
	var wire struct {
		ReceiptB64      string             `json:"receipt_b64"`
		EntryHash       string             `json:"entry_hash"`
		EntryHashScheme string             `json:"entry_hash_scheme"`
		LeafIndex       int64              `json:"leaf_index"`
		TreeSize        int64              `json:"tree_size"`
		Checkpoint      *CheckpointWitness `json:"checkpoint_witness"`
	}
	// The anchor response is versioned additively. Validate every field this
	// client consumes while allowing new, bounded fields from newer servers.
	if err := json.Unmarshal(raw, &wire); err != nil {
		return Receipt{}, fmt.Errorf("decode anchor response: %w", err)
	}
	scheme := wire.EntryHashScheme
	checkpointWitness := CheckpointWitness{}
	switch scheme {
	case EntryHashSchemeSigStructure, EntryHashSchemeLegacy:
		if wire.Checkpoint == nil {
			return Receipt{}, fmt.Errorf("checkpoint witness response is required")
		}
		checkpointWitness = *wire.Checkpoint
	case "":
		// The currently deployed hosted service predates the explicit scheme and
		// checkpoint-continuity echo. Its documented entry hash is SHA-256 over
		// the exact statement bytes. Preserve that as an explicit local scheme so
		// receipt verification cannot silently apply the newer hash rule.
		if wire.Checkpoint != nil {
			return Receipt{}, fmt.Errorf("omitted-scheme response must not include checkpoint witness")
		}
		scheme = EntryHashSchemeStatementBytes
	default:
		return Receipt{}, fmt.Errorf("unsupported entry hash scheme %q", wire.EntryHashScheme)
	}
	entryHash, err := hex.DecodeString(wire.EntryHash)
	if err != nil || len(entryHash) != 32 || hex.EncodeToString(entryHash) != wire.EntryHash {
		return Receipt{}, fmt.Errorf("invalid entry hash")
	}
	if wire.TreeSize <= 0 || wire.LeafIndex < 0 || wire.LeafIndex >= wire.TreeSize {
		return Receipt{}, fmt.Errorf("invalid transparency position")
	}
	if wire.Checkpoint != nil {
		if checkpointWitness.Status != "first-seen" && checkpointWitness.Status != "witnessed" && checkpointWitness.Status != "already-registered" {
			return Receipt{}, fmt.Errorf("invalid checkpoint witness status")
		}
		if checkpointWitness.LogID != claims.LogID || checkpointWitness.KeyID != claims.KeyID ||
			checkpointWitness.MMRRoot != claims.MMRRoot || checkpointWitness.MMRSize != claims.MMRSize ||
			checkpointWitness.PrevSize != claims.PrevSize || checkpointWitness.Timestamp != claims.Timestamp {
			return Receipt{}, fmt.Errorf("checkpoint witness echo mismatch")
		}
	}
	receiptBytes, err := base64.StdEncoding.Strict().DecodeString(wire.ReceiptB64)
	if err != nil || len(receiptBytes) == 0 {
		return Receipt{}, fmt.Errorf("invalid receipt base64")
	}
	return Receipt{Bytes: receiptBytes, EntryHash: wire.EntryHash, EntryHashScheme: scheme, LeafIndex: wire.LeafIndex, TreeSize: wire.TreeSize, Checkpoint: checkpointWitness}, nil
}

func boundedText(value string) string {
	if len(value) <= 4096 {
		return value
	}
	return value[:4096]
}

func decodeCheckpointClaims(statement []byte) (CheckpointWitness, error) {
	var message cose.Sign1Message
	if err := message.UnmarshalCBOR(statement); err != nil {
		return CheckpointWitness{}, fmt.Errorf("decode checkpoint statement: %w", err)
	}
	if message.Payload == nil {
		return CheckpointWitness{}, fmt.Errorf("checkpoint statement payload must be embedded")
	}
	var claims struct {
		ArtifactType string `json:"artifact_type"`
		LogID        string `json:"log_id"`
		KeyID        string `json:"key_id"`
		MMRRoot      string `json:"mmr_root"`
		MMRSize      uint64 `json:"mmr_size"`
		PrevSize     uint64 `json:"prev_size"`
		Timestamp    string `json:"timestamp"`
		Root         string `json:"root"`
	}
	if err := json.Unmarshal(message.Payload, &claims); err != nil {
		return CheckpointWitness{}, fmt.Errorf("decode checkpoint payload: %w", err)
	}
	if claims.ArtifactType != "mmr-checkpoint" || claims.LogID == "" || claims.KeyID == "" ||
		claims.MMRRoot == "" || claims.MMRRoot != claims.Root || claims.MMRSize == 0 || claims.Timestamp == "" {
		return CheckpointWitness{}, fmt.Errorf("invalid checkpoint payload")
	}
	return CheckpointWitness{LogID: claims.LogID, KeyID: claims.KeyID, MMRRoot: claims.MMRRoot, MMRSize: claims.MMRSize, PrevSize: claims.PrevSize, Timestamp: claims.Timestamp}, nil
}

// IsRetryable reports timeout, 408, 429, and 5xx failures.
func IsRetryable(err error) bool {
	var target *HTTPError
	if errors.As(err, &target) && target.Retryable() {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

// IsContinuityConflict reports the terminal checkpoint HTTP 409 response.
func IsContinuityConflict(err error) bool {
	var target *HTTPError
	return errors.As(err, &target) && target.ContinuityConflict()
}
