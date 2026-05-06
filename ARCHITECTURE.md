# Architecture

## Overview

This repository packages Yggdrasil as a set of Go libraries with a thin daemon
and CLI around them.

At runtime, the system is composed as:

1. `config` loads or generates node configuration and identity material.
2. `logger` defines the shared logging interface used by runtime packages.
3. `transport` provides pluggable carrier transports and host-scoped
   `gonnect.Network` selection for connection setup.
4. `autopeer` fetches public peer lists from one or more external sources and
   can maintain public peer counts at runtime by adding filtered candidates to
   `core` when configured thresholds are not met.
5. `core` creates the Yggdrasil node and owns routed encrypted packet delivery.
6. `admin` exposes a local control API over TCP or UNIX sockets.
   The daemon wires admin adapters to `core`, `multicast`, and `tun` at
   startup time.
7. `multicast` optionally discovers local peers and feeds them back into
   `core` through a small runtime adapter.
8. `ipv6rwc` adapts `core` packet routing into IPv6 packet semantics.
9. `tun` supervises a runtime attachment for that IPv6 packet stream.
10. `tunnative` provides OS-specific native TUN creation/configuration for the
   daemon.
11. `sockstun` provides a VTun-backed local SOCKS TUN implementation for the
    daemon.
12. `outproxy` provides a VTun-backed Yggdrasil-hosted SOCKS outproxy
    implementation for the daemon.

`yggd/yggd` is the composition root. It wires the packages together but
keeps most behavior inside `ygglib/`.

## Top-Level Components

### `ygglib/config`

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
- `Transport.DefaultNetwork`, `Transport.NetworkMappings` for daemon-managed
  `transport.Manager` construction
- `AdminListen`
- `LocalDNSListen` for the optional daemon-owned local DNS listener
- `MulticastInterfaces`
- `TunType`, `IfName`, `IfMTU`, `TunSocksListen`, `TunMWO`, `TunMRO` for
  daemon-owned TUN setup

This package is intentionally passive. It does not start services.

### `ygglib/logger`

Purpose:
- Define the process-wide logging contract used by library packages and the
  daemon.
- Provide a standard-library-backed implementation for embedders that do not
  want to bring their own logger.

Main types:
- `logger.Logger`
- `logger.StdLogger`

Runtime use:
- `core.Logger` and package-local logger aliases resolve to
  `logger.Logger`, so callers pass one logging contract through `core`,
  `admin`, `autopeer`, `multicast`, `tun`, `sockstun`, `outproxy`, and
  `tunnative`.
- `logger.StdLogger` wraps Go's `log` package, supports error/warn/info/debug
  filtering, and is safe for concurrent use.
- `logger.Discard()` provides the default no-op logger for library code when a
  caller does not provide one.

The logging package has no dependency on runtime packages. Runtime packages
depend on the interface only and do not construct process log destinations.

### `ygglib/core`

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

Daemon default:
- `yggd/yggd` currently builds the manager from `config.Transport`.
- If the config does not explicitly override that block, the daemon creates one
  native default network plus `nil` mappings for `*.onion`, `*.i2p`, and
  `*.loki`.

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

`ygglib/core/link.go` owns peer lifecycle and handshake policy, but carrier
creation is delegated to the injected `transport.Manager`.

Currently supported carrier families are only those provided by the configured
manager. The reusable `ygglib/transport` package keeps only the generic manager
plus TCP and TLS implementations. The daemon registers additional
`transport.Transport` implementations during node setup for:
- UNIX sockets
- WebSocket (`ws`)
- Secure WebSocket (`wss`) outbound dialing, intended for peers behind a reverse
  proxy
- QUIC

`socks` and `sockstls` remain compatibility aliases that are normalized by
`core` onto the TCP/TLS transport-manager path rather than implemented as
separate daemon transports.

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
- Exposes accessor helpers so higher layers can inspect the current default
  network and host-pattern mappings.
- Provides a small built-in network factory currently supporting `native`, so
  daemon and admin code can create the same network kind consistently.
- The daemon default config keeps the native default network enabled but
  installs `nil` host mappings for `*.onion`, `*.i2p`, and `*.loki`, so those
  peers stay disabled unless a config file or admin API call maps them to a
  real network.
- The daemon layers an additional runtime network resolver on top of the
  transport package so config and admin can also create SOCKS-backed networks
  from objects such as `{type:"socks", proxy_url:"socks5://proxy:1080"}`.
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

### `autopeer`

Purpose:
- Fetch public peer documents from one or more configured source URLs.
- Support one default `gonnect.Network` plus optional source-specific network
  overrides.
- Keep an aggregated, thread-safe snapshot of candidate peers.
- Optionally apply runtime autopeering policy against a small `core`-like
  peer-management interface.

Main types:
- `autopeer.Fetcher`
- `autopeer.Manager`

Behavior:
- Treats sources as an ordered list of document URLs plus the special
  `BUILTIN` source.
- Periodically fetches non-built-in sources one by one, using the effective
  `gonnect.Network` for each source.
- Skips network fetches for sources whose effective network is `nil`, and drops
  their contributed peers from the aggregate so stale data is not retained.
- Loads `BUILTIN` immediately from generated embedded data and never refreshes
  it over the network.
- Logs fetch and parse failures without interrupting other sources.
- `Manager` starts and stops the fetcher, periodically evaluates configured
  thresholds, and attempts to add at most one new public peer per check when
  thresholds are unmet.
- Candidate selection filters endpoints by configured countries and transport
  schemes. If either filter is empty, the manager remains idle.
- Supported thresholds today are:
  - minimum total connected peers
  - minimum connected peers whose URIs are present in the filtered fetcher set
- The manager will not attempt to re-add URIs that `core` already knows about,
  so it complements existing persistent-peer retry logic instead of fighting it.
- When multiple eligible endpoints remain, the manager prefers higher
  `uptime_7d_raw` values with a small random tie-breaker.

Current boundary:
- `autopeer` still does not import `ygglib/core`.
- Integration happens through a tiny peer-management interface implemented by
  higher layers, keeping source fetching and peer-add policy decoupled from the
  transport and handshake logic inside `core`.

#### Protocol subsystem

`protoHandler` is the in-band control plane built on top of `Core.ReadFrom` and
`Core.WriteTo`.

It handles:
- nodeinfo exchange
- remote debug requests/responses
- request tracking and timeouts

This path is separate from the local admin socket. Admin commands may trigger
protocol messages to remote nodes through this subsystem.

### `ygglib/admin`

Purpose:
- Expose a local management API for the daemon.
- Own admin transport, request dispatch, and runtime adapter registration.
- Provide the protocol used by `yggdrasilctl`.

Main type:
- `admin.AdminSocket`

Behavior:
- Listens on UNIX or TCP depending on configuration.
- Accepts JSON requests of the form `{request, arguments, keepalive}`.
- Dispatches to registered handlers and returns JSON responses.
- Exposes `getTransport` and `setTransport` for inspecting and mutating the
  runtime `transport.Manager` used by `core`.

Dependency boundary:
- `ygglib/admin` depends on `ygglib/core` and may also adapt optional runtime
  components like `*multicast.Multicast` and `*tun.TunAdapter`.
- `ygglib/core`, `ygglib/multicast`, and `ygglib/tun` do not depend on `ygglib/admin`.
- `yggd/yggd` is the composition root that decides which adapters to
  register.

The admin package owns transport, request dispatch, and adapter glue. Domain
logic remains in the underlying packages, which expose ordinary public methods
and state accessors instead of accepting an admin instance.

### `ygglib/multicast`

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
- `ygglib/multicast` does not import `ygglib/core`.
- `yggd/yggd` adapts `*core.Core` to the narrow multicast runtime
  interface at startup time.
- Attaching multicast is optional and decided by the binary, not by `core`.

This package is a peer discovery module only. It does not route data packets.

### `ygglib/ipv6rwc`

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

### `ygglib/tun`

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
- Keep `ygglib/` packages free of direct dependencies on `tuntap`, `netlink`,
  platform ioctls, and OS-specific interface setup.

Main API:
- `tunnative.Create(log, tunnative.Config) (gonnect/tun.Tun, error)`

Behavior:
- Uses `github.com/asciimoth/tuntap` for native device creation.
- Performs per-OS address, MTU, and link-up setup where needed.
- Returns a generic `gonnect/tun.Tun` so callers can attach it through
  `ygglib/tun` without importing platform-specific details into library packages.

### `sockstun`

Purpose:
- Create a VTun-backed userspace TUN implementation.
- Expose a local SOCKS server using `github.com/asciimoth/socksgo`.
- Proxy the default `socksgo` command set through the attached VTun netstack.

Main API:
- `sockstun.Create(sockstun.Config) (*sockstun.Tun, error)`

Behavior:
- Builds a `github.com/asciimoth/gonnect-netstack/vtun.VTun` with the node's
  Yggdrasil IPv6 address as the local address.
- Listens on a local TCP address, defaulting to `127.0.0.1:1080`.
- Configures `socksgo.Server` to use the VTun dialer and listener, so local
  applications can reach Yggdrasil IPv6 services without an OS TUN device.
- Optionally wraps the VTun in a mutable `gonnect.Network` router. Host filters
  built with `socksgo.BuildFilter` can send matching CONNECT, BIND, UDP, and
  extension traffic to a second-hop SOCKS proxy reachable over Yggdrasil while unmatched
  traffic continues to use VTun directly. A default fallback SOCKS proxy can
  also handle unmatched non-Yggdrasil destinations; unmatched addresses in
  `200::/7`, including node addresses and routed node subnets, stay direct.
- Runs mnlib DNS resolution before route selection for sockstun dial/listen
  operations. Successful lookups replace the hostname with the resolved address
  before proxy filters or default proxy routing are evaluated. Optional fallback
  DNS uses `gonnect.ResolverCfg` and sends DNS traffic through the same mutable
  route network, so fallback DNS obeys sockstun proxy routing too. The protected
  zones `*.onion`, `*.i2p`, and `*.loki`, plus configured extra no-resolve
  zones, are never resolved and remain hostnames for routing.
- Can optionally install a selective TLS MITM CONNECT handler. When configured
  with user-supplied CA certificate and key files, matching TCP/443 requests are
  selected using the original SOCKS hostname before sockstun DNS resolution or
  proxy routing. Sockstun generates a per-host leaf certificate, terminates the
  client TLS session, and dials plaintext TCP to port 80 on the same hostname
  through the unresolved route path so hostname-based proxy routing still sees
  the original destination. The default intercepted hostname patterns are
  `*.ygg`, `*.meshname`, `*.meship`, `*.onion`, and `*.i2p`.
- Implements `gonnect/tun.Tun` by embedding VTun, and closes both the SOCKS
  listener and VTun when detached or replaced.

### `outproxy`

Purpose:
- Create a VTun-backed userspace TUN implementation.
- Expose a SOCKS server on the node's Yggdrasil address.
- Proxy SOCKS traffic from Yggdrasil clients to the outer network.

Main API:
- `outproxy.Create(outproxy.Config) (*outproxy.Tun, error)`

Behavior:
- Builds a `github.com/asciimoth/gonnect-netstack/vtun.VTun` with the node's
  Yggdrasil IPv6 address as the local address.
- Listens on VTun instead of the local OS network. When `TunSocksListen` uses a
  loopback or unspecified host, outproxy keeps the port and binds to the node's
  Yggdrasil address.
- Uses `gonnect/native` as the default outbound network.
- Supports the same `TunSocksProxies` and `TunSocksDefaultProxy` route controls
  as sockstun, but outbound proxy connections are made through the outer
  network. Outproxy deliberately does not run sockstun's mnlib/fallback DNS
  resolution pipeline.
- Implements `gonnect/tun.Tun` by embedding VTun, and closes both the SOCKS
  listener and VTun when detached or replaced.

### `ygglib/address`

Purpose:
- Define the Yggdrasil IPv6 address and subnet types.
- Convert between public keys and IPv6 addresses/subnets.
- Recover partial keys from addresses/subnets for lookup.

This package is pure logic and is used by `core`, `ipv6rwc`, admin responses,
and tests.

## Binaries

### `yggd/yggd`

The daemon is intentionally small. Its job is to:
- parse flags
- load config
- build the logger using `logger.StdLogger`
- instantiate `core`
- construct `admin`
- register admin adapters for `core`
- optionally attach `multicast`
- register admin adapters for `multicast` when enabled
- create native TUNs through `tunnative` or SOCKS-backed VTuns through
  `sockstun`/`outproxy` when configured
- attach `tun` using `ipv6rwc.NewReadWriteCloser(core)`
- register admin adapters for `tun`, including runtime attach/detach/replace
  commands
- optionally run a local DNS server backed by `mnlib.Resolver`
- manage shutdown ordering

The daemon does not reimplement protocol logic. It is mostly dependency
injection and process lifecycle.

`yggd/yggd` is also the default logging composition point. It chooses the log
destination from `-logto` (`stdout`, `syslog`, or a file), applies `-loglevel`,
and passes the resulting `logger.StdLogger` to the runtime packages.

### `yggd/yggctl`

This is a client for the admin socket.

It:
- connects to the configured admin endpoint
- sends JSON admin requests
- renders responses in JSON or table form

### `yggd/genkeys`

Standalone helper for generating Ed25519 keys with favorable address ordering.
It is operationally separate from the running node.

## Main Runtime Interfaces

### 1. Configuration to runtime

`config.NodeConfig` is translated by `yggd/yggd` into package-specific
options:

- `core.SetupOption`
- `admin.SetupOption`
- `multicast.SetupOption`
- `tun.SetupOption`

TUN type selection and implementation-specific parameters from config are
interpreted in `yggd/yggd`, not inside `ygglib/tun`. Native TUN setup is
delegated to `tunnative`; SOCKS-backed VTun setup is delegated to `sockstun`
and `outproxy`.
When `LocalDNSListen` is configured, `yggd/yggd` also owns the local DNS
server lifecycle. That server answers only `IN A` and `IN AAAA` queries through
`mnlib.Resolver`; when sockstun or outproxy is active it uses the active TUN
SOCKS route network, and otherwise it uses the native network.

This keeps parsing concerns out of runtime packages.

For multicast specifically, `yggd/yggd` is also responsible for:
- deciding whether the module is attached at all
- passing the current Yggdrasil protocol version
- adapting `*core.Core` to the narrow interface expected by `ygglib/multicast`

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

In the default daemon, attachments are created one layer above this via
`tunnative.Create(...)`, `sockstun.Create(...)` or `outproxy.Create(...)` and
then explicitly attached to `tun.TunAdapter`.

### 5. Local control API

`admin.AdminSocket` exposes a handler registry:
- `AddHandler(name, desc, args, handler)`

Runtime wiring is owned by `yggd/yggd`:
- `admin.SetupCoreHandlers()`
- `admin.SetupMulticastHandlers(...)`
- `admin.SetupTunHandlers(...)`

The TUN admin adapter exposes `getTun` and, when a daemon controller is wired
in, `attachTun`, `replaceTun`, and `detachTun`. `attachTun`/`replaceTun` accept
the same implementation type names as config (`native`, `sockstun`, `outproxy`,
`none`) plus implementation options such as `socks_listen`, `socks_proxies`,
`socks_default_proxy`, `socks_dns_fallback`, `socks_no_resolve`, `mtu`, `mwo`,
and `mro`. When the controller supports TUN SOCKS proxy routing, admin also
exposes `getTunSocksProxies` and `setTunSocksProxies` for runtime filter and
fallback proxy updates. Sockstun DNS settings are exposed as `getTunSocksDNS`
and `setTunSocksDNS`; outproxy rejects non-empty DNS routing settings.

This keeps admin transport and adapter glue centralized while leaving business
logic distributed in packages that do not depend on the admin API.

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
3. The handler adapter queries or mutates `core`, `multicast`, or `tun`.

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
