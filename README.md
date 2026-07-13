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
go get github.com/ronhuafeng/llmcaller-codex-go@v0.4.0
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

For options returned by `ReadOnlyEphemeralOptions`, the adapter additionally
owns thread ephemeral, sandbox, and approval fields plus turn sandbox and
approval fields. `New` fills unset profile fields with their safe values and
rejects explicit conflicts before a caller can be constructed. It then clones
the normalized defaults. Every request reapplies those profile values before
the SDK runner is invoked, while model, CWD, effort, service tier, workspace
roots, and all other non-profile generated defaults remain caller-controlled.

## Result Paths

- `Call` implements `llmadapter.Caller` and projects final text, provider name,
  effective model, and total token usage.
- `CallDetailed` returns the exact `codexsdk.StartedThreadRun` and is the core
  execution path.
- `CallStream` returns an adapter-owned exact stream wrapper and uses the same
  request builder. `Stream.SDKStream` is the adjacent typed SDK escape hatch.

See the [v0.4 migration guide](docs/v0.4-migration.md) when updating code that
stored the pre-v0.4 SDK stream return type explicitly.

`Call` places an immutable exact run in `codexcaller.Details`. Notifications,
diagnostics, IDs, exact usage, sandbox, approval, service tier, and generated
configuration remain available there. If the SDK returns a partial run and an
error, the adapter returns both the available response evidence and the same
error cause chain.

Requested-policy enforcement happens before transport for `Call`,
`CallDetailed`, and `CallStream`: no explicitly conflicting named-profile
defaults can reach the SDK runner. All three paths also apply the same
post-execution check that Codex's effective result is read-only, never-approve,
and ephemeral. `Stream.Wait` returns the complete exact terminal or partial
result together with SDK and `ErrEffectiveProfile` causes; `Stream.Err` exposes
the same joined terminal causes. Notification, usage, diagnostics, metadata,
effective configuration, and partial evidence remain exact SDK values. Use
`Stream.SDKStream` when a lower-level SDK operation is required, and observe the
terminal result through the adapter wrapper when named-profile verification is
required.

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

### Normative schema-equivalence contract

The words **MUST**, **MUST NOT**, **SHOULD**, and **MAY** below are normative.

Provider-neutral Go type projection and response decoding remain owned by
`llmkit-go`. This adapter MUST apply only Codex-specific schema policy to the
exact JSON Schema it receives; it MUST NOT infer a broader schema from Go type
shape or add transformations not described here.

At the JSON Schema instance-language layer, the adapter MUST preserve every
constraint except the following intentional narrowing: each object property
that is absent from `required` is promoted to required, but only after the
property's complete schema is proven to accept the JSON instance `null` under
the selected draft. Thus the transformed language no longer admits omission of
that property, while its admitted explicit JSON values and all other supported
constraints remain unchanged. This is not universal JSON Schema language
equivalence.

At the ordinary Go decoded-value layer, that narrowing is intended only for
fields where omission and explicit `null` decode to the same value using normal
`encoding/json` pointer, slice, map, or value behavior. The adapter does not
decode results and MUST NOT claim this equivalence for custom
`UnmarshalJSON` implementations, `json.RawMessage`, or domain types that attach
meaning to presence. It makes no guarantee of arbitrary application semantic
equivalence.

Unknown annotation and assertion keyword values retained by the generated SDK
parser/serializer MUST survive by decoded JSON value semantics. The adapter
does not promise that an unsupported assertion keyword is enforced. Supported
local `$ref` values are URI fragments containing JSON Pointer references; their
target meaning and applicable sibling constraints MUST be preserved according
to the selected draft. External resources, `$dynamicRef`, recursive or cyclic
graphs, unresolvable references, unsupported draft identifiers, and any schema
whose null admission cannot be proven MUST fail closed. `$vocabulary`
declarations also fail closed. A `$dynamicAnchor` value without `$dynamicRef`
is retained, but dynamic-resolution semantics are not supported or guaranteed;
schemas that rely on those semantics are outside this contract.

Fail-closed errors MUST be `*SchemaPolicyError` with the stable `Kind` and JSON
Pointer `Path` documented in the matrix. A failure returned by `Caller` MUST
occur before invoking the Codex runner. Whole-document compilation failures use
`invalid_schema` at the root; property null-analysis failures use the exact
property pointer.

Serialization MAY change object-key order, whitespace, number spelling, escape
spelling, and the order of the normalized `required` array. Byte identity is
not promised.

### Normative compatibility matrix

Each row ID is backed by the same-named subtest in
`TestStrictOutputSchemaCompatibilityMatrix`. “Preserved” means accepted with
preserved ordinary decoded-value semantics; “limitation” means accepted with
the stated explicit limitation; “fail-closed” means rejected before execution
with the listed stable error.

| Normative row / Go or schema shape | Required result |
| --- | --- |
| `required-scalar-preserved` — required scalar | Preserved; no presence normalization |
| `optional-pointer-preserved` — `*T,omitempty` whose exact schema admits null | Preserved; property promoted to required |
| `optional-scalar-fails-closed` — non-nullable scalar with `omitempty` | Fail-closed: `optional_non_nullable` at `/properties/score` |
| `nested-optional-pointer-preserved` — nested nullable pointer | Preserved; the same rule applies at the nested pointer |
| `optional-map-fails-closed` — `map[string]T,omitempty` projected as non-nullable by the locked `llmkit-go` | Fail-closed: `optional_non_nullable` at `/properties/labels`; no map-specific widening |
| `optional-slice-preserved` — `[]T,omitempty` whose exact schema admits null | Preserved under ordinary nil-slice decoding |
| `optional-pointer-to-slice-preserved` — `*[]T,omitempty` | Preserved under ordinary nil-pointer decoding |
| `optional-raw-message-has-decoding-limitation` — `json.RawMessage,omitempty` | Limitation: accepted, but absence decodes to nil while explicit null is retained as `"null"` |
| `custom-unmarshaler-has-decoding-limitation` — nullable schema for a custom unmarshaler | Limitation: accepted schema; decoded-value or application equivalence is not guaranteed |
| `local-ref-preserved` — supported local JSON Pointer `$ref` | Preserved after resolving and validating the complete referenced schema |
| `nested-ref-with-sibling-constraint-fails-closed` — nested references whose applicable sibling rejects null | Fail-closed: `optional_non_nullable` at `/properties/value` |
| `boolean-schema-has-codex-limitation` — `true` or `false` schema | Limitation: accepted unchanged by this policy; no object normalization or Codex acceptance guarantee |
| `draft-2020-12-preserved` — explicit draft 2020-12 | Preserved using draft 2020-12 semantics |
| `draft-7-ref-sibling-limitation` — explicit draft-07 `$ref` with siblings | Limitation: accepted using draft-07 semantics, where `$ref` siblings are ignored |
| `unsupported-draft-fails-closed` — unknown explicit draft identifier | Fail-closed: `invalid_schema` at the root path |
| `unknown-annotation-preserved` — unknown annotation keyword | Preserved by decoded JSON value semantics |
| `unknown-assertion-has-validation-limitation` — unknown assertion keyword | Limitation: value preserved, enforcement not guaranteed |
| `dynamic-anchor-has-resolution-limitation` — `$dynamicAnchor` without `$dynamicRef` | Limitation: value preserved, dynamic-resolution semantics not guaranteed |
| `vocabulary-fails-closed` — `$vocabulary` declaration | Fail-closed: `invalid_schema` at the root path |
| `additional-properties-schema-preserved` — object schema in `additionalProperties` | Preserved; nested object properties undergo the same required/null rule |
| `conditional-null-fails-closed` — conditional schema that rejects null | Fail-closed: `optional_non_nullable` at `/properties/value` |
| `cyclic-ref-fails-closed` — cyclic local reference graph | Fail-closed: `cyclic_ref` at the reference pointer |
| `external-ref-fails-closed` — external `$ref` | Fail-closed: `external_ref` at `/$ref` |
| `unresolvable-ref-fails-closed` — missing local target | Fail-closed: `unresolvable_ref` at `/$ref` |
| `dynamic-ref-fails-closed` — `$dynamicRef` | Fail-closed: `unsupported_dynamic_ref` at `/$dynamicRef` |

Schemas without an explicit `$schema` use draft 2020-12, except documents with
legacy tuple-form `items`, which retain draft-07 interpretation for
compatibility. Other drafts and vocabularies are supported only when explicitly
added to this contract and matrix.

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

Active compatibility sources are machine-listed in
[`compatibility.json`](compatibility.json): the exported API inventory, resolved
upstream module tags, schema matrix, clean external consumer, and fast/full
three-layer canaries. Historical proposal documents are design context only and
are not build, CI, or release gates. Every pushed `v*` tag runs the
[`proxy-tag-consumer` workflow](.github/workflows/proxy-tag-consumer.yml), which
waits at most ten minutes for the exact tag on `proxy.golang.org`, records
the tagged compatibility contract digest plus exact declared and resolved
versions and sums, and runs a typed clean consumer without `replace`, `exclude`,
`go.work`, or pseudo-version upstreams.

This project is MIT licensed. Dependency provenance is recorded in
[`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md).
