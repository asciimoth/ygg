set shell := ["bash", "-euo", "pipefail", "-c"]
set dotenv-load := true

test:
	go test ./ygglib/... ./yggd/... ./examples/... ./web/... --race -count=1

coverage:
	go test ./ygglib/... ./yggd/... ./examples/... -coverprofile=coverage.out -coverpkg=./...

vet:
	go vet ./ygglib/... ./yggd/... ./examples/... ./web/...

tidy:
	go -C ygglib mod tidy
	go -C yggd mod tidy
	go -C examples mod tidy
	go -C web mod tidy
	go work sync

release-check-env:
	@missing=0; \
	for name in GITHUB_TOKEN GPG_FINGERPRINT PACKAGE_MAINTAINER AUR_KEY; do \
		if [ -z "${!name:-}" ]; then \
			echo "missing required environment variable: ${name}" >&2; \
			missing=1; \
		fi; \
	done; \
	if [ "${missing}" -ne 0 ]; then \
		exit 1; \
	fi

release-check: release-check-env
	goreleaser check

release-snapshot: release-check-env
	goreleaser release --clean --snapshot --skip=publish --skip=validate

release: release-check-env
	goreleaser release --clean --skip=validate

web-build:
	GOOS=js GOARCH=wasm go -C web build -o app.wasm .
	@if [ -f "$(go env GOROOT)/misc/wasm/wasm_exec.js" ]; then \
		cp -f "$(go env GOROOT)/misc/wasm/wasm_exec.js" web/wasm_exec.js; \
	elif [ -f "$(go env GOROOT)/lib/wasm/wasm_exec.js" ]; then \
		cp -f "$(go env GOROOT)/lib/wasm/wasm_exec.js" web/wasm_exec.js; \
	else \
		echo "wasm_exec.js not found in GOROOT" >&2; \
		exit 1; \
	fi

web-serve: web-build
	go run ./web/server -dir web

# Run a temporary daemon with sockstun and global built-in autopeering enabled.
# Override with SOCKS_LISTEN=127.0.0.1:1081 ADMIN_LISTEN=tcp://localhost:9002 ADMIN_WEB_LISTEN=127.0.0.1:9003 LOGLEVEL=debug.
run-sockstun-autopeer:
	@tmpdir="$(mktemp -d)"; \
	trap 'rm -rf "${tmpdir}"' EXIT; \
	cfg="${tmpdir}/ygg.json"; \
	ca_crt="${tmpdir}/mitm-ca.crt"; \
	ca_key="${tmpdir}/mitm-ca.key"; \
	openssl genrsa -out "${ca_key}" 2048; \
	openssl req -x509 -new -nodes -key "${ca_key}" -sha256 -days 1 -out "${ca_crt}" -subj "/CN=ygg sockstun MITM temporary CA"; \
	countries="$(jq -r '[.peers[].country] | unique | join(",")' ygglib/autopeer/builtin_peers_generated.json)"; \
	go run ./yggd/yggd -genconf -json \
		| jq --arg admin "${ADMIN_LISTEN:-tcp://localhost:9001}" --arg admin_web "${ADMIN_WEB_LISTEN:-127.0.0.1:9003}" --arg socks "${SOCKS_LISTEN:-127.0.0.1:1080}" --arg countries "${countries}" --arg ca_crt "${ca_crt}" --arg ca_key "${ca_key}" '.AdminListen = $admin | .AdminWebListen = $admin_web | .TunType = "sockstun" | .IfName = "auto" | .TunSocksListen = $socks | .TunSocksDNSFallback = "[300:6223::53]:53" | .TunSocksTLSMITM = { ca_file: $ca_crt, key_file: $ca_key } | .Listen = [] | .Peers = [] | .InterfacePeers = {} | .MulticastInterfaces = [] | .AutoPeer.Enabled = true | .AutoPeer.Sources = ["BUILTIN"] | .AutoPeer.FetchInterval = "1h" | .AutoPeer.CheckInterval = "5s" | .AutoPeer.MinimumConnected = 1 | .AutoPeer.MinimumConnectedFromFetch = 1 | .AutoPeer.Countries = ($countries | split(",") | map(select(. != ""))) | .AutoPeer.TransportSchemes = ["tls", "tcp"]' \
		>"${cfg}"; \
	echo "Admin: ${ADMIN_LISTEN:-tcp://localhost:9001}"; \
	echo "Admin web: http://${ADMIN_WEB_LISTEN:-127.0.0.1:9003}/"; \
	echo "SOCKS: ${SOCKS_LISTEN:-127.0.0.1:1080}"; \
	echo "MITM CA: ${ca_crt}"; \
	echo "Curl:  curl -g --socks5-hostname ${SOCKS_LISTEN:-127.0.0.1:1080} http://myip.ygg"; \
	echo "HTTPS: curl -g --socks5-hostname ${SOCKS_LISTEN:-127.0.0.1:1080} --cacert ${ca_crt} https://myip.ygg"; \
	echo "resolve: tor-resolve myip.ygg ${SOCKS_LISTEN:-127.0.0.1:1080}"; \
	go run ./yggd/yggd -useconffile "${cfg}" -logto stdout -loglevel "${LOGLEVEL:-info}"

run:
  rm ./ygg
  go build -o ygg ./yggd/yggd
  sudo ./ygg -autoconf

# Docker-based compatibility tests against pinned upstream yggdrasil-go. Uses sudo.
test-compat:
	sudo ./tests/compat/run.sh

# Docker-based public autopeering test between two isolated local daemons. Uses sudo.
test-autopeer:
	sudo ./tests/compat/run-autopeer.sh

# Docker-based NodeInfo jumper test across an indirect relay path. Uses sudo.
test-jumper:
	sudo ./tests/compat/run-jumper.sh

# Docker-based transport-manager control test between two local daemons. Uses sudo.
test-transport:
	sudo ./tests/compat/run-transport.sh

# Docker-based sockstun test between native-TUN and SOCKS-backed local daemons. Uses sudo.
test-sockstun:
	sudo ./tests/compat/run-sockstun.sh

# Docker-based multi-node local topology e2e test. Uses sudo.
test-topology:
	sudo ./tests/topology/run.sh

test-total: test test-compat test-autopeer test-jumper test-transport test-sockstun test-topology
