# Issue tracker: GitHub

Specs and tickets live in GitHub Issues for
`thedavidweng/immich-amazon-sync`. Use the `gh` CLI.

Until a Git remote is configured, pass
`--repo thedavidweng/immich-amazon-sync` explicitly.

## Conventions

- Publish a spec by creating an issue with `ready-for-agent`.
- Create implementation tickets referencing the parent spec issue.
- Use `--body-file` for multiline issue and PR descriptions.
- Read tickets with `gh issue view <number> --comments`.
- List tickets with `gh issue list`, including body, labels and assignee.
- Record blocking relationships using native GitHub issue dependencies.
- Claim only unassigned tickets whose blockers are all resolved.
- Assign claimed tickets to `@me`.
- Resolve tickets with implementation and validation evidence.
- Close completed tickets using `gh issue close <number>`.

If the repository or required tracker capability is unavailable,
report the blocker before changing issue state.

## Pull requests as a triage surface

PRs as a request surface: no.
