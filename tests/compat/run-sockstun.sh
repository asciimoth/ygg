#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RUN_ID="ygg-sockstun-$$"
TMP_DIR="${ROOT_DIR}/.tmp/compat/${RUN_ID}"

NETWORK_NAME="${RUN_ID}-net"
LOCAL_IMAGE="ygg-compat-local:${RUN_ID}"
SERVER_CONT="${RUN_ID}-server"
CLIENT_CONT="${RUN_ID}-client"

SERVER_LISTEN_PORT="${SERVER_LISTEN_PORT:-11001}"
CLIENT_LISTEN_PORT="${CLIENT_LISTEN_PORT:-11002}"
HTTP_PORT="${HTTP_PORT:-8080}"
ADMIN_ENDPOINT="unix:///var/run/yggdrasil.sock"

SERVER_DIR="${TMP_DIR}/server"
CLIENT_DIR="${TMP_DIR}/client"

mkdir -p "${SERVER_DIR}" "${CLIENT_DIR}"

log() {
	printf '==> %s\n' "$*" >&2
}

run() {
	log "$*"
	"$@"
}

docker_shell() {
	local cont="$1"
	local cmd="$2"
	docker exec "${cont}" sh -ceu "${cmd}"
}

ctl_json() {
	local cont="$1"
	shift
	docker exec "${cont}" yggdrasilctl -endpoint="${ADMIN_ENDPOINT}" -json "$@"
}

capture_state() {
	local cont="$1"
	local dir="$2"
	local prefix="$3"

	set +e
	docker logs "${cont}" >"${dir}/${prefix}.log" 2>&1 || true
	docker_shell "${cont}" "ip addr show; printf '\n'; ip -6 route show; printf '\n'; ss -ltnp" >"${dir}/${prefix}-network.txt" 2>&1 || true
	ctl_json "${cont}" getSelf >"${dir}/${prefix}-self.json" 2>&1 || true
	ctl_json "${cont}" getPeers >"${dir}/${prefix}-peers.json" 2>&1 || true
	ctl_json "${cont}" getTun >"${dir}/${prefix}-tun.json" 2>&1 || true
	set -e
}

cleanup() {
	set +e
	capture_state "${SERVER_CONT}" "${SERVER_DIR}" "server" || true
	capture_state "${CLIENT_CONT}" "${CLIENT_DIR}" "client" || true
	docker rm -f "${SERVER_CONT}" "${CLIENT_CONT}" >/dev/null 2>&1 || true
	docker network rm "${NETWORK_NAME}" >/dev/null 2>&1 || true
	docker image rm -f "${LOCAL_IMAGE}" >/dev/null 2>&1 || true
	set -e
}

trap cleanup EXIT

wait_for_cmd() {
	local cont="$1"
	local cmd="$2"
	local attempts="${3:-30}"
	local i

	for ((i = 0; i < attempts; i++)); do
		if docker_shell "${cont}" "${cmd}" >/dev/null 2>&1; then
			return 0
		fi
		sleep 1
	done

	echo "timed out waiting for command in ${cont}: ${cmd}" >&2
	return 1
}

wait_for_peer_count() {
	local cont="$1"
	local expected="$2"
	local attempts="${3:-30}"
	local i

	for ((i = 0; i < attempts; i++)); do
		if [ "$(ctl_json "${cont}" getPeers | docker exec -i "${cont}" jq '.peers | length')" = "${expected}" ]; then
			return 0
		fi
		sleep 1
	done

	echo "timed out waiting for ${cont} to report ${expected} peers" >&2
	return 1
}

wait_for_curl_success() {
	local proxy="$1"
	local attempts="${2:-30}"
	local i

	for ((i = 0; i < attempts; i++)); do
		if docker_shell "${CLIENT_CONT}" "curl -gfsS --socks5-hostname ${proxy} --max-time 3 http://[${SERVER_ADDR}]:${HTTP_PORT}/ | grep -q sockstun-ok"; then
			return 0
		fi
		sleep 1
	done

	echo "expected curl through ${proxy} to succeed" >&2
	return 1
}

wait_for_curl_failure() {
	local proxy="$1"
	local attempts="${2:-10}"
	local i

	for ((i = 0; i < attempts; i++)); do
		if ! docker_shell "${CLIENT_CONT}" "curl -gfsS --socks5-hostname ${proxy} --max-time 2 http://[${SERVER_ADDR}]:${HTTP_PORT}/"; then
			return 0
		fi
		sleep 1
	done

	echo "expected curl through ${proxy} to fail" >&2
	return 1
}

render_config() {
	local image="$1"
	local listen_port="$2"
	local tun_type="$3"
	local socks_listen="$4"

	docker run --rm \
		-e ADMIN_ENDPOINT="${ADMIN_ENDPOINT}" \
		-e LISTEN_URI="tls://0.0.0.0:${listen_port}" \
		-e TUN_TYPE="${tun_type}" \
		-e SOCKS_LISTEN="${socks_listen}" \
		"${image}" \
		sh -ceu '
			yggdrasil -genconf -json \
				| jq --arg admin "${ADMIN_ENDPOINT}" --arg listen "${LISTEN_URI}" --arg tun "${TUN_TYPE}" --arg socks "${SOCKS_LISTEN}" \
					'"'"'.AdminListen = $admin
					| .TunType = $tun
					| .IfName = "auto"
					| .TunSocksListen = $socks
					| .Listen = [$listen]
					| .Peers = []
					| .InterfacePeers = {}
					| .MulticastInterfaces = []'"'"'
		'
}

start_container() {
	local name="$1"
	local dir="$2"

	run docker run -d \
		--name "${name}" \
		--hostname "${name}" \
		--network "${NETWORK_NAME}" \
		--network-alias "${name}" \
		--privileged \
		-v "${dir}:/config" \
		"${LOCAL_IMAGE}" \
		yggdrasil -useconffile /config/ygg.json -logto stdout -loglevel debug
}

get_self_field() {
	local cont="$1"
	local field="$2"

	ctl_json "${cont}" getSelf | docker exec -i "${cont}" jq -r ".${field}"
}

command -v docker >/dev/null 2>&1 || {
	echo "missing required command: docker" >&2
	exit 1
}

log "building local compatibility image"
run docker build -f "${ROOT_DIR}/tests/compat/docker/local.Dockerfile" -t "${LOCAL_IMAGE}" "${ROOT_DIR}"

log "rendering sockstun compatibility configs"
render_config "${LOCAL_IMAGE}" "${SERVER_LISTEN_PORT}" "native" "127.0.0.1:1080" >"${SERVER_DIR}/ygg.json"
render_config "${LOCAL_IMAGE}" "${CLIENT_LISTEN_PORT}" "sockstun" "127.0.0.1:1080" >"${CLIENT_DIR}/ygg.json"

run docker network create "${NETWORK_NAME}"
start_container "${SERVER_CONT}" "${SERVER_DIR}"
start_container "${CLIENT_CONT}" "${CLIENT_DIR}"

wait_for_cmd "${SERVER_CONT}" "test -S /var/run/yggdrasil.sock"
wait_for_cmd "${CLIENT_CONT}" "test -S /var/run/yggdrasil.sock"

SERVER_OUTER_IP="$(docker inspect -f '{{range.NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "${SERVER_CONT}")"
SERVER_ADDR="$(get_self_field "${SERVER_CONT}" address)"
SERVER_URI="tls://${SERVER_OUTER_IP}:${SERVER_LISTEN_PORT}"

run docker exec "${CLIENT_CONT}" yggdrasilctl -endpoint="${ADMIN_ENDPOINT}" addPeer "uri=${SERVER_URI}"
wait_for_peer_count "${SERVER_CONT}" 1
wait_for_peer_count "${CLIENT_CONT}" 1

docker_shell "${SERVER_CONT}" "mkdir -p /tmp/www && printf sockstun-ok >/tmp/www/index.html && cd /tmp/www && nohup python3 -m http.server ${HTTP_PORT} --bind ${SERVER_ADDR} >/tmp/http.log 2>&1 &"
wait_for_cmd "${SERVER_CONT}" "ss -ltn | grep -q ':${HTTP_PORT}'"

log "scenario 1: curl reaches native TUN HTTP server through sockstun"
wait_for_curl_success "127.0.0.1:1080"

log "scenario 2: detached sockstun stops proxy reachability"
run docker exec "${CLIENT_CONT}" yggdrasilctl -endpoint="${ADMIN_ENDPOINT}" detachTun
wait_for_curl_failure "127.0.0.1:1080"

log "scenario 3: attach sockstun again on the original port"
run docker exec "${CLIENT_CONT}" yggdrasilctl -endpoint="${ADMIN_ENDPOINT}" attachTun type=sockstun name=sockstun-a mtu=1500 socks_listen=127.0.0.1:1080
wait_for_curl_success "127.0.0.1:1080"

log "scenario 4: replace sockstun onto a new local port"
run docker exec "${CLIENT_CONT}" yggdrasilctl -endpoint="${ADMIN_ENDPOINT}" replaceTun type=sockstun name=sockstun-b mtu=1400 socks_listen=127.0.0.1:1081
wait_for_curl_failure "127.0.0.1:1080"
wait_for_curl_success "127.0.0.1:1081"

log "sockstun compatibility suite completed successfully"
