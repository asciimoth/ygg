# Testing

This repository has two main test layers:

- Fast package tests with `go test ./... --race`
- A Linux Docker compatibility suite that runs this fork against pinned upstream `yggdrasil-go`

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
