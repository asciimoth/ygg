# Testing

This repository has three main test layers:

- Fast package tests with `go test ./... --race`
- A Linux Docker compatibility suite that runs this fork against pinned upstream `yggdrasil-go`
- A Linux Docker public-autopeering suite that validates runtime autopeer setup through the admin API

## Standard Checks

Run the normal package checks during development:

```bash
just test
just vet
just tidy
```

`just test` runs `go test ./... --race`, which should remain the default pre-merge check.

## Compatibility Suite

Run the Docker compatibility suite with:

```bash
just test-compat
```

The `Justfile` uses `sudo` for this target because the suite needs Docker access and privileged containers.

### What It Tests

The compatibility suite validates interoperability between:

- A daemon built from this repository
- A daemon built from upstream `yggdrasil-go`

It covers:

- Outbound peering from this fork to upstream
- Outbound peering from upstream to this fork
- Runtime peer removal and re-addition through `yggdrasilctl`
- End-to-end IPv6 reachability over the Yggdrasil TUN interfaces

### How It Works

The runner is [tests/compat/run.sh](/home/moth/projects/ygg/tests/compat/run.sh).

For each run it:

1. Builds two temporary Docker images:
   - [tests/compat/docker/local.Dockerfile](/home/moth/projects/ygg/tests/compat/docker/local.Dockerfile)
   - [tests/compat/docker/upstream.Dockerfile](/home/moth/projects/ygg/tests/compat/docker/upstream.Dockerfile)
2. Pins the upstream image to `yggdrasil-go` commit `b88fec63ff4eb94add47191fca292fc3306ee71c` (`v0.5.13`).
3. Generates fresh JSON configs with real `yggdrasil -genconf -json` output.
4. Starts two privileged containers on an isolated Docker network.
5. Configures peerings through the real admin socket using `yggdrasilctl addPeer` and `removePeer`.
6. Discovers each node's Yggdrasil IPv6 address using `yggdrasilctl getSelf`.
7. Verifies tunnel behavior with `ping -6`.

### Prerequisites

The compatibility suite is Linux-only and expects:

- Docker installed and usable through `sudo`
- Support for privileged containers
- A host kernel that allows TUN devices inside containers

### Artifacts

Temporary logs and interface snapshots are written under:

```text
.tmp/compat/
```

Each run captures:

- Container logs
- `ip addr` and `ip route` snapshots
- `yggdrasilctl -json getSelf`
- `yggdrasilctl -json getPeers`
- `yggdrasilctl -json getTun`

Containers, Docker network, and temporary Docker images are removed during cleanup.

## Docker Autopeering Suite

Run the Docker autopeering suite with:

```bash
just test-autopeer
```

This target also uses `sudo` because it needs Docker access and privileged containers.

### What It Tests

The Docker autopeering suite validates that:

- Two daemons built from this repository can start in separate Docker networks with no shared container network
- Autopeering can be configured at runtime through `yggdrasilctl` and the admin socket
- Each daemon fetches and connects to public peers from the built-in autopeer source
- The two isolated daemons become mutually reachable over the Yggdrasil network

### How It Works

The runner is [tests/compat/run-autopeer.sh](/home/moth/projects/ygg/tests/compat/run-autopeer.sh).

For each run it:

1. Builds a temporary Docker image from [tests/compat/docker/local.Dockerfile](/home/moth/projects/ygg/tests/compat/docker/local.Dockerfile).
2. Generates two fresh JSON configs with admin enabled, no static peers, and autopeer disabled initially.
3. Starts two privileged containers on two distinct Docker bridge networks so they do not share a direct container network.
4. Enables runtime autopeering through `yggdrasilctl setAutoPeer`.
5. Forces an immediate refresh through `yggdrasilctl refreshAutoPeer`.
6. Waits verbosely for each node to report fetched autopeer candidates and at least one connected runtime peer.
7. Verifies bidirectional `ping -6` reachability between the two nodes' Yggdrasil IPv6 addresses.

### Prerequisites

The Docker autopeering suite is Linux-only and expects:

- Docker installed and usable through `sudo`
- Support for privileged containers
- A host kernel that allows TUN devices inside containers
- Outbound Internet access from Docker containers so public autopeer endpoints can be reached

### Configuration Knobs

The suite is intentionally verbose and can be tuned with environment variables:

- `AUTOPEER_COUNTRIES`
- `AUTOPEER_SCHEMES`
- `AUTOPEER_FETCH_INTERVAL`
- `AUTOPEER_CHECK_INTERVAL`
- `AUTOPEER_MIN_CONNECTED`
- `AUTOPEER_MIN_CONNECTED_FROM_FETCH`

### Artifacts

Temporary logs and interface snapshots are written under:

```text
.tmp/compat-autopeer/
```

Each run captures:

- Container logs
- `ip addr` and `ip route` snapshots
- `yggdrasilctl -json getSelf`
- `yggdrasilctl -json getPeers`
- `yggdrasilctl -json getAutoPeer`
- `yggdrasilctl -json getTun`
