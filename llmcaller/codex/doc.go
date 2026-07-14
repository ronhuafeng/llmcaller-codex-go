// Package codexcaller adapts llmadapter structured calls to exact Codex thread
// lifecycle operations.
//
// It owns Codex schema policy, request/result projection, exact defaults, and
// named Codex safety profiles. Named-profile request policy is enforced before
// every runner invocation; Call, CallDetailed, and the adapter-owned Stream
// apply the same effective-policy verification after execution. Stream keeps
// exact SDK notifications, lifecycle observation, terminal results, errors,
// and a typed SDKStream escape hatch without projecting away generated facts.
// A decoded partial start remains observable and profile-checked when its
// required thread identity is missing; pre-response failures do not create a
// synthetic profile mismatch.
// The package does not own Go type projection, decoding, validation, retries,
// transport, or business semantics.
//
// Deprecated: this legacy module path is frozen at v0.4.2. Migrate to
// github.com/ronhuafeng/llm-go/llmcaller/codex, beginning with v0.5.0. No
// feature or security maintenance continues on this module path; immutable
// legacy versions remain available through the public Go proxy.
//
// StrictOutputSchemaFromJSON preserves supported constraints and unknown
// keyword JSON values, but intentionally narrows the JSON Schema instance
// language by promoting an optional property to required only when its complete
// schema admits null. That narrowing preserves ordinary Go decoded values only
// when absence and explicit null decode alike; custom unmarshalers,
// json.RawMessage, presence-sensitive domain meaning, and arbitrary application
// semantic equivalence are outside the guarantee. Unprovable null admission and
// unsupported references, draft identifiers, or vocabulary declarations fail
// closed with a stable SchemaPolicyError kind and JSON Pointer path before a
// Caller invokes its runner. Retaining a boolean schema, unknown keyword, or
// lone dynamic anchor does not guarantee Codex acceptance or unsupported
// assertion and dynamic-resolution semantics. Serialization is not
// byte-preserving.
package codexcaller
