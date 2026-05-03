# Architecture

## Overview

This repository packages Yggdrasil as a set of Go libraries with a thin daemon
and CLI around them.

At runtime, the system is composed as:

1. `config` loads or generates node configuration and identity material.
2. `transport` provides pluggable carrier transports and host-scoped
   `gonnect.Network` selection for connection setup.
3. `core` creates the Yggdrasil node and owns routed encrypted packet delivery.
4. `admin` exposes a local control API over TCP or UNIX sockets.
5. `multicast` optionally discovers local peers and feeds them back into
   `core` through a small runtime adapter.
6. `ipv6rwc` adapts `core` packet routing into IPv6 packet semantics.
7. `tun` supervises a runtime attachment for that IPv6 packet stream.
8. `tunnative` provides OS-specific native TUN creation/configuration for the
   daemon.

`cmd/yggdrasil` is the composition root. It wires the packages together but
keeps most behavior inside `src/`.

## Top-Level Components

### `src/config`

Purpose:
- Define persistent node configuration.
- Generate defaults.
- Hold the node private key and self-signed certificate.
- Translate HJSON/JSON/PEM input into runtime structures.

Main type:
- `config.NodeConfig`

Key outputs consumed by other packages:
- `Certificate` for `core.New`
- `Peers`, `InterfacePeers`, `Listen` for link setup
- `AllowedPublicKeys`, `NodeInfo`, `NodeInfoPrivacy`
- `AdminListen`
- `MulticastInterfaces`
- `IfName`, `IfMTU` for daemon-owned native TUN setup

This package is intentionally passive. It does not start services.

### `src/core`

Purpose:
- Represent a running Yggdrasil node.
- Own the local identity and encrypted routed packet transport.
- Manage peer listeners and outbound peer links over multiple carriers.
- Expose node state and peer-management APIs to higher layers.

Main type:
- `core.Core`

Construction:
- `core.New` now requires the caller to provide a preconfigured
  `transport.Manager` via `core.TransportManager{Manager: ...}`.
- `core` does not create any default `gonnect.Network` or register any default
  transports on behalf of the caller.
- The embedding application is responsible for deciding which carrier schemes
  exist and which `gonnect.Network` instances they use before starting `core`.

Important internals:
- `Core.PacketConn`: Ironwood encrypted packet router (`encrypted.PacketConn`)
- `links`: manages direct peer connections and listeners
- `protoHandler`: handles in-band protocol/debug messages such as nodeinfo and
  remote debug requests

Responsibilities:
- Create the underlying routed encrypted packet connection from the node
  `ed25519` keypair.
- Accept traffic as a `net.PacketConn`-like API with `ReadFrom`/`WriteTo`.
- Mux session traffic and internal protocol traffic.
- Maintain configured listeners and persistent peers.
- Enforce incoming peer policy such as `AllowedPublicKeys`.
- Surface operational state: self, peers, tree, paths, sessions.

#### Link subsystem

`src/core/link.go` owns peer lifecycle and handshake policy, but carrier
creation is delegated to the injected `transport.Manager`.

Currently supported carrier families are only those provided by the configured
manager. In this repository today, the maintained built-in transports are:
- TCP
- TLS

Former in-core carrier implementations for UNIX sockets, SOCKS, QUIC,
WebSocket, and secure WebSocket have been removed. If those schemes are needed
again, they should come back as `transport.Transport` implementations rather
than as `core`-owned dialers/listeners.

The transport manager is responsible for turning a registered scheme into a
reliable ordered `net.Conn` or `net.Listener`. Once a connection is
established, `core` still owns:
- Yggdrasil version/password handshake
- pinned-key checks
- `AllowedPublicKeys` enforcement
- persistent-peer retry and backoff behavior
- handoff into Ironwood via `HandleConn`

`core` also exposes its manager through public methods so callers can change
default or mapped networks at runtime without rebuilding the node.

### `transport`

Purpose:
- Provide a standalone carrier transport abstraction decoupled from `core`.
- Allow runtime registration of transports by URL scheme, with no hardcoded
  built-ins inside the manager.
- Select a `gonnect.Network` per host using a default network plus wildcard
  host mappings.
- Track dialed connections, listeners, and listener-accepted child
  connections so live network remaps can force-close affected resources.

Main type:
- `transport.Manager`

Construction model:
- The manager starts empty.
- It does not create or assume any default `gonnect.Network`.
- It does not register any transports automatically.
- Callers must explicitly set a default network if they want unmatched hosts to
  be routable, and must explicitly register each supported transport scheme.

Main interfaces:
- `transport.Transport`
  - `Schemes() []string`
  - `Dial(ctx, network, url, options) (net.Conn, error)`
  - `Listen(ctx, network, url, options) (net.Listener, error)`
- `transport.Options`
  - per-call carrier options such as source-interface selection

Behavior:
- Routes `Dial` and `Listen` by URL scheme to registered transports.
- Also exposes option-aware `DialWithOptions` and `ListenWithOptions` entry
  points, while keeping zero-option helpers for simple callers.
- Chooses the `gonnect.Network` to pass into a transport based on the target
  host, preferring the most specific matching host pattern.
- Passes the original URL through to transports so carrier-specific query
  parameters such as TLS `sni` can be implemented at the transport layer while
  unrelated parameters remain available to higher layers.
- Rejects operations when the selected network mapping is `nil`.
- Supports live `SetDefaultNetwork`, `MapNetwork`, and `UnmapNetwork` updates.
- Closes all affected listeners, accepted children, and dialed connections when
  a mapping changes so no resource survives on the wrong network.
- Treats `Options.SourceInterface` as a best-effort hint in the built-in TCP
  and TLS transports: use it when the selected `gonnect.Network` and underlying
  socket implementation support interface-aware dialing or binding, otherwise
  continue with the normal connection path.

Current built-in transport implementations in this repository:
- TCP
- TLS

The transport package intentionally does not implement peer reconnection,
timeouts, password protection, or Yggdrasil handshakes. Those concerns stay in
higher layers such as `core`.

#### Protocol subsystem

`protoHandler` is the in-band control plane built on top of `Core.ReadFrom` and
`Core.WriteTo`.

It handles:
- nodeinfo exchange
- remote debug requests/responses
- request tracking and timeouts

This path is separate from the local admin socket. Admin commands may trigger
protocol messages to remote nodes through this subsystem.

### `src/admin`

Purpose:
- Expose a local management API for the daemon.
- Register handlers from `core`, `multicast`, and `tun`.
- Provide the protocol used by `yggdrasilctl`.

Main type:
- `admin.AdminSocket`

Main interfaces:
- `core.AddHandler`
- `core.AddHandlerFunc`

Behavior:
- Listens on UNIX or TCP depending on configuration.
- Accepts JSON requests of the form `{request, arguments, keepalive}`.
- Dispatches to registered handlers and returns JSON responses.

The admin package owns transport and request dispatch only. It does not
implement most domain logic itself; instead it delegates into the packages that
register handlers.

### `src/multicast`

Purpose:
- Discover link-local peers on allowed interfaces.
- Optionally advertise this node's presence.
- Create trusted local listeners and ephemeral peerings through an injected
  runtime interface.

Main type:
- `multicast.Multicast`

Inputs:
- a small runtime interface implemented by the daemon
- interface regex configuration
- beacon/listen/priority/password settings
- protocol version

Outputs into `core`:
- `ListenLocal(...)` for interface-scoped local listeners
- `CallPeer(...)` for discovered peers

Decoupling boundary:
- `src/multicast` does not import `src/core`.
- `cmd/yggdrasil` adapts `*core.Core` to the narrow multicast runtime
  interface at startup time.
- Attaching multicast is optional and decided by the binary, not by `core`.

This package is a peer discovery module only. It does not route data packets.

### `src/ipv6rwc`

Purpose:
- Convert between Yggdrasil public-key routing and IPv6 packet routing.
- Present `core` as an `io.ReadWriteCloser` suitable for the TUN package.

Main type:
- `ipv6rwc.ReadWriteCloser`

Behavior:
- Reads packets from `core`, validates IPv6 framing, and emits IPv6 packets.
- Writes IPv6 packets into `core` by mapping destination address/subnet back to
  partial public keys.
- Maintains a temporary key/address/subnet cache.
- Uses `Core.SendLookup` and path notifications to resolve unknown
  address-to-key mappings.
- Generates ICMPv6 Packet Too Big responses when packets exceed the configured
  path MTU.

This package is the boundary between Yggdrasil's native address space
(`ed25519` public keys) and the exported IPv6 view.

### `src/tun`

Purpose:
- Connect the IPv6 packet stream to an attached `gonnect/tun.Tun`
  implementation.
- Supervise a replaceable runtime attachment that implements
  `github.com/asciimoth/gonnect/tun.Tun`.
- Remain generic so library consumers can choose how TUN devices are created
  and attached.

Main type:
- `tun.TunAdapter`

Primary dependency:
- `tun.ReadWriteCloser`

`tun.ReadWriteCloser` requires:
- `io.ReadWriteCloser`
- `MaxMTU() uint64`
- `SetMTU(uint64)`

In the default daemon wiring, `ipv6rwc.ReadWriteCloser` implements this
contract.

Behavior:
- Starts as a long-lived supervisor and keeps one upstream queue reader alive
  for the lifetime of the adapter.
- Can attach, detach, and replace the active TUN implementation at runtime.
- Runs attachment-local read/write/event loops so an old TUN can stop or close
  without killing the adapter or `core`.
- Updates the effective upstream MTU whenever a new attachment is installed or
  the active TUN reports an MTU change.
- Can run detached while still draining the upstream queue so higher layers do
  not block.

Runtime model:
- `TunAdapter` owns the stable control plane and packet queue.
- The active attachment owns device-specific packet I/O and event handling.
- The generic runtime boundary is `github.com/asciimoth/gonnect/tun.Tun`.

### `tunnative`

Purpose:
- Create and configure native OS TUN devices in a platform-specific package at
  the repository root.
- Keep `src/` packages free of direct dependencies on `tuntap`, `netlink`,
  platform ioctls, and OS-specific interface setup.

Main API:
- `tunnative.Create(log, tunnative.Config) (gonnect/tun.Tun, error)`

Behavior:
- Uses `github.com/asciimoth/tuntap` for native device creation.
- Performs per-OS address, MTU, and link-up setup where needed.
- Returns a generic `gonnect/tun.Tun` so callers can attach it through
  `src/tun` without importing platform-specific details into library packages.

### `src/address`

Purpose:
- Define the Yggdrasil IPv6 address and subnet types.
- Convert between public keys and IPv6 addresses/subnets.
- Recover partial keys from addresses/subnets for lookup.

This package is pure logic and is used by `core`, `ipv6rwc`, admin responses,
and tests.

## Binaries

### `cmd/yggdrasil`

The daemon is intentionally small. Its job is to:
- parse flags
- load config
- build the logger
- instantiate `core`
- attach `admin`
- optionally attach `multicast`
- create native TUNs through `tunnative` when configured
- attach `tun` using `ipv6rwc.NewReadWriteCloser(core)`
- manage shutdown ordering

The daemon does not reimplement protocol logic. It is mostly dependency
injection and process lifecycle.

### `cmd/yggdrasilctl`

This is a client for the admin socket.

It:
- connects to the configured admin endpoint
- sends JSON admin requests
- renders responses in JSON or table form

### `cmd/genkeys`

Standalone helper for generating Ed25519 keys with favorable address ordering.
It is operationally separate from the running node.

## Main Runtime Interfaces

### 1. Configuration to runtime

`config.NodeConfig` is translated by `cmd/yggdrasil` into package-specific
options:

- `core.SetupOption`
- `admin.SetupOption`
- `multicast.SetupOption`
- `tun.SetupOption`

Platform-specific native TUN parameters from config are interpreted in
`cmd/yggdrasil`, not inside `src/tun`.

This keeps parsing concerns out of runtime packages.

For multicast specifically, `cmd/yggdrasil` is also responsible for:
- deciding whether the module is attached at all
- passing the current Yggdrasil protocol version
- adapting `*core.Core` to the narrow interface expected by `src/multicast`

### 2. Peer transport to routed overlay

Carrier-specific link code produces:
- `net.Conn` for each peer
- `net.Listener` for incoming peers

`core` consumes those connections and attaches them to Ironwood. This is the
main seam between transport-specific code and overlay routing.

### 3. Routed overlay to IPv6 adaptation

`core.Core` exposes packet I/O:
- `ReadFrom([]byte) (int, net.Addr, error)`
- `WriteTo([]byte, net.Addr) (int, error)`
- `MTU() uint64`
- `SendLookup(...)` via embedded Ironwood packet conn
- `SetPathNotify(func(ed25519.PublicKey))`

`ipv6rwc` consumes this API and translates it into IPv6 packet semantics.

### 4. IPv6 adaptation to attachable networking

`tun.TunAdapter` depends only on the `tun.ReadWriteCloser` interface. This is
the key decoupling point that allows TUN handling to stay independent from the
details of `core`.

Below that boundary, the active runtime attachment depends only on
`gonnect/tun.Tun`, which is what allows native TUNs and VTun-backed
implementations to share one lifecycle and packet path.

In the default daemon, native attachments are created one layer above this via
`tunnative.Create(...)` and then explicitly attached to `tun.TunAdapter`.

### 5. Local control API

`admin.AdminSocket` exposes a handler registry:
- `AddHandler(name, desc, args, handler)`

Packages register their own commands:
- `core.SetAdmin(...)`
- `admin.SetupAdminHandlers()`
- `multicast.SetupAdminHandlers(...)`
- `tun.SetupAdminHandlers(...)`

This keeps admin transport centralized while leaving business logic distributed.

## Data Flow

### Data-plane packet flow

For local application traffic:

1. The active TUN attachment reads an IPv6 packet from the kernel or virtual
   implementation.
2. `tun` forwards the packet to `ipv6rwc`.
3. `ipv6rwc` validates source/destination addressing and resolves the remote
   public key from the destination address or subnet.
4. `core` writes the payload into the Ironwood encrypted routed overlay.
5. `core.links` ensures at least one direct peer path exists to carry traffic.

For inbound traffic:

1. `core` receives an encrypted routed packet from Ironwood.
2. `core.ReadFrom` filters for session traffic.
3. `ipv6rwc` validates the IPv6 payload and source mapping.
4. `tun` queues the IPv6 packet for the current attachment.
5. The active attachment writes the packet into the OS or virtual TUN.

If no attachment is active, `tun` continues draining upstream packets and drops
them rather than deadlocking `core`.

### Control-plane flow

Local control:

1. `yggdrasilctl` sends a JSON request to `admin`.
2. `admin` dispatches to a registered handler.
3. The handler queries or mutates `core`, `multicast`, or `tun`.

Remote control and metadata:

1. A local admin request may invoke a `core` remote debug or nodeinfo handler.
2. `protoHandler` sends an in-band control message over the overlay.
3. The remote node responds over the same protocol path.
4. `admin` returns the result to the local client.

## Concurrency Model

Several major components embed `phony.Inbox` and use the actor model for
serialized state mutation:

- `core.Core`
- `core.links`
- `core.protoHandler`
- `multicast.Multicast`

Implications:
- mutable package state is often owned by one actor
- public methods frequently use `phony.Block` for synchronized access
- background goroutines are used for I/O loops, timers, retries, and listeners

`tun.TunAdapter` no longer embeds `phony.Inbox`. Its state is instead owned by a
dedicated supervisor goroutine with control, packet, and event channels.

When refactoring, preserving these ownership boundaries is more important than
preserving file layout.

## Trust and Policy Boundaries

- `core` enforces incoming peer policy such as `AllowedPublicKeys`.
- `core.PeerFilter` rejects unwanted remote IPs during peering.
- `multicast` uses its injected `ListenLocal` hook for locally discovered
  peers so they are not
  blocked by the normal allowed-key policy.
- `admin` currently has no authentication and should be treated as a local
  privileged control surface.
