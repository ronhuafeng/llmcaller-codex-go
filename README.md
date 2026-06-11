# llmcaller-codex-go

Codex caller adapter for `llmkit-go`.

`llmcaller-codex-go` connects the provider-neutral typed request API from
[`llmkit-go`](https://github.com/ronhuafeng/llmkit-go) to the Codex app-server
transport in [`codexsdk-go`](https://github.com/ronhuafeng/codexsdk-go).

It intentionally owns only one narrow boundary:

- accept an `llmadapter.Request` or `llmadapter.Value[T]` call;
- normalize the generated JSON Schema for Codex structured output;
- start a Codex thread through a caller-supplied `codexsdk.ThreadClient`;
- return the final Codex response text to `llmkit-go` for typed decoding.

Business semantics, retry policy, prompt construction, type projection, Codex
authentication, and app-server process management belong to the application,
`llmkit-go`, or `codexsdk-go`.

## Packages

| Package | Purpose |
| --- | --- |
| `github.com/ronhuafeng/llmcaller-codex-go/llmcaller/codex` | Implements `llmadapter.Caller` for Codex. The package name is `codexcaller`. |

## Installation

```sh
go get github.com/ronhuafeng/llmcaller-codex-go
```

This module requires Go 1.23 or newer.

Applications usually import all three layers:

```go
import (
	"github.com/ronhuafeng/codexsdk-go/codexsdk"
	codexcaller "github.com/ronhuafeng/llmcaller-codex-go/llmcaller/codex"
	"github.com/ronhuafeng/llmkit-go/llmadapter"
)
```

## Quick Start

The adapter needs a `ThreadClient`. The example below starts the Codex app
server through `codexsdk-go`, creates a thread client, wraps it as an
`llmadapter.Caller`, and asks `llmkit-go` to decode the final JSON response
into a Go struct. Set `CODEXSDK_EXAMPLE_MODEL` to a model available to your
Codex account, such as `gpt-5` when your account supports it.

```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/ronhuafeng/codexsdk-go/codexsdk"
	codexcaller "github.com/ronhuafeng/llmcaller-codex-go/llmcaller/codex"
	"github.com/ronhuafeng/llmkit-go/llmadapter"
)

type Summary struct {
	Answer     string `json:"answer"`
	Confidence string `json:"confidence"`
}

func main() {
	ctx := context.Background()

	workspace, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	model := os.Getenv("CODEXSDK_EXAMPLE_MODEL")
	if model == "" {
		panic("CODEXSDK_EXAMPLE_MODEL is required")
	}

	root, err := codexsdk.New(codexsdk.ClientOptions{
		CWD:     workspace,
		Command: []string{"codex", "app-server", "--listen", "stdio://"},
	})
	if err != nil {
		panic(err)
	}
	defer root.Close()

	threads := root.ThreadClient(codexsdk.ThreadClientOptions{
		DefaultModel: model,
		DefaultCWD:   workspace,
	})
	defer threads.Close()

	caller := codexcaller.New(codexcaller.Options{
		ThreadClient:   threads,
		ApprovalPolicy: codexsdk.ApprovalPolicyNever,
		Ephemeral:      codexsdk.Bool(true),
	})

	out, err := llmadapter.Value[Summary](
		ctx,
		caller,
		"Return a short JSON object describing what this repository adapter does.",
	)
	if err != nil {
		panic(err)
	}

	fmt.Printf("%s (%s)\n", out.Answer, out.Confidence)
}
```

If you already have a `codexsdk.ThreadClient`, pass it directly. `Options`
also lets callers set a per-call model, working directory, reasoning effort,
approval policy, approvals reviewer, and ephemeral-thread preference.

## Typed Requests

Most applications should use `llmadapter.Value[T]` or `llmadapter.Op[I, O]`.
Those helpers create an `llmadapter.Request` with a JSON Schema derived from
the target Go type, call this adapter, and decode `Response.FinalResponse`
back into the requested Go type.

For lower-level control, construct a request explicitly:

```go
req, err := llmadapter.RequestFor[Summary]("Answer with JSON only.")
if err != nil {
	return err
}

resp, err := caller.Call(ctx, req)
if err != nil {
	return err
}

fmt.Println(resp.FinalResponse)
```

`Call` does not decode the final response. It returns the final Codex response
string exactly as reported by `codexsdk-go`.

## Structured Output Schema Normalization

Codex structured output expects object schemas to make intended fields
required. `llmcaller-codex-go` applies this provider-specific policy in
`StrictOutputSchemaFromJSON` before sending the schema to Codex:

- every object with a non-empty `properties` map gets `required` set to all
  property names, sorted for deterministic output;
- nested object properties are normalized recursively;
- array `items` and `anyOf`, `oneOf`, and `allOf` branches are normalized
  recursively;
- invalid JSON, empty schemas, or schemas rejected by `codexsdk-go` fail
  before a Codex thread is started.

This package does not generate schemas from Go types. That remains the
provider-neutral responsibility of `llmkit-go`.

## Failure Semantics

The adapter fails closed:

- a nil caller or nil thread client returns `ErrNilThreadClient`;
- an empty `Request.OutputSchema` returns `ErrMissingSchemaJSON`;
- malformed schema JSON or a schema rejected by `codexsdk-go` is returned as
  an error and no thread is started;
- transport, app-server, approval, authentication, model, cancellation, and
  context errors are returned from `ThreadClient.StartThread`;
- typed JSON decoding and semantic validation errors are owned by
  `llmkit-go`, after this adapter returns `Response.FinalResponse`.

Callers should treat any error as "no typed result was produced" unless their
own higher-level policy says otherwise.

## Security And Authentication

This module does not read API keys, manage login flows, store credentials, or
start shell commands by itself. Security-sensitive behavior is delegated to the
objects supplied by the application:

- `codexsdk.New` owns the app-server command, current working directory, and
  server request handler;
- `codexsdk.ThreadClient` owns Codex transport and thread execution;
- `ApprovalPolicy`, `ApprovalsReviewer`, and the SDK server request handler
  decide whether tool calls, file edits, and command execution are allowed;
- callers should prefer the narrowest practical working directory and use
  `ApprovalPolicyNever` for read-only structured calls.

Do not place credentials, private repository paths, or business data in tests,
fixtures, issues, or examples.

## Relationship To llmkit-go And codexsdk-go

`llmkit-go` is the provider-neutral layer. It owns typed request creation,
schema generation from Go types, output decoding, and reusable operation
helpers.

`codexsdk-go` is the Codex protocol and transport layer. It owns the app-server
client, thread lifecycle APIs, streaming APIs, approval hooks, and protocol
types.

`llmcaller-codex-go` is deliberately small glue between them. It should not
grow provider-neutral schema logic, Codex protocol facades, business prompt
logic, or application-specific retries.

## Stability And Compatibility

This project uses Semantic Versioning for tagged releases.

Before `v1.0.0`, public API changes may occur in minor releases, but breaking
changes should be documented in `CHANGELOG.md` and kept small. After `v1.0.0`,
the exported API under `llmcaller/codex` follows normal SemVer compatibility:
breaking changes require a new major version.

The intended stable surface is:

- `New`;
- `Options`;
- `Caller.Call`;
- `ThreadStarter`;
- `StrictOutputSchemaFromJSON`;
- exported sentinel errors.

## Testing

Run the same checks as CI:

```sh
gofmt -w llmcaller internal
test -z "$(gofmt -l llmcaller internal)"
go vet ./...
go test ./...
```

For standalone module verification outside a local Go workspace:

```sh
GOWORK=off go test ./...
```

## License And Notices

`llmcaller-codex-go` is released under the MIT License. See `LICENSE`.

Dependency license provenance is tracked in `THIRD_PARTY_NOTICES.md`.
