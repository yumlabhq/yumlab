# `token-permissions` — a job missing a permission its actions require

**Severity:** error · **Network:** not required · **Estimated cost:** 16 minutes
per missing scope

This control is static. It needs no token, runs under `--offline`, and is
therefore usable in a pre-commit hook.

## What it checks

For each job, Yumlab looks at the actions it runs, and at the `permissions:`
block that governs it. When an action cannot work without a scope the block does
not grant, the job will fail — deterministically, every run.

Two GitHub rules make that provable rather than guessed:

- a job's `permissions:` block **replaces** the workflow's, it does not merge;
- naming **any** scope sets every scope that is not named to `none`.

So `permissions: contents: read` grants exactly that and nothing else. An action
needing `id-token: write` under that block will fail.

## Why it costs time

The failure message rarely names the permission. An OIDC login that cannot mint
its token fails inside the cloud provider's action, so you go and check your IAM
trust policy, your role ARN, your audience — and the problem is one missing line
in the workflow.

Scheduled jobs are the worst case. A weekly cron that opens a pull request will
fail every week without anyone watching, until someone notices the automation
stopped months ago.

## Example

```yaml
permissions:
  contents: read

jobs:
  deploy:
    steps:
      - uses: aws-actions/configure-aws-credentials@v4
        with:
          role-to-assume: arn:aws:iam::111111111111:role/deploy
```

```
  ✗ aws-actions/configure-aws-credentials needs id-token: write, which is not granted
      .github/workflows/deploy.yml:18
      ~16 min wasted
      In job "deploy", because it authenticates with OIDC, which needs a token the
      runner cannot mint without this. The workflow permissions block at
      .github/workflows/deploy.yml:9 grants contents: read, and naming any scope
      sets every other scope to none. Add id-token: write to it.
```

## How to fix it

Add the scope, at the workflow level or on the job:

```yaml
    permissions:
      id-token: write
      contents: read
```

Remember that a job-level block replaces the workflow's entirely. If you add one
to a job, restate every scope that job needs — a scope granted at the workflow
level is lost.

## What it deliberately does not report

**No `permissions:` block at all.** The effective grant then comes from a
repository or organization setting, and only the API can reveal it. That is
reported as UNKNOWN, never as a finding. Reporting "your token is too broad"
here would be a security judgement, not a claim about lost time.

**Actions handed a token other than `GITHUB_TOKEN`.** An action given a personal
access token or a GitHub App token authenticates as something else, so the
`permissions:` block says nothing about what it may do:

```yaml
      - uses: peter-evans/create-pull-request@v6
        with:
          token: ${{ secrets.MY_PAT }}     # not governed by permissions:
```

Running the corpus found several major projects doing exactly this. Every one of
them would have been a false positive.

**Actions whose needs depend on their configuration.** `actions/stale` is
deliberately absent from the table: whether it needs `issues: write` or
`pull-requests: write` depends on `days-before-issue-stale` and
`days-before-pr-stale`, which are routinely set to `-1` to disable one side.

**Actions Yumlab does not know.** An unknown action is ignored, never guessed at.

**What a `run:` script does.** Inferring intent from shell is exactly where false
positives come from. Out of scope.

**Too many permissions.** The other direction — a job granted more than it needs
— is not reported yet.

## The action table

Only actions whose requirements are certain are listed. The table lives in
`internal/controls/permissions.go` and is deliberately short: five certain
entries are worth more than thirty approximate ones.

| Action | Required | When |
| --- | --- | --- |
| `aws-actions/configure-aws-credentials` | `id-token: write` | with `role-to-assume` |
| `google-github-actions/auth` | `id-token: write` | with `workload_identity_provider` |
| `azure/login` | `id-token: write` | with `client-id` |
| `hashicorp/vault-action` | `id-token: write` | with `method` (JWT) |
| `softprops/action-gh-release` | `contents: write` | always |
| `ncipollo/release-action` | `contents: write` | always |
| `peter-evans/create-pull-request` | `contents: write`, `pull-requests: write` | always |

The OIDC entries are conditional on purpose: the same action using static access
keys needs no `id-token` at all.

## How to silence it legitimately

```yaml
# .yumlab.yaml
controls:
  token-permissions: false
```

If Yumlab is wrong about an action, that is a bug worth reporting — a false
positive here is the most expensive mistake the tool can make.

## How the estimate is computed

`16 minutes` is `2 × 8`: one wasted run plus the re-run after the fix, at an
assumed 8 minutes per run. Same model as `ghost-secrets`, deliberately: the
failure shape is identical. See `internal/score/score.go`.
