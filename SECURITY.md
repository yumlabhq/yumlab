# Security

Yumlab is a tool you hand a GitHub token to, so what it does with that token is a
fair question to ask before installing it. This page answers it, and explains how
to report a problem.

## Reporting a vulnerability

Use GitHub's private vulnerability reporting:

**https://github.com/yumlabhq/yumlab/security/advisories/new**

Please do not open a public issue for a security problem.

Include what you need to reproduce it: the version (`yumlab --version`), the
command you ran, and a workflow file that triggers it if one is involved. You can
expect a first response within a week. If a fix is needed, the advisory is
published alongside the release that carries it.

## Supported versions

Yumlab is pre-1.0. Only the latest release is supported — fixes ship in a new
release rather than as patches to older tags.

| Version | Supported |
| --- | --- |
| latest release | yes |
| anything older | no |

## What Yumlab does with your token

These are properties of the implementation, not promises of intent.

**It never writes to your repository.** No pull request, no commit, no workflow
edit, no settings change. Every GitHub API call it makes is a `GET`. This is an
architectural constraint of the project, not a default that can be flipped.

**It never reads secret values.** The GitHub API does not expose them, and Yumlab
does not ask: the secrets and variables endpoints are read purely for their
`name` fields (`internal/github/client.go`). Values are never fetched, never held
in memory, and never written to a report.

**Your token goes to one host.** It is sent only to the GitHub API endpoint you
configured — `api.github.com`, or your own host via `--api-url` /
`GITHUB_API_URL`. It is never logged, never printed in a report, and never
included in `--json` output.

**There is no backend.** No account, no server, no database, no telemetry. The
binary runs on your machine or in your CI with your token, and the only outbound
connections it makes are to the GitHub API. Workflow files are read from disk and
need no token at all.

**It runs no code from your repository.** Workflow files are parsed as data.
Yumlab does not execute steps, does not resolve or download actions, and does not
evaluate `${{ }}` expressions — it parses them to extract references
(`internal/expr`). A malicious workflow file is a parsing input, not an execution
path.

## Handling your token safely

- Prefer a fine-grained token scoped to the repositories you scan, with only the
  read permissions listed in [docs/token-and-permissions.md](docs/token-and-permissions.md).
- Pass it in the environment (`GITHUB_TOKEN`), not as `--token` on the command
  line, where it lands in your shell history and in the process list.
- In CI, store it as a secret. Note that the automatic `GITHUB_TOKEN` cannot list
  secrets, so this will be a PAT — scope it as tightly as the table allows.

## Out of scope

**Findings about your workflows are output, not vulnerabilities in Yumlab.** If
Yumlab reports a missing secret, or fails to report one, that is a bug or a
feature request — please use the [issue tracker](https://github.com/yumlabhq/yumlab/issues).
A false positive is a serious bug worth reporting loudly, but it is not a
security issue.

Also out of scope: vulnerabilities in GitHub Actions itself, and problems that
require an attacker to already control your token or your machine.
