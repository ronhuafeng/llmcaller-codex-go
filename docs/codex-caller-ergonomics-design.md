# Codex Caller Ergonomics

Status: incorporated into the v0.2 API

## Decision

The adapter exposes one exact-default constructor, one named safety profile,
and adjacent neutral, detailed, and streaming paths. It does not add helpers
that only shorten generated field assignment.

```go
type Options struct {
	Runner   ThreadRunner
	Defaults codexsdk.StartThreadRunRequest
}

func New(Options) (*Caller, error)
func ReadOnlyEphemeralOptions(ThreadRunner) Options
```

`Options.Defaults` preserves all exact generated request capabilities. The
adapter owns only turn thread ID, input, and output schema because those values
come from each neutral call. Construction rejects conflicts instead of silently
overwriting them.

## Safety Profile

`ReadOnlyEphemeralOptions` has independent policy meaning. It configures:

- ephemeral thread creation;
- read-only thread and turn sandboxes;
- never-approve thread and turn policies.

The detailed path also checks the exact effective start response. The streaming
path remains an unwrapped SDK escape hatch, so its consumer inspects the exact
eventual result when effective-policy verification is required.

## Result Depth

`CallDetailed` is the exact execution core. `Call` projects only stable neutral
facts while retaining the same exact run in typed `Details`. `CallStream` uses
the same request construction and returns the exact SDK stream. Partial runs
remain available when an error also occurs.

## Rejected Alternatives

- Field-by-field adapter options duplicate generated protocol ownership and
  create drift.
- Raw JSON or `map[string]any` escape hatches weaken typed capability.
- Adapter-owned retry, decode, validation, transport, or workflow overlaps the
  adjacent repositories.
- A read-only helper that sets only convenient request defaults but never
  verifies effective facts has a misleading name.

The normative API and acceptance criteria live in
[`v0.2-refactor-plan.md`](v0.2-refactor-plan.md).
