# Three-layer canary

The canary exercises `llmkit-go -> llmcaller-codex-go -> codexsdk-go` through
the SDK's real subprocess JSON-RPC transport and a deterministic fake
app-server. It does not use a workspace, replacement, or local module source.

Released module evidence:

- `github.com/ronhuafeng/llmkit-go v0.3.0`
  `h1:jZsUK5xgGvn5Cy+ojdPCl0elgc76qRwSsEcMVJPtHvA=`
- `github.com/ronhuafeng/codexsdk-go v0.3.0`
  `h1:5ThbXqdTStCAq6dATHZu19ikSxzClq/6LlakjJd8Lpo=`

`TestThreeLayerCanaryFast` is the normal CI subset. The full invariant suite is
`LLMCALLER_FULL_CANARY=1 go test ./llmcaller/codex -run '^TestThreeLayerCanary'`
and runs only from the release/manual workflow.
