set shell := ["bash", "-euo", "pipefail", "-c"]

test:
	go test ./... --race

vet:
	go vet ./...

tidy:
	go mod tidy

# Docker-based compatibility tests against pinned upstream yggdrasil-go. Uses sudo.
test-compat:
	sudo ./tests/compat/run.sh

# Docker-based public autopeering test between two isolated local daemons. Uses sudo.
test-autopeer:
	sudo ./tests/compat/run-autopeer.sh

# Docker-based transport-manager control test between two local daemons. Uses sudo.
test-transport:
	sudo ./tests/compat/run-transport.sh
