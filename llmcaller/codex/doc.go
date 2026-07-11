// Package codexcaller adapts llmadapter structured calls to exact Codex thread
// lifecycle operations.
//
// It owns Codex schema policy, request/result projection, exact defaults, and
// named Codex safety profiles. Named-profile request policy is enforced before
// every runner invocation; non-streaming detailed calls also verify the
// effective policy reported after execution. Detailed and streaming paths
// preserve generated SDK facts and partial results. The package does not own Go
// type projection, decoding, validation, retries, transport, or business
// semantics.
package codexcaller
