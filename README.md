# llmcaller-codex-go

`llmcaller-codex-go` adapts provider-neutral structured calls from
[`llmkit-go`](https://github.com/ronhuafeng/llmkit-go) to exact Codex lifecycle
operations from [`codexsdk-go`](https://github.com/ronhuafeng/codexsdk-go).

The adapter owns only Codex schema policy, exact request/result translation,
construction defaults, and the named read-only ephemeral profile. Typed schema
generation, decoding, validation, and retry belong to `llmkit-go`; transport,
protocol, streaming, and thread lifecycle belong to `codexsdk-go`.

## Install

```sh
go get github.com/ronhuafeng/llmcaller-codex-go@v0.2.0-rc.1
```

Go 1.23 or newer is required.

## Typed Call

An SDK `ThreadRunner` satisfies the adapter's smaller consumer-owned interface.
The safety profile sets ephemeral thread creation, read-only sandboxing, and
never-approve policy at both thread and turn scope.

```go
runner := client.ThreadRunner()
options := codexcaller.ReadOnlyEphemeralOptions(runner)
options.Defaults.Thread.Model = protocolv2.Value("gpt-5")
options.Defaults.Thread.CWD = protocolv2.Value(workspace)

caller, err := codexcaller.New(options)
if err != nil {
	return err
}

type Result struct {
	Answer string `json:"answer"`
}

value, err := llmadapter.Value[Result](ctx, caller, "Return JSON.")
```

A compile-checked fake-runner version of the complete three-layer path is in
[`llmcaller/codex/example_test.go`](llmcaller/codex/example_test.go).

## Exact Defaults

`Options.Defaults` is `codexsdk.StartThreadRunRequest`, so every generated
Codex request fact remains expressible without a copied option model. The
adapter owns and rejects non-zero values for:

- `Defaults.Turn.ThreadID`;
- `Defaults.Turn.Input`;
- `Defaults.Turn.OutputSchema`.

`New` clones mutable defaults. All other generated fields remain caller
controlled.

## Result Paths

- `Call` implements `llmadapter.Caller` and projects final text, provider name,
  effective model, and total token usage.
- `CallDetailed` returns the exact `codexsdk.StartedThreadRun` and is the core
  execution path.
- `CallStream` returns the exact SDK stream and uses the same request builder.

`Call` places an immutable exact run in `codexcaller.Details`. Notifications,
diagnostics, IDs, exact usage, sandbox, approval, service tier, and generated
configuration remain available there. If the SDK returns a partial run and an
error, the adapter returns both the available response evidence and the same
error cause chain.

The detailed path verifies the effective read-only, never-approve, and
ephemeral start result. `CallStream` is an exact escape hatch: because the
adapter does not consume or wrap the SDK stream, its caller must inspect the
eventual exact result when effective-policy verification is required.

```go
response, err := caller.Call(ctx, request)
if details, ok := response.ProviderDetails.(codexcaller.Details); ok {
	inspect(details.Run)
}
if err != nil {
	return err
}
```

## Schema Policy

`StrictOutputSchemaFromJSON` converts neutral JSON Schema to the generated
Codex `OutputSchema`. It recursively visits supported subschema positions,
resolves local references, and preserves unknown keywords by JSON value
semantics.

Codex requires object properties to be required. An optional property is
promoted only if its existing schema admits `null`; otherwise the adapter
returns `*SchemaPolicyError` before starting Codex. External, unresolved,
dynamic, and cyclic references fail closed. Serialization may change key order,
whitespace, number spelling, or escapes; byte identity is not promised.

| Go/schema shape | Result |
| --- | --- |
| Non-`omitempty` scalar | Accepted as required, non-nullable |
| Pointer with `omitempty` | Accepted only when generated schema admits null |
| Non-pointer scalar with `omitempty` | Rejected as `optional_non_nullable` |
| Nested optional property | Same rule at its exact JSON pointer |
| Optional property through local `$ref` | Resolved, then checked for null |
| Cyclic, external, or unresolved `$ref` | Rejected before execution |
| Unknown keyword | JSON value semantics preserved |

See [`docs/v0.2-migration.md`](docs/v0.2-migration.md) for concrete examples.

## Boundaries

This module does not start processes, manage credentials, handle approvals,
decode typed values, validate business output, retry calls, or own workflow.
Applications configure those behaviors through the adjacent layers.

## Verification

```sh
GOWORK=off go test ./...
GOWORK=off go vet ./...
GOWORK=off go test -race ./...
```

The release process also byte-compares mirrored contracts and runs temporary
all-heads and real-tag canaries without committed `replace` or `go.work` files.

This project is MIT licensed. Dependency provenance is recorded in
[`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md).
