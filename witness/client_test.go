package witness

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/action-state-group/cll-go/checkpoint"
	"github.com/stretchr/testify/require"
)

func TestClientUsesCheckpointOnlyEndpoint(t *testing.T) {
	statement := checkpointStatement(t)
	record, err := checkpoint.ParseRecord(statement)
	require.NoError(t, err)
	entry, err := record.EntryHash()
	require.NoError(t, err)
	receiptB64 := base64.StdEncoding.EncodeToString([]byte("receipt"))
	entryHash := fmt.Sprintf("%x", entry)
	tests := []struct {
		name      string
		response  string
		wantError string
	}{
		{name: "current", response: fmt.Sprintf(`{"receipt_b64":%q,"entry_hash":%q,"entry_hash_scheme":"legacy","leaf_index":0,"tree_size":1,"future_field":"allowed"}`, receiptB64, entryHash)},
		{name: "missing scheme", response: fmt.Sprintf(`{"receipt_b64":%q,"entry_hash":%q,"leaf_index":0,"tree_size":1}`, receiptB64, entryHash), wantError: "unsupported entry hash scheme"},
		{name: "old statement scheme", response: fmt.Sprintf(`{"receipt_b64":%q,"entry_hash":%q,"entry_hash_scheme":"sig_structure","leaf_index":0,"tree_size":1}`, receiptB64, entryHash), wantError: "unsupported entry hash scheme"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				require.Equal(t, "/checkpoints", request.URL.Path)
				require.Equal(t, http.MethodPost, request.Method)
				require.Equal(t, checkpoint.ContentType, request.Header.Get("Content-Type"))
				require.Equal(t, "application/json", request.Header.Get("Accept"))
				body, err := io.ReadAll(request.Body)
				require.NoError(t, err)
				require.True(t, bytes.Equal(statement, body))
				writer.Header().Set("Content-Type", "application/json")
				_, err = writer.Write([]byte(test.response))
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
			require.Equal(t, EntryHashSchemeCheckpointDigest, receipt.EntryHashScheme)
		})
	}
}

func TestClientRejectsRedirectOversizeAndInvalidPosition(t *testing.T) {
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

	t.Run("invalid position", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, err := fmt.Fprintf(writer, `{"receipt_b64":%q,"entry_hash":%q,"entry_hash_scheme":"legacy","leaf_index":1,"tree_size":1}`, base64.StdEncoding.EncodeToString([]byte("receipt")), fmt.Sprintf("%064x", 1))
			require.NoError(t, err)
		}))
		t.Cleanup(server.Close)
		client, err := NewClient(server.URL, server.Client(), 4096)
		require.NoError(t, err)
		_, err = client.Submit(t.Context(), statement)
		require.ErrorContains(t, err, "invalid transparency position")
	})
}

func TestClientClassifiesRetryableFailures(t *testing.T) {
	statement := checkpointStatement(t)
	status := http.StatusConflict
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { http.Error(writer, "failure", status) }))
	t.Cleanup(server.Close)
	client, err := NewClient(server.URL, server.Client(), 4096)
	require.NoError(t, err)
	_, err = client.Submit(t.Context(), statement)
	require.False(t, IsRetryable(err))
	status = http.StatusTooManyRequests
	_, err = client.Submit(t.Context(), statement)
	require.True(t, IsRetryable(err))
}

func checkpointStatement(t *testing.T) []byte {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := checkpoint.NewEd25519Signer(private)
	require.NoError(t, err)
	root := bytes.Repeat([]byte{0x02}, 32)
	payload, err := (checkpoint.Payload{
		LogID: "log", KeyID: signer.KeyID(), MMRSize: 1, Root: fmt.Sprintf("%x", root),
		Timestamp: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
	}).CanonicalJSON()
	require.NoError(t, err)
	statement, err := signer.SignCheckpoint(t.Context(), payload, [][]byte{root}, nil, nil)
	require.NoError(t, err)
	return statement
}
