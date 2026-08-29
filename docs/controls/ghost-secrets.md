# `ghost-secrets` — secrets and variables referenced but never defined

**Severity:** error · **Network:** required · **Estimated cost:** 16 minutes per
missing name

## What it checks

Every `secrets.X` and `vars.X` reference in `.github/workflows/*.yml` is compared
against what actually exists in the repository.

A reference resolves against the union of the scopes the job can read:

- repository secrets and variables,
- organization secrets and variables shared with this repository,
- the secrets and variables of the deployment environment the job declares.

A finding is produced only when **every one of those scopes was read
successfully** and none of them holds the name.

## Why it costs time

A missing secret does not fail fast. GitHub substitutes an empty string, so the
step that needed it usually succeeds, and the failure surfaces later — in a
`docker push` that returns 401, in a deploy that times out, in a test suite that
gets a blank API base URL. You read the wrong logs first.

Worse, CI reveals missing secrets one at a time. Fix the first, push, wait, and
the second one appears. Each name costs a full cycle. That is why the estimate is
counted per distinct missing name and not per pull request.

## Example

```yaml
jobs:
  deploy:
    runs-on: ubuntu-latest
    environment: production
    steps:
      - run: ./deploy.sh
        env:
          AWS_ROLE: ${{ secrets.AWS_DEPLOY_ROLE }}
```

```
  ✗ secrets.AWS_DEPLOY_ROLE is not defined  .github/workflows/release.yml:41
      ~16 min wasted
      Referenced from job "deploy" (environment production). Looked in: repository,
      organization, environment production.
```

## How to fix it

Add the secret at the level the job actually reads:

- **Repository** — Settings → Secrets and variables → Actions → Repository secrets
- **Environment** — Settings → Environments → *production* → Environment secrets
- **Organization** — organization settings, and make sure the secret is shared
  with this repository

A frequent cause is a secret that exists in an environment while the job never
declares that environment. Environment secrets are invisible to a job without an
`environment:` key:

```yaml
  deploy:
    environment: production   # without this line, production secrets do not exist
```

## What it deliberately does not report

Each of these produces an `UNKNOWN` line instead of a finding.

**Computed names.** A name assembled at runtime cannot be resolved without
running the workflow:

```yaml
KEY: ${{ secrets[format('{0}_KEY', inputs.region)] }}
```

**Jobs with a computed environment.** If the environment is an expression, the
set of readable secrets is unknown:

```yaml
    environment: ${{ inputs.target }}
```

**Reusable workflows.** In a workflow with `on: workflow_call`, secrets can be
supplied by the caller. Names declared under `on.workflow_call.secrets` are
recognised as parameters and count as verified; anything else is reported as
unverified, because a caller in another repository may pass it with
`secrets: inherit`.

**Runner-provided secrets.** `GITHUB_TOKEN` and the `ACTIONS_*` runtime tokens are
injected by the runner and are always valid.

**Any scope that could not be read.** See
[token and permissions](../token-and-permissions.md).

## External secret managers

If your secrets live in HashiCorp Vault, AWS Secrets Manager, Doppler or
1Password, this control does not produce false positives on them — and the
reason is structural rather than a special case.

Nothing outside GitHub can write into the `secrets` context. An external manager
hands its values to later steps as environment variables or step outputs:

```yaml
      - uses: hashicorp/vault-action@v3
        id: vault
        with:
          method: jwt                       # OIDC: no GitHub secret at all
          secrets: secret/data/ci npm_token | NPM_TOKEN

      - run: npm publish
        env:
          NODE_AUTH_TOKEN: ${{ env.NPM_TOKEN }}          # env, not secrets
          OTHER: ${{ steps.vault.outputs.NPM_TOKEN }}    # step output
```

This control reads `secrets.` and `vars.` only. `env.`, `steps.*.outputs` and
`needs.*.outputs` are parsed but never checked against your repository, so a
value that arrives from Vault is never claimed to be missing.

The one GitHub secret a Vault setup may still have is the credential used to
authenticate to Vault itself — `secrets.VAULT_TOKEN` with token or AppRole auth.
That one is a real repository secret and is checked normally. With JWT/OIDC
there is no GitHub secret at all, and nothing for this control to say.

The consequence is worth being honest about: **on a repository fully backed by
an external manager, this control has very little to check.** The static
controls are what serve those teams — in particular
[`token-permissions`](token-permissions.md), since OIDC fails silently without
`id-token: write`.

## How to silence it legitimately

If the name exists but Yumlab cannot see it — typically an organization secret
you have no permission to list — declare it:

```yaml
# .yumlab.yaml
secrets:
  organization:
    - SLACK_WEBHOOK
```

To turn the control off entirely:

```yaml
# .yumlab.yaml
controls:
  ghost-secrets: false
```

## How the estimate is computed

`16 minutes` is `2 × 8`: one wasted run that fails, plus the re-run after the
fix, at an assumed 8 minutes per run. The assumption is a constant today; from
v0.4 Yumlab will read real run durations from the API and replace it. The
constants live in `internal/score/score.go` and are deliberately readable.
