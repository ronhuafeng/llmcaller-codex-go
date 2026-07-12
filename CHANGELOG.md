# Changelog

All notable changes to this project should be documented in this file.

This project follows Semantic Versioning.

## [Unreleased]

## [0.3.0-rc.1] - 2026-07-13

- Defined the normative schema-equivalence and fail-closed contract, expanded
  the compatibility matrix with same-named public-boundary tests, and documented
  decoded-value and application-semantic limitations without promising byte
  identity.
- Replaced handwritten schema null-admission analysis with draft-compatible
  validator probes that preserve JSON values and fail closed before runner
  invocation when a property schema cannot be compiled or resolved.
- Enforced the named read-only ephemeral profile before every runner call by
  rejecting conflicting defaults, normalizing unset safety fields, and
  reapplying the requested policy while retaining effective-policy checks.
- Adapted the caller and complete three-layer canary to the concrete SDK root
  client while preserving the narrow consumer-owned `ThreadRunner` boundary.
- Strengthened canary coverage for accepted evidence after transport failure,
  pending and live notification ordering, attribution, shutdown, and first-cause
  preservation.
- Requires `llmkit-go v0.3.0-rc.1` and `codexsdk-go v0.3.0-rc.1` from their
  published module tags.

## [0.2.0] - 2026-07-11

- Replaced projected Codex options with exact `StartThreadRunRequest` defaults
  and a minimal consumer-owned `ThreadRunner` interface.
- Added exact detailed and streaming paths while keeping `Call` as a neutral
  projection with immutable provider details and partial-result preservation.
- Added effective-model reroute and total-token-usage projection.
- Made the read-only ephemeral profile enforce exact thread/turn sandbox,
  approval, and ephemeral facts.
- Changed structured schema handling to preserve unknown JSON values and reject
  optional non-nullable, external, unresolved, dynamic, and cyclic references.
- Added a canonical handwritten API allowlist, three-layer compiled example,
  migration guide, compatibility matrix, and cross-repository release gates.
- Requires `llmkit-go v0.2.0` and `codexsdk-go v0.2.0`.

## [0.1.0] - 2026-06-11

- Initial public release.
- Initial Codex caller adapter for `llmkit-go`.
- Added Codex structured output schema normalization before thread start.
- Added boundary tests for adapter dependencies.
- Added open-source project documentation for installation, quick start,
  failure semantics, security boundaries, compatibility, release, support, code
  of conduct, issue templates, pull request template, and third-party notices.
- Added GitHub Actions CI for `gofmt`, `go vet`, and `go test ./...`.
- Added Dependabot configuration for Go modules and GitHub Actions.
