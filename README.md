# Yumlab

Find what will break your GitHub Actions pipeline, before you push it.

A pipeline runs for twenty minutes and dies on the last step. A missing secret, a
token without the right permission, a cache key that never hits. You fix it,
commit, and wait twenty minutes again.

Yumlab reads `.github/workflows/*.yml` and your repository configuration, and
tells you in a few seconds what is going to fail — sorted by the minutes it will
waste.

## Before

```
$ git push
# ... 18 minutes later
Run ./deploy.sh
  Error: could not assume role: empty role ARN
Error: Process completed with exit code 1
```

## After

```
$ yumlab scan

yumlab  acme/app  3 workflows · repository, organization and environments

  ✗ secrets.AWS_DEPLOY_ROLE is not defined  .github/workflows/release.yml:41
      ~16 min wasted
      Referenced from job "deploy" (environment production). Looked in: repository,
      organization, environment production.

  ✗ vars.API_BASE is not defined  .github/workflows/ci.yml:12
      ~16 min wasted
      Referenced from job "test".

UNKNOWN  3 references could not be verified
  2  cannot read organization secrets: the token needs the "Secrets" repository permission (read)
  1  the secret name is computed at runtime

2 findings  ~32 min wasted per pipeline  7 checked · 3 unverified
```

## Install

```bash
go install github.com/yumlabhq/yumlab/cmd/yumlab@latest
```

Binaries, a Homebrew formula and a Docker image are on the way.

## Use

```bash
export GITHUB_TOKEN=github_pat_...
yumlab scan                 # scan the current repository
yumlab scan ../other-repo   # scan somewhere else
yumlab scan --offline       # no network call at all
```

`yumlab scan` exits `1` when it finds something, so it gates a CI job or a
pre-commit hook without any server involved. It exits `2` on an error.

| Flag | Meaning |
| --- | --- |
| `--offline` | Run only the controls that need no network access |
| `--repo owner/name` | Override the repository detected from the `origin` remote |
| `--token` | GitHub token (default `$GITHUB_TOKEN`, then `$GH_TOKEN`) |
| `--api-url` | GitHub Enterprise API base URL (default `$GITHUB_API_URL`) |
| `--color` | `auto`, `always` or `never` |

## Permissions

**Read [docs/token-and-permissions.md](docs/token-and-permissions.md) before
anything else.** Listing a repository's secrets requires admin access on that
repository, which most developers do not have on their employer's repositories.

Yumlab is built around that. When it cannot read a scope it says so and counts
the references it could not check:

```
UNKNOWN  3 references could not be verified
  3  cannot read organization secrets: the token needs the "Secrets" repository permission (read)
```

You will never see "you are missing a secret" because Yumlab lacked the
permission to look. If you cannot get the permission, declare the names you know
in `.yumlab.yaml` — see [`.yumlab.example.yaml`](.yumlab.example.yaml).

Yumlab only ever reads, never reads secret *values*, and sends your token
nowhere but the GitHub API. [SECURITY.md](SECURITY.md) spells that out, and is
where to report a vulnerability.

## Controls

| ID | What it finds | Network |
| --- | --- | --- |
| [`ghost-secrets`](docs/controls/ghost-secrets.md) | `secrets.X` and `vars.X` referenced but defined nowhere the job can read | yes |
| [`token-permissions`](docs/controls/token-permissions.md) | a job running an action that needs a permission its `permissions:` block does not grant | no |

`token-permissions` needs no token, so `yumlab scan --offline` works in any
repository with no setup at all.

More are coming: long-lived credentials, dead caches, and job ordering based on
run history.

## Design rules

These three rules outrank feature coverage. When they conflict with adding a
check, they win.

**No model decides whether there is a problem.** Every control is a
deterministic rule you can read and audit. A confident model and a correct model
look identical in a terminal.

**UNKNOWN is a first-class answer.** When Yumlab cannot verify something, it says
so and counts it. It does not guess.

**Zero false positives.** A wrong "you are missing a secret" when the secret
exists destroys trust permanently. That is a far more expensive mistake than a
missed check. In doubt: UNKNOWN, never a finding.

And three things Yumlab does not do: it never writes to your repository, it has
no backend or account, and it sends no telemetry. The only network calls it makes
are to the GitHub API.

## Is this for you?

The usual objection is fair: *if your CI is red all the time, the problem is your
development practices, not your CI.*

If red always means a genuine failure of your code in your repositories, you are
not the user Yumlab is for — and that is a good place to be. Yumlab is for
repositories where the failure comes from configuration that cannot be verified
before the push: a secret that exists in one environment and not another, a token
whose permissions do not match what the job does, a cache that silently stopped
working.

## Development

Requires Go 1.23 or newer.

```bash
make test      # run the tests
make build     # build ./bin/yumlab
make check     # fmt, vet and test
```

The layout follows the packages: `internal/expr` parses `${{ }}` expressions,
`internal/parse` turns workflows into a positioned model, `internal/github`
reads repository state, `internal/controls` holds one file per control, and
`internal/report` renders the result. Adding a control touches no other package.

## Licence

Apache 2.0. The CLI is free and complete, permanently — including the local
score gate. Nothing already released as open source will move behind a payment.
