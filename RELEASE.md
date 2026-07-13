# Release Checklist

Use this checklist for tagged releases.

## Before Tagging

- Confirm `README.md` describes the current public API and examples.
- Confirm `CHANGELOG.md` has an entry for the release.
- Confirm `THIRD_PARTY_NOTICES.md` matches `GOWORK=off go list -m all`.
- Confirm `compatibility.json` matches the resolved `go.mod` module graph and
  names the active API, schema, clean-consumer, and canary gates.
- Confirm the handwritten API allowlist test passes without update mode.
- Run the complete schema compatibility matrix.
- Run the complete three-layer canary.
- Run the proxy-backed clean real-tag consumer with no `replace`, `exclude`,
  `go.work`, or pseudo-version.
- Run `gofmt -w llmcaller internal`.
- Run `go vet ./...`.
- Run `go test ./...`.
- Run `GOWORK=off go test ./...`.
- Run `GOWORK=off go test -race ./...`.
- Search for private paths, credentials, fixtures, and business data:

```sh
rg -n "(/Users/|/home/[^/]+/|C:\\\\Users\\\\|BEGIN .*PRIVATE KEY|AKIA|OPENAI_API_KEY|SECRET|TOKEN|PASSWORD)" .
```

- Confirm CI is green on the release commit.

## Tagging

Use Semantic Versioning:

```sh
version=v0.3.0 # replace with the new release version
git tag -a "$version" -m "$version"
git push origin "$version"
```

For `v0.x`, document breaking changes in the changelog. For `v1.0.0` and
later, breaking exported API changes require a new major version.

## After Tagging

- Create a GitHub release from the tag.
- Include changelog highlights, compatibility notes, and any migration steps.
- Verify the module is available through the Go module proxy.
- Open a follow-up issue for any deferred cleanup discovered during release.

## Repository Settings

Before making the repository public, enable GitHub branch protection, Dependabot
alerts/security updates, private vulnerability reporting, and secret scanning.
Add CodeQL or OpenSSF Scorecard workflows once those features are available for
the repository visibility and organization plan.
