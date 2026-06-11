// Package codexcaller adapts llmadapter typed calls to a Codex thread client.
//
// It owns the Codex-specific request bridge and structured-output schema policy.
// It does not own Go type projection, retry loops, or business semantics.
package codexcaller
