#!/usr/bin/env bash
set -euo pipefail

usage() {
    echo "usage: $0 OUTPUT_DIR REVISION VERSION" >&2
    exit 2
}

[[ $# -eq 3 ]] || usage
OUTPUT_DIR="$1"
REVISION="$2"
VERSION="$3"

[[ "$REVISION" =~ ^[0-9a-f]{40}$ ]] || {
    echo "revision must be a full lowercase Git SHA" >&2
    exit 1
}
[[ -n "$VERSION" ]] || {
    echo "version must not be empty" >&2
    exit 1
}

specs=(
    "amd64|linux/amd64|novaveil-linux-amd64.tar"
    "386|linux/386|novaveil-linux-386.tar"
    "arm64|linux/arm64|novaveil-linux-arm64.tar"
    "arm-v7|linux/arm/v7|novaveil-linux-arm-v7.tar"
)

rm -rf "$OUTPUT_DIR"
mkdir -p "$OUTPUT_DIR"
printf '%s\n' "$REVISION" >"$OUTPUT_DIR/SOURCE_SHA"
printf '%s\n' "$VERSION" >"$OUTPUT_DIR/IMAGE_VERSION"
: >"$OUTPUT_DIR/IMAGES.tsv"

for spec in "${specs[@]}"; do
    IFS='|' read -r slug platform archive <<<"$spec"
    binary="build/docker/${platform}/novaveil"
    [[ -s "$binary" ]] || {
        echo "missing Docker binary for ${platform}: ${binary}" >&2
        exit 1
    }
    local_ref="novaveil-candidate:${slug}"
    docker buildx build \
        --platform "$platform" \
        --file scripts/dockerfile/Dockerfile \
        --tag "$local_ref" \
        --label org.opencontainers.image.title=novaveil \
        --label org.opencontainers.image.source=https://github.com/kingsunb/NovaVeil \
        --label "org.opencontainers.image.revision=${REVISION}" \
        --label "org.opencontainers.image.version=${VERSION}" \
        --provenance=false \
        --output "type=docker,dest=${OUTPUT_DIR}/${archive}" \
        .
    printf '%s\t%s\t%s\t%s\n' "$slug" "$platform" "$archive" "$local_ref" >>"$OUTPUT_DIR/IMAGES.tsv"
done

(
    cd "$OUTPUT_DIR"
    sha256sum novaveil-linux-amd64.tar novaveil-linux-386.tar \
        novaveil-linux-arm64.tar novaveil-linux-arm-v7.tar >ARCHIVES.sha256
    sha256sum -c ARCHIVES.sha256
)

echo "image archives built: ${OUTPUT_DIR}"
