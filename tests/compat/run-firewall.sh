#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RUN_ID="ygg-firewall-$$"
TMP_DIR="${ROOT_DIR}/.tmp/compat/${RUN_ID}"

NETWORK_NAME="${RUN_ID}-net"
LOCAL_IMAGE="ygg-firewall-local:${RUN_ID}"
NODE_A_CONT="${RUN_ID}-node-a"
NODE_B_CONT="${RUN_ID}-node-b"
ADMIN_ENDPOINT="unix:///var/run/yggdrasil.sock"
LISTEN_PORT="${FIREWALL_LISTEN_PORT:-12001}"
HTTP_PORT="${FIREWALL_HTTP_PORT:-8080}"

NODE_A_DIR="${TMP_DIR}/node-a"
NODE_B_DIR="${TMP_DIR}/node-b"

mkdir -p "${NODE_A_DIR}" "${NODE_B_DIR}"

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

cleanup() {
	set +e
	docker rm -f "${NODE_A_CONT}" "${NODE_B_CONT}" >/dev/null 2>&1 || true
	docker network rm "${NETWORK_NAME}" >/dev/null 2>&1 || true
	docker image rm -f "${LOCAL_IMAGE}" >/dev/null 2>&1 || true
	set -e
}

trap cleanup EXIT

wait_for_cmd() {
	local cont="$1"
	local cmd="$2"
	local attempts="${3:-45}"
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
	local attempts="${3:-45}"
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

wait_for_ping_success() {
	local cont="$1"
	local addr="$2"
	local attempts="${3:-45}"
	local i

	for ((i = 0; i < attempts; i++)); do
		if docker_shell "${cont}" "ping -6 -c 1 -W 1 ${addr}" >/dev/null 2>&1; then
			return 0
		fi
		sleep 1
	done

	echo "expected ping from ${cont} to ${addr} to succeed" >&2
	return 1
}

wait_for_http_success() {
	local cont="$1"
	local addr="$2"
	local attempts="${3:-30}"
	local i

	for ((i = 0; i < attempts; i++)); do
		if docker_shell "${cont}" "curl -gfsS --max-time 2 http://[${addr}]:${HTTP_PORT}/ | grep -q firewall-ok"; then
			return 0
		fi
		sleep 1
	done

	echo "expected HTTP from ${cont} to [${addr}]:${HTTP_PORT} to succeed" >&2
	return 1
}

wait_for_http_failure() {
	local cont="$1"
	local addr="$2"
	local attempts="${3:-10}"
	local i

	for ((i = 0; i < attempts; i++)); do
		if ! docker_shell "${cont}" "curl -gfsS --max-time 2 http://[${addr}]:${HTTP_PORT}/" >/dev/null 2>&1; then
			return 0
		fi
		sleep 1
	done

	echo "expected HTTP from ${cont} to [${addr}]:${HTTP_PORT} to fail" >&2
	return 1
}

render_config() {
	local listen_port="$1"
	docker run --rm \
		-e ADMIN_ENDPOINT="${ADMIN_ENDPOINT}" \
		-e LISTEN_URI="tls://0.0.0.0:${listen_port}" \
		"${LOCAL_IMAGE}" \
		sh -ceu '
			yggdrasil -genconf -json \
				| jq --arg admin "${ADMIN_ENDPOINT}" --arg listen "${LISTEN_URI}" \
					'"'"'.AdminListen = $admin
					| .TunType = "native"
					| .IfName = "auto"
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

log "building local firewall image"
run docker build -f "${ROOT_DIR}/tests/compat/docker/local.Dockerfile" -t "${LOCAL_IMAGE}" "${ROOT_DIR}"

render_config "${LISTEN_PORT}" >"${NODE_A_DIR}/ygg.json"
render_config "$((LISTEN_PORT + 1))" >"${NODE_B_DIR}/ygg.json"

run docker network create "${NETWORK_NAME}"
start_container "${NODE_A_CONT}" "${NODE_A_DIR}"
start_container "${NODE_B_CONT}" "${NODE_B_DIR}"

wait_for_cmd "${NODE_A_CONT}" "test -S /var/run/yggdrasil.sock"
wait_for_cmd "${NODE_B_CONT}" "test -S /var/run/yggdrasil.sock"

NODE_B_OUTER_IP="$(docker inspect -f '{{range.NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "${NODE_B_CONT}")"
NODE_A_ADDR="$(get_self_field "${NODE_A_CONT}" address)"
NODE_B_ADDR="$(get_self_field "${NODE_B_CONT}" address)"
NODE_B_URI="tls://${NODE_B_OUTER_IP}:$((LISTEN_PORT + 1))"

run docker exec "${NODE_A_CONT}" yggdrasilctl -endpoint="${ADMIN_ENDPOINT}" addPeer "uri=${NODE_B_URI}"
wait_for_peer_count "${NODE_A_CONT}" 1
wait_for_peer_count "${NODE_B_CONT}" 1
wait_for_ping_success "${NODE_A_CONT}" "${NODE_B_ADDR}"
wait_for_ping_success "${NODE_B_CONT}" "${NODE_A_ADDR}"

if [ "$(ctl_json "${NODE_B_CONT}" getTunFirewall | docker exec -i "${NODE_B_CONT}" jq -r '.enabled')" != "true" ]; then
	echo "expected native TUN firewall to be enabled by default" >&2
	exit 1
fi

docker_shell "${NODE_B_CONT}" "mkdir -p /tmp/firewall && printf firewall-ok >/tmp/firewall/index.html && cd /tmp/firewall && nohup python3 -m http.server ${HTTP_PORT} --bind ${NODE_B_ADDR} >/tmp/firewall-http.log 2>&1 &"
wait_for_cmd "${NODE_B_CONT}" "ss -ltn | grep -q ':${HTTP_PORT}'"

log "native firewall blocks unsolicited TCP by default"
wait_for_http_failure "${NODE_A_CONT}" "${NODE_B_ADDR}"

log "admin allow-list opens the TCP service port"
run docker exec "${NODE_B_CONT}" yggdrasilctl -endpoint="${ADMIN_ENDPOINT}" setTunFirewall "allowed_tcp_ports=${HTTP_PORT}"
wait_for_http_success "${NODE_A_CONT}" "${NODE_B_ADDR}"

log "admin allow-list removal closes the TCP service port"
run docker exec "${NODE_B_CONT}" yggdrasilctl -endpoint="${ADMIN_ENDPOINT}" setTunFirewall "allowed_tcp_ports=[]"
wait_for_http_failure "${NODE_A_CONT}" "${NODE_B_ADDR}"
wait_for_ping_success "${NODE_A_CONT}" "${NODE_B_ADDR}"

log "non-native TUN replacement defaults firewall to disabled"
run docker exec "${NODE_A_CONT}" yggdrasilctl -endpoint="${ADMIN_ENDPOINT}" replaceTun type=sockstun name=sockstun-firewall socks_listen=127.0.0.1:1080
if [ "$(ctl_json "${NODE_A_CONT}" getTunFirewall | docker exec -i "${NODE_A_CONT}" jq -r '.enabled')" != "false" ]; then
	echo "expected sockstun firewall default to be disabled" >&2
	exit 1
fi

log "firewall suite completed successfully"
