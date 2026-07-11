# Third-Party Notices

This document summarizes dependency provenance for source releases of
`llmcaller-codex-go`. It is informational and does not replace the license text
in dependency repositories or module archives.

## Project License

`llmcaller-codex-go` is licensed under the MIT License. See `LICENSE`.

## Module Dependencies

| Module | Version | Relationship | Provenance | License |
| --- | --- | --- | --- | --- |
| `github.com/ronhuafeng/llmkit-go` | `v0.2.0-rc.1` | Direct | Go module declared in `go.mod`; provider-neutral typed request, schema, decode, validation, and retry primitives. | MIT |
| `github.com/ronhuafeng/codexsdk-go` | `v0.2.0-rc.1` | Direct | Go module declared in `go.mod`; exact Codex app-server protocol, transport, and lifecycle API. | MIT |
| `github.com/google/jsonschema-go` | `v0.4.3` | Indirect | Go module declared as indirect in `go.mod`; JSON Schema support used by the typed schema stack. | MIT |
| `github.com/santhosh-tekuri/jsonschema/v6` | `v6.0.2` | Indirect | Strict JSON Schema validation used by the typed schema stack. | MIT |
| `golang.org/x/text` | `v0.14.0` | Indirect | Unicode and text support used by transitive schema dependencies. | BSD-3-Clause |

## Transitive Dependencies

The following module is present in `go.sum` through dependency test suites or
transitive implementation details:

| Module | Version | Provenance | License |
| --- | --- | --- | --- |
| `github.com/dlclark/regexp2` | `v1.11.0` | Listed by `GOWORK=off go list -m all`. | MIT |
| `github.com/google/go-cmp` | `v0.7.0` | Listed by `GOWORK=off go list -m all`. | BSD-3-Clause |
| `golang.org/x/mod` | `v0.8.0` | Listed by `GOWORK=off go list -m all`. | BSD-3-Clause |
| `golang.org/x/sys` | `v0.5.0` | Listed by `GOWORK=off go list -m all`. | BSD-3-Clause |
| `golang.org/x/tools` | `v0.6.0` | Listed by `GOWORK=off go list -m all`. | BSD-3-Clause |

## Maintenance

When dependencies change:

1. run `GOWORK=off go list -m all`;
2. inspect each new module's license from the module archive or upstream
   repository;
3. update this file and `go.sum` in the same pull request;
4. confirm all dependency licenses are OSI-compatible and compatible with the
   MIT-licensed project distribution.
