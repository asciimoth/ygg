#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RUN_ID="ygg-transport-$$"
TMP_DIR="${ROOT_DIR}/.tmp/compat-transport/${RUN_ID}"

NETWORK_NAME="${RUN_ID}-net"
LOCAL_IMAGE="ygg-transport-local:${RUN_ID}"
NODE_A_CONT="${RUN_ID}-node-a"
NODE_B_CONT="${RUN_ID}-node-b"

NODE_A_DIR="${TMP_DIR}/node-a"
NODE_B_DIR="${TMP_DIR}/node-b"
NODE_A_HOST="node-a"
NODE_B_HOST="node-b"
NODE_B_LISTEN_PORT="${NODE_B_LISTEN_PORT:-10021}"
ADMIN_ENDPOINT="unix:///var/run/yggdrasil.sock"

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

ctl() {
	local cont="$1"
	shift
	run docker exec "${cont}" yggdrasilctl -endpoint="${ADMIN_ENDPOINT}" "$@"
}

capture_state() {
	local cont="$1"
	local dir="$2"
	local prefix="$3"

	set +e
	docker logs "${cont}" >"${dir}/${prefix}.log" 2>&1 || true
	docker_shell "${cont}" "ip addr show; printf '\n'; ip -6 route show; printf '\n'; ip route show" >"${dir}/${prefix}-network.txt" 2>&1 || true
	ctl_json "${cont}" getSelf >"${dir}/${prefix}-self.json" 2>&1 || true
	ctl_json "${cont}" getPeers >"${dir}/${prefix}-peers.json" 2>&1 || true
	ctl_json "${cont}" getTransport >"${dir}/${prefix}-transport.json" 2>&1 || true
	ctl_json "${cont}" getTun >"${dir}/${prefix}-tun.json" 2>&1 || true
	set -e
}

cleanup() {
	set +e
	capture_state "${NODE_A_CONT}" "${NODE_A_DIR}" "node-a" || true
	capture_state "${NODE_B_CONT}" "${NODE_B_DIR}" "node-b" || true
	docker rm -f "${NODE_A_CONT}" "${NODE_B_CONT}" >/dev/null 2>&1 || true
	docker network rm "${NETWORK_NAME}" >/dev/null 2>&1 || true
	docker image rm -f "${LOCAL_IMAGE}" >/dev/null 2>&1 || true
	set -e
}

trap cleanup EXIT

require_cmd() {
	command -v "$1" >/dev/null 2>&1 || {
		echo "missing required command: $1" >&2
		exit 1
	}
}

wait_for_cmd() {
	local cont="$1"
	local cmd="$2"
	local attempts="${3:-30}"
	local i

	for ((i = 1; i <= attempts; i++)); do
		if docker_shell "${cont}" "${cmd}" >/dev/null 2>&1; then
			return 0
		fi
		sleep 1
	done

	echo "timed out waiting for command in ${cont}: ${cmd}" >&2
	return 1
}

wait_for_up_peer_count() {
	local cont="$1"
	local expected="$2"
	local attempts="${3:-45}"
	local i

	for ((i = 1; i <= attempts; i++)); do
		if [ "$(ctl_json "${cont}" getPeers | docker exec -i "${cont}" jq '[.peers[] | select(.up == true)] | length')" = "${expected}" ]; then
			return 0
		fi
		sleep 1
	done

	echo "timed out waiting for ${cont} to report ${expected} connected peers" >&2
	return 1
}

wait_for_ping_success() {
	local cont="$1"
	local addr="$2"
	local attempts="${3:-45}"
	local i

	for ((i = 1; i <= attempts; i++)); do
		if docker_shell "${cont}" "ping -6 -c 1 -W 1 ${addr}" >/dev/null 2>&1; then
			return 0
		fi
		sleep 1
	done

	echo "expected ping from ${cont} to ${addr} to succeed" >&2
	return 1
}

wait_for_ping_failure() {
	local cont="$1"
	local addr="$2"
	local attempts="${3:-15}"
	local i

	for ((i = 1; i <= attempts; i++)); do
		if ! docker_shell "${cont}" "ping -6 -c 1 -W 1 ${addr}" >/dev/null 2>&1; then
			return 0
		fi
		sleep 1
	done

	echo "expected ping from ${cont} to ${addr} to fail" >&2
	return 1
}

render_config() {
	local image="$1"
	local listen_uri="$2"

	docker run --rm \
		-e ADMIN_ENDPOINT="${ADMIN_ENDPOINT}" \
		-e LISTEN_URI="${listen_uri}" \
		"${image}" \
		sh -ceu '
			yggdrasil -genconf -json \
				| jq --arg admin "${ADMIN_ENDPOINT}" --arg listen "${LISTEN_URI}" \
					'"'"'.AdminListen = $admin
					| .IfName = "auto"
					| .Listen = (if $listen == "" then [] else [$listen] end)
					| .Peers = []
					| .InterfacePeers = {}
					| .MulticastInterfaces = []'"'"'
		'
}

start_container() {
	local name="$1"
	local hostname="$2"
	local dir="$3"

	run docker run -d \
		--name "${name}" \
		--hostname "${hostname}" \
		--network "${NETWORK_NAME}" \
		--network-alias "${hostname}" \
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

add_peer() {
	local cont="$1"
	local uri="$2"

	ctl "${cont}" addPeer "uri=${uri}"
}

set_transport() {
	local cont="$1"
	shift

	ctl "${cont}" setTransport "$@"
}

require_cmd docker

log "building local transport-control image"
run docker build -f "${ROOT_DIR}/tests/compat/docker/local.Dockerfile" -t "${LOCAL_IMAGE}" "${ROOT_DIR}"

log "rendering transport-control configs"
render_config "${LOCAL_IMAGE}" "" >"${NODE_A_DIR}/ygg.json"
render_config "${LOCAL_IMAGE}" "tls://0.0.0.0:${NODE_B_LISTEN_PORT}" >"${NODE_B_DIR}/ygg.json"

run docker network create "${NETWORK_NAME}"
start_container "${NODE_A_CONT}" "${NODE_A_HOST}" "${NODE_A_DIR}"
start_container "${NODE_B_CONT}" "${NODE_B_HOST}" "${NODE_B_DIR}"

wait_for_cmd "${NODE_A_CONT}" "test -S /var/run/yggdrasil.sock"
wait_for_cmd "${NODE_B_CONT}" "test -S /var/run/yggdrasil.sock"

NODE_A_ADDR="$(get_self_field "${NODE_A_CONT}" address)"
NODE_B_ADDR="$(get_self_field "${NODE_B_CONT}" address)"
PEER_URI="tls://${NODE_B_HOST}:${NODE_B_LISTEN_PORT}"

log "scenario 1: baseline connection over native default network"
add_peer "${NODE_A_CONT}" "${PEER_URI}"
wait_for_up_peer_count "${NODE_A_CONT}" 1
wait_for_up_peer_count "${NODE_B_CONT}" 1
wait_for_ping_success "${NODE_A_CONT}" "${NODE_B_ADDR}"
wait_for_ping_success "${NODE_B_CONT}" "${NODE_A_ADDR}"

log "scenario 2: nil default network drops the persistent peer"
set_transport "${NODE_A_CONT}" "default_network=nil"
wait_for_up_peer_count "${NODE_A_CONT}" 0
wait_for_up_peer_count "${NODE_B_CONT}" 0
wait_for_ping_failure "${NODE_A_CONT}" "${NODE_B_ADDR}"

log "scenario 3: restoring native default network reconnects the peer"
set_transport "${NODE_A_CONT}" "default_network=native"
wait_for_up_peer_count "${NODE_A_CONT}" 1
wait_for_up_peer_count "${NODE_B_CONT}" 1
wait_for_ping_success "${NODE_A_CONT}" "${NODE_B_ADDR}"

log "scenario 4: nil optional mapping overrides native default and disconnects"
set_transport "${NODE_A_CONT}" "network_mappings={\"${NODE_B_HOST}\":null}"
wait_for_up_peer_count "${NODE_A_CONT}" 0
wait_for_up_peer_count "${NODE_B_CONT}" 0
wait_for_ping_failure "${NODE_A_CONT}" "${NODE_B_ADDR}"

log "scenario 5: native optional mapping restores connectivity"
set_transport "${NODE_A_CONT}" "network_mappings={\"${NODE_B_HOST}\":\"native\"}"
wait_for_up_peer_count "${NODE_A_CONT}" 1
wait_for_up_peer_count "${NODE_B_CONT}" 1
wait_for_ping_success "${NODE_A_CONT}" "${NODE_B_ADDR}"

log "scenario 6: default nil leaves mapped native peer connected"
set_transport "${NODE_A_CONT}" "default_network=nil"
wait_for_up_peer_count "${NODE_A_CONT}" 1
wait_for_up_peer_count "${NODE_B_CONT}" 1
wait_for_ping_success "${NODE_A_CONT}" "${NODE_B_ADDR}"

log "scenario 7: removing the optional mapping disconnects under nil default"
set_transport "${NODE_A_CONT}" "unset_network_mappings=${NODE_B_HOST}"
wait_for_up_peer_count "${NODE_A_CONT}" 0
wait_for_up_peer_count "${NODE_B_CONT}" 0
wait_for_ping_failure "${NODE_A_CONT}" "${NODE_B_ADDR}"

log "transport-control suite passed"
