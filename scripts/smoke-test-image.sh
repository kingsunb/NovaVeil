#!/bin/sh
set -eu

usage() {
    echo "usage: $0 IMAGE [NAME] [PLATFORM]" >&2
    exit 2
}

[ "$#" -ge 1 ] && [ "$#" -le 3 ] || usage
IMAGE="$1"
NAME="${2:-novaveil-image-smoke}"
PLATFORM="${3:-}"
VOLUME="${NAME}-data"

cleanup() {
    docker rm -f "${NAME}" >/dev/null 2>&1 || true
    docker volume rm "${VOLUME}" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM
cleanup

docker volume create "${VOLUME}" >/dev/null
set -- docker run -d
if [ -n "${PLATFORM}" ]; then
    set -- "$@" --platform "${PLATFORM}"
fi
# Named volumes are initialized from /app/data by Docker with the image's 10001:10001 ownership.
"$@" \
    --name "${NAME}" \
    --user 10001:10001 \
    --read-only \
    --tmpfs /tmp:size=64m,mode=1777,noexec,nosuid,nodev \
    --cap-drop ALL \
    --security-opt no-new-privileges:true \
    -p 127.0.0.1::8080 \
    -v "${VOLUME}:/app/data" \
    "${IMAGE}" >/dev/null

binding="$(docker port "${NAME}" 8080/tcp | head -n1)"
port="${binding##*:}"
[ -n "${port}" ] || { echo "failed to resolve mapped port" >&2; exit 1; }

ready=0
i=0
while [ "${i}" -lt 60 ]; do
    state="$(docker inspect "${NAME}" --format '{{.State.Status}}')"
    [ "${state}" = "running" ] || {
        docker logs "${NAME}" >&2 || true
        echo "container stopped during smoke test: ${state}" >&2
        exit 1
    }
    if wget -q -T 3 -O /dev/null "http://127.0.0.1:${port}/"; then
        ready=1
        break
    fi
    i=$((i + 1))
    sleep 1
done
[ "${ready}" -eq 1 ] || { docker logs "${NAME}" >&2 || true; echo "HTTP readiness timed out" >&2; exit 1; }

[ "$(docker inspect "${NAME}" --format '{{.Config.User}}')" = "10001:10001" ]
[ "$(docker inspect "${NAME}" --format '{{.HostConfig.ReadonlyRootfs}}')" = "true" ]
[ "$(docker inspect "${NAME}" --format '{{json .HostConfig.CapDrop}}')" = '["ALL"]' ]
[ "$(docker inspect "${NAME}" --format '{{json .HostConfig.SecurityOpt}}')" = '["no-new-privileges:true"]' ]
docker exec "${NAME}" sh -eu -c '
    test -x /entrypoint.sh
    test -x /app/novaveil
    test -w /app/data
    test ! -w /app/novaveil
    test ! -w /entrypoint.sh
    test "$(id -u):$(id -g)" = "10001:10001"
'

# Wait for Docker's own image healthcheck, not only the external HTTP request.
i=0
while [ "${i}" -lt 45 ]; do
    health="$(docker inspect "${NAME}" --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}missing{{end}}')"
    [ "${health}" = "healthy" ] && break
    [ "${health}" != "unhealthy" ] || { docker logs "${NAME}" >&2 || true; exit 1; }
    i=$((i + 1))
    sleep 1
done
[ "${health}" = "healthy" ] || { echo "image healthcheck did not become healthy: ${health}" >&2; exit 1; }

# ---------------------------------------------------------------------------
# Smoke: real login, database read/write, and mock forwarding.
# Exercises the real Cookie auth, database persistence, and relay pipeline
# against the live container — not mocked API routes.
# Requires curl and python3; skipped gracefully on hosts without them.
# ---------------------------------------------------------------------------
if command -v curl >/dev/null 2>&1 && command -v python3 >/dev/null 2>&1; then
    cookie_jar="/tmp/${NAME}-cookies.$$"
    rm -f "${cookie_jar}"

    # Read the one-time initial admin password written by UserInit.
    admin_pw="$(docker exec "${NAME}" sh -c 'sed -n "s/^password: //p" /app/data/initial-admin-password 2>/dev/null')"
    [ -n "${admin_pw}" ] || { echo "smoke: failed to read initial admin password" >&2; exit 1; }

    # --- Real login: POST /api/v1/user/login with real credentials ---
    login_body="$(curl -s --max-time 10 -c "${cookie_jar}" -X POST "http://127.0.0.1:${port}/api/v1/user/login" \
        -H 'Content-Type: application/json' \
        -d "{\"username\":\"admin\",\"password\":\"${admin_pw}\"}")"
    printf '%s' "${login_body}" | grep -q '"code":200' || { echo "smoke: login failed: ${login_body}" >&2; exit 1; }
    printf '%s' "${login_body}" | grep -q '"username":"admin"' || { echo "smoke: login response missing username: ${login_body}" >&2; exit 1; }
    grep -q 'auth' "${cookie_jar}" || { echo "smoke: login did not set auth cookie" >&2; exit 1; }
    echo "smoke: real login passed"

    # --- DB read: GET /api/v1/user/status returns persisted admin row ---
    status_body="$(curl -s --max-time 10 -b "${cookie_jar}" "http://127.0.0.1:${port}/api/v1/user/status")"
    printf '%s' "${status_body}" | grep -q '"username":"admin"' || { echo "smoke: status read failed: ${status_body}" >&2; exit 1; }
    echo "smoke: database read passed"

    # --- DB write: change password and verify it persists across re-login ---
    new_pw="smoke-pw-changed"
    curl -s --max-time 10 -b "${cookie_jar}" -X POST "http://127.0.0.1:${port}/api/v1/user/change-password" \
        -H 'Content-Type: application/json' \
        -d "{\"old_password\":\"${admin_pw}\",\"new_password\":\"${new_pw}\"}" | grep -q '"code":200' \
        || { echo "smoke: change-password failed" >&2; exit 1; }
    curl -s --max-time 10 -c "${cookie_jar}" -X POST "http://127.0.0.1:${port}/api/v1/user/login" \
        -H 'Content-Type: application/json' \
        -d "{\"username\":\"admin\",\"password\":\"${new_pw}\"}" | grep -q '"code":200' \
        || { echo "smoke: re-login with new password failed" >&2; exit 1; }
    echo "smoke: database write passed"

    # --- Mock forwarding: custom channel → group → API key → /v1/chat/completions ---
    # A custom (fixed-reply) channel needs no external upstream, exercising the
    # full relay pipeline (API-key auth → group resolution → channel → response).
    chan_body="$(curl -s --max-time 10 -b "${cookie_jar}" -X POST "http://127.0.0.1:${port}/api/v1/channel/create" \
        -H 'Content-Type: application/json' \
        -d '{"name":"smoke-chan","type":"custom","enabled":true,"fixed_reply":"smoke-forward-ok","models":[{"name":"smoke-model","source":"manual"}]}')"
    printf '%s' "${chan_body}" | grep -q '"code":200' || { echo "smoke: channel create failed: ${chan_body}" >&2; exit 1; }
    chan_model_id="$(printf '%s' "${chan_body}" | python3 -c 'import sys,json; print(json.load(sys.stdin)["data"]["models"][0]["id"])')"
    [ -n "${chan_model_id}" ] || { echo "smoke: could not extract channel model ID" >&2; exit 1; }

    group_body="$(curl -s --max-time 10 -b "${cookie_jar}" -X POST "http://127.0.0.1:${port}/api/v1/group/create" \
        -H 'Content-Type: application/json' \
        -d "{\"name\":\"smoke-model\",\"mode\":\"failover\",\"items\":[{\"channel_model_id\":${chan_model_id},\"priority\":1}]}")"
    printf '%s' "${group_body}" | grep -q '"code":200' || { echo "smoke: group create failed: ${group_body}" >&2; exit 1; }

    key_body="$(curl -s --max-time 10 -b "${cookie_jar}" -X POST "http://127.0.0.1:${port}/api/v1/apikey/create" \
        -H 'Content-Type: application/json' \
        -d '{"name":"smoke-key","enabled":true}')"
    printf '%s' "${key_body}" | grep -q '"code":200' || { echo "smoke: apikey create failed: ${key_body}" >&2; exit 1; }
    api_key="$(printf '%s' "${key_body}" | python3 -c 'import sys,json; print(json.load(sys.stdin)["data"]["api_key"])')"
    [ -n "${api_key}" ] || { echo "smoke: could not extract API key" >&2; exit 1; }

    fwd_body="$(curl -s --max-time 10 -X POST "http://127.0.0.1:${port}/v1/chat/completions" \
        -H 'Content-Type: application/json' \
        -H "Authorization: Bearer ${api_key}" \
        -d '{"model":"smoke-model","messages":[{"role":"user","content":"hi"}],"stream":false}')"
    printf '%s' "${fwd_body}" | grep -q 'smoke-forward-ok' || { echo "smoke: forwarding did not return fixed reply: ${fwd_body}" >&2; exit 1; }
    echo "smoke: mock forwarding passed"

    rm -f "${cookie_jar}"
else
    echo "smoke: curl or python3 not found, skipping API contract checks"
fi

echo "image smoke test passed: ${IMAGE}"
