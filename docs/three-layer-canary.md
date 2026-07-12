# Three-layer canary

The canary exercises `llmkit-go -> llmcaller-codex-go -> codexsdk-go` through
the SDK's real subprocess JSON-RPC transport and a deterministic fake
app-server. It does not use a workspace, replacement, or local module source.

Released module evidence:

- `github.com/ronhuafeng/llmkit-go v0.3.0-rc.1`
  `h1:5ILhbW3xyyRHIhc+TyTZn6Xn1p0M2v7ZLiR6Km0zHvE=`
- `github.com/ronhuafeng/codexsdk-go v0.3.0-rc.2`
  `h1:TYdvhQKbfw7eO6fT7jYCqxWM2BpdTXfP+Yn0RK9JWx0=`

`TestThreeLayerCanaryFast` is the normal CI subset. The full invariant suite is
`LLMCALLER_FULL_CANARY=1 go test ./llmcaller/codex -run '^TestThreeLayerCanary'`
and runs only from the release/manual workflow.
