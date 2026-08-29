package ledger

// baselineRegistries mirrors spec/REGISTRY.md at agent-action-capsule commit
// 7e112c8b877ad79d4d2a53be7b522a63470a2b1d (BSD-3-Clause). Keeping the
// parsed baseline in the binary prevents verifier behavior from depending on
// the host working directory.
var baselineRegistries = map[string]map[string]bool{
	"verdict_class": set(
		"executed", "blocked", "hitl_dispatched", "denied", "timeout",
		"errored", "engine_failure", "deferred", "needs_decision", "expired",
		"escalated", "resolved", "epoch_boundary",
	),
	"disposition.decision": set("accept", "reject", "needs_input", "deferred"),
	"effect.type":          set("write_order", "send_payment"),
	"irreversibility_class": set(
		"two_way", "one_way_recoverable", "one_way_consequential", "one_way_terminal",
	),
	"effect_attestation": set("gate_executed", "runtime_claimed"),
	"chain.relation":     set("confirms", "supersedes", "epoch_opens"),
}

func set(values ...string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}
