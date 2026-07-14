# Contributing

> [!IMPORTANT]
> This legacy repository is frozen at `v0.4.2`. Active development has moved
> to [`ronhuafeng/llm-go`](https://github.com/ronhuafeng/llm-go). No feature or
> security maintenance continues here. Before archival, contributions to this
> repository are limited to corrections required for migration, release
> evidence, or archival.

Thank you for helping keep the final migration and release evidence accurate.

This repository is intentionally small. It should stay focused on adapting
`llmkit-go` typed requests to `codexsdk-go` Codex thread calls.

## Development Setup

Requirements:

- Go 1.23 or newer.
- A normal Go module checkout. The project should build with `GOWORK=off`.

Useful commands:

```sh
go mod download
gofmt -w llmcaller internal
go vet ./...
go test ./...
GOWORK=off go test ./...
```

## Scope

Before archival, accepted corrections are limited to migration mappings, final
release or public-proxy evidence, and archival metadata. Runtime, API, schema,
test, CI-feature, and dependency development belongs in `llm-go`.

Out of scope for this repository:

- provider-neutral typed schema generation or decoding, which belongs in
  `llmkit-go`;
- Codex protocol transport, app-server lifecycle, and streaming APIs, which
  belong in `codexsdk-go`;
- business prompts, application workflows, private paths, credentials, or
  organization-specific examples.

## Compatibility

Follow Semantic Versioning. Before `v1.0.0`, breaking changes can happen in
minor releases, but they must be documented in `CHANGELOG.md`. After `v1.0.0`,
breaking exported API changes require a new major version.

Avoid unnecessary public API churn. If an issue can be fixed with docs, tests,
or internal code, prefer that over changing exported names or behavior.

## Pull Request Checklist

Before opening a pull request:

- run `gofmt`, `go vet ./...`, and `go test ./...`;
- run `GOWORK=off go test ./...` if you normally develop inside a local
  workspace;
- update README or package docs for user-visible behavior changes;
- update `CHANGELOG.md` for notable changes;
- update `THIRD_PARTY_NOTICES.md` when dependency license provenance changes;
- confirm no credentials, private paths, production data, or organization-only
  details were added.

## Dependency Policy

New dependencies should be rare. Prefer the Go standard library and the
existing `llmkit-go` and `codexsdk-go` contracts. Any new dependency must have
an OSI-compatible license and a clear purpose documented in the pull request.
