# AAC interim registry baseline

Vendored from `action-state-group/agent-action-capsule/spec/REGISTRY.md` at
commit `7dcef86634355c0d3335b3050b1bc18845716275`. BSD-3-Clause applies.

## 1. `verdict_class`

Initial contents: `executed`, `blocked`, `hitl_dispatched`, `denied`, `timeout`,
`errored`, `engine_failure`, `deferred`, `needs_decision`, `expired`, `escalated`,
`resolved`, `epoch_boundary`.

## 2. `disposition.decision`

Initial contents: `accept`, `reject`, `needs_input`, `deferred`.

## 3. `effect.type`

Initial contents: `write_order`, `send_payment`.

## 4. `irreversibility_class`

Initial contents:

1. `two_way`
2. `one_way_recoverable`
3. `one_way_consequential`
4. `one_way_terminal`

## 5. `effect_attestation`

Initial contents: `gate_executed`, `runtime_claimed`.

## 6. `chain.relation`

Initial contents: `confirms`, `supersedes`, `epoch_opens`.
