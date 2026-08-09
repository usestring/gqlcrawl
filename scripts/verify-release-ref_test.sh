#!/usr/bin/env bash

set -euo pipefail

script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
test_root="$(mktemp -d)"
trap 'rm -rf "$test_root"' EXIT

remote_repository="${test_root}/remote.git"
source_repository="${test_root}/source"
runner_repository="${test_root}/runner"

git init --bare --initial-branch=main "$remote_repository" >/dev/null
git init --initial-branch=main "$source_repository" >/dev/null
git -C "$source_repository" config user.name "Release Test"
git -C "$source_repository" config user.email "release-test@example.invalid"
printf '%s\n' fixture > "${source_repository}/fixture.txt"
git -C "$source_repository" add fixture.txt
git -C "$source_repository" commit -m "Add release fixture" >/dev/null
git -C "$source_repository" remote add origin "$remote_repository"

annotated_tag="v1.2.3"
git -C "$source_repository" tag --annotate "$annotated_tag" --message "$annotated_tag"
git -C "$source_repository" push origin main "refs/tags/${annotated_tag}" >/dev/null
release_commit="$(git -C "$source_repository" rev-parse HEAD)"

mkdir -p "${source_repository}/scripts"
cp "$script_directory/verify-release-ref.sh" "${source_repository}/scripts/verify-release-ref.sh"
printf '%s\n' next >> "${source_repository}/fixture.txt"
git -C "$source_repository" add fixture.txt scripts/verify-release-ref.sh
git -C "$source_repository" commit -m "Add release recovery source" >/dev/null
git -C "$source_repository" push origin main >/dev/null

git clone "$remote_repository" "$runner_repository" >/dev/null 2>&1
git -C "$runner_repository" update-ref "refs/tags/${annotated_tag}" "$release_commit"
test "$(git -C "$runner_repository" cat-file -t "refs/tags/${annotated_tag}")" = "commit"
git -C "$runner_repository" checkout --detach "$release_commit" >/dev/null 2>&1
tag_push_commit="$(
  cd "$runner_repository"
  "$script_directory/verify-release-ref.sh" "$annotated_tag"
)"
test "$tag_push_commit" = "$release_commit"
test "$(git -C "$runner_repository" cat-file -t "refs/tags/${annotated_tag}")" = "tag"

git -C "$runner_repository" checkout --detach origin/main >/dev/null 2>&1
git -C "$runner_repository" update-ref "refs/tags/${annotated_tag}" "$release_commit"
test "$(git -C "$runner_repository" cat-file -t "refs/tags/${annotated_tag}")" = "commit"
recovery_commit="$(
  cd "$runner_repository"
  ./scripts/verify-release-ref.sh "$annotated_tag"
)"
test "$recovery_commit" = "$release_commit"
git -C "$runner_repository" checkout --detach "$recovery_commit" >/dev/null 2>&1
test "$(git -C "$runner_repository" rev-parse --verify 'HEAD^{commit}')" = "$release_commit"
test ! -e "${runner_repository}/scripts/verify-release-ref.sh"

invalid_tag="release-1.2.3"
if (
  cd "$runner_repository"
  "$script_directory/verify-release-ref.sh" "$invalid_tag"
) >"${test_root}/invalid.out" 2>&1; then
  echo "expected invalid tag verification to fail" >&2
  exit 1
fi
grep -F "release tag must match vMAJOR.MINOR.PATCH" "${test_root}/invalid.out" >/dev/null

lightweight_tag="v1.2.4"
git -C "$source_repository" tag "$lightweight_tag"
git -C "$source_repository" push origin "refs/tags/${lightweight_tag}" >/dev/null
if (
  cd "$runner_repository"
  "$script_directory/verify-release-ref.sh" "$lightweight_tag"
) >"${test_root}/lightweight.out" 2>&1; then
  echo "expected lightweight tag verification to fail" >&2
  exit 1
fi
grep -F "release tag must be annotated: ${lightweight_tag}" "${test_root}/lightweight.out" >/dev/null

git -C "$source_repository" switch --detach "$release_commit" >/dev/null 2>&1
printf '%s\n' side > "${source_repository}/side.txt"
git -C "$source_repository" add side.txt
git -C "$source_repository" commit -m "Add side release" >/dev/null
side_tag="v1.2.5"
git -C "$source_repository" tag --annotate "$side_tag" --message "$side_tag"
git -C "$source_repository" push origin "refs/tags/${side_tag}" >/dev/null
git -C "$runner_repository" fetch --no-tags origin "refs/tags/${side_tag}" >/dev/null
git -C "$runner_repository" checkout --detach 'FETCH_HEAD^{commit}' >/dev/null 2>&1
if (
  cd "$runner_repository"
  "$script_directory/verify-release-ref.sh" "$side_tag"
) >"${test_root}/off-main.out" 2>&1; then
  echo "expected off-main release verification to fail" >&2
  exit 1
fi
grep -F "release commit is not on origin/main:" "${test_root}/off-main.out" >/dev/null
