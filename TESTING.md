# Testing

This repository has several main test layers:

- Fast package tests with `go test ./ygglib/... ./yggd/... ./examples/... --race`
- A Linux Docker compatibility suite that runs this fork against pinned upstream `yggdrasil-go`
- A Linux Docker public-autopeering suite that validates runtime autopeer setup through the admin API
- A Linux Docker jumper suite that validates NodeInfo-based direct peering over an indirect route
- A Linux Docker transport-control suite that validates runtime `transport.Manager` updates through the admin API
- A Linux Docker sockstun suite that validates VTun-backed local SOCKS proxying and runtime TUN swaps
- A Linux Docker TUN firewall suite that validates native-TUN firewall defaults and runtime allow-list changes
- A Linux Docker topology suite that validates multi-hop local daemon graphs and runtime failover

## Standard Checks

Run the normal package checks during development:

```bash
just test
just vet
just tidy
```

`just test` runs `go test ./ygglib/... ./yggd/... ./examples/... --race`, which should remain the default pre-merge check.

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

- Outbound peering from this fork to upstream over `tcp`, `tls`, `ws`, `quic`
  and `unix`
- Outbound peering from upstream to this fork over `tcp`, `tls`, `ws`, `quic`
  and `unix`
- Runtime peer removal and re-addition through `yggdrasilctl`
- End-to-end IPv6 reachability over the Yggdrasil TUN interfaces

The suite deliberately skips `wss` because a meaningful compatibility case
requires a TLS-terminating WebSocket reverse proxy in front of the listener.

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

## Docker TUN Firewall Suite

Run the Docker firewall suite with:

```bash
just test-firewall
```

This target also uses `sudo` because it needs Docker access and privileged
containers with native TUN devices.

### What It Tests

The Docker firewall suite validates that:

- Native TUN attachments enable the TUN firewall by default
- ICMPv6 ping remains reachable through the firewall
- Unsolicited incoming TCP is blocked by default
- Runtime `setTunFirewall` can allow and then disallow a specific incoming TCP port
- Replacing a native TUN with `sockstun` defaults the firewall to disabled

### How It Works

The runner is [tests/compat/run-firewall.sh](/home/moth/projects/ygg/tests/compat/run-firewall.sh).

For each run it:

1. Builds a temporary Docker image from [tests/compat/docker/local.Dockerfile](/home/moth/projects/ygg/tests/compat/docker/local.Dockerfile).
2. Generates two native-TUN daemon configs with admin enabled and multicast disabled.
3. Starts two privileged containers on an isolated Docker network.
4. Adds a TLS peer from one node to the other.
5. Starts a Python HTTP server on one node's Yggdrasil address.
6. Verifies ping succeeds while HTTP is blocked, allowed, and blocked again through `getTunFirewall` and `setTunFirewall`.
7. Replaces the other node's native TUN with sockstun and verifies the firewall default changes to disabled.

### Prerequisites

The Docker firewall suite is Linux-only and expects:

- Docker installed and usable through `sudo`
- Support for privileged containers
- A host kernel that allows TUN devices inside containers

### Artifacts

Temporary Docker resources are removed during cleanup. The suite is intended to
fail fast and prints the failing Docker/admin command in the shell output.

Containers, Docker network, and temporary Docker images are removed during cleanup.

## Docker Topology Suite

Run the Docker topology suite with:

```bash
just test-topology
```

This target also uses `sudo` because it needs Docker access and privileged containers.

### What It Tests

The Docker topology suite validates that:

- Six daemons built from this repository can form a non-trivial local graph:
  `node1 -> node3 <- node2`, `node3 -> node4`, and
  `node4 -> node5`, `node4 -> node6`
- Every graph edge runs on its own Docker bridge network, so direct container
  connectivity matches the intended physical topology
- End-to-end IPv6 reachability works between every node pair over Yggdrasil TUN interfaces
- Learned path state appears in `getPaths` after traffic crosses the graph
- Removing the central `node3 -> node4` peer partitions the overlay
- Adding the pre-wired backup `node2 -> node5` peer at runtime restores reachability

The graph shape is adapted from [misc/run-schannel-netns](/home/moth/projects/ygg/misc/run-schannel-netns), but runs in Docker containers instead of host network namespaces.

### How It Works

The runner is [tests/topology/run.sh](/home/moth/projects/ygg/tests/topology/run.sh).

For each run it:

1. Builds a temporary Docker image from [tests/compat/docker/local.Dockerfile](/home/moth/projects/ygg/tests/compat/docker/local.Dockerfile).
2. Generates six fresh JSON configs with admin enabled, one TLS listener, no static peers, multicast disabled, and autopeering disabled.
3. Creates one Docker bridge network per topology edge and connects only the two endpoint containers to each edge network.
4. Starts six privileged containers running `yggd`.
5. Adds the primary graph peerings through the real admin socket using `yggdrasilctl addPeer`.
6. Waits for expected direct peer counts on every node.
7. Verifies all-pairs `ping -6` reachability over Yggdrasil addresses.
8. Removes the central bridge peering with `yggdrasilctl removePeer` and verifies cross-partition pings fail.
9. Adds a backup peering with `yggdrasilctl addPeer` and verifies cross-partition pings recover.

### Prerequisites

The Docker topology suite is Linux-only and expects:

- Docker installed and usable through `sudo`
- Support for privileged containers
- A host kernel that allows TUN devices inside containers

### Artifacts

Temporary logs and interface snapshots are written under:

```text
.tmp/topology/
```

Each run captures:

- Container logs
- `ip addr` and `ip route` snapshots
- `yggdrasilctl -json getSelf`
- `yggdrasilctl -json getPeers`
- `yggdrasilctl -json getTree`
- `yggdrasilctl -json getPaths`
- `yggdrasilctl -json getTun`

## Docker Jumper Suite

Run the Docker jumper suite with:

```bash
just test-jumper
```

This target also uses `sudo` because it needs Docker access and privileged containers.

### What It Tests

The Docker jumper suite validates that:

- Three daemons built from this repository can form an indirect relay topology
- Jumper can be enabled and configured at runtime through `yggdrasilctl setJumper`
- Node B publishes an explicit jumper address in NodeInfo
- Traffic from Node A to Node B first succeeds through the relay path
- Node A fetches Node B's NodeInfo and adds Node B's published address as a direct peer

### How It Works

The runner is [tests/compat/run-jumper.sh](/home/moth/projects/ygg/tests/compat/run-jumper.sh).

For each run it:

1. Builds a temporary Docker image from [tests/compat/docker/local.Dockerfile](/home/moth/projects/ygg/tests/compat/docker/local.Dockerfile).
2. Generates fresh JSON configs for Node A, Node B, and a relay.
3. Connects Node A and Node B only to the relay initially.
4. Enables jumper on Node A and Node B through `setJumper`, with Node B publishing `tls://node-b:10031`.
5. Sends `ping -6` traffic from Node A to Node B through the relay.
6. Waits for Node A to report the published Node B URI as an additional direct peer.

### Prerequisites

The Docker jumper suite is Linux-only and expects:

- Docker installed and usable through `sudo`
- Support for privileged containers
- A host kernel that allows TUN devices inside containers

### Artifacts

Temporary logs and interface snapshots are written under:

```text
.tmp/compat-jumper/
```

Each run captures:

- Container logs
- `ip addr` and `ip route` snapshots
- `yggdrasilctl -json getSelf`
- `yggdrasilctl -json getPeers`
- `yggdrasilctl -json getJumper`
- `yggdrasilctl -json getNodeInfo`

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

## Docker Transport-Control Suite

Run the Docker transport-control suite with:

```bash
just test-transport
```

This target also uses `sudo` because it needs Docker access and privileged containers.

### What It Tests

The Docker transport-control suite validates that:

- Two daemons built from this repository can connect over a configured persistent TLS peer
- Setting the default transport network to `nil` at runtime drops that peering
- Setting the default transport network back to `native` restores the peering
- Optional host-pattern transport mappings can be set to `nil`, changed back to `native`, and removed again
- A host-pattern transport mapping can be changed to a SOCKS network backed by a local `gogost/gost:3.2.6` proxy and reconnect successfully through that proxy

### How It Works

The runner is [tests/compat/run-transport.sh](/home/moth/projects/ygg/tests/compat/run-transport.sh).

For each run it:

1. Builds a temporary Docker image from [tests/compat/docker/local.Dockerfile](/home/moth/projects/ygg/tests/compat/docker/local.Dockerfile).
2. Generates fresh JSON configs with admin enabled, one listening daemon, and no static peers in the file.
3. Starts two privileged containers on an isolated Docker network.
4. Starts a third container running a local GOST SOCKS5 proxy on the same Docker network.
5. Adds a persistent peer through `yggdrasilctl addPeer` using a hostname so host-pattern transport mappings apply.
6. Mutates transport-manager state through `yggdrasilctl setTransport`.
7. Verifies connection loss and recovery with `getPeers` and `ping -6`, including a reconnection path that requires a SOCKS mapping.

### Prerequisites

The Docker transport-control suite is Linux-only and expects:

- Docker installed and usable through `sudo`
- Support for privileged containers
- A host kernel that allows TUN devices inside containers

### Artifacts

Temporary logs and interface snapshots are written under:

```text
.tmp/compat-transport/
```

Each run captures:

- Container logs
- `ip addr` and `ip route` snapshots
- `yggdrasilctl -json getSelf`
- `yggdrasilctl -json getPeers`
- `yggdrasilctl -json getTransport`
- `yggdrasilctl -json getTun`
- GOST proxy logs

## Docker Sockstun Suite

Run the Docker sockstun suite with:

```bash
just test-sockstun
```

This target also uses `sudo` because it needs Docker access and privileged
containers.

### What It Tests

The Docker sockstun suite validates that:

- One daemon can run with `TunType: outproxy`, exposing a SOCKS proxy on its Yggdrasil address
- A paired daemon can run with `TunType: sockstun`, exposing a local SOCKS proxy backed by VTun
- `curl` can fetch a clearnet-style HTTP server through sockstun when its default proxy is the Yggdrasil-hosted outproxy
- Runtime `detachTun`, `attachTun`, and `replaceTun` admin operations update reachability
- Replacing sockstun onto a new local SOCKS port closes the old proxy and makes the new one usable
- Runtime `setTunSocksProxies` can route fallback non-Yggdrasil traffic through an outproxy inside Yggdrasil
- A native-TUN client can use the outproxy directly with `curl --socks5-hostname`
- Sockstun selective TLS MITM intercepts a matching TCP/443 hostname, requires
  the client to trust the configured CA, and forwards plaintext HTTP to port 80
  on the same hostname through the Yggdrasil-hosted outproxy

### How It Works

The runner is [tests/compat/run-sockstun.sh](/home/moth/projects/ygg/tests/compat/run-sockstun.sh).

For each run it:

1. Builds a temporary Docker image from [tests/compat/docker/local.Dockerfile](/home/moth/projects/ygg/tests/compat/docker/local.Dockerfile).
2. Generates one `outproxy` server config, one `sockstun` client config, and a
   temporary CA pair for the client's `TunSocksTLSMITM` option.
3. Starts two privileged containers on an isolated Docker network.
4. Adds a TLS peer from the client container to the server container.
5. Starts Python HTTP servers for the loopback outproxy test and the
   hostname-based TLS MITM test.
6. Configures the client sockstun default proxy to the server outproxy and uses `curl --socks5-hostname` inside the client container to fetch that HTTP endpoint.
7. Mutates the client TUN attachment through `yggdrasilctl detachTun`, `attachTun`, and `replaceTun`.
8. Clears the client sockstun default proxy and verifies clearnet-style traffic becomes unreachable again.
9. Replaces the client TUN with a native TUN and verifies `curl` can use the server outproxy directly over Yggdrasil.
10. Replaces the client TUN with sockstun again and verifies HTTPS to the
    configured `.ygg` Docker alias fails without the CA, then succeeds with the
    CA while the upstream server receives plaintext HTTP on port 80.

### Prerequisites

The Docker sockstun suite is Linux-only and expects:

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
- `ip addr`, `ip route`, and listening socket snapshots
- `yggdrasilctl -json getSelf`
- `yggdrasilctl -json getPeers`
- `yggdrasilctl -json getTun`
