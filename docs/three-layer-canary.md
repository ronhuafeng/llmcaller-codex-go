# Three-layer canary

The canary exercises `llmkit-go -> llmcaller-codex-go -> codexsdk-go` through
the SDK's real subprocess JSON-RPC transport and a deterministic fake
app-server. It does not use a workspace, replacement, or local module source.

Released module evidence:

- `github.com/ronhuafeng/llmkit-go v0.4.1`
  `h1:JrWgxC16zV0yCI5mCAoRnsXpZOkh+SKVVr5KSN0SFZE=`
- `github.com/ronhuafeng/codexsdk-go v0.5.0`
  `h1:7yI6KvyEzyO09HoZABWlaGvz8Rh0070JlITk47sDr4M=`

`TestThreeLayerCanaryFast` is the normal CI subset. The full invariant suite is
`LLMCALLER_FULL_CANARY=1 go test ./llmcaller/codex -run '^TestThreeLayerCanary'`
and runs only from the release/manual workflow.

Every pushed `v*` caller tag also runs a smaller external-consumer canary from a
new temporary module. It resolves the caller exclusively through
`proxy.golang.org`, requires exact caller-tag resolution and the exact tagged
`llmkit-go`/`codexsdk-go` versions declared by `compatibility.json` with sums,
rejects module/workspace
overrides, and executes a typed call using a deterministic fake at the SDK
runner seam. The proxy propagation retry is bounded to ten minutes; subsequent
validation has its own ten-minute total and five-minute per-command bounds. Its
module graph and call evidence are retained as a workflow artifact.
