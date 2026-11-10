#!/usr/bin/env bash
set -euo pipefail

usage() {
    echo "usage: $0 IMAGE_ARCHIVE_DIR" >&2
    exit 2
}

[[ $# -eq 1 ]] || usage
IMAGE_DIR="$1"
command -v trivy >/dev/null 2>&1 || {
    echo "trivy is required to verify image archives" >&2
    exit 1
}

expected_manifest=$'amd64\tlinux/amd64\tnovaveil-linux-amd64.tar\tnovaveil-candidate:amd64\n386\tlinux/386\tnovaveil-linux-386.tar\tnovaveil-candidate:386\narm64\tlinux/arm64\tnovaveil-linux-arm64.tar\tnovaveil-candidate:arm64\narm-v7\tlinux/arm/v7\tnovaveil-linux-arm-v7.tar\tnovaveil-candidate:arm-v7'
actual_manifest="$(cat "$IMAGE_DIR/IMAGES.tsv")"
[[ "$actual_manifest" == "$expected_manifest" ]] || {
    echo "unexpected image archive manifest" >&2
    diff -u <(printf '%s\n' "$expected_manifest") "$IMAGE_DIR/IMAGES.tsv" >&2 || true
    exit 1
}

revision="$(cat "$IMAGE_DIR/SOURCE_SHA")"
version="$(cat "$IMAGE_DIR/IMAGE_VERSION")"
[[ "$revision" =~ ^[0-9a-f]{40}$ ]] || {
    echo "invalid SOURCE_SHA" >&2
    exit 1
}
[[ -n "$version" ]] || {
    echo "IMAGE_VERSION is empty" >&2
    exit 1
}

# Detect the runner's native platform to avoid QEMU-emulated container smoke tests.
# QEMU user-mode emulation of non-native Go binaries is too slow for the container
# runtime to start within CI timeouts (observed: >85 min for linux/386 and
# linux/arm/v7 on an amd64 runner). Trivy scans and image metadata verification
# still run for ALL architectures; only the container-based smoke test is skipped
# for non-native platforms.
native_platform=""
case "$(uname -m)" in
    x86_64)  native_platform="linux/amd64" ;;
    aarch64) native_platform="linux/arm64" ;;
esac

(
    cd "$IMAGE_DIR"
    sha256sum -c ARCHIVES.sha256
)

: >"$IMAGE_DIR/IMAGE_IDS.tsv"
while IFS=$'\t' read -r slug platform archive local_ref; do
    archive_path="$IMAGE_DIR/$archive"
    [[ -s "$archive_path" ]] || {
        echo "missing image archive: $archive_path" >&2
        exit 1
    }

    echo "verify: scanning ${slug} (${platform})"
    trivy image \
        --input "$archive_path" \
        --scanners vuln \
        --pkg-types os,library \
        --severity HIGH,CRITICAL \
        --ignore-unfixed \
        --exit-code 1 \
        --no-progress \
        --format table
    trivy image \
        --input "$archive_path" \
        --scanners vuln \
        --format cyclonedx \
        --output "$IMAGE_DIR/sbom-${slug}.cdx.json" \
        --no-progress

    echo "verify: loading ${slug}"
    docker image rm "$local_ref" >/dev/null 2>&1 || true
    docker load --input "$archive_path" >/dev/null

    os="$(docker image inspect "$local_ref" --format '{{.Os}}')"
    arch="$(docker image inspect "$local_ref" --format '{{.Architecture}}')"
    variant="$(docker image inspect "$local_ref" --format '{{.Variant}}')"
    case "$platform" in
        linux/amd64) [[ "$os/$arch" == "linux/amd64" && -z "$variant" ]] ;;
        linux/386) [[ "$os/$arch" == "linux/386" && -z "$variant" ]] ;;
        linux/arm64) [[ "$os/$arch" == "linux/arm64" && -z "$variant" ]] ;;
        linux/arm/v7) [[ "$os/$arch/$variant" == "linux/arm/v7" ]] ;;
        *) echo "unsupported platform in manifest: $platform" >&2; exit 1 ;;
    esac

    [[ "$(docker image inspect "$local_ref" --format '{{index .Config.Labels "org.opencontainers.image.revision"}}')" == "$revision" ]]
    [[ "$(docker image inspect "$local_ref" --format '{{index .Config.Labels "org.opencontainers.image.version"}}')" == "$version" ]]
    [[ "$(docker image inspect "$local_ref" --format '{{index .Config.Labels "org.opencontainers.image.source"}}')" == "https://github.com/kingsunb/NovaVeil" ]]
    image_id="$(docker image inspect "$local_ref" --format '{{.Id}}')"
    printf '%s\t%s\t%s\t%s\t%s\n' "$slug" "$platform" "$archive" "$local_ref" "$image_id" >>"$IMAGE_DIR/IMAGE_IDS.tsv"

    if [ -n "${native_platform}" ] && [ "${platform}" = "${native_platform}" ]; then
        echo "verify: smoke testing ${slug}"
        if command -v timeout >/dev/null 2>&1; then
            timeout 600 scripts/smoke-test-image.sh "$local_ref" "novaveil-${slug}-smoke" "$platform"
        else
            scripts/smoke-test-image.sh "$local_ref" "novaveil-${slug}-smoke" "$platform"
        fi
    else
        echo "Skipping smoke test for ${platform} (non-native; QEMU emulation too slow for container runtime)"
    fi
done <"$IMAGE_DIR/IMAGES.tsv"

(
    cd "$IMAGE_DIR"
    sha256sum \
        SOURCE_SHA IMAGE_VERSION IMAGES.tsv IMAGE_IDS.tsv ARCHIVES.sha256 \
        novaveil-linux-amd64.tar novaveil-linux-386.tar \
        novaveil-linux-arm64.tar novaveil-linux-arm-v7.tar \
        sbom-amd64.cdx.json sbom-386.cdx.json \
        sbom-arm64.cdx.json sbom-arm-v7.cdx.json \
        >PUBLISH.sha256
    sha256sum -c PUBLISH.sha256
)

echo "image archives scanned; smoke tested ${native_platform:-none}: ${IMAGE_DIR}"
