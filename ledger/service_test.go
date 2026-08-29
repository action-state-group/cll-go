package ledger_test

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/action-state-group/agent-action-capsule/go/canonical"
	"github.com/ethanyzhang/capsule-ledger-go/ledger"
	"github.com/ethanyzhang/capsule-ledger-go/store/jsonl"
	"github.com/stretchr/testify/require"
)

func TestServiceOwnsAACVerificationAndCopiesRegistryExtensions(t *testing.T) {
	const relation = "urn:alchemy:chain:investigation-followup:v1"
	store, err := jsonl.Open(t.TempDir(), "service-test")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	extensions := map[string]map[string]bool{
		"chain.relation": {relation: true},
	}
	service, err := ledger.New(store, ledger.Config{
		RegistryExtensions: extensions,
		Clock:              func() time.Time { return time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, err)

	// Configuration belongs to the constructed service. Later host mutation
	// must not change verification behavior or introduce a data race.
	delete(extensions["chain.relation"], relation)
	capsule := capsuleWithRelation(t, relation)
	record, err := service.Append(t.Context(), ledger.AdmissionUnsigned, capsule)
	require.NoError(t, err)
	require.NotContains(t, findingCodes(record.Verification.Findings), "unknown_registry_value")

	audit, err := service.Audit(t.Context(), 10)
	require.NoError(t, err)
	require.Len(t, audit, 1)
	// The frozen fixture intentionally points to a parent outside this one-row
	// store, so store-level audit reports that separate chain failure.
	require.False(t, audit[0].Result.OK)
	require.Contains(t, findingCodes(audit[0].Result.Findings), "chain_parent_missing")
	require.NotContains(t, findingCodes(audit[0].Result.Findings), "unknown_registry_value")

	var tampered map[string]any
	require.NoError(t, json.Unmarshal(capsule, &tampered))
	tampered["capsule_id"] = "0000000000000000000000000000000000000000000000000000000000000000"
	raw, err := json.Marshal(tampered)
	require.NoError(t, err)
	_, err = service.Append(t.Context(), ledger.AdmissionUnsigned, raw)
	require.ErrorIs(t, err, ledger.ErrInvalid)
}

func TestServiceDefaultConfigUsesEmbeddedRegistry(t *testing.T) {
	store, err := jsonl.Open(t.TempDir(), "default-service-test")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	service, err := ledger.New(store, ledger.Config{})
	require.NoError(t, err)
	raw, err := os.ReadFile("testdata/valid-v4.json")
	require.NoError(t, err)
	record, err := service.Append(t.Context(), ledger.AdmissionUnsigned, raw)
	require.NoError(t, err)
	require.True(t, record.Verification.OK)
	require.NotContains(t, findingCodes(record.Verification.Findings), "unknown_registry_value")
}

func TestServiceRequiresDeclaredModeAndDoesNotInferFromEnvelopePresence(t *testing.T) {
	store, err := jsonl.Open(t.TempDir(), "explicit-admission-test")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	service, err := ledger.New(store, ledger.Config{})
	require.NoError(t, err)
	raw, err := os.ReadFile("testdata/valid-v4.json")
	require.NoError(t, err)

	_, err = service.Append(t.Context(), "", raw)
	require.ErrorIs(t, err, ledger.ErrInvalid)
	_, err = service.Append(t.Context(), ledger.AdmissionUnsigned, raw, []byte("envelope-presence-must-not-select-signed"))
	require.ErrorIs(t, err, ledger.ErrAdmission)
	records, err := store.ScanIDs(t.Context(), 0, 1)
	require.NoError(t, err)
	require.Empty(t, records)
}

func TestAuditBounds(t *testing.T) {
	store, err := jsonl.Open(t.TempDir(), "audit-bound-test")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	service, err := ledger.New(store, ledger.Config{})
	require.NoError(t, err)
	first := mutatedCapsule(t, func(map[string]any) {})
	_, err = service.Append(t.Context(), ledger.AdmissionUnsigned, first)
	require.NoError(t, err)

	// The largest documented bound is valid even though Store.Scan itself is
	// bounded by the same constant.
	audit, err := service.Audit(t.Context(), ledger.MaxScanLimit)
	require.NoError(t, err)
	require.Len(t, audit, 1)

	second := mutatedCapsule(t, func(capsule map[string]any) {
		capsule["action_id"] = "v4-chain-second"
	})
	_, err = service.Append(t.Context(), ledger.AdmissionUnsigned, second)
	require.NoError(t, err)
	audit, err = service.Audit(t.Context(), 2)
	require.NoError(t, err)
	require.Len(t, audit, 2)
	_, err = service.Audit(t.Context(), 1)
	require.ErrorIs(t, err, ledger.ErrInvalid)
}

func TestNewRequiresStore(t *testing.T) {
	_, err := ledger.New(nil, ledger.Config{})
	require.ErrorIs(t, err, ledger.ErrInvalid)
}

func capsuleWithRelation(t *testing.T, relation string) []byte {
	return mutatedCapsule(t, func(capsule map[string]any) {
		chain, ok := capsule["chain"].(map[string]any)
		require.True(t, ok)
		chain["relation"] = relation
	})
}

func mutatedCapsule(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/valid-v4.json")
	require.NoError(t, err)
	var capsule map[string]any
	require.NoError(t, json.Unmarshal(raw, &capsule))
	mutate(capsule)
	id, err := canonical.ComputeCapsuleID(capsule)
	require.NoError(t, err)
	capsule["capsule_id"] = id
	encoded, err := json.Marshal(capsule)
	require.NoError(t, err)
	return encoded
}

func findingCodes(findings []ledger.Finding) []string {
	codes := make([]string, 0, len(findings))
	for _, finding := range findings {
		codes = append(codes, finding.Code)
	}
	return codes
}
