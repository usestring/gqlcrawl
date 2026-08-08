#!/usr/bin/env bash

set -euo pipefail

if [[ "$#" -ne 1 ]]; then
  echo "usage: $0 <vMAJOR.MINOR.PATCH>" >&2
  exit 2
fi

release_tag="$1"
if [[ ! "$release_tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "release tag must match vMAJOR.MINOR.PATCH" >&2
  exit 2
fi

tag_ref="refs/tags/${release_tag}"
main_ref="refs/remotes/origin/main"

git fetch --force --no-tags origin "${tag_ref}:${tag_ref}"
git fetch --no-tags origin "refs/heads/main:${main_ref}"

if [[ "$(git cat-file -t "$tag_ref")" != "tag" ]]; then
  echo "release tag must be annotated: ${release_tag}" >&2
  exit 1
fi

release_commit="$(git rev-parse --verify "${tag_ref}^{commit}")"
checked_out_commit="$(git rev-parse --verify 'HEAD^{commit}')"
if [[ "$release_commit" != "$checked_out_commit" ]]; then
  echo "release tag does not resolve to the checked-out commit: ${release_tag}" >&2
  exit 1
fi

if ! git merge-base --is-ancestor "$release_commit" "$main_ref"; then
  echo "release commit is not on origin/main: ${release_commit}" >&2
  exit 1
fi
