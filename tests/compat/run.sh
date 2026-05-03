#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RUN_ID="ygg-compat-$$"
TMP_DIR="${ROOT_DIR}/.tmp/compat/${RUN_ID}"

NETWORK_NAME="${RUN_ID}-net"
LOCAL_IMAGE="ygg-compat-local:${RUN_ID}"
UPSTREAM_IMAGE="ygg-compat-upstream:${RUN_ID}"
LOCAL_CONT="${RUN_ID}-local"
UPSTREAM_CONT="${RUN_ID}-upstream"

LOCAL_LISTEN_PORT="${LOCAL_LISTEN_PORT:-10001}"
UPSTREAM_LISTEN_PORT="${UPSTREAM_LISTEN_PORT:-10002}"
ADMIN_ENDPOINT="unix:///var/run/yggdrasil.sock"
YGG_UPSTREAM_REF="${YGG_UPSTREAM_REF:-b88fec63ff4eb94add47191fca292fc3306ee71c}"

LOCAL_DIR="${TMP_DIR}/local"
UPSTREAM_DIR="${TMP_DIR}/upstream"

mkdir -p "${LOCAL_DIR}" "${UPSTREAM_DIR}"

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
	docker_shell "${cont}" "ip addr show; printf '\n'; ip -6 route show; printf '\n'; ip route show" >"${dir}/${prefix}-network.txt" 2>&1 || true
	ctl_json "${cont}" getSelf >"${dir}/${prefix}-self.json" 2>&1 || true
	ctl_json "${cont}" getPeers >"${dir}/${prefix}-peers.json" 2>&1 || true
	ctl_json "${cont}" getTun >"${dir}/${prefix}-tun.json" 2>&1 || true
	set -e
}

cleanup() {
	set +e
	capture_state "${LOCAL_CONT}" "${LOCAL_DIR}" "local" || true
	capture_state "${UPSTREAM_CONT}" "${UPSTREAM_DIR}" "upstream" || true
	docker rm -f "${LOCAL_CONT}" "${UPSTREAM_CONT}" >/dev/null 2>&1 || true
	docker network rm "${NETWORK_NAME}" >/dev/null 2>&1 || true
	docker image rm -f "${LOCAL_IMAGE}" "${UPSTREAM_IMAGE}" >/dev/null 2>&1 || true
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

wait_for_ping_success() {
	local cont="$1"
	local addr="$2"
	local attempts="${3:-30}"
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

wait_for_ping_failure() {
	local cont="$1"
	local addr="$2"
	local attempts="${3:-10}"
	local i

	for ((i = 0; i < attempts; i++)); do
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
	local listen_port="$2"

	docker run --rm \
		-e ADMIN_ENDPOINT="${ADMIN_ENDPOINT}" \
		-e LISTEN_URI="tls://0.0.0.0:${listen_port}" \
		"${image}" \
		sh -ceu '
			yggdrasil -genconf -json \
				| jq --arg admin "${ADMIN_ENDPOINT}" --arg listen "${LISTEN_URI}" \
					'"'"'.AdminListen = $admin
					| .IfName = "auto"
					| .Listen = [$listen]
					| .Peers = []
					| .InterfacePeers = {}
					| .MulticastInterfaces = []'"'"'
		'
}

start_container() {
	local name="$1"
	local image="$2"
	local dir="$3"

	run docker run -d \
		--name "${name}" \
		--hostname "${name}" \
		--network "${NETWORK_NAME}" \
		--network-alias "${name}" \
		--privileged \
		-v "${dir}:/config" \
		"${image}" \
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

	run docker exec "${cont}" yggdrasilctl -endpoint="${ADMIN_ENDPOINT}" addPeer "uri=${uri}"
}

remove_peer() {
	local cont="$1"
	local uri="$2"

	run docker exec "${cont}" yggdrasilctl -endpoint="${ADMIN_ENDPOINT}" removePeer "uri=${uri}"
}

require_cmd docker

log "building local compatibility image"
run docker build -f "${ROOT_DIR}/tests/compat/docker/local.Dockerfile" -t "${LOCAL_IMAGE}" "${ROOT_DIR}"

log "building pinned upstream compatibility image (${YGG_UPSTREAM_REF})"
run docker build \
	-f "${ROOT_DIR}/tests/compat/docker/upstream.Dockerfile" \
	--build-arg "YGG_UPSTREAM_REF=${YGG_UPSTREAM_REF}" \
	-t "${UPSTREAM_IMAGE}" \
	"${ROOT_DIR}"

log "rendering compatibility configs"
render_config "${LOCAL_IMAGE}" "${LOCAL_LISTEN_PORT}" >"${LOCAL_DIR}/ygg.json"
render_config "${UPSTREAM_IMAGE}" "${UPSTREAM_LISTEN_PORT}" >"${UPSTREAM_DIR}/ygg.json"

run docker network create "${NETWORK_NAME}"
start_container "${LOCAL_CONT}" "${LOCAL_IMAGE}" "${LOCAL_DIR}"
start_container "${UPSTREAM_CONT}" "${UPSTREAM_IMAGE}" "${UPSTREAM_DIR}"

wait_for_cmd "${LOCAL_CONT}" "test -S /var/run/yggdrasil.sock"
wait_for_cmd "${UPSTREAM_CONT}" "test -S /var/run/yggdrasil.sock"

LOCAL_OUTER_IP="$(docker inspect -f '{{range.NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "${LOCAL_CONT}")"
UPSTREAM_OUTER_IP="$(docker inspect -f '{{range.NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "${UPSTREAM_CONT}")"
LOCAL_ADDR="$(get_self_field "${LOCAL_CONT}" address)"
UPSTREAM_ADDR="$(get_self_field "${UPSTREAM_CONT}" address)"
LOCAL_URI="tls://${LOCAL_OUTER_IP}:${LOCAL_LISTEN_PORT}"
UPSTREAM_URI="tls://${UPSTREAM_OUTER_IP}:${UPSTREAM_LISTEN_PORT}"

log "scenario 1: this fork dials upstream"
add_peer "${LOCAL_CONT}" "${UPSTREAM_URI}"
wait_for_peer_count "${LOCAL_CONT}" 1
wait_for_peer_count "${UPSTREAM_CONT}" 1
wait_for_ping_success "${LOCAL_CONT}" "${UPSTREAM_ADDR}"
wait_for_ping_success "${UPSTREAM_CONT}" "${LOCAL_ADDR}"

log "scenario 2: remove outbound peer from this fork"
remove_peer "${LOCAL_CONT}" "${UPSTREAM_URI}"
wait_for_peer_count "${LOCAL_CONT}" 0
wait_for_peer_count "${UPSTREAM_CONT}" 0
wait_for_ping_failure "${LOCAL_CONT}" "${UPSTREAM_ADDR}"

log "scenario 3: upstream dials this fork"
add_peer "${UPSTREAM_CONT}" "${LOCAL_URI}"
wait_for_peer_count "${LOCAL_CONT}" 1
wait_for_peer_count "${UPSTREAM_CONT}" 1
wait_for_ping_success "${LOCAL_CONT}" "${UPSTREAM_ADDR}"
wait_for_ping_success "${UPSTREAM_CONT}" "${LOCAL_ADDR}"

log "scenario 4: remove outbound peer from upstream"
remove_peer "${UPSTREAM_CONT}" "${LOCAL_URI}"
wait_for_peer_count "${LOCAL_CONT}" 0
wait_for_peer_count "${UPSTREAM_CONT}" 0
wait_for_ping_failure "${UPSTREAM_CONT}" "${LOCAL_ADDR}"

log "scenario 5: re-add peer from this fork after runtime mutation"
add_peer "${LOCAL_CONT}" "${UPSTREAM_URI}"
wait_for_peer_count "${LOCAL_CONT}" 1
wait_for_peer_count "${UPSTREAM_CONT}" 1
wait_for_ping_success "${LOCAL_CONT}" "${UPSTREAM_ADDR}"
wait_for_ping_success "${UPSTREAM_CONT}" "${LOCAL_ADDR}"

log "compatibility suite completed successfully"
