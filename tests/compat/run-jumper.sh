#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RUN_ID="ygg-jumper-$$"
TMP_DIR="${ROOT_DIR}/.tmp/compat-jumper/${RUN_ID}"

NETWORK_NAME="${RUN_ID}-net"
LOCAL_IMAGE="ygg-jumper-local:${RUN_ID}"
NODE_A_CONT="${RUN_ID}-node-a"
NODE_B_CONT="${RUN_ID}-node-b"
RELAY_CONT="${RUN_ID}-relay"

NODE_A_DIR="${TMP_DIR}/node-a"
NODE_B_DIR="${TMP_DIR}/node-b"
RELAY_DIR="${TMP_DIR}/relay"
NODE_A_HOST="node-a"
NODE_B_HOST="node-b"
RELAY_HOST="relay"
NODE_B_LISTEN_PORT="${NODE_B_LISTEN_PORT:-10031}"
RELAY_LISTEN_PORT="${RELAY_LISTEN_PORT:-10030}"
ADMIN_ENDPOINT="unix:///var/run/yggdrasil.sock"

mkdir -p "${NODE_A_DIR}" "${NODE_B_DIR}" "${RELAY_DIR}"

log() {
	printf '==> %s\n' "$*" >&2
}

run() {
	log "$*"
	"$@"
}

run_quiet() {
	log "$*"
	"$@" >/dev/null
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
	run_quiet docker exec "${cont}" yggdrasilctl -endpoint="${ADMIN_ENDPOINT}" "$@"
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
	ctl_json "${cont}" getJumper >"${dir}/${prefix}-jumper.json" 2>&1 || true
	ctl_json "${cont}" getNodeInfo "key=$(ctl_json "${cont}" getSelf | docker exec -i "${cont}" jq -r .key)" >"${dir}/${prefix}-nodeinfo.json" 2>&1 || true
	set -e
}

cleanup() {
	set +e
	capture_state "${NODE_A_CONT}" "${NODE_A_DIR}" "node-a" || true
	capture_state "${NODE_B_CONT}" "${NODE_B_DIR}" "node-b" || true
	capture_state "${RELAY_CONT}" "${RELAY_DIR}" "relay" || true
	docker rm -f "${NODE_A_CONT}" "${NODE_B_CONT}" "${RELAY_CONT}" >/dev/null 2>&1 || true
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
	local attempts="${3:-45}"
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
	local attempts="${3:-60}"
	local i

	for ((i = 1; i <= attempts; i++)); do
		local count
		count="$(ctl_json "${cont}" getPeers | docker exec -i "${cont}" jq '[.peers[] | select(.up == true)] | length')"
		if [ "${count}" = "${expected}" ]; then
			return 0
		fi
		log "${cont}: waiting for ${expected} up peer(s), got ${count}"
		sleep 1
	done

	echo "timed out waiting for ${cont} to report ${expected} connected peers" >&2
	return 1
}

wait_for_peer_uri_up() {
	local cont="$1"
	local uri="$2"
	local attempts="${3:-90}"
	local i

	for ((i = 1; i <= attempts; i++)); do
		local up
		up="$(ctl_json "${cont}" getPeers | docker exec -i "${cont}" jq --arg uri "${uri}" '[.peers[] | select(.remote == $uri and .up == true)] | length')"
		if [ "${up}" = "1" ]; then
			log "${cont}: jumper direct peer is up: ${uri}"
			return 0
		fi
		log "${cont}: waiting for jumper direct peer (${i}/${attempts}): ${uri}"
		sleep 1
	done

	echo "timed out waiting for ${cont} to connect jumper peer ${uri}" >&2
	return 1
}

wait_for_ping_success() {
	local cont="$1"
	local addr="$2"
	local attempts="${3:-60}"
	local i

	for ((i = 1; i <= attempts; i++)); do
		if docker_shell "${cont}" "ping -6 -c 1 -W 1 ${addr}" >/dev/null 2>&1; then
			return 0
		fi
		log "${cont}: waiting for ping to ${addr}"
		sleep 1
	done

	echo "expected ping from ${cont} to ${addr} to succeed" >&2
	return 1
}

render_config() {
	local image="$1"
	local listen_json="$2"
	local peers_json="$3"
	local jumper_enabled="$4"
	local jumper_addresses_json="$5"

	docker run --rm \
		-e ADMIN_ENDPOINT="${ADMIN_ENDPOINT}" \
		-e LISTEN_JSON="${listen_json}" \
		-e PEERS_JSON="${peers_json}" \
		-e JUMPER_ENABLED="${jumper_enabled}" \
		-e JUMPER_ADDRESSES_JSON="${jumper_addresses_json}" \
		"${image}" \
		sh -ceu '
			yggdrasil -genconf -json \
				| jq --arg admin "${ADMIN_ENDPOINT}" \
					--argjson listen "${LISTEN_JSON}" \
					--argjson peers "${PEERS_JSON}" \
					--argjson jumper_enabled "${JUMPER_ENABLED}" \
					--argjson jumper_addresses "${JUMPER_ADDRESSES_JSON}" \
					'"'"'.AdminListen = $admin
					| .IfName = "auto"
					| .Listen = $listen
					| .Peers = $peers
					| .InterfacePeers = {}
					| .MulticastInterfaces = []
					| .AutoPeer.Enabled = false
					| .Jumper.Enabled = $jumper_enabled
					| .Jumper.Addresses = $jumper_addresses
					| .Jumper.CheckInterval = "1s"
					| .Jumper.LinkTimeout = "5s"'"'"'
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

configure_jumper() {
	local cont="$1"
	local addresses="$2"

	ctl "${cont}" setJumper \
		"enabled=true" \
		"addresses=${addresses}" \
		"check_interval=1s" \
		"link_timeout=5s"
	if [ "$(ctl_json "${cont}" getJumper | docker exec -i "${cont}" jq -r .enabled)" != "true" ]; then
		echo "jumper did not enable on ${cont}" >&2
		return 1
	fi
}

require_cmd docker

log "building local jumper image"
run docker build -f "${ROOT_DIR}/tests/compat/docker/local.Dockerfile" -t "${LOCAL_IMAGE}" "${ROOT_DIR}"

RELAY_URI="tls://${RELAY_HOST}:${RELAY_LISTEN_PORT}"
NODE_B_JUMPER_URI="tls://${NODE_B_HOST}:${NODE_B_LISTEN_PORT}"

log "rendering jumper configs"
render_config "${LOCAL_IMAGE}" "[]" "[\"${RELAY_URI}\"]" "false" "[]" >"${NODE_A_DIR}/ygg.json"
render_config "${LOCAL_IMAGE}" "[\"tls://0.0.0.0:${NODE_B_LISTEN_PORT}\"]" "[\"${RELAY_URI}\"]" "false" "[]" >"${NODE_B_DIR}/ygg.json"
render_config "${LOCAL_IMAGE}" "[\"tls://0.0.0.0:${RELAY_LISTEN_PORT}\"]" "[]" "false" "[]" >"${RELAY_DIR}/ygg.json"

run docker network create "${NETWORK_NAME}"
start_container "${RELAY_CONT}" "${RELAY_HOST}" "${RELAY_DIR}"
start_container "${NODE_A_CONT}" "${NODE_A_HOST}" "${NODE_A_DIR}"
start_container "${NODE_B_CONT}" "${NODE_B_HOST}" "${NODE_B_DIR}"

wait_for_cmd "${NODE_A_CONT}" "test -S /var/run/yggdrasil.sock"
wait_for_cmd "${NODE_B_CONT}" "test -S /var/run/yggdrasil.sock"
wait_for_cmd "${RELAY_CONT}" "test -S /var/run/yggdrasil.sock"

log "enabling jumper through the admin API"
configure_jumper "${NODE_A_CONT}" ""
configure_jumper "${NODE_B_CONT}" "${NODE_B_JUMPER_URI}"

NODE_B_ADDR="$(get_self_field "${NODE_B_CONT}" address)"

log "waiting for relay topology"
wait_for_up_peer_count "${NODE_A_CONT}" 1
wait_for_up_peer_count "${NODE_B_CONT}" 1
wait_for_up_peer_count "${RELAY_CONT}" 2

log "sending traffic from node A to node B through relay"
wait_for_ping_success "${NODE_A_CONT}" "${NODE_B_ADDR}"

log "waiting for node A jumper to add node B direct peer"
wait_for_peer_uri_up "${NODE_A_CONT}" "${NODE_B_JUMPER_URI}"
wait_for_up_peer_count "${NODE_A_CONT}" 2

log "Docker jumper suite completed successfully"
