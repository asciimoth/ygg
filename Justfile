set shell := ["bash", "-euo", "pipefail", "-c"]

test:
	go test ./... --race

vet:
	go vet ./...

tidy:
	go mod tidy

# Run a temporary daemon with sockstun and global built-in autopeering enabled.
# Override with SOCKS_LISTEN=127.0.0.1:1081 ADMIN_LISTEN=tcp://localhost:9002 LOGLEVEL=debug.
run-sockstun-autopeer:
	@tmpdir="$(mktemp -d)"; \
	trap 'rm -rf "${tmpdir}"' EXIT; \
	cfg="${tmpdir}/ygg.json"; \
	countries="$(jq -r '[.peers[].country] | unique | join(",")' autopeer/builtin_peers_generated.json)"; \
	go run ./cmd/yggdrasil -genconf -json \
		| jq --arg admin "${ADMIN_LISTEN:-tcp://localhost:9001}" --arg socks "${SOCKS_LISTEN:-127.0.0.1:1080}" --arg countries "${countries}" '.AdminListen = $admin | .TunType = "sockstun" | .IfName = "auto" | .TunSocksListen = $socks | .TunSocksDNSFallback = "[300:6223::53]:53" | .Listen = [] | .Peers = [] | .InterfacePeers = {} | .MulticastInterfaces = [] | .AutoPeer.Enabled = true | .AutoPeer.Sources = ["BUILTIN"] | .AutoPeer.FetchInterval = "1h" | .AutoPeer.CheckInterval = "5s" | .AutoPeer.MinimumConnected = 1 | .AutoPeer.MinimumConnectedFromFetch = 1 | .AutoPeer.Countries = ($countries | split(",") | map(select(. != ""))) | .AutoPeer.TransportSchemes = ["tls", "tcp"]' \
		>"${cfg}"; \
	echo "Admin: ${ADMIN_LISTEN:-tcp://localhost:9001}"; \
	echo "SOCKS: ${SOCKS_LISTEN:-127.0.0.1:1080}"; \
	echo "Curl:  curl -g --socks5-hostname ${SOCKS_LISTEN:-127.0.0.1:1080} http://myip.ygg"; \
	go run ./cmd/yggdrasil -useconffile "${cfg}" -logto stdout -loglevel "${LOGLEVEL:-info}"

# Docker-based compatibility tests against pinned upstream yggdrasil-go. Uses sudo.
test-compat:
	sudo ./tests/compat/run.sh

# Docker-based public autopeering test between two isolated local daemons. Uses sudo.
test-autopeer:
	sudo ./tests/compat/run-autopeer.sh

# Docker-based transport-manager control test between two local daemons. Uses sudo.
test-transport:
	sudo ./tests/compat/run-transport.sh

# Docker-based sockstun test between native-TUN and SOCKS-backed local daemons. Uses sudo.
test-sockstun:
	sudo ./tests/compat/run-sockstun.sh

test-total: test test-compat test-autopeer test-transport test-sockstun
