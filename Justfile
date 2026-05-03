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
