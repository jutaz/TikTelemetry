# Branch protection (repository ruleset)

`main` is protected by a repository **ruleset** (the modern replacement for
classic branch protection), mirroring the setup used across these projects.
The canonical definition lives in [`ruleset-main.json`](./ruleset-main.json).

## What it enforces

On the default branch (`~DEFAULT_BRANCH`):

- **Pull request required** — 1 approving review, stale approvals dismissed on
  new pushes, and all review threads must be resolved before merge.
- **Required status checks** (strict — the branch must be up to date first):
  `Lint`, `Doc references`, `Unit tests`, `Vulnerability scan`,
  `Cross-compile (amd64)`, `Cross-compile (arm64)`, `Cross-compile (arm, 7)`,
  and `End-to-end (RouterOS CHR)`.
- **Linear history** — no merge commits (squash or rebase only).
- **No force-pushes** (`non_fast_forward`) and **no branch deletion**.
- Repository admins can bypass (`bypass_actors`: `RepositoryRole` 5 = admin).

Repository-level merge settings that pair with this (already applied): squash
and rebase merges only (no merge commits), auto-delete head branches after
merge.

## Applying / updating it

Rulesets require a **public repository** or a **GitHub Pro/Team/Enterprise**
plan — they cannot be created on a private repo on the free plan. Once the repo
is public (or upgraded), apply the ruleset with:

```sh
# Create it (first time):
gh api -X POST repos/jutaz/TikTelemetry/rulesets \
  --input .github/ruleset-main.json

# Update an existing ruleset (replace <id> with the ruleset id):
gh api repos/jutaz/TikTelemetry/rulesets                 # list ids
gh api -X PUT repos/jutaz/TikTelemetry/rulesets/<id> \
  --input .github/ruleset-main.json
```

If CI job names change, update the `required_status_checks` contexts in
`ruleset-main.json` to match (the context is the job's `name:`; matrix jobs
include the matrix value, e.g. `Cross-compile (amd64)`).
