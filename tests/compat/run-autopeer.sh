#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RUN_ID="ygg-autopeer-$$"
TMP_DIR="${ROOT_DIR}/.tmp/compat-autopeer/${RUN_ID}"

LOCAL_IMAGE="ygg-autopeer-local:${RUN_ID}"
NODE_A_CONT="${RUN_ID}-node-a"
NODE_B_CONT="${RUN_ID}-node-b"
NODE_A_NET="${RUN_ID}-node-a-net"
NODE_B_NET="${RUN_ID}-node-b-net"

ADMIN_ENDPOINT="unix:///var/run/yggdrasil.sock"
NODE_A_DIR="${TMP_DIR}/node-a"
NODE_B_DIR="${TMP_DIR}/node-b"

AUTOPEER_COUNTRIES="${AUTOPEER_COUNTRIES:-united-states,germany,france,finland,netherlands,united-kingdom,canada,austria,czechia,singapore,japan,australia}"
AUTOPEER_SCHEMES="${AUTOPEER_SCHEMES:-tls,tcp}"
AUTOPEER_FETCH_INTERVAL="${AUTOPEER_FETCH_INTERVAL:-1h}"
AUTOPEER_CHECK_INTERVAL="${AUTOPEER_CHECK_INTERVAL:-5s}"
AUTOPEER_MIN_CONNECTED="${AUTOPEER_MIN_CONNECTED:-1}"
AUTOPEER_MIN_CONNECTED_FROM_FETCH="${AUTOPEER_MIN_CONNECTED_FROM_FETCH:-1}"

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
	ctl_json "${cont}" getAutoPeer >"${dir}/${prefix}-autopeer.json" 2>&1 || true
	ctl_json "${cont}" getTun >"${dir}/${prefix}-tun.json" 2>&1 || true
	set -e
}

cleanup() {
	set +e
	capture_state "${NODE_A_CONT}" "${NODE_A_DIR}" "node-a" || true
	capture_state "${NODE_B_CONT}" "${NODE_B_DIR}" "node-b" || true
	docker rm -f "${NODE_A_CONT}" "${NODE_B_CONT}" >/dev/null 2>&1 || true
	docker network rm "${NODE_A_NET}" "${NODE_B_NET}" >/dev/null 2>&1 || true
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
	local attempts="${3:-60}"
	local i

	for ((i = 1; i <= attempts; i++)); do
		if docker_shell "${cont}" "${cmd}" >/dev/null 2>&1; then
			log "${cont}: command became ready after ${i} attempts: ${cmd}"
			return 0
		fi
		log "${cont}: waiting for command (${i}/${attempts}): ${cmd}"
		sleep 1
	done

	echo "timed out waiting for command in ${cont}: ${cmd}" >&2
	return 1
}

render_config() {
	local image="$1"

	docker run --rm \
		-e ADMIN_ENDPOINT="${ADMIN_ENDPOINT}" \
		"${image}" \
		sh -ceu '
			yggdrasil -genconf -json \
				| jq --arg admin "${ADMIN_ENDPOINT}" \
					'"'"'.AdminListen = $admin
					| .IfName = "auto"
					| .Listen = []
					| .Peers = []
					| .InterfacePeers = {}
					| .MulticastInterfaces = []
					| .AutoPeer.Enabled = false
					| .AutoPeer.Sources = ["BUILTIN"]
					| .AutoPeer.FetchInterval = "1h"
					| .AutoPeer.CheckInterval = "1m"
					| .AutoPeer.MinimumConnected = 0
					| .AutoPeer.MinimumConnectedFromFetch = 0
					| .AutoPeer.Countries = []
					| .AutoPeer.TransportSchemes = []'"'"'
		'
}

start_container() {
	local name="$1"
	local network="$2"
	local dir="$3"

	run docker run -d \
		--name "${name}" \
		--hostname "${name}" \
		--network "${network}" \
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

assert_no_shared_network() {
	local shared
	shared="$(
		{
			docker inspect -f '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}}{{"\n"}}{{end}}' "${NODE_A_CONT}"
			echo "---"
			docker inspect -f '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}}{{"\n"}}{{end}}' "${NODE_B_CONT}"
		} | awk '
			BEGIN { section = 0 }
			$0 == "---" { section = 1; next }
			section == 0 && $0 != "" { a[$0] = 1; next }
			section == 1 && $0 != "" && ($0 in a) { print $0 }
		'
	)"
	if [ -n "${shared}" ]; then
		echo "containers unexpectedly share docker network(s): ${shared}" >&2
		return 1
	fi
	log "verified ${NODE_A_CONT} and ${NODE_B_CONT} do not share a Docker network"
}

configure_autopeer() {
	local cont="$1"

	log "${cont}: enabling runtime autopeering"
	ctl "${cont}" setAutoPeer \
		"enabled=true" \
		"sources=BUILTIN" \
		"fetch_interval=${AUTOPEER_FETCH_INTERVAL}" \
		"check_interval=${AUTOPEER_CHECK_INTERVAL}" \
		"minimum_connected=${AUTOPEER_MIN_CONNECTED}" \
		"minimum_connected_from_fetch=${AUTOPEER_MIN_CONNECTED_FROM_FETCH}" \
		"countries=${AUTOPEER_COUNTRIES}" \
		"transport_schemes=${AUTOPEER_SCHEMES}"
	log "${cont}: forcing immediate autopeer refresh"
	ctl "${cont}" refreshAutoPeer
}

wait_for_autopeer_effect() {
	local cont="$1"
	local attempts="${2:-90}"
	local i

	for ((i = 1; i <= attempts; i++)); do
		local autopeer_json
		local peers_json
		local active
		local fetched
		local connected
		local connected_up

		autopeer_json="$(ctl_json "${cont}" getAutoPeer)"
		peers_json="$(ctl_json "${cont}" getPeers)"

		active="$(printf '%s' "${autopeer_json}" | docker exec -i "${cont}" jq -r '.active')"
		fetched="$(printf '%s' "${autopeer_json}" | docker exec -i "${cont}" jq -r '.peers | length')"
		connected="$(printf '%s' "${peers_json}" | docker exec -i "${cont}" jq -r '.peers | length')"
		connected_up="$(printf '%s' "${peers_json}" | docker exec -i "${cont}" jq -r '[.peers[] | select(.up == true)] | length')"

		log "${cont}: autopeer attempt ${i}/${attempts} active=${active} fetched=${fetched} peers_total=${connected} peers_up=${connected_up}"
		printf '%s\n' "${autopeer_json}" | docker exec -i "${cont}" jq -c '{enabled,active,sources,check_interval,minimum_connected,minimum_connected_from_fetch,countries,transport_schemes,peer_count:(.peers|length)}' >&2
		printf '%s\n' "${peers_json}" | docker exec -i "${cont}" jq -c '[.peers[] | {uri:.remote, up, inbound, address}]' >&2

		if [ "${active}" = "true" ] && [ "${fetched}" -gt 0 ] && [ "${connected_up}" -gt 0 ]; then
			log "${cont}: autopeering took effect"
			return 0
		fi
		sleep 2
	done

	echo "timed out waiting for autopeering to take effect on ${cont}" >&2
	return 1
}

wait_for_ping_success() {
	local cont="$1"
	local addr="$2"
	local attempts="${3:-120}"
	local i

	for ((i = 1; i <= attempts; i++)); do
		log "${cont}: ping attempt ${i}/${attempts} to ${addr}"
		if docker_shell "${cont}" "ping -6 -c 1 -W 2 ${addr}" >/dev/null 2>&1; then
			log "${cont}: ping to ${addr} succeeded"
			return 0
		fi
		sleep 2
	done

	echo "expected ping from ${cont} to ${addr} to succeed" >&2
	return 1
}

require_cmd docker

log "building local autopeer image"
run docker build -f "${ROOT_DIR}/tests/compat/docker/local.Dockerfile" -t "${LOCAL_IMAGE}" "${ROOT_DIR}"

log "rendering isolated autopeer configs"
render_config "${LOCAL_IMAGE}" >"${NODE_A_DIR}/ygg.json"
render_config "${LOCAL_IMAGE}" >"${NODE_B_DIR}/ygg.json"

run docker network create "${NODE_A_NET}"
run docker network create "${NODE_B_NET}"
start_container "${NODE_A_CONT}" "${NODE_A_NET}" "${NODE_A_DIR}"
start_container "${NODE_B_CONT}" "${NODE_B_NET}" "${NODE_B_DIR}"

assert_no_shared_network
wait_for_cmd "${NODE_A_CONT}" "test -S /var/run/yggdrasil.sock"
wait_for_cmd "${NODE_B_CONT}" "test -S /var/run/yggdrasil.sock"

NODE_A_ADDR="$(get_self_field "${NODE_A_CONT}" address)"
NODE_B_ADDR="$(get_self_field "${NODE_B_CONT}" address)"

log "node A address: ${NODE_A_ADDR}"
log "node B address: ${NODE_B_ADDR}"

configure_autopeer "${NODE_A_CONT}"
configure_autopeer "${NODE_B_CONT}"

wait_for_autopeer_effect "${NODE_A_CONT}"
wait_for_autopeer_effect "${NODE_B_CONT}"

log "verifying end-to-end reachability between isolated autopeered nodes"
wait_for_ping_success "${NODE_A_CONT}" "${NODE_B_ADDR}"
wait_for_ping_success "${NODE_B_CONT}" "${NODE_A_ADDR}"

log "Docker autopeering suite completed successfully"
