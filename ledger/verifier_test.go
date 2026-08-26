package ledger

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/action-state-group/agent-action-capsule/go/registries"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedRegistryMatchesUpstreamLoader(t *testing.T) {
	loaded, err := registries.Load("testdata/REGISTRY.md")
	require.NoError(t, err)
	require.True(t, reflect.DeepEqual(baselineRegistries, loaded))
}

func TestAACVerifierUsesEmbeddedRegistryAndPreservesInformationalFindings(t *testing.T) {
	data, err := os.ReadFile("testdata/valid-v4.json")
	require.NoError(t, err)

	result, id, err := (AACVerifier{}).VerifyCapsule(data)
	require.NoError(t, err)
	require.True(t, result.OK)
	require.Equal(t, CapsuleID("862024869f00481bb4f59d9528a45c2d4885f64c5222a9324a38ac2c2cd119f2"), id)
	require.Contains(t, findingCodes(result.Findings), "chain_check_store_level")
}

func TestAACVerifierRejectsTamperedCapsule(t *testing.T) {
	data, err := os.ReadFile("testdata/valid-v4.json")
	require.NoError(t, err)
	tampered := append([]byte(nil), data...)
	for index := range tampered {
		if tampered[index] == 'A' {
			tampered[index] = 'B'
			break
		}
	}

	result, _, err := (AACVerifier{}).VerifyCapsule(tampered)
	require.ErrorIs(t, err, ErrInvalid)
	require.False(t, result.OK)
	require.Contains(t, findingCodes(result.Findings), "capsule_id_mismatch")
}

func TestAACVerifierFormat4CanonicalizationMatrix(t *testing.T) {
	data, err := os.ReadFile("testdata/valid-v4.json")
	require.NoError(t, err)
	tests := []struct {
		name, code string
		value      any
		remove     bool
	}{
		{"missing", "canonicalization_id_missing", nil, true},
		{"withdrawn", "canonicalization_profile_mismatch", "jcs-n", false},
		{"unknown", "canonicalization_profile_mismatch", "future", false},
		{"non-string", "canonicalization_id_not_string", json.Number("7"), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.UseNumber()
			var capsule map[string]any
			require.NoError(t, decoder.Decode(&capsule))
			if test.remove {
				delete(capsule, "canonicalization_id")
			} else {
				capsule["canonicalization_id"] = test.value
			}
			encoded, err := json.Marshal(capsule)
			require.NoError(t, err)
			result, _, err := (AACVerifier{}).VerifyCapsule(encoded)
			require.ErrorIs(t, err, ErrInvalid)
			require.Contains(t, findingCodes(result.Findings), test.code)
		})
	}
}

// Frozen semantic cases from agent-action-capsule/test-vectors at
// 7dcef86634355c0d3335b3050b1bc18845716275 (BSD-3-Clause).
func TestAACVerifierRejectsFloatAndUnsafeIntegerBeforeJCS(t *testing.T) {
	const prefix = `{"action_type":"decide","assurance":{"attestation_mode":"self_attested","effect_mode":"confirmed","ledger_mode":"standalone"},"capsule_id":"0000000000000000000000000000000000000000000000000000000000000000","developer":"agent@v1","disposition":{"approver":"human","decision":"accept","human_disposed":true},"effect":{"amount":`
	const suffix = `,"effect_attestation":"gate_executed","response_digest":"1111111111111111111111111111111111111111111111111111111111111111","status":"confirmed","type":"write_order"},"format_version":"2","operator":"ACME-CO","spec_version":"draft-mih-scitt-agent-action-capsule-00","timestamp":"2026-06-13T00:00:00Z"}`
	for name, value := range map[string]string{"float": "12.5", "unsafe-integer": "9007199254740992"} {
		t.Run(name, func(t *testing.T) {
			_, _, err := (AACVerifier{}).VerifyCapsule([]byte(prefix + value + suffix))
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
}

// Frozen from agent-action-capsule/producer-envelope-vectors at
// 7dcef86634355c0d3335b3050b1bc18845716275 (BSD-3-Clause).
func TestProducerEnvelopeConformanceVectors(t *testing.T) {
	vectors := []struct {
		name, capsuleID, envelope, code, publicKey string
		ok                                         bool
	}{
		{"valid", "202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f", "0oRYTKMDeCNhcHBsaWNhdGlvbi9hZ2VudC1hY3Rpb24tY2Fwc3VsZS1pZARYIAOhB7/zzhC+HXDdGOdLwJln5NYwm6UNXx3chmQSVTG4ASegWCAgISIjJCUmJygpKissLS4vMDEyMzQ1Njc4OTo7PD0+P1hAM/jDffzghW38REjR8GxlllwVcnImNBA1RNsRq+LUYRZ+wQHqK1tHA5WetpARDtkRHuApsSw0xaUwZ4XszqpDCQ==", "", "03a107bff3ce10be1d70dd18e74bc09967e4d6309ba50d5f1ddc8664125531b8", true},
		{"bad-signature", "202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f", "0oRYTKMDeCNhcHBsaWNhdGlvbi9hZ2VudC1hY3Rpb24tY2Fwc3VsZS1pZARYIAOhB7/zzhC+HXDdGOdLwJln5NYwm6UNXx3chmQSVTG4ASegWCAgISIjJCUmJygpKissLS4vMDEyMzQ1Njc4OTo7PD0+P1hAM/jDffzghW38REjR8GxlllwVcnImNBA1RNsRq+LUYRZ+wQHqK1tHA5WetpARDtkRHuApsSw0xaUwZ4XszqpDCA==", "envelope_signature_invalid", "", false},
		{"payload-mismatch", "404142434445464748494a4b4c4d4e4f505152535455565758595a5b5c5d5e5f", "0oRYTKMDeCNhcHBsaWNhdGlvbi9hZ2VudC1hY3Rpb24tY2Fwc3VsZS1pZARYIAOhB7/zzhC+HXDdGOdLwJln5NYwm6UNXx3chmQSVTG4ASegWCAgISIjJCUmJygpKissLS4vMDEyMzQ1Njc4OTo7PD0+P1hAM/jDffzghW38REjR8GxlllwVcnImNBA1RNsRq+LUYRZ+wQHqK1tHA5WetpARDtkRHuApsSw0xaUwZ4XszqpDCQ==", "envelope_payload_mismatch", "", false},
		{"wrong-algorithm", "202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f", "0oRYTKMBJgN4I2FwcGxpY2F0aW9uL2FnZW50LWFjdGlvbi1jYXBzdWxlLWlkBFggA6EHv/POEL4dcN0Y50vAmWfk1jCbpQ1fHdyGZBJVMbigWCAgISIjJCUmJygpKissLS4vMDEyMzQ1Njc4OTo7PD0+P1hAltX/Cu92lc3WKtGRNd5ZsP0g7BfKaY9kJHE8KEeCdG/WxDkblcdkINSku/Tnb+vYA+BmXFJVOLscuAPN4ownBw==", "envelope_algorithm_mismatch", "", false},
		{"wrong-content-type", "202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f", "0oRYQaMDeBhhcHBsaWNhdGlvbi9vY3RldC1zdHJlYW0EWCADoQe/884Qvh1w3RjnS8CZZ+TWMJulDV8d3IZkElUxuAEnoFggICEiIyQlJicoKSorLC0uLzAxMjM0NTY3ODk6Ozw9Pj9YQGt64Z9zlgJb0lQRXrP+/6R9EEA/4710UHAQTc3kJbDhhMH0a6bD3zSSsUHJWTdT1X7q/rq3/xDgAyMRgFrNjgU=", "envelope_content_type_mismatch", "", false},
		{"nonempty-unprotected", "202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f", "0oRYTKMDeCNhcHBsaWNhdGlvbi9hZ2VudC1hY3Rpb24tY2Fwc3VsZS1pZARYIAOhB7/zzhC+HXDdGOdLwJln5NYwm6UNXx3chmQSVTG4ASehCfVYICAhIiMkJSYnKCkqKywtLi8wMTIzNDU2Nzg5Ojs8PT4/WEAz+MN9/OCFbfxESNHwbGWWXBVyciY0EDVE2xGr4tRhFn7BAeorW0cDlZ62kBEO2REe4CmxLDTFpTBnhezOqkMJ", "envelope_malformed", "", false},
		{"extra-protected-header", "202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f", "0oRYVKQDeCNhcHBsaWNhdGlvbi9hZ2VudC1hY3Rpb24tY2Fwc3VsZS1pZARYIAOhB7/zzhC+HXDdGOdLwJln5NYwm6UNXx3chmQSVTG4GGNlZXh0cmEBJ6BYICAhIiMkJSYnKCkqKywtLi8wMTIzNDU2Nzg5Ojs8PT4/WECP8VuuJmwqX6waOETWhvRTQh55arFu3paC2T80K9B/Azd9sWEEc/7WxOQ00RnCFtym/OcMYUpoMPEjN3ez86EG", "envelope_protected_headers_invalid", "", false},
		{"invalid-kid", "202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f", "0oRYS6MDeCNhcHBsaWNhdGlvbi9hZ2VudC1hY3Rpb24tY2Fwc3VsZS1pZARYHwOhB7/zzhC+HXDdGOdLwJln5NYwm6UNXx3chmQSVTEBJ6BYICAhIiMkJSYnKCkqKywtLi8wMTIzNDU2Nzg5Ojs8PT4/WEDtMp+TWtboa9idtBYUeXvOg5mSz9UDcqHLFywSDJRMdSBRYo3XliotxR3wB/CejMr2bpeelmOPamn3h8QY9agH", "envelope_kid_invalid", "", false},
	}
	for _, vector := range vectors {
		t.Run(vector.name, func(t *testing.T) {
			raw, err := base64.StdEncoding.DecodeString(vector.envelope)
			require.NoError(t, err)
			result, verifyErr := (AACVerifier{}).VerifyEnvelope(CapsuleID(vector.capsuleID), raw)
			require.Equal(t, vector.ok, result.OK)
			if vector.ok {
				require.NoError(t, verifyErr)
				require.Equal(t, vector.publicKey, hex.EncodeToString(result.PublicKey))
			} else {
				require.ErrorIs(t, verifyErr, ErrInvalid)
				require.Contains(t, findingCodes(result.Findings), vector.code)
			}
		})
	}
}

func TestAACVerifierAuditPreservesStoredIdentityAndDecodeFailure(t *testing.T) {
	data, err := os.ReadFile("testdata/valid-v4.json")
	require.NoError(t, err)
	results := (AACVerifier{}).VerifyRecords([]Record{
		{Seq: 7, CapsuleID: "stored-id", Capsule: data},
		{Seq: 8, CapsuleID: "corrupt-id", Capsule: []byte("not-json")},
	})
	require.Len(t, results, 2)
	require.Equal(t, uint64(7), results[0].Seq)
	require.Equal(t, CapsuleID("stored-id"), results[0].CapsuleID)
	require.Equal(t, CapsuleID("862024869f00481bb4f59d9528a45c2d4885f64c5222a9324a38ac2c2cd119f2"), results[0].Result.CapsuleID)
	require.NotEmpty(t, results[0].Result.Findings)
	require.Equal(t, uint64(8), results[1].Seq)
	require.NotEmpty(t, results[1].Error)
}

// Frozen from the two store-shaped agent-action-capsule vectors at
// 7dcef86634355c0d3335b3050b1bc18845716275 (BSD-3-Clause).
func TestAACVerifierStoreConformanceFindings(t *testing.T) {
	const parent = `{"action_id":"po-parent2","action_type":"decide","assurance":{"attestation_mode":"self_attested","effect_mode":"not_applicable","ledger_mode":"standalone"},"capsule_id":"2e0a7892fe326fb2303ddce1852360f9da7ece3dff72e227a68b0f57effff6cb","developer":"agent@v1","disposition":{"approver":"human","decision":"needs_input","human_disposed":false,"verdict_class":"hitl_dispatched"},"format_version":"2","operator":"ACME-CO","spec_version":"draft-mih-scitt-agent-action-capsule-00","timestamp":"2026-06-13T00:00:00Z"}`
	const resolution = `{"action_id":"po-res-a","action_type":"decide","assurance":{"attestation_mode":"self_attested","effect_mode":"not_applicable","ledger_mode":"chained"},"capsule_id":"14ab84795b87e072d90013e4ef0c2c6bfeffdfca609519674f4c3b3974c341fc","chain":{"parent_capsule_id":"2e0a7892fe326fb2303ddce1852360f9da7ece3dff72e227a68b0f57effff6cb","relation":"supersedes"},"developer":"agent@v1","disposition":{"approver":"human","decision":"reject","human_disposed":true,"verdict_class":"resolved"},"format_version":"2","operator":"ACME-CO","spec_version":"draft-mih-scitt-agent-action-capsule-00","timestamp":"2026-06-13T00:00:00Z"}`
	const concurrent = `{"action_id":"po-res-b","action_type":"decide","assurance":{"attestation_mode":"self_attested","effect_mode":"not_applicable","ledger_mode":"chained"},"capsule_id":"577573c19ffb50d4c5e21ff23f9ed2cae2e49736ed30b9f949a98a520223a481","chain":{"parent_capsule_id":"2e0a7892fe326fb2303ddce1852360f9da7ece3dff72e227a68b0f57effff6cb","relation":"supersedes"},"developer":"agent@v1","disposition":{"approver":"human","decision":"reject","human_disposed":true,"verdict_class":"resolved"},"format_version":"2","operator":"ACME-CO","spec_version":"draft-mih-scitt-agent-action-capsule-00","timestamp":"2026-06-13T00:00:00Z"}`
	records := []Record{{Seq: 1, Capsule: json.RawMessage(parent)}, {Seq: 2, Capsule: json.RawMessage(resolution)}, {Seq: 3, Capsule: json.RawMessage(concurrent)}}
	results := (AACVerifier{}).VerifyRecords(records)
	require.Len(t, results, 3)
	require.Empty(t, results[0].Result.Findings)
	require.Empty(t, results[1].Result.Findings)
	require.Contains(t, findingCodes(results[2].Result.Findings), "concurrent_supersedes")
	require.True(t, results[2].Result.OK)

	const orphan = `{"action_id":"neg-orphan","action_type":"decide","assurance":{"attestation_mode":"self_attested","effect_mode":"confirmed","ledger_mode":"chained"},"capsule_id":"3b1f02ab14a769b630bf39d03deb8b29b4399201614524dd7d1814b12f6cbb26","chain":{"parent_capsule_id":"9999999999999999999999999999999999999999999999999999999999999999","relation":"supersedes"},"developer":"agent@v1","disposition":{"approver":"human","decision":"accept","human_disposed":true,"verdict_class":"executed"},"effect":{"effect_attestation":"gate_executed","response_digest":"1111111111111111111111111111111111111111111111111111111111111111","status":"confirmed","type":"write_order"},"format_version":"2","operator":"ACME-CO","spec_version":"draft-mih-scitt-agent-action-capsule-00","timestamp":"2026-06-13T00:00:00Z"}`
	orphanResult := (AACVerifier{}).VerifyRecords([]Record{{Seq: 1, Capsule: json.RawMessage(orphan)}})
	require.Contains(t, findingCodes(orphanResult[0].Result.Findings), "chain_parent_missing")
	require.False(t, orphanResult[0].Result.OK)
}

func findingCodes(findings []Finding) []string {
	result := make([]string, 0, len(findings))
	for _, finding := range findings {
		result = append(result, finding.Code)
	}
	return result
}
