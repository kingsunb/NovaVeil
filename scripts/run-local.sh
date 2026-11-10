#!/bin/bash
# 用最新本地构建产物启动 NovaVeil，并做一次 HTTP 冒烟。
# 保留 data/（配置、SQLite、管理员密码），只替换正在跑的进程。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

PORT="${NOVAVEIL_SERVER_PORT:-8080}"
HOST="${NOVAVEIL_SERVER_HOST:-127.0.0.1}"
BIN=""
LOCAL_OS="$(go env GOOS 2>/dev/null || echo linux)"
LOCAL_ARCH="$(go env GOARCH 2>/dev/null || echo amd64)"
for candidate in \
    "${ROOT}/build/bin/novaveil-${LOCAL_OS}-${LOCAL_ARCH}" \
    "${ROOT}/build/bin/novaveil-linux-amd64" \
    "${ROOT}/build/bin/novaveil-linux-arm64" \
    "${ROOT}/novaveil"; do
    if [ -x "${candidate}" ]; then
        BIN="${candidate}"
        break
    fi
done
if [ -z "${BIN}" ]; then
    echo "no binary found. run: bash scripts/build.sh --local" >&2
    exit 1
fi

LOG="${ROOT}/logs/novaveil.log"
mkdir -p "${ROOT}/data" "${ROOT}/logs"

stop_old() {
    local pids=""
    if [ -f "${ROOT}/logs/novaveil.pid" ]; then
        local pid
        pid="$(cat "${ROOT}/logs/novaveil.pid" 2>/dev/null || true)"
        if [ -n "${pid}" ] && kill -0 "${pid}" 2>/dev/null; then
            case "$(ps -p "${pid}" -o args= 2>/dev/null || true)" in
                *novaveil*) pids="${pid}" ;;
            esac
        fi
    fi
    if [ -z "${pids}" ] && command -v pgrep >/dev/null 2>&1; then
        pids="$(pgrep -f "${ROOT}/build/bin/novaveil-" 2>/dev/null || true)"
        if [ -z "${pids}" ]; then
            pids="$(pgrep -f "${ROOT}/novaveil start" 2>/dev/null || true)"
        fi
    fi
    if [ -n "${pids}" ]; then
        echo "stopping previous instance: ${pids}"
        # shellcheck disable=SC2086
        kill ${pids} 2>/dev/null || true
        sleep 1
        # shellcheck disable=SC2086
        kill -9 ${pids} 2>/dev/null || true
    fi
}

probe_host="${HOST#[}"
probe_host="${probe_host%]}"
case "${probe_host}" in
    0.0.0.0|"") probe_host="127.0.0.1" ;;
    ::) probe_host="[::1]" ;;
    *:*) probe_host="[${probe_host}]" ;;
esac
BASE_URL="http://${probe_host}:${PORT}"

wait_listen() {
    local i
    for i in $(seq 1 30); do
        if curl -fsS --max-time 2 -o /dev/null "${BASE_URL}/healthz"; then
            return 0
        fi
        sleep 0.3
    done
    echo "service did not pass /healthz on ${BASE_URL} within timeout" >&2
    tail -n 40 "${LOG}" >&2 || true
    return 1
}

stop_old
echo "starting ${BIN} on ${HOST}:${PORT}"
NOVAVEIL_SERVER_HOST="${HOST}" NOVAVEIL_SERVER_PORT="${PORT}" \
    nohup "${BIN}" start >>"${LOG}" 2>&1 &
echo $! >"${ROOT}/logs/novaveil.pid"
wait_listen

code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "${BASE_URL}/" || true)"
if [ "${code}" != "200" ]; then
    echo "smoke failed: GET / returned HTTP ${code:-none}" >&2
    tail -n 40 "${LOG}" >&2 || true
    exit 1
fi

echo "ok  ${BASE_URL}/  (HTTP ${code})"
echo "log ${LOG}"
if [ -f "${ROOT}/data/initial-admin-password" ]; then
    echo "bootstrap password: ${ROOT}/data/initial-admin-password"
fi
if [ -x "${BIN}" ]; then
    "${BIN}" version || true
fi
