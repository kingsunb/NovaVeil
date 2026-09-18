#!/bin/bash
set -euo pipefail

readonly APP_NAME="novaveil" # 发布产物和容器内的可执行文件名。
readonly OUTPUT_DIR="build" # 所有构建、归档、许可证和容器输入的根目录。
readonly DEFAULT_TARGET="linux/amd64" # 本地默认只构建 Docker/服务器最常用架构。
readonly DEFAULT_VERSION="$(git describe --tags --abbrev=0 2>/dev/null || echo 'dev')"
readonly VERSION="${VERSION:-${DEFAULT_VERSION}}" # CI 可传入已验证版本, 避免提前推送 tag。
readonly COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo 'unknown')" # 当前提交短哈希。
readonly BUILD_TIME="$(TZ='Asia/Shanghai' date +'%F %T %z')"
readonly LDFLAGS="-X 'github.com/kingsunb/NovaVeil/internal/conf.Version=${VERSION}' \
                  -X 'github.com/kingsunb/NovaVeil/internal/conf.BuildTime=${BUILD_TIME}' \
                  -X 'github.com/kingsunb/NovaVeil/internal/conf.Author=Kingsun' \
                  -X 'github.com/kingsunb/NovaVeil/internal/conf.Commit=${COMMIT}' \
                  -s -w" # 注入版本信息并缩小发布二进制。

TARGETS="${DEFAULT_TARGET}"
FRONTEND=1
ANDROID=0
ARCHIVE=1
LICENSES=1

usage() {
    cat <<'USAGE'
Usage: scripts/build.sh [options]

Options:
  --targets LIST       Comma-separated GOOS/GOARCH list, e.g. linux/amd64,darwin/arm64.
                       Use "all" for the non-Android release matrix. Default: linux/amd64.
  --include-android    Add all four Android release targets. Requires ANDROID_NDK_HOME;
                       the official release workflow always enables this flag.
  --local              Local test build: skip licenses and zip archives.
                       Same frontend + Go binary as a release, without
                       publish artifacts. Pair with scripts/run-local.sh.
  --skip-frontend      Do not rebuild frontend assets before Go build.
  --skip-licenses      Do not generate third-party license files.
  --no-archive         Build binaries only, no zip archives or SHA256SUMS.
  -h, --help           Show this help.

Environment:
  VERSION              Override release version embedded into binaries.
  GOPROXY              Go module proxy. In restricted networks set e.g.
                       GOPROXY=https://goproxy.cn,direct
  GOROOT / PATH        Point at a Go 1.26+ toolchain; build.sh fails fast
                       when the active Go is older than go.mod requires.
  ANDROID_NDK_HOME     Required only with --include-android.
USAGE
}

while [ $# -gt 0 ]; do
    case "$1" in
        --targets)
            TARGETS="${2:?--targets requires a value}"
            shift 2
            ;;
        --targets=*)
            TARGETS="${1#*=}"
            shift
            ;;
        --include-android)
            ANDROID=1
            shift
            ;;
        --local)
            LICENSES=0
            ARCHIVE=0
            shift
            ;;
        --skip-frontend)
            FRONTEND=0
            shift
            ;;
        --skip-licenses)
            LICENSES=0
            shift
            ;;
        --no-archive)
            ARCHIVE=0
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "unknown option: $1" >&2
            usage >&2
            exit 2
            ;;
    esac
done

readonly -a STANDARD_TARGETS=(
    "linux/amd64"
    "linux/arm64"
    "linux/arm"
    "linux/386"
    "windows/amd64"
    "darwin/arm64"
    "darwin/amd64"
) # 不依赖 cgo 的固定发布矩阵。
readonly -a ANDROID_TARGETS=(
    "android/amd64:x86_64-linux-android21-clang"
    "android/arm64:aarch64-linux-android21-clang"
    "android/arm:armv7a-linux-androideabi21-clang"
    "android/386:i686-linux-android21-clang"
) # Android API 21 的固定 ABI 与 NDK clang 映射。

declare -a RESOLVED_TARGETS=()
if [ "${TARGETS}" = "all" ]; then
    RESOLVED_TARGETS=("${STANDARD_TARGETS[@]}")
else
    IFS=',' read -r -a RESOLVED_TARGETS <<<"${TARGETS}"
fi

if [ "${ANDROID}" -eq 1 ]; then
    : "${ANDROID_NDK_HOME:?ANDROID_NDK_HOME is required when --include-android is used}"
fi
if [ "${LICENSES}" -eq 0 ] && [ "${ARCHIVE}" -eq 1 ]; then
    echo "--skip-licenses requires --no-archive; release archives must include real license reports" >&2
    exit 2
fi

# 校验本地 Go 工具链满足 go.mod 的 1.26 要求。旧工具链（如 1.22）要到构建末端才会
# 报 crypto/sha3、iter 等「is not in std」的误导性错误；这里提前拦截并给可操作提示。
check_go_toolchain() {
    if ! command -v go >/dev/null 2>&1; then
        echo "go not found on PATH; go.mod requires Go 1.26+" >&2
        exit 2
    fi
    local version major minor
    version="$(go env GOVERSION 2>/dev/null || true)" # 形如 go1.26.7
    version="${version#go}"
    major="${version%%.*}"
    minor="${version#*.}"; minor="${minor%%.*}"
    # 非稳定版本（devel 等）交给 go build 自行判断，不在这里阻塞。
    if [[ "${major}" =~ ^[0-9]+$ ]] && [[ "${minor}" =~ ^[0-9]+$ ]]; then
        if [ "${major}" -lt 1 ] || { [ "${major}" -eq 1 ] && [ "${minor}" -lt 26 ]; }; then
            echo "go.mod requires Go 1.26+; found \"$(go version)\" (GOROOT=$(go env GOROOT))" >&2
            echo "Install Go 1.26+ and re-run with PATH/GOROOT pointing at it, e.g.:" >&2
            echo "  GOROOT=/path/to/go PATH=/path/to/go/bin:\$PATH bash scripts/build.sh" >&2
            exit 2
        fi
    fi
}

check_go_toolchain

validate_target() {
    local target="$1"
    case "${target}" in
        linux/amd64|linux/arm64|linux/arm|linux/386|windows/amd64|darwin/arm64|darwin/amd64)
            return 0
            ;;
        *)
            echo "unsupported target: ${target}" >&2
            return 1
            ;;
    esac
}

binary_name_for_target() {
    local target="$1"
    local os arch
    IFS=/ read -r os arch <<<"${target}"
    echo "${APP_NAME}-${os}-${arch}"
}

docker_platform_dir() {
    local target="$1"
    case "${target}" in
        linux/amd64) echo "linux/amd64" ;;
        linux/386) echo "linux/386" ;;
        linux/arm) echo "linux/arm/v7" ;;
        linux/arm64) echo "linux/arm64" ;;
        *) return 1 ;;
    esac
}

build_frontend() {
    echo "Building frontend from a clean output directory"
    rm -rf static/out
    (cd web-next && pnpm install --frozen-lockfile \
        && VITE_APP_VERSION="${VERSION}" VITE_APP_COMMIT="${COMMIT}" pnpm run build)
}

build_standard() {
    local target="$1"
    validate_target "${target}"

    local os arch
    IFS=/ read -r os arch <<<"${target}"
    local build_env=(GOOS="${os}" GOARCH="${arch}" CGO_ENABLED=0)
    if [ "${arch}" = "arm" ]; then
        build_env+=(GOARM=7)
    fi

    echo "Building ${os}/${arch}"
    env "${build_env[@]}" go build -trimpath -o "${OUTPUT_DIR}/bin/$(binary_name_for_target "${target}")" \
        -ldflags="${LDFLAGS}" -tags=jsoniter .
}

build_android() {
    local spec="$1"
    local target compiler os arch
    IFS=: read -r target compiler <<<"${spec}"
    IFS=/ read -r os arch <<<"${target}"

    local build_env=(GOOS=android GOARCH="${arch}" CGO_ENABLED=1 \
        CC="${ANDROID_NDK_HOME}/toolchains/llvm/prebuilt/linux-x86_64/bin/${compiler}")
    if [ "${arch}" = "arm" ]; then
        build_env+=(GOARM=7)
    fi

    echo "Building android/${arch}"
    env "${build_env[@]}" go build -trimpath -o "${OUTPUT_DIR}/bin/$(binary_name_for_target "${target}")" \
        -ldflags="${LDFLAGS}" -tags=jsoniter .
}

generate_licenses() {
    # Generate the Go third-party license inventory by walking module directory
    # metadata directly. google/go-licenses is no longer maintained and rejects
    # Go 1.26 stdlib packages; using a silent fallback hides that from CI.
    # The fail-closed path is the only path: any unrecognized license text or
    # missing license file aborts the release.
    echo "Generating Go third-party license report from module metadata"
    local go_license_rows="${OUTPUT_DIR}/THIRD_PARTY_LICENSES.rows.csv"
    GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go list -deps -json -tags=jsoniter . \
        | node scripts/licenses/go-module-report.mjs >"${go_license_rows}"
    {
        printf '%s\n' 'module,license_url,license_type'
        cat "${go_license_rows}"
    } >"${OUTPUT_DIR}/THIRD_PARTY_LICENSES.csv"
    rm -f "${go_license_rows}"

    echo "Generating frontend production dependency license report"
    local frontend_license_raw="${OUTPUT_DIR}/THIRD_PARTY_LICENSES.frontend.raw.json"
    (cd web-next && pnpm licenses list --prod --json >"../${frontend_license_raw}")
    node scripts/licenses/normalize-frontend-report.mjs \
        "${frontend_license_raw}" "${OUTPUT_DIR}/THIRD_PARTY_LICENSES.frontend.json"
    rm -f "${frontend_license_raw}"

    node scripts/licenses/check-reports.mjs \
        "${OUTPUT_DIR}/THIRD_PARTY_LICENSES.csv" \
        "${OUTPUT_DIR}/THIRD_PARTY_LICENSES.frontend.json"
}

prepare_outputs() {
    mkdir -p "${OUTPUT_DIR}/bin" "${OUTPUT_DIR}/archives" "${OUTPUT_DIR}/docker"
    rm -f "${OUTPUT_DIR}"/bin/"${APP_NAME}"-* "${OUTPUT_DIR}"/archives/*.zip "${OUTPUT_DIR}/archives/SHA256SUMS"
    rm -rf "${OUTPUT_DIR}/docker"
    mkdir -p "${OUTPUT_DIR}/docker"
}

prepare_docker_inputs() {
    local target dir
    for target in "${RESOLVED_TARGETS[@]}"; do
        if dir="$(docker_platform_dir "${target}")"; then
            mkdir -p "${OUTPUT_DIR}/docker/${dir}"
            cp "${OUTPUT_DIR}/bin/$(binary_name_for_target "${target}")" "${OUTPUT_DIR}/docker/${dir}/${APP_NAME}"
        fi
    done
}

create_archives() {
    [ "${ARCHIVE}" -eq 1 ] || return 0
    cp README.md LICENSE "${OUTPUT_DIR}/THIRD_PARTY_LICENSES.csv" \
        "${OUTPUT_DIR}/THIRD_PARTY_LICENSES.frontend.json" "${OUTPUT_DIR}/archives/"

    local file archive_name executable_name
    for file in "${OUTPUT_DIR}"/bin/"${APP_NAME}"-*; do
        [ -f "${file}" ] || continue
        archive_name="$(basename "${file}").zip"
        executable_name="${APP_NAME}"
        if [[ "${file}" == *-windows-* ]]; then
            executable_name="${APP_NAME}.exe"
        fi
        cp "${file}" "${OUTPUT_DIR}/archives/${executable_name}"
        (cd "${OUTPUT_DIR}/archives" && zip -q "${archive_name}" "${executable_name}" \
            README.md LICENSE THIRD_PARTY_LICENSES.csv THIRD_PARTY_LICENSES.frontend.json)
        rm -f "${OUTPUT_DIR:?}/archives/${executable_name}"
    done

    rm -f "${OUTPUT_DIR:?}/archives/README.md" "${OUTPUT_DIR}/archives/LICENSE" \
        "${OUTPUT_DIR}/archives/THIRD_PARTY_LICENSES.csv" \
        "${OUTPUT_DIR}/archives/THIRD_PARTY_LICENSES.frontend.json"
    (cd "${OUTPUT_DIR}/archives" && sha256sum ./*.zip >SHA256SUMS)
}

echo "Building ${APP_NAME} ${VERSION} (${COMMIT})"
if [ "${FRONTEND}" -eq 1 ]; then
    build_frontend
fi
prepare_outputs

for target in "${RESOLVED_TARGETS[@]}"; do
    build_standard "${target}"
done
if [ "${ANDROID}" -eq 1 ]; then
    for spec in "${ANDROID_TARGETS[@]}"; do
        build_android "${spec}"
    done
fi

prepare_docker_inputs
if [ "${LICENSES}" -eq 1 ]; then
    generate_licenses
fi
create_archives

echo "Artifacts: ${OUTPUT_DIR}/archives"
