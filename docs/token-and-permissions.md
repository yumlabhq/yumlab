# Token and permissions

This is the page to read first. Yumlab reads your workflow files from disk — that
needs no token at all — but confirming that a secret *exists* requires asking the
GitHub API, and that API is guarded.

The short version: **listing secrets requires admin access on the repository.**
Most developers do not have it on their employer's repositories. Yumlab is
designed around that fact rather than against it.

## What happens when a permission is missing

Yumlab never treats "I could not read this" as "this does not exist". A scope it
cannot read produces an `UNKNOWN` line with a count, not a finding:

```
UNKNOWN  3 references could not be verified
  3  cannot read organization secrets: the token needs the "Secrets" repository
     permission (read)
```

You will never see `secrets.NPM_TOKEN is not defined` because Yumlab lacked the
permission to look. That rule is the reason the tool is worth trusting.

## Creating a token

Yumlab reads the token from `--token`, then `GITHUB_TOKEN`, then `GH_TOKEN`.

```bash
export GITHUB_TOKEN=github_pat_...
yumlab scan
```

### Fine-grained personal access token (recommended)

Create one at **Settings → Developer settings → Personal access tokens →
Fine-grained tokens**, scoped to the repositories you want to scan.

| Repository permission | Access | What it unlocks |
| --- | --- | --- |
| Metadata | Read | Required by GitHub for every other permission |
| Secrets | Read | Repository secrets, and organization secrets shared with the repository |
| Variables | Read | Repository and organization variables |
| Environments | Read | The list of deployment environments |

Granting **Secrets: read** requires you to be an administrator of the repository.
There is no lesser permission that lists secret names.

### Classic personal access token

| Scope | What it unlocks |
| --- | --- |
| `repo` | Repository secrets and variables, environments |
| `admin:org` | Organization secrets and variables, read directly at the organization level |

Prefer fine-grained tokens. A classic `repo` token can do far more than Yumlab
needs, and Yumlab only ever reads.

### In GitHub Actions

The automatic `GITHUB_TOKEN` **cannot list secrets**, whatever its `permissions`
block says. Running Yumlab in CI needs a PAT stored as a secret:

```yaml
- name: Scan workflows
  env:
    GITHUB_TOKEN: ${{ secrets.YUMLAB_TOKEN }}
  run: yumlab scan
```

This is the nominal mode. An administrator grants the permission once, and the
whole team gets the check on every pull request. Local scanning is the discovery
mode.

## What each control needs

| Control | Network | Permissions | Without them |
| --- | --- | --- | --- |
| [`ghost-secrets`](controls/ghost-secrets.md) | yes | Secrets: read, Variables: read, Environments: read | Every affected reference becomes `UNKNOWN`; no finding is produced |

Controls that need the network never run under `--offline`, and never will in a
pre-commit hook.

## Organization secrets

Yumlab reads organization secrets through
`GET /repos/{owner}/{repo}/actions/organization-secrets`, which lists the
organization secrets **shared with this repository**. Two consequences:

- It only needs repository-level `Secrets: read`, not `admin:org`.
- It is more accurate than listing every organization secret. An organization
  secret that exists but is not granted to this repository is unusable in its
  workflows, so counting it would hide a real failure.

## When you cannot get the permission

Declare the names you know about in `.yumlab.yaml` at the repository root:

```yaml
secrets:
  organization:
    - NPM_TOKEN
    - SLACK_WEBHOOK
variables:
  organization:
    - AWS_REGION
```

Declared names are treated exactly like names read from the API — but only to
prove that something **exists**. Declaring names never makes an unreadable scope
readable, so it can never cause a false "missing secret".

Only names go in this file. Never values.

## GitHub Enterprise

Point Yumlab at your instance with `--api-url` or `GITHUB_API_URL`:

```bash
yumlab scan --api-url https://github.example.com/api/v3
```

## What Yumlab never does

- It never writes to your repository: no pull request, no workflow edit, no
  commit.
- It never reads a secret's value. The API only exposes names, and names are all
  Yumlab asks for.
- It makes no network call other than to the GitHub API. There is no telemetry
  and no backend.
