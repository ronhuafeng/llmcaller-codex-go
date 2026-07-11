// Package codexcaller adapts llmadapter structured calls to exact Codex thread
// lifecycle operations.
//
// It owns Codex schema policy, request/result projection, exact defaults, and
// named Codex safety profiles. Detailed and streaming paths preserve generated
// SDK facts and partial results. The package does not own Go type projection,
// decoding, validation, retries, transport, or business semantics.
package codexcaller
