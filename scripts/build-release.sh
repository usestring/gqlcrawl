#!/usr/bin/env bash

set -euo pipefail

if [[ "$#" -ne 2 ]]; then
  echo "usage: $0 <vMAJOR.MINOR.PATCH> <output-directory>" >&2
  exit 2
fi

release_tag="$1"
output_directory="$2"
if [[ ! "$release_tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "release tag must match vMAJOR.MINOR.PATCH" >&2
  exit 2
fi

version="${release_tag#v}"
mkdir -p "$output_directory"

targets=(
  linux/amd64
  linux/arm64
  darwin/amd64
  darwin/arm64
  windows/amd64
)

for target in "${targets[@]}"; do
  goos="${target%/*}"
  goarch="${target#*/}"
  artifact="gqlcrawl_${version}_${goos}_${goarch}"
  if [[ "$goos" == "windows" ]]; then
    artifact="${artifact}.exe"
  fi

  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build \
    -trimpath \
    -ldflags "-s -w -X main.version=${release_tag}" \
    -o "${output_directory}/${artifact}" \
    ./cmd/gqlcrawl
done

(
  cd "$output_directory"
  sha256sum gqlcrawl_* > checksums.txt
)
