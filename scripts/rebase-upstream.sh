#!/usr/bin/env bash
set -euo pipefail

upstream_remote="${UPSTREAM_REMOTE:-upstream}"
upstream_branch="${UPSTREAM_BRANCH:-main}"
current_branch="$(git symbolic-ref --quiet --short HEAD)"
upstream_ref="${upstream_remote}/${upstream_branch}"

if [ -n "$(git status --porcelain)" ]; then
  echo "Refusing to rebase a dirty worktree."
  echo "Create an isolated worktree first so local and untracked files stay untouched."
  exit 1
fi

if ! git remote get-url "$upstream_remote" >/dev/null 2>&1; then
  echo "Missing git remote: $upstream_remote"
  exit 1
fi

stamp="$(date +%Y%m%d-%H%M%S)"
backup_branch="backup/${current_branch//\//-}-before-upstream-${stamp}"

echo "Fetching ${upstream_ref}..."
git fetch "$upstream_remote" "$upstream_branch"

old_tip="$(git rev-parse HEAD)"
git branch "$backup_branch" "$old_tip"
echo "Backup: ${backup_branch} (${old_tip})"

echo "Rebasing ${current_branch} onto ${upstream_ref} with merge topology preserved..."
git rebase --rebase-merges "$upstream_ref"

echo "Checking whitespace and unresolved conflict markers..."
git diff --check "$upstream_ref"...HEAD
if git grep -n -E '^(<<<<<<<|=======|>>>>>>>)' -- ':!pnpm-lock.yaml'; then
  echo "Conflict markers remain in tracked files."
  exit 1
fi

echo "Range diff against the pre-rebase PATCH stack:"
git range-diff "${upstream_ref}...${backup_branch}" "${upstream_ref}...HEAD"

echo "Rebase complete. Before integration, follow docs/fork-patches.md verification checklist."
