#!/usr/bin/env bash

# Two Ygg nodes discover each other on one isolated IPv6 Docker bridge.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RUN_ID="ygg-link-local-$$"
IMAGE="${RUN_ID}:test"
NETWORK="${RUN_ID}-net"
WORK_DIR="${ROOT_DIR}/.tmp/compat-link-local/${RUN_ID}"
ADMIN_ENDPOINT="unix:///var/run/yggdrasil.sock"

cleanup() {
	local node
	for node in a b; do
		docker logs "${RUN_ID}-${node}" >"${WORK_DIR}/${node}.log" 2>&1 || true
		docker rm -f "${RUN_ID}-${node}" >/dev/null 2>&1 || true
	done
	docker network rm "${NETWORK}" >/dev/null 2>&1 || true
	docker image rm -f "${IMAGE}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

command -v docker >/dev/null
mkdir -p "${WORK_DIR}"
docker build -q -f "${ROOT_DIR}/tests/compat/docker/local.Dockerfile" -t "${IMAGE}" "${ROOT_DIR}"
docker network create --ipv6 --subnet fd42:7967:676c::/64 "${NETWORK}" >/dev/null

for node in a b; do
	mkdir -p "${WORK_DIR}/${node}"
	docker run --rm "${IMAGE}" sh -ceu '
		yggdrasil -genconf -json | jq \
			--arg admin "'"${ADMIN_ENDPOINT}"'" \
			'"'"'.AdminListen = $admin
			| .IfName = "auto"
			| .Listen = []
			| .Peers = []
			| .InterfacePeers = {}
			| .AutoPeer.Enabled = false
			| .MulticastInterfaces = [{Regex:"^eth0$", Beacon:true, Listen:true, Port:0}]'"'"'
	' >"${WORK_DIR}/${node}/ygg.json"
	docker run -d --name "${RUN_ID}-${node}" --network "${NETWORK}" --privileged \
		-v "${WORK_DIR}/${node}:/config" "${IMAGE}" \
		yggdrasil -useconffile /config/ygg.json -logto stdout -loglevel debug >/dev/null
done

for attempt in $(seq 1 60); do
	ready=0
	for node in a b; do
		if docker exec "${RUN_ID}-${node}" test -S /var/run/yggdrasil.sock 2>/dev/null; then
			ready=$((ready + 1))
		fi
	done
	if [ "${ready}" -eq 2 ]; then
		break
	fi
	sleep 1
done
[ "${ready}" -eq 2 ] || { echo "Ygg admin sockets did not start" >&2; exit 1; }

for attempt in $(seq 1 90); do
	connected=0
	for node in a b; do
		count="$(docker exec "${RUN_ID}-${node}" yggdrasilctl -endpoint="${ADMIN_ENDPOINT}" -json getPeers \
			| docker exec -i "${RUN_ID}-${node}" jq '[.peers[] | select(.up == true)] | length')"
		if [ "${count}" -gt 0 ]; then
			connected=$((connected + 1))
		fi
	done
	if [ "${connected}" -eq 2 ]; then
		echo "Both nodes discovered and connected over link-local multicast"
		exit 0
	fi
	sleep 2
done

echo "Link-local discovery did not connect both nodes; see ${WORK_DIR}/*.log" >&2
exit 1
