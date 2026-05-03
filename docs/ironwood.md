# Ironwood Public Interface and Integration Guide

This document describes the public API and the externally observable behavior of
Ironwood as it exists in this tree.

It is written for consumers who want to build a complete system on top of
Ironwood, including something with the same integration shape as
[yggdrasil-go](https://github.com/yggdrasil-network/yggdrasil-go).

Ironwood is explicitly pre-alpha.
The API described here is the current contract of the codebase,
not a stability promise across future commits.

## What Ironwood is

Ironwood is a routed `net.PacketConn`-like library whose node addresses are
`ed25519.PublicKey` values.

Ironwood does **not** provide:
- peer discovery
- peer authentication outside of the selected wrapper package
- a listener/dialer API
- a transport protocol for peers
- a TUN/TAP interface
- an IP address scheme

Ironwood **does** provide:
- a per-node identity based on an Ed25519 keypair
- multi-hop packet routing between those identities
- a way to attach direct peers using externally created `net.Conn` streams
- optional message signing or authenticated encryption wrappers
- a lookup mechanism for destinations that are known only partially

## Packages

### `types`

`types` contains the interface and address/error types that consumers should
usually program against.

`types.PacketConn` extends `net.PacketConn` with:

- `HandleConn(key ed25519.PublicKey, conn net.Conn, prio uint8) error`
- `IsClosed() bool`
- `PrivateKey() ed25519.PrivateKey`
- `MTU() uint64`
- `SendLookup(target ed25519.PublicKey)`

`types.Addr` is a `net.Addr` wrapper around `ed25519.PublicKey`.

### `network`

`network` is the core routed transport. Payloads are routed but otherwise
plaintext and unsigned.

Use this package only if you are adding your own integrity/confidentiality
layer above it, or if plaintext routing is acceptable for your environment.

### `signed`

`signed` wraps `network` and adds per-message Ed25519 signatures.

This provides sender authentication and message integrity, but not confidentiality.

### `encrypted`

`encrypted` wraps `network` and adds authenticated encryption using ephemeral
`nacl/box` sessions with ratcheting and replay protection.

This is the package to use for a Yggdrasil-like overlay.

## Core model

Each local node is created from an Ed25519 private key.
Its public key is its stable network identity and packet address.

Application traffic is sent with `WriteTo(payload, types.Addr(remotePubKey))`
and received with `ReadFrom`.

Routing only works once the local node has one or more direct peers.
A direct peer is added by creating a reliable ordered full-duplex
`net.Conn` yourself and passing it to `HandleConn`.

Ironwood expects every direct peer connection to already be associated with
exactly one remote Ed25519 public key.

## Constructing a node

Create one packet connection per local identity:

```go
pc, err := encrypted.NewPacketConn(priv, opts...)
```

or:

```go
pc, err := signed.NewPacketConn(priv, opts...)
```

or:

```go
pc, err := network.NewPacketConn(priv, opts...)
```

The returned concrete type satisfies `types.PacketConn`.

`LocalAddr()` returns `types.Addr`, which is the local public key.

`PrivateKey()` returns the private key used at construction time.

## Addressing

Ironwood addresses are raw Ed25519 public keys.

- `LocalAddr()` returns a `types.Addr`
- `ReadFrom()` returns a `types.Addr`
- `WriteTo()` requires the destination `net.Addr` to be a `types.Addr`

Passing any other address type to `WriteTo()` returns `types.ErrBadAddress`.

`types.Addr.String()` is the hex encoding of the public key.
`types.Addr.Network()` returns `"ed25519.PublicKey"`.

## Direct peer integration

### Required transport properties

`HandleConn` requires a `net.Conn` with stream semantics equivalent to TCP:

- reliable
- ordered
- full-duplex
- long-lived

Message boundaries are handled by Ironwood.
Do not frame or multiplex unrelated application traffic on the same stream
unless you add your own stream multiplexer and pass Ironwood a dedicated
substream with the same semantics.

Ironwood sets deadlines on the peer connection internally for liveness detection.
Treat the `net.Conn` as fully owned by Ironwood after calling `HandleConn`.

### Expected handshake shape

Ironwood does not negotiate peer identity on the wire for you. The usual pattern is:

1. Discover or configure a peer using your own mechanism.
2. Open a `net.Conn` to that peer using your own transport.
3. Exchange public keys using your own handshake.
4. Verify that the remote key is the one you expected.
5. Call `HandleConn(remoteKey, conn, prio)` on both sides.

The example program under `cmd/ironwood-example` does exactly this using
multicast discovery plus a TCP connection that starts by exchanging raw public keys.

### `HandleConn` behavior

`HandleConn`:

- blocks for as long as the peer connection is active
- returns when the peer connection fails, is removed, or the packet conn is closed
- always closes the supplied `net.Conn` before returning
- returns `types.ErrBadKey` if the provided key length is invalid or if it is the local node's own key
- returns `types.ErrClosed` quickly if the packet conn has already been closed

You normally run one goroutine per direct peering:

```go
go func() {
    if err := pc.HandleConn(remoteKey, conn, 0); err != nil {
        // log disconnect
    }
}()
```

### Multiple links to the same peer

Multiple direct links to the same remote public key are allowed.

The `prio` argument is a link preference for duplicate links to the same key:

- lower numeric `prio` is preferred
- higher numeric `prio` acts as a fallback

Priority does not rank different remote keys against each other.
It only breaks ties between multiple links to the same node identity.

## Sending and receiving packets

### `WriteTo`

`WriteTo` accepts a payload and a destination `types.Addr`.

Important observable behavior:

- success means the packet was accepted locally, not that it was delivered remotely
- payloads larger than `MTU()` fail with `types.ErrOversizedMessage`
- writes to a closed packet conn fail with `types.ErrClosed`
- writes do not block on remote delivery
- write deadlines are ignored by the core implementation

### `ReadFrom`

`ReadFrom` returns the payload and the sender `types.Addr`.

Important observable behavior:

- if the destination buffer is too small, the payload is truncated to fit
- the returned byte count is the number of bytes copied into your buffer
- reads unblock with `types.ErrClosed` after `Close()`
- reads respect `SetReadDeadline` and return `types.ErrTimeout`
- if you stop reading, Ironwood can accumulate queued traffic and may eventually drop packets

The core `network.PacketConn` explicitly warns that failing to call `ReadFrom` may block the connection and/or leak memory.

## MTU

Always call `MTU()` on the concrete packet conn you are using and treat that as the largest safe payload size.

The wrappers reduce usable MTU:

- `network.PacketConn`: base routed payload MTU
- `signed.PacketConn`: base MTU minus one Ed25519 signature
- `encrypted.PacketConn`: base MTU minus encrypted session overhead

The default peer maximum message size is 1 MiB, but the safe application MTU is smaller because Ironwood adds protocol overhead.

## Closing and deadlines

`Close()`:

- shuts down the packet conn
- closes all active direct peer streams
- causes blocked `ReadFrom` calls to return `types.ErrClosed`
- causes `HandleConn` calls to return quickly
- returns `types.ErrClosed` if called more than once

Deadline behavior:

- `SetReadDeadline` works
- `SetDeadline` only affects reads in practice
- `SetWriteDeadline` is a no-op in the core implementation

If your application assumes real write deadlines, you must enforce them outside Ironwood.

## Route discovery, lookups, and partial keys

Ironwood can send directly to a full destination public key with `WriteTo`. If it does not yet know a usable route, it starts internal lookup/path discovery.

`SendLookup(target ed25519.PublicKey)` exists for consumers that know only a partial or transformed destination key.

This is the key mechanism used by Yggdrasil-like integrations:

- applications often route by an address derived from the public key, not by the full key itself
- several full public keys may map to the same routable prefix or transformed value
- the consumer asks Ironwood to discover the full key that matches that transformed value

Two options control this flow:

- `WithBloomTransform(func(ed25519.PublicKey) ed25519.PublicKey)`
- `WithPathNotify(func(ed25519.PublicKey))`

### `WithBloomTransform`

Ironwood uses transformed keys in its lookup bloom filters.

The transform must map:

- a full remote public key, and
- any partial key representation you pass to `SendLookup`

to the same transformed value.

For Yggdrasil-style addressing, this is typically "derive the overlay address prefix from the public key, then turn that address back into a lookup key-shaped byte string".

### `WithPathNotify`

The callback is invoked when Ironwood learns or refreshes a usable full public key for a lookup target.

The callback receives the full responder public key, not the transformed lookup key.

Use this callback to:

- populate your own address-to-key cache
- flush any application-side packets buffered while waiting for resolution

### Important delivery caveat during discovery

Ironwood does some internal packet caching during path/session establishment, but it is intentionally shallow.

For unresolved routing state in `network`:

- at most one pending packet is retained per transformed destination lookup
- newer packets replace older cached packets

For unresolved encrypted session state in `encrypted`:

- at most one pending packet is retained per remote key before a session exists
- newer packets replace older cached packets

This means `WriteTo` is **not** a reliable queue while a route or encrypted session is being established. If your application must not lose early packets, you should buffer them yourself and flush them from `WithPathNotify`, exactly like the example program does.

## Public options in `network`

All constructors accept `network.Option`.

### Topology and maintenance

- `WithRouterRefresh(time.Duration)`
- `WithRouterTimeout(time.Duration)`

These tune route announcement refresh and expiry behavior.

### Direct peer liveness

- `WithPeerKeepAliveDelay(time.Duration)`
- `WithPeerTimeout(time.Duration)`

These tune keepalive timing and failure detection for direct peer streams.

### Message sizing

- `WithPeerMaxMessageSize(uint64)`

This changes the maximum protocol message size on each direct peer stream and therefore changes `MTU()`.

All directly connected peers in a deployment should use compatible sizing.

### Lookup behavior

- `WithBloomTransform(func(ed25519.PublicKey) ed25519.PublicKey)`
- `WithPathNotify(func(ed25519.PublicKey))`
- `WithPathTimeout(time.Duration)`
- `WithPathThrottle(time.Duration)`

These control transformed-key lookup behavior, path cache lifetime, and lookup retry rate.

## Security and payload semantics by package

### `network`

- payloads are plaintext
- application payloads are not signed
- sender addresses are whatever the routing layer accepted as source identity

Use this package only inside another authenticated or encrypted transport, or when insecure payload carriage is acceptable.

### `signed`

`signed.PacketConn` signs each payload with the local Ed25519 key.

Observable behavior:

- outgoing overhead is one 64-byte Ed25519 signature
- incoming packets with invalid signatures are silently discarded
- `ReadFrom` loops until it gets a valid signed packet or an underlying read error
- sender authenticity is tied to the source public key returned by `ReadFrom`

The signed wrapper signs `destinationPublicKey || plaintext`. That binds the signature to the intended destination as well as the message body.

### `encrypted`

`encrypted.PacketConn` provides authenticated encryption and replay-resistant session traffic.

Observable behavior:

- first traffic to a remote key may be delayed while a session is created
- idle sessions eventually expire and are recreated on later traffic
- usable MTU is smaller than in `network`
- `ReadFrom` returns only decrypted application payloads

For a Yggdrasil-like overlay, this is the normal package to build on.

## Debug APIs

These are public, but operational rather than transport-critical.

### `network.PacketConn.Debug`

Available snapshot methods:

- `GetSelf()`
- `GetPeers()`
- `GetTree()`
- `GetPaths()`
- `GetBlooms()`
- `SetDebugLookupLogger(func(DebugLookupInfo))`

These expose routing, peer, path, and bloom-filter state for diagnostics.

### `encrypted.PacketConn.Debug`

Available snapshot method:

- `GetSessions()`

This exposes current encrypted session state and byte counters.

## Errors you should expect

From `types`:

- `ErrClosed`
- `ErrTimeout`
- `ErrOversizedMessage`
- `ErrBadAddress`
- `ErrBadKey`
- `ErrPeerNotFound`
- `ErrBadMessage`
- `ErrEmptyMessage`
- `ErrEncode`
- `ErrDecode`
- `ErrUnrecognizedMessage`

You may also receive underlying transport errors from `HandleConn`, because it owns a real `net.Conn`.

## Integration recipe for building something like Yggdrasil

### 1. Choose `encrypted`

Use `encrypted.NewPacketConn` with one long-lived Ed25519 identity per node.

### 2. Define an address scheme

Derive your overlay address from the Ed25519 public key.

Keep two helpers:

- full key -> overlay address
- overlay address or prefix -> lookup key representation for `SendLookup`

### 3. Configure lookup hooks

Set:

- `WithBloomTransform` to convert keys into the value used for routing lookups
- `WithPathNotify` to learn full keys and flush buffered traffic

### 4. Maintain your own address-to-key cache

When you receive a packet, you know the sender's full public key. Cache the mapping from your overlay address to that key.

When `WithPathNotify` fires, update the same cache.

### 5. Buffer application packets externally while resolving unknown destinations

When you know only an overlay address:

1. consult your cache
2. if the full key is known, `WriteTo`
3. if not known, derive the lookup key, call `SendLookup`, and buffer the packet yourself
4. when `WithPathNotify` fires, retry the buffered packet to the resolved full key

This external buffering is necessary because Ironwood itself retains only a very small amount of unresolved traffic.

### 6. Provide peer discovery and peering transport yourself

You need your own machinery for:

- static peers
- multicast peer discovery
- inbound listeners
- outbound dialing
- reconnect loops
- optional peer authentication policy

The bundled example uses:

- IPv6 multicast to advertise public keys on a LAN
- TCP for the direct peer stream
- a simple initial key exchange over that TCP stream

That is only one possible transport design. Any reliable ordered stream works.

### 7. Run one read loop

Have one or more goroutines continuously calling `ReadFrom` and handing payloads to your overlay protocol or virtual interface.

Do not leave the packet conn unread.

## Minimal usage example

```go
package main

import (
    "crypto/ed25519"
    "net"

    iw "github.com/Arceliar/ironwood/encrypted"
    iwn "github.com/Arceliar/ironwood/network"
    iwt "github.com/Arceliar/ironwood/types"
)

func main() {
    _, priv, _ := ed25519.GenerateKey(nil)
    pc, _ := iw.NewPacketConn(priv, iwn.WithPathNotify(func(key ed25519.PublicKey) {
        // update key cache / flush buffered traffic
    }))
    defer pc.Close()

    _ = func(remoteKey ed25519.PublicKey, conn net.Conn) error {
        go pc.HandleConn(remoteKey, conn, 0)
        return nil
    }

    _ = func(remoteKey ed25519.PublicKey, msg []byte) error {
        _, err := pc.WriteTo(msg, iwt.Addr(remoteKey))
        return err
    }

    buf := make([]byte, 65535)
    for {
        n, from, err := pc.ReadFrom(buf)
        if err != nil {
            return
        }
        _ = n
        _ = from.(iwt.Addr)
        // handle payload
    }
}
```

## Practical rules

- Program to `types.PacketConn`, but construct with `network`, `signed`, or `encrypted`.
- Treat `types.Addr` as the only valid packet address type.
- Always own peer discovery and peer transport outside Ironwood.
- Give `HandleConn` a dedicated reliable ordered stream and let Ironwood own it.
- Use `MTU()` instead of assuming a payload size.
- Keep reading continuously.
- Buffer unresolved traffic yourself if first-packet loss matters.
- Use `encrypted` unless you have a strong reason not to.

