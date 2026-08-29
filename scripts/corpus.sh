#!/usr/bin/env bash
#
# Validate the expression parser against real-world workflows.
#
# Clones the .github/workflows directory of a fixed list of public repositories
# and runs `yumlab scan --offline` on each. No token, no API call, no findings:
# the only thing being measured is whether the parser can read what people
# actually write.
#
# A clean run reports zero unreadable expressions and zero rejected files.
# Anything else is a gap in internal/expr or internal/parse, and deserves a test
# case before it is fixed.
#
#   ./scripts/corpus.sh          # the whole list
#   ./scripts/corpus.sh 10       # the first 10 only
#   CORPUS_DIR=/tmp/c ./scripts/corpus.sh
#
# The repository list is hard-coded and versioned on purpose, so two runs are
# comparable and a regression is attributable.

set -uo pipefail

# Chosen for workflow variety rather than popularity: large matrices, reusable
# workflows, OIDC, container jobs, and heavy expression use. The last three are
# the tools Yumlab sits next to, so their own workflows are fair game.
REPOS=(
  kubernetes/kubernetes
  golang/go
  rust-lang/rust
  microsoft/vscode
  facebook/react
  vercel/next.js
  denoland/deno
  home-assistant/core
  grafana/grafana
  hashicorp/terraform
  prometheus/prometheus
  elastic/elasticsearch
  angular/angular
  vuejs/core
  sveltejs/kit
  nodejs/node
  python/cpython
  django/django
  rails/rails
  symfony/symfony
  spring-projects/spring-boot
  cli/cli
  goreleaser/goreleaser
  actions/runner
  docker/compose
  traefik/traefik
  caddyserver/caddy
  gohugoio/hugo
  astral-sh/ruff
  tokio-rs/tokio
  pallets/flask
  fastapi/fastapi
  pytorch/pytorch
  huggingface/transformers
  apache/airflow
  dbt-labs/dbt-core
  supabase/supabase
  withastro/astro
  vitejs/vite
  storybookjs/storybook
  nestjs/nest
  strapi/strapi
  appwrite/appwrite
  n8n-io/n8n
  ollama/ollama
  langchain-ai/langchain
  zizmorcore/zizmor
  rhysd/actionlint
  gitleaks/gitleaks
  pre-commit/pre-commit
)

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
corpus_dir="${CORPUS_DIR:-$root/.corpus}"
binary="$root/bin/yumlab"

limit="${1:-${#REPOS[@]}}"

if ! [[ "$limit" =~ ^[0-9]+$ ]]; then
  echo "usage: $0 [number of repositories]" >&2
  exit 2
fi

echo "building yumlab"
make -C "$root" build >/dev/null || { echo "build failed" >&2; exit 2; }

mkdir -p "$corpus_dir"

# Fetch only .github/workflows. A blobless, shallow, sparse clone keeps a
# 50-repository corpus in the tens of megabytes instead of many gigabytes.
fetch() {
  local repo="$1" dest="$2"
  if [ -d "$dest/.git" ]; then
    return 0
  fi
  rm -rf "$dest"
  git clone --depth 1 --filter=blob:none --sparse \
    "https://github.com/$repo.git" "$dest" >/dev/null 2>&1 || return 1
  git -C "$dest" sparse-checkout set .github/workflows >/dev/null 2>&1 || return 1
}

total_repos=0
total_workflows=0
skipped=0
repos_with_bad_expr=0
repos_with_bad_file=0

echo
for repo in "${REPOS[@]:0:$limit}"; do
  dest="$corpus_dir/${repo//\//__}"

  if ! fetch "$repo" "$dest"; then
    printf '  %-40s clone failed, skipped\n' "$repo"
    skipped=$((skipped + 1))
    continue
  fi

  if [ ! -d "$dest/.github/workflows" ]; then
    printf '  %-40s no workflows, skipped\n' "$repo"
    skipped=$((skipped + 1))
    continue
  fi

  out="$("$binary" scan --offline --color=never "$dest" 2>&1)"
  total_repos=$((total_repos + 1))

  # "yumlab  current directory  37 workflows · offline"
  n=$(printf '%s\n' "$out" | sed -n 's/.*  \([0-9][0-9]*\) workflows\{0,1\} .*/\1/p' | head -1)
  total_workflows=$((total_workflows + ${n:-0}))

  bad_expr="$(printf '%s\n' "$out" | grep -A"${MAX_SHOWN:-20}" 'could not be parsed' | grep -E '^\s+\S+\.ya?ml:[0-9]+' || true)"
  bad_file="$(printf '%s\n' "$out" | sed -n '/Could not parse/,/^$/p' | grep -E '\.ya?ml' || true)"

  if [ -n "$bad_expr" ] || [ -n "$bad_file" ]; then
    printf '  %-40s %s workflows\n' "$repo" "${n:-0}"
    if [ -n "$bad_file" ]; then
      echo "$bad_file" | sed 's/^/      REJECTED FILE  /'
      repos_with_bad_file=$((repos_with_bad_file + 1))
    fi
    if [ -n "$bad_expr" ]; then
      echo "$bad_expr" | sed 's/^ */      UNREADABLE     /'
      repos_with_bad_expr=$((repos_with_bad_expr + 1))
    fi
  else
    printf '  %-40s %s workflows, clean\n' "$repo" "${n:-0}"
  fi
done

echo
echo "  repositories scanned   $total_repos"
echo "  workflows parsed       $total_workflows"
echo "  skipped                $skipped"
echo "  with unreadable expr   $repos_with_bad_expr"
echo "  with rejected file     $repos_with_bad_file"
echo
echo "  corpus cached in $corpus_dir (delete it to refetch)"

if [ "$repos_with_bad_expr" -gt 0 ] || [ "$repos_with_bad_file" -gt 0 ]; then
  echo
  echo "  Each line above is a gap in internal/expr or internal/parse."
  echo "  Add a case to internal/expr/expr_test.go before fixing it."
  exit 1
fi
