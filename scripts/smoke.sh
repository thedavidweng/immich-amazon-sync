#!/bin/sh
set -eu
cd "$(dirname "$0")/.."

if [ "$#" -eq 0 ]; then
    set -- linux/amd64 linux/arm64
fi
mkdir -p .scratch
scratch=$(mktemp -d "$(pwd)/.scratch/container-smoke.XXXXXX")
name="immich-sync-smoke-$(basename "$scratch" | tr '[:upper:]' '[:lower:]')"
container=
volume=
cleanup() {
    if [ -n "$container" ]; then docker rm -f "$container" >/dev/null; fi
    if [ -n "$volume" ]; then docker volume rm "$volume" >/dev/null; fi
    rm -rf "$scratch"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
printf '%s\n' 'offline-smoke-admin-only' > "$scratch/admin"
chmod 644 "$scratch/admin"

check_admin() {
    docker exec -i "$container" sh -eu <<'ADMIN'
request() {
    {
        printf '%s %s HTTP/1.0\r\nHost: 127.0.0.1:8080\r\nConnection: close\r\n' "$1" "$2"
        if [ -n "$3" ]; then printf 'Cookie: %s\r\n' "$3"; fi
        printf 'Content-Type: application/x-www-form-urlencoded\r\nContent-Length: %s\r\n\r\n%s' "${#4}" "$4"
    } | nc -w 5 127.0.0.1 8080 > /tmp/admin-response
}
status() {
    grep -q "^HTTP/1[.][01] $1 " /tmp/admin-response
}
cookie() {
    sed -n 's/^Set-Cookie: \(immich_admin=[^;]*\);.*/\1/p' /tmp/admin-response
}
csrf() {
    sed -n 's/.*name="csrf" value="\([^"]*\)".*/\1/p' /tmp/admin-response
}
credential=$(cat /run/secrets/admin)
request GET / "" ""
status 303
grep -q '^Location: /login' /tmp/admin-response
request POST /login "" "password=$credential"
status 403
grep -q csrf_invalid /tmp/admin-response
request GET /login "" ""
status 200
preauth_cookie=$(cookie)
preauth_csrf=$(csrf)
test -n "$preauth_cookie"
test -n "$preauth_csrf"
grep -q 'HttpOnly' /tmp/admin-response
grep -q 'SameSite=Strict' /tmp/admin-response
grep -q 'Path=/' /tmp/admin-response
grep -q '^Cache-Control: no-store' /tmp/admin-response
grep -q '^Content-Security-Policy:' /tmp/admin-response
request GET / "$preauth_cookie" ""
status 303
grep -q '^Location: /login' /tmp/admin-response
request POST /login "$preauth_cookie" "password=$credential"
status 403
request POST /login "$preauth_cookie" "csrf=$preauth_csrf&password=$credential"
status 303
session_cookie=$(cookie)
test -n "$session_cookie"
test "$session_cookie" != "$preauth_cookie"
request GET / "$session_cookie" ""
status 200
grep -q 'action="/logout"' /tmp/admin-response
session_csrf=$(csrf)
test -n "$session_csrf"
test "$session_csrf" != "$preauth_csrf"
request POST /logout "$session_cookie" ""
status 403
grep -q csrf_invalid /tmp/admin-response
request GET / "$session_cookie" ""
status 200
request POST /logout "$session_cookie" "csrf=$session_csrf"
status 303
grep -q '^Location: /login' /tmp/admin-response
request GET / "$session_cookie" ""
status 303
grep -q '^Location: /login' /tmp/admin-response
ADMIN
}

for platform do
    arch=${platform#linux/}
    case "$platform" in
        linux/amd64|linux/arm64) ;;
        *) echo "Unsupported smoke platform: $platform" >&2; exit 1 ;;
    esac
    image="immich-amazon-sync:smoke-$arch"
    if [ -n "${BUILDX:-}" ]; then
        "$BUILDX" build --platform "$platform" --load --tag "$image" \
            --build-arg "VERSION=smoke-$arch" .
    else
        docker buildx build --platform "$platform" --load --tag "$image" \
            --build-arg "VERSION=smoke-$arch" .
    fi
    test "$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "$image")" = "$platform"
    test "$(docker image inspect --format '{{.Config.User}}' "$image")" = 10001:10001
    docker run --rm --network none --platform "$platform" "$image" doctor --help
    test "$(docker run --rm --network none --platform "$platform" "$image" --version)" = "smoke-$arch"
    docker run --rm --network none --platform "$platform" "$image" version --json
    if docker run --rm --network none --platform "$platform" \
        --mount "type=bind,source=$(pwd)/docs/reference/config.example.json,target=/run/example.json,readonly" \
        "$image" --config /run/example.json doctor --json > "$scratch/doctor" 2>&1; then
        echo "Doctor must reject the example's missing credential files" >&2
        exit 1
    fi
    grep -q 'secret_unreadable' "$scratch/doctor"

    volume="$name-$arch"
    docker volume create "$volume" >/dev/null
    docker run --rm --network none --platform "$platform" \
        --volume "$volume:/data" --entrypoint sh "$image" -ec \
        'test "$(id -u)" = 10001; test -w /data; printf persistent > /data/smoke-marker'
    for mode in environment setup; do
        set -- serve
        if [ "$mode" = setup ]; then set -- serve --setup --config /data/config.json; fi
        container="$name-$arch"
        docker run --detach --name "$container" --network none --platform "$platform" \
            --read-only --tmpfs /tmp --cap-drop ALL --security-opt no-new-privileges=true \
            --volume "$volume:/data" \
            --mount "type=bind,source=$scratch/admin,target=/run/secrets/admin,readonly" \
            --env ADMIN_PASSWORD_FILE=/run/secrets/admin \
            --health-interval 1s --health-start-period 1s --health-timeout 5s \
            "$image" "$@" >/dev/null
        for phase in start restart; do
            if [ "$phase" = restart ]; then docker restart --time 30 "$container" >/dev/null; fi
            attempts=0
            until [ "$(docker inspect --format '{{.State.Health.Status}}' "$container")" = healthy ]; do
                attempts=$((attempts + 1))
                if [ "$attempts" -ge 60 ]; then
                    docker logs "$container"
                    docker inspect --format '{{json .State}}' "$container"
                    exit 1
                fi
                sleep 1
            done
            docker exec "$container" sh -ec \
                'test "$(cat /data/smoke-marker)" = persistent; wget -q -O /dev/null http://127.0.0.1:8080/healthz; wget -q -O /dev/null http://127.0.0.1:8080/readyz'
        done
        check_admin
        docker exec "$container" test ! -e /data/config.json
        docker stop --time 30 "$container" >/dev/null
        test "$(docker inspect --format '{{.State.ExitCode}}' "$container")" = 0
        docker rm "$container" >/dev/null
        container=
        echo "PASS $platform ($mode): nonroot volume, health, admin session/CSRF, SIGTERM, restart"
    done
    docker volume rm "$volume" >/dev/null
    volume=
    echo "PASS $platform: help, version, example config"
done
