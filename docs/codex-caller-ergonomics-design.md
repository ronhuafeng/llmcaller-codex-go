# Codex Caller Ergonomics Design

Status: implemented

## Summary

Keep `llmcaller-codex-go` as a narrow Codex transport adapter for
`llmkit-go`. Improve its ergonomics for the common safe structured-output
case, but do not add retry, validation, prompt rendering, or workflow DSL
logic.

The proposed local addition is a small helper for read-only ephemeral
structured calls:

```go
opts := codexcaller.ReadOnlyEphemeralOptions(threads)
opts.Model = model
opts.CWD = workspace
caller := codexcaller.New(opts)
```

When `llmkit-go/llmstep` becomes available, update documentation and examples
to show how the Codex caller plugs into that provider-neutral step DSL.

## Motivation

Current examples repeat the same safety-oriented options:

- `ApprovalPolicy: codexsdk.ApprovalPolicyNever`;
- `Ephemeral: codexsdk.Bool(true)`;
- a caller-supplied `ThreadClient`;
- structured output schema normalization before `StartThread`.

That repetition is small but easy to get wrong. It also obscures the intended
boundary: Codex caller owns transport defaults and Codex schema normalization,
while `llmkit-go` owns typed request and retry mechanics.

## Domain Language

**Codex Caller**
: An implementation of `llmadapter.Caller` that converts a provider-neutral
  request into a Codex app-server thread run.

**Read-Only Ephemeral Call**
: A structured-output Codex call that should not request tool approval and
  should not persist as a normal visible thread.

**Schema Normalization**
: Codex-specific conversion of provider-neutral JSON Schema into the stricter
  output schema accepted by Codex app-server.

## Current Boundaries

Keep these boundaries intact:

- `llmkit-go` owns schema projection, typed decode, and typed retry.
- `codexsdk-go` owns app-server protocol, thread lifecycle, and request types.
- `llmcaller-codex-go` owns only the adapter between `llmadapter.Request` and
  `codexsdk.ThreadClient.StartThread`.

## Proposed API

```go
package codexcaller

func ReadOnlyEphemeralOptions(threadClient ThreadStarter) Options
```

The helper should return:

```go
Options{
    ThreadClient:   threadClient,
    ApprovalPolicy: codexsdk.ApprovalPolicyNever,
    Ephemeral:      codexsdk.Bool(true),
}
```

Callers can still set `Model`, `CWD`, `Effort`, or `ApprovalsReviewer` on the
returned value before passing it to `New`.

Do not add a fluent options DSL unless repeated use proves the struct helper is
insufficient. The existing `Options` struct is clear and testable.

## llmstep Integration

After `llmkit-go` exposes `llmstep`, add a compile-checked example showing:

```go
caller := codexcaller.New(codexcaller.ReadOnlyEphemeralOptions(threads))

out, err := llmstep.Run(ctx, llmstep.Step[Input, Output]{
    Caller: caller,
    Render: render,
    Validate: validate,
    MaxIter: 3,
}, input)
```

This module should not import application-specific validators or prompt
templates. The example should use small synthetic structs.

## Behavior

`Caller.Call` behavior should remain unchanged:

1. Reject nil callers or nil thread clients.
2. Reject empty or malformed output schema JSON before starting a thread.
3. Normalize schemas with `StrictOutputSchemaFromJSON`.
4. Start one Codex thread through the supplied `ThreadStarter`.
5. Return `ThreadRunResult.FinalResponse` unchanged to `llmkit-go`.

The helper only changes how users create `Options`; it must not change existing
zero-value behavior of `New(Options{...})`.

## Non-Goals

- No retry loop.
- No `llmstep` implementation.
- No prompt rendering API.
- No business validation API.
- No automatic app-server process management.
- No credentials or auth handling.
- No direct dependency on journal/session-management code.

## Test Plan

Add focused tests:

- `ReadOnlyEphemeralOptions` sets the supplied thread client.
- It sets `ApprovalPolicyNever`.
- It sets `Ephemeral` to `true`.
- The returned options remain mutable for `Model`, `CWD`, and `Effort`.
- Existing `Caller.Call` tests continue to pass.

When `llmstep` is available, add a compile-checked example that uses a fake
thread starter and verifies the caller still receives a Codex output schema.

## Acceptance Criteria

- `go test ./...` passes.
- `go vet ./...` passes.
- README documents the helper as the recommended structured-output default.
- README explicitly says retry and validation belong to `llmkit-go/llmstep`.
- No new workflow, prompt, or business semantic API is added.
- Existing `Options`, `New`, `Caller.Call`, and `StrictOutputSchemaFromJSON`
  compatibility is preserved.

## Dependency Order

This work can be split:

1. Implement `ReadOnlyEphemeralOptions` immediately.
2. After a `llmkit-go` release or local dependency update exposes `llmstep`,
   add the `llmstep` integration example.
3. If `codexsdk-go` later adds thread-client defaults for ephemeral and
   approval policy, this helper can keep its public shape and delegate less
   per-request state to `StartThreadRequest`.
