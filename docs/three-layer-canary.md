# Three-layer canary

The canary exercises `llmkit-go -> llmcaller-codex-go -> codexsdk-go` through
the SDK's real subprocess JSON-RPC transport and a deterministic fake
app-server. It does not use a workspace, replacement, or local module source.

Released module evidence:

- `github.com/ronhuafeng/llmkit-go v0.2.0`
  `h1:Mc7Flrhf3Q4oDMYLTZpXd1fuTLhd47NYh5LdgdnCl0Y=`
- `github.com/ronhuafeng/codexsdk-go v0.2.1`
  `h1:En46l12WoC535gzMWCLdj0YrrkfhZiNgT7eeYoo6Dy0=`

`TestThreeLayerCanaryFast` is the normal CI subset. The full invariant suite is
`LLMCALLER_FULL_CANARY=1 go test ./llmcaller/codex -run '^TestThreeLayerCanary'`
and runs only from the release/manual workflow.
