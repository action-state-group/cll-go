package capsuleanchor

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethanyzhang/capsule-ledger-go/checkpoint"
	"github.com/stretchr/testify/require"
)

func TestClientUsesCheckpointEndpointAndSupportsHostedCompatibility(t *testing.T) {
	statement := checkpointStatement(t)
	receiptB64 := base64.StdEncoding.EncodeToString([]byte("receipt"))
	entryHash := fmt.Sprintf("%064x", 1)
	root := fmt.Sprintf("%064x", 2)
	withWitness := func(scheme string) string {
		return fmt.Sprintf(`{"receipt_b64":%q,"entry_hash":%q,"entry_hash_scheme":%q,"leaf_index":0,"tree_size":1,"future_field":"allowed","checkpoint_witness":{"log_id":"log","key_id":"key","mmr_root":%q,"mmr_size":1,"prev_size":0,"timestamp":"2026-08-25T00:00:00Z","status":"first-seen"}}`, receiptB64, entryHash, scheme, root)
	}
	digest := sha256.Sum256(statement)
	tests := []struct {
		name       string
		response   string
		wantScheme string
		wantStatus string
		wantError  string
	}{
		{name: "current", response: withWitness(EntryHashSchemeSigStructure), wantScheme: EntryHashSchemeSigStructure, wantStatus: "first-seen"},
		{name: "current migration", response: withWitness(EntryHashSchemeLegacy), wantScheme: EntryHashSchemeLegacy, wantStatus: "first-seen"},
		{name: "older hosted", response: fmt.Sprintf(`{"receipt_b64":%q,"entry_hash":%q,"leaf_index":0,"tree_size":1}`, receiptB64, hex.EncodeToString(digest[:])), wantScheme: EntryHashSchemeStatementBytes},
		{name: "current without witness", response: fmt.Sprintf(`{"receipt_b64":%q,"entry_hash":%q,"entry_hash_scheme":"sig_structure","leaf_index":0,"tree_size":1}`, receiptB64, entryHash), wantError: "checkpoint witness response is required"},
		{name: "omitted scheme with witness", response: withWitness(""), wantError: "omitted-scheme response"},
		{name: "local scheme on wire", response: withWitness(EntryHashSchemeStatementBytes), wantError: "unsupported entry hash scheme"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				require.Equal(t, "/transparency/register-statement", request.URL.Path)
				writer.Header().Set("Content-Type", "application/json")
				_, err := writer.Write([]byte(test.response))
				require.NoError(t, err)
			}))
			t.Cleanup(server.Close)
			client, err := NewClient(server.URL, server.Client(), 4096)
			require.NoError(t, err)
			receipt, err := client.Submit(t.Context(), statement)
			if test.wantError != "" {
				require.ErrorContains(t, err, test.wantError)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.wantScheme, receipt.EntryHashScheme)
			require.Equal(t, test.wantStatus, receipt.Checkpoint.Status)
		})
	}
}

func TestClientRejectsRedirectOversizeAndEchoMismatch(t *testing.T) {
	statement := checkpointStatement(t)
	t.Run("redirect", func(t *testing.T) {
		followed := false
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/other" {
				followed = true
			}
			http.Redirect(writer, request, "/other", http.StatusFound)
		}))
		t.Cleanup(server.Close)
		client, err := NewClient(server.URL, server.Client(), 4096)
		require.NoError(t, err)
		_, err = client.Submit(t.Context(), statement)
		require.Error(t, err)
		require.False(t, followed)
	})

	t.Run("oversize", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, err := writer.Write(make([]byte, 65))
			require.NoError(t, err)
		}))
		t.Cleanup(server.Close)
		client, err := NewClient(server.URL, server.Client(), 64)
		require.NoError(t, err)
		_, err = client.Submit(t.Context(), statement)
		require.ErrorContains(t, err, "exceeds")
	})

	t.Run("echo mismatch", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, err := fmt.Fprintf(writer, `{"receipt_b64":%q,"entry_hash":%q,"entry_hash_scheme":"sig_structure","leaf_index":0,"tree_size":1,"checkpoint_witness":{"log_id":"another-log","key_id":"key","mmr_root":%q,"mmr_size":1,"prev_size":0,"timestamp":"2026-08-25T00:00:00Z","status":"first-seen"}}`, base64.StdEncoding.EncodeToString([]byte("receipt")), fmt.Sprintf("%064x", 1), fmt.Sprintf("%064x", 2))
			require.NoError(t, err)
		}))
		t.Cleanup(server.Close)
		client, err := NewClient(server.URL, server.Client(), 4096)
		require.NoError(t, err)
		_, err = client.Submit(t.Context(), statement)
		require.ErrorContains(t, err, "echo mismatch")
	})
}

func TestClientClassifiesContinuityAndRetryableFailures(t *testing.T) {
	statement := checkpointStatement(t)
	status := http.StatusConflict
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { http.Error(writer, "failure", status) }))
	t.Cleanup(server.Close)
	client, err := NewClient(server.URL, server.Client(), 4096)
	require.NoError(t, err)
	_, err = client.Submit(t.Context(), statement)
	require.True(t, IsContinuityConflict(err))
	status = http.StatusTooManyRequests
	_, err = client.Submit(t.Context(), statement)
	require.True(t, IsRetryable(err))
}

func checkpointStatement(t *testing.T) []byte {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := checkpoint.NewEd25519Signer("key", private)
	require.NoError(t, err)
	payload, err := (checkpoint.Payload{
		LogID: "log", KeyID: "key", MMRSize: 1, Root: fmt.Sprintf("%064x", 2),
		Timestamp: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
	}).CanonicalJSON()
	require.NoError(t, err)
	statement, err := signer.SignCheckpoint(t.Context(), payload)
	require.NoError(t, err)
	return statement
}
