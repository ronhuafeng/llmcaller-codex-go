# Security Policy

## Supported Versions

Security fixes are provided for the latest released minor version. Before
`v1.0.0`, support is best-effort and focused on the latest tag.

## Reporting A Vulnerability

Please report suspected vulnerabilities through GitHub private vulnerability
reporting:

https://github.com/ronhuafeng/llmcaller-codex-go/security/advisories/new

If private vulnerability reporting is unavailable, open a public issue asking
for a private reporting channel. Do not include vulnerability details, exploit
steps, credentials, private file names, or other sensitive information in the
public issue.

A useful report includes:

- affected version or commit;
- reproduction steps or a minimal proof of concept;
- expected impact;
- whether credentials, private files, command execution, or approval handling
  are involved.

## Security Boundaries

`llmcaller-codex-go` does not read credentials or start processes directly. It
uses the `codexsdk.ThreadClient` supplied by the application.

Applications are responsible for:

- Codex authentication and account configuration;
- the app-server command used by `codexsdk-go`;
- the working directory exposed to Codex;
- approval policy and server request handling for file edits, shell commands,
  and other tool calls;
- deciding whether model output is trusted enough for their business domain.

For least privilege, pass the smallest practical working directory and use
`codexsdk.ApprovalPolicyNever` when the call should be read-only.
