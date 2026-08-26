package ledger

import (
	"fmt"

	aacenvelope "github.com/action-state-group/agent-action-capsule/go/envelope"
	aacverify "github.com/action-state-group/agent-action-capsule/go/verify"
)

// AACVerifier adapts the upstream format-4 verifier without interpreting any
// application-private registry values. Service construction always uses this
// verifier; the exported concrete type remains available for explicit
// standalone verification.
type AACVerifier struct {
	registries map[string]map[string]bool
}

// NewAACVerifier constructs a standalone verifier with an immutable copy of
// application registry extensions merged into the embedded baseline.
func NewAACVerifier(extensions map[string]map[string]bool) AACVerifier {
	return AACVerifier{registries: mergedRegistries(extensions)}
}

func (v AACVerifier) VerifyCapsule(data []byte) (VerificationResult, CapsuleID, error) {
	if len(data) == 0 || len(data) > MaxCapsuleBytes {
		return VerificationResult{}, "", fmt.Errorf("%w: capsule size %d", ErrInvalid, len(data))
	}
	capsule, err := aacverify.DecodeCapsuleJSON(data)
	if err != nil {
		return VerificationResult{}, "", fmt.Errorf("%w: decode capsule: %v", ErrInvalid, err)
	}
	upstream := aacverify.Verify(capsule, nil, v.registrySet())
	result := convertVerification(upstream)
	if !upstream.OK || upstream.CapsuleID == nil {
		return result, result.CapsuleID, fmt.Errorf("%w: capsule verification failed", ErrInvalid)
	}
	return result, result.CapsuleID, nil
}

func convertVerification(upstream aacverify.VerificationResult) VerificationResult {
	result := VerificationResult{
		OK:        upstream.OK,
		Assurance: cloneMap(upstream.Assurance),
		Findings:  make([]Finding, 0, len(upstream.Findings)),
	}
	for _, finding := range upstream.Findings {
		result.Findings = append(result.Findings, Finding{
			Code: finding.Code, Detail: finding.Detail,
			Severity: finding.Severity, Check: cloneInt(finding.Check),
		})
	}
	if upstream.CapsuleID != nil {
		result.CapsuleID = CapsuleID(*upstream.CapsuleID)
	}
	return result
}

func (v AACVerifier) VerifyRecords(records []Record) []RecordVerification {
	parsed := make([]any, 0, len(records))
	results := make([]RecordVerification, len(records))
	validIndexes := make([]int, 0, len(records))
	for index, record := range records {
		results[index] = RecordVerification{Seq: record.Seq, CapsuleID: record.CapsuleID}
		capsule, err := aacverify.DecodeCapsuleJSON(record.Capsule)
		if err != nil {
			results[index].Error = fmt.Sprintf("decode stored capsule: %v", err)
			continue
		}
		parsed = append(parsed, capsule)
		validIndexes = append(validIndexes, index)
	}
	verified := aacverify.VerifyStore(parsed, v.registrySet())
	for index, upstream := range verified {
		results[validIndexes[index]].Result = convertVerification(upstream)
	}
	return results
}

func mergedRegistries(extensions map[string]map[string]bool) map[string]map[string]bool {
	merged := make(map[string]map[string]bool, len(baselineRegistries)+len(extensions))
	for name, values := range baselineRegistries {
		merged[name] = make(map[string]bool, len(values))
		addRegistered(merged[name], values)
	}
	for name, values := range extensions {
		if merged[name] == nil {
			merged[name] = make(map[string]bool)
		}
		addRegistered(merged[name], values)
	}
	return merged
}

func addRegistered(output, input map[string]bool) {
	for value, registered := range input {
		if registered {
			output[value] = true
		}
	}
}

func (v AACVerifier) registrySet() map[string]map[string]bool {
	if v.registries == nil {
		return mergedRegistries(nil)
	}
	return v.registries
}

func (AACVerifier) VerifyEnvelope(id CapsuleID, data []byte) (EnvelopeVerification, error) {
	if len(data) == 0 || len(data) > MaxEnvelopeBytes {
		return EnvelopeVerification{}, fmt.Errorf("%w: envelope size %d", ErrInvalid, len(data))
	}
	upstream := aacenvelope.Verify(string(id), data)
	result := EnvelopeVerification{OK: upstream.OK, PublicKey: cloneBytes(upstream.PublicKey)}
	for _, finding := range upstream.Findings {
		result.Findings = append(result.Findings, Finding{
			Code: finding.Code, Detail: finding.Detail, Severity: "error",
		})
	}
	if !upstream.OK {
		return result, fmt.Errorf("%w: producer envelope verification failed", ErrInvalid)
	}
	return result, nil
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
