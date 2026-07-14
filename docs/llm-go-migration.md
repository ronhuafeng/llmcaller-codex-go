# Migrating to llm-go

`github.com/ronhuafeng/llmcaller-codex-go` ends at `v0.4.2`. Development moves
to the independently released Codex adapter module inside
[`github.com/ronhuafeng/llm-go`](https://github.com/ronhuafeng/llm-go):

```sh
go get github.com/ronhuafeng/llm-go/llmcaller/codex@v0.5.0
```

Do not add a `replace`, `go.work`, forwarding package, or re-export layer to
bridge the repository paths. Update imports directly after the new module tag
has passed its public-proxy and clean-consumer release gates. Until then, the
immutable legacy release remains the consumable version.

## Import mappings

The adapter package moves to the new module root:

```text
github.com/ronhuafeng/llmcaller-codex-go/llmcaller/codex
  -> github.com/ronhuafeng/llm-go/llmcaller/codex
```

Its two upstream modules move into the same repository but remain independent
Go modules:

| Legacy module | Replacement module |
| --- | --- |
| `github.com/ronhuafeng/llmkit-go` | `github.com/ronhuafeng/llm-go/llmkit` |
| `github.com/ronhuafeng/codexsdk-go` | `github.com/ronhuafeng/llm-go/codexsdk` |

The public package imports used by this adapter map as follows:

```text
github.com/ronhuafeng/llmkit-go/llmadapter
  -> github.com/ronhuafeng/llm-go/llmkit/llmadapter

github.com/ronhuafeng/codexsdk-go/codexsdk
  -> github.com/ronhuafeng/llm-go/codexsdk
github.com/ronhuafeng/codexsdk-go/codexsdk/protocolv2
  -> github.com/ronhuafeng/llm-go/codexsdk/protocolv2
```

The new adapter module remains the only dependency join. The toolkit and SDK
modules remain independent, and the adapter's released `go.mod` records the
exact compatible upstream versions.

## Lifecycle

The new adapter begins at `v0.5.0`. Exported identifiers, package names, exact
Codex evidence, neutral projections, effective-profile checks, and schema
policy retain their existing ownership and behavior; this migration changes
repository and import paths rather than creating a compatibility layer.

No feature or security maintenance continues on the legacy module path after
cutover. Its tags are immutable and remain available through the public Go
proxy. This repository is archived only after all three replacement modules
have been released and the final cross-module provenance audit passes.
