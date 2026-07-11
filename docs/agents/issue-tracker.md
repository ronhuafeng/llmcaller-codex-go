# Issue tracker: GitHub

Issues and PRDs for this repository live in GitHub Issues. Use the `gh` CLI
from this checkout so the repository is inferred from `origin`.

## Conventions

- Read an issue with `gh issue view <number> --comments` and include labels.
- Create an issue with `gh issue create --title "..." --body "..."`.
- Comment with `gh issue comment <number> --body "..."`.
- Apply or remove labels with `gh issue edit`.
- Close an issue with `gh issue close <number> --comment "..."`.
- Resolve an ambiguous `#<number>` as a pull request first, then as an issue.

## Pull requests as a triage surface

**PRs as a request surface: no.**

## When a skill requests a ticket

Fetch the relevant GitHub issue, including its comments and labels, with `gh`.
