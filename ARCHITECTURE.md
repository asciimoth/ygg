# Architecture

## Overview

This repository packages Yggdrasil as a set of Go libraries with a thin daemon
and CLI around them.

At runtime, the system is composed as:

1. `config` loads or generates node configuration and identity material.
2. `core` creates the Yggdrasil node and owns routed encrypted packet delivery.
3. `admin` exposes a local control API over TCP or UNIX sockets.
4. `multicast` discovers local peers and feeds them back into `core`.
5. `ipv6rwc` adapts `core` packet routing into IPv6 packet semantics.
6. `tun` connects that IPv6 packet stream to the operating system TUN device.

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
- `IfName`, `IfMTU`

This package is intentionally passive. It does not start services.

### `src/core`

Purpose:
- Represent a running Yggdrasil node.
- Own the local identity and encrypted routed packet transport.
- Manage peer listeners and outbound peer links over multiple carriers.
- Expose node state and peer-management APIs to higher layers.

Main type:
- `core.Core`

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

`src/core/link*.go` implements carrier-specific peering behind one shared
manager.

Supported carrier families include:
- TCP
- TLS
- UNIX sockets
- SOCKS-wrapped TCP
- QUIC
- WebSocket / secure WebSocket

The internal abstraction is:

- `linkProtocol`
  - `dial(ctx, url, info, options) (net.Conn, error)`
  - `listen(ctx, url, sintf) (net.Listener, error)`

Each carrier is responsible for turning its scheme into a reliable ordered
`net.Conn`. Once a connection is established and the remote public key is known,
`core` passes ownership into Ironwood with `HandleConn`.

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
- Create trusted local listeners and ephemeral peerings through `core`.

Main type:
- `multicast.Multicast`

Inputs:
- `*core.Core`
- interface regex configuration
- beacon/listen/priority/password settings

Outputs into `core`:
- `core.ListenLocal(...)` for interface-scoped local listeners
- `core.CallPeer(...)` for discovered peers

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
- Connect the IPv6 packet stream to an OS TUN interface.
- Handle platform-specific TUN creation and configuration.

Main type:
- `tun.TunAdapter`

Primary dependency:
- `tun.ReadWriteCloser`

`tun.ReadWriteCloser` requires:
- `io.ReadWriteCloser`
- `Address() address.Address`
- `Subnet() address.Subnet`
- `MaxMTU() uint64`
- `SetMTU(uint64)`

In the default daemon wiring, `ipv6rwc.ReadWriteCloser` implements this
contract.

Behavior:
- Creates or adopts a TUN device.
- Reads packets from the kernel and writes them into the provided packet stream.
- Reads packets from the packet stream and writes them to the kernel.
- Can run disabled with `ifname=none`, while still draining the upstream queue.

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
- attach `multicast`
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

This keeps parsing concerns out of runtime packages.

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

### 4. IPv6 adaptation to OS networking

`tun.TunAdapter` depends only on the `tun.ReadWriteCloser` interface. This is
the key decoupling point that allows TUN handling to stay independent from the
details of `core`.

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

1. The kernel writes an IPv6 packet to the TUN device.
2. `tun` reads the packet and writes it to `ipv6rwc`.
3. `ipv6rwc` validates source/destination addressing and resolves the remote
   public key from the destination address or subnet.
4. `core` writes the payload into the Ironwood encrypted routed overlay.
5. `core.links` ensures at least one direct peer path exists to carry traffic.

For inbound traffic:

1. `core` receives an encrypted routed packet from Ironwood.
2. `core.ReadFrom` filters for session traffic.
3. `ipv6rwc` validates the IPv6 payload and source mapping.
4. `tun` writes the IPv6 packet into the OS TUN device.

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
- `tun.TunAdapter` partially

Implications:
- mutable package state is often owned by one actor
- public methods frequently use `phony.Block` for synchronized access
- background goroutines are used for I/O loops, timers, retries, and listeners

When refactoring, preserving actor ownership boundaries is more important than
preserving file layout.

## Trust and Policy Boundaries

- `core` enforces incoming peer policy such as `AllowedPublicKeys`.
- `core.PeerFilter` rejects unwanted remote IPs during peering.
- `multicast` uses `ListenLocal` for locally discovered peers so they are not
  blocked by the normal allowed-key policy.
- `admin` currently has no authentication and should be treated as a local
  privileged control surface.

