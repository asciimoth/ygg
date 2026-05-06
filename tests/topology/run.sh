#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RUN_ID="ygg-topology-$$"
TMP_DIR="${ROOT_DIR}/.tmp/topology/${RUN_ID}"

LOCAL_IMAGE="ygg-topology-local:${RUN_ID}"
ADMIN_ENDPOINT="unix:///var/run/yggdrasil.sock"
LISTEN_PORT="${TOPOLOGY_LISTEN_PORT:-10000}"

NODES=(node1 node2 node3 node4 node5 node6)
CONTAINERS=()

# Initial topology, adapted from misc/run-schannel-netns:
#
# 1      5
#  \    /
#   3--4
#  /    \
# 2      6
#
# The node2<->node5 edge is connected at the Docker layer but is only peered
# after the node3<->node4 bridge is removed, exercising runtime failover.
EDGES=(
	"node1 node3 primary"
	"node2 node3 primary"
	"node3 node4 primary"
	"node4 node5 primary"
	"node4 node6 primary"
	"node2 node5 backup"
)

declare -A NODE_DIRS
declare -A FIRST_NET
declare -A ADDRS

mkdir -p "${TMP_DIR}"

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

cont_for_node() {
	printf '%s-%s' "${RUN_ID}" "$1"
}

net_for_edge() {
	local left="$1"
	local right="$2"
	printf '%s-%s-%s' "${RUN_ID}" "${left}" "${right}"
}

peer_uri() {
	local node="$1"
	printf 'tls://%s:%s' "${node}" "${LISTEN_PORT}"
}

capture_state() {
	local node="$1"
	local cont
	local dir

	cont="$(cont_for_node "${node}")"
	dir="${NODE_DIRS[${node}]}"

	set +e
	docker logs "${cont}" >"${dir}/${node}.log" 2>&1 || true
	docker_shell "${cont}" "ip addr show; printf '\n'; ip -6 route show; printf '\n'; ip route show" >"${dir}/${node}-network.txt" 2>&1 || true
	ctl_json "${cont}" getSelf >"${dir}/${node}-self.json" 2>&1 || true
	ctl_json "${cont}" getPeers >"${dir}/${node}-peers.json" 2>&1 || true
	ctl_json "${cont}" getTree >"${dir}/${node}-tree.json" 2>&1 || true
	ctl_json "${cont}" getPaths >"${dir}/${node}-paths.json" 2>&1 || true
	ctl_json "${cont}" getTun >"${dir}/${node}-tun.json" 2>&1 || true
	set -e
}

cleanup() {
	set +e
	for node in "${NODES[@]}"; do
		capture_state "${node}" || true
	done
	if [ "${#CONTAINERS[@]}" -gt 0 ]; then
		docker rm -f "${CONTAINERS[@]}" >/dev/null 2>&1 || true
	fi
	for edge in "${EDGES[@]}"; do
		read -r left right _kind <<<"${edge}"
		docker network rm "$(net_for_edge "${left}" "${right}")" >/dev/null 2>&1 || true
	done
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
	local node="$1"
	local expected="$2"
	local attempts="${3:-75}"
	local cont
	local i

	cont="$(cont_for_node "${node}")"
	for ((i = 1; i <= attempts; i++)); do
		local count
		count="$(ctl_json "${cont}" getPeers | docker exec -i "${cont}" jq '[.peers[] | select(.up == true)] | length')"
		if [ "${count}" = "${expected}" ]; then
			return 0
		fi
		log "${node}: waiting for ${expected} up peer(s), got ${count}"
		sleep 1
	done

	echo "timed out waiting for ${node} to report ${expected} connected peers" >&2
	return 1
}

wait_for_ping_success() {
	local from_node="$1"
	local to_addr="$2"
	local attempts="${3:-75}"
	local cont
	local i

	cont="$(cont_for_node "${from_node}")"
	for ((i = 1; i <= attempts; i++)); do
		if docker_shell "${cont}" "ping -6 -c 1 -W 1 ${to_addr}" >/dev/null 2>&1; then
			return 0
		fi
		log "${from_node}: waiting for ping to ${to_addr}"
		sleep 1
	done

	echo "expected ping from ${from_node} to ${to_addr} to succeed" >&2
	return 1
}

wait_for_ping_failure() {
	local from_node="$1"
	local to_addr="$2"
	local attempts="${3:-12}"
	local cont
	local i

	cont="$(cont_for_node "${from_node}")"
	for ((i = 1; i <= attempts; i++)); do
		if ! docker_shell "${cont}" "ping -6 -c 1 -W 1 ${to_addr}" >/dev/null 2>&1; then
			return 0
		fi
		sleep 1
	done

	echo "expected ping from ${from_node} to ${to_addr} to fail" >&2
	return 1
}

wait_for_path_to_address() {
	local from_node="$1"
	local to_addr="$2"
	local attempts="${3:-45}"
	local cont
	local i

	cont="$(cont_for_node "${from_node}")"
	for ((i = 1; i <= attempts; i++)); do
		local found
		found="$(ctl_json "${cont}" getPaths | docker exec -i "${cont}" jq --arg addr "${to_addr}" '[.paths[] | select(.address == $addr)] | length')"
		if [ "${found}" != "0" ]; then
			return 0
		fi
		log "${from_node}: waiting for path entry to ${to_addr}"
		sleep 1
	done

	echo "expected ${from_node} to learn a path to ${to_addr}" >&2
	return 1
}

render_config() {
	docker run --rm \
		-e ADMIN_ENDPOINT="${ADMIN_ENDPOINT}" \
		-e LISTEN_PORT="${LISTEN_PORT}" \
		"${LOCAL_IMAGE}" \
		sh -ceu '
			yggdrasil -genconf -json \
				| jq --arg admin "${ADMIN_ENDPOINT}" --arg listen "tls://0.0.0.0:${LISTEN_PORT}" \
					'"'"'.AdminListen = $admin
					| .IfName = "auto"
					| .Listen = [$listen]
					| .Peers = []
					| .InterfacePeers = {}
					| .MulticastInterfaces = []
					| .AutoPeer.Enabled = false'"'"'
		'
}

start_container() {
	local node="$1"
	local cont
	local dir
	local net

	cont="$(cont_for_node "${node}")"
	dir="${NODE_DIRS[${node}]}"
	net="${FIRST_NET[${node}]}"

	CONTAINERS+=("${cont}")
	run docker run -d \
		--name "${cont}" \
		--hostname "${node}" \
		--network "${net}" \
		--network-alias "${node}" \
		--privileged \
		-v "${dir}:/config" \
		"${LOCAL_IMAGE}" \
		yggdrasil -useconffile /config/ygg.json -logto stdout -loglevel debug
}

connect_extra_networks() {
	local edge

	for edge in "${EDGES[@]}"; do
		read -r left right _kind <<<"${edge}"
		local net
		net="$(net_for_edge "${left}" "${right}")"
		if [ "${FIRST_NET[${left}]}" != "${net}" ]; then
			run docker network connect --alias "${left}" "${net}" "$(cont_for_node "${left}")"
		fi
		if [ "${FIRST_NET[${right}]}" != "${net}" ]; then
			run docker network connect --alias "${right}" "${net}" "$(cont_for_node "${right}")"
		fi
	done
}

get_self_field() {
	local node="$1"
	local field="$2"

	ctl_json "$(cont_for_node "${node}")" getSelf | docker exec -i "$(cont_for_node "${node}")" jq -r ".${field}"
}

add_peer() {
	local from_node="$1"
	local to_node="$2"

	ctl "$(cont_for_node "${from_node}")" addPeer "uri=$(peer_uri "${to_node}")"
}

remove_peer() {
	local from_node="$1"
	local to_node="$2"

	ctl "$(cont_for_node "${from_node}")" removePeer "uri=$(peer_uri "${to_node}")"
}

wait_for_full_mesh_pings() {
	local from
	local to

	for from in "${NODES[@]}"; do
		for to in "${NODES[@]}"; do
			if [ "${from}" = "${to}" ]; then
				continue
			fi
			wait_for_ping_success "${from}" "${ADDRS[${to}]}"
		done
	done
}

require_cmd docker

log "building local topology image"
run docker build -f "${ROOT_DIR}/tests/compat/docker/local.Dockerfile" -t "${LOCAL_IMAGE}" "${ROOT_DIR}"

log "rendering topology configs"
for node in "${NODES[@]}"; do
	NODE_DIRS["${node}"]="${TMP_DIR}/${node}"
	mkdir -p "${NODE_DIRS[${node}]}"
	render_config >"${NODE_DIRS[${node}]}/ygg.json"
done

log "creating per-edge Docker networks"
for edge in "${EDGES[@]}"; do
	read -r left right _kind <<<"${edge}"
	net="$(net_for_edge "${left}" "${right}")"
	run docker network create "${net}"
	: "${FIRST_NET[${left}]:=${net}}"
	: "${FIRST_NET[${right}]:=${net}}"
done

log "starting topology containers"
for node in "${NODES[@]}"; do
	start_container "${node}"
done
connect_extra_networks

for node in "${NODES[@]}"; do
	wait_for_cmd "$(cont_for_node "${node}")" "test -S /var/run/yggdrasil.sock"
	ADDRS["${node}"]="$(get_self_field "${node}" address)"
done

log "adding primary graph peerings"
for edge in "${EDGES[@]}"; do
	read -r left right kind <<<"${edge}"
	if [ "${kind}" = "primary" ]; then
		add_peer "${left}" "${right}"
	fi
done

log "waiting for s-channel topology"
wait_for_up_peer_count node1 1
wait_for_up_peer_count node2 1
wait_for_up_peer_count node3 3
wait_for_up_peer_count node4 3
wait_for_up_peer_count node5 1
wait_for_up_peer_count node6 1

log "verifying all nodes are reachable over Yggdrasil"
wait_for_full_mesh_pings
wait_for_path_to_address node1 "${ADDRS[node6]}"

log "removing bridge peer node3 -> node4 and expecting partition"
remove_peer node3 node4
wait_for_up_peer_count node3 2
wait_for_up_peer_count node4 2
wait_for_ping_failure node1 "${ADDRS[node6]}"
wait_for_ping_failure node6 "${ADDRS[node1]}"

log "adding backup peer node2 -> node5 and expecting recovery"
add_peer node2 node5
wait_for_up_peer_count node2 2
wait_for_up_peer_count node5 2
wait_for_ping_success node1 "${ADDRS[node6]}"
wait_for_ping_success node6 "${ADDRS[node1]}"
wait_for_path_to_address node1 "${ADDRS[node6]}"

log "Docker topology suite completed successfully"
