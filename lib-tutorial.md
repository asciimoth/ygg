# ygglib Tutorial

This tutorial shows the library composition used by the runnable companion in
[`examples/lib_tutorial`](examples/lib_tutorial). Run it with:

```sh
go run ./examples/lib_tutorial
go test ./examples/lib_tutorial --race
```

The example creates two in-process Yggdrasil nodes, peers them through a custom
transport, attaches a VTun to each node, then sends TCP, UDP, and HTTP traffic
over the Yggdrasil Core.

See [`lib_tutorial example`](./examples/lib_tutorial/main.go) for the
complete runnable code.

## Step 1: Choose a Carrier Network

`core.Core` does not create transport networks itself. The embedding program
chooses the `gonnect.Network` used by carrier transports.

The tutorial example uses an in-memory loopback network so tests are
deterministic:

```go
network := loopback.NewLoopbackNetwok()
```

Production code usually uses a native network:

```go
network := &native.Network{}
if err := network.Up(); err != nil {
	return err
}
defer network.Down()
```

## Step 2: Plug Transport Implementations

A transport implements `transport.Transport`. It declares one or more URL
schemes and turns a selected `gonnect.Network` plus URL into a `net.Conn` or
`net.Listener`.

The example wraps the built-in TCP transport with a custom `metered+tcp`
scheme:

```go
type meteredTransport struct {
	base    transport.Transport
	dials   atomic.Uint64
	listens atomic.Uint64
}

func (t *meteredTransport) Schemes() []string {
	return []string{"metered+tcp"}
}

func (t *meteredTransport) Dial(
	ctx context.Context,
	network transport.Network,
	u *url.URL,
	opts transport.Options,
) (transport.Conn, error) {
	t.dials.Add(1)
	return t.base.Dial(ctx, network, rewriteScheme(u, "tcp"), opts)
}

func (t *meteredTransport) Listen(
	ctx context.Context,
	network transport.Network,
	u *url.URL,
	opts transport.Options,
) (transport.Listener, error) {
	t.listens.Add(1)
	return t.base.Listen(ctx, network, rewriteScheme(u, "tcp"), opts)
}
```

Register transports before constructing `core.Core`:

```go
manager := transport.NewManager(nil)

if err := manager.RegisterTransport(&meteredTransport{
	base: transport.NewTCPTransport(),
}); err != nil {
	return err
}

tlsConfig, err := core.GenerateTLSConfig(cert)
if err != nil {
	return err
}
if err := manager.RegisterTransport(transport.NewTLSTransport(tlsConfig)); err != nil {
	return err
}
```

## Step 3: Use Non-Default Network Mappings

`transport.Manager` chooses a carrier network by host. You can set a default
network for all unmatched hosts, or leave the default as `nil` and route only
explicit mappings.

The example intentionally uses no default network and maps only `127.0.0.1`:

```go
manager := transport.NewManager(nil)
if err := manager.MapNetwork("127.0.0.1", network); err != nil {
	return err
}
```

Useful patterns include:

```go
manager.SetDefaultNetwork(nativeNetwork)
_ = manager.MapNetwork("*.onion", torNetwork)
_ = manager.MapNetwork("*.i2p", i2pNetwork)
_ = manager.MapNetwork("*.loki", nil)
```

A `nil` mapping disables matching hosts. Mapping changes are live: the manager
closes affected listeners and connections so new dials use the new mapping.

## Step 4: Create Core

Generate or load identity material, then pass the prepared transport manager
into `core.New`:

```go
cfg := config.GenerateConfig()
if err := cfg.GenerateSelfSignedCertificate(); err != nil {
	return err
}

coreNode, err := core.New(
	cfg.Certificate,
	ygglogger.Discard(),
	core.TransportManager{Manager: manager},
)
if err != nil {
	return err
}
defer coreNode.Stop()
```

A listener plus a peer call is enough to connect two cores:

```go
listener, err := serverCore.Listen(mustParseURL("metered+tcp://127.0.0.1:0"), "")
if err != nil {
	return err
}

peerURL := mustParseURL("metered+tcp://" + listener.Addr().String())
if err := clientCore.CallPeer(peerURL, ""); err != nil {
	return err
}
```

Use `AddPeer` instead of `CallPeer` when the link should be persistent and
retried after disconnects.

## Step 5: Attach VTun

Core routes encrypted packets. VTun turns that packet stream into a userspace
IPv6 network that can run normal TCP, UDP, and HTTP clients and servers.

```go
rwc := ipv6rwc.NewReadWriteCloser(coreNode)
adapter, err := yggtun.New(rwc, ygglogger.Discard(), yggtun.InterfaceMTU(1500))
if err != nil {
	return err
}

addr, ok := netip.AddrFromSlice(coreNode.Address())
if !ok {
	return fmt.Errorf("invalid core address")
}

vt, err := (&vtun.Opts{
	Name:           "node-a",
	LocalAddrs:     []netip.Addr{addr},
	NoLoopbackAddr: true,
	NetStackOpts: &helpers.Opts{
		MTU: 1500,
	},
}).Build()
if err != nil {
	return err
}

if err := adapter.Attach(vt, yggtun.AttachmentType("vtun")); err != nil {
	return err
}
```

## Step 6: Run TCP Over Core

After both nodes are peered and have VTun attached, use the server node's
Yggdrasil IPv6 address as the listen address:

```go
listener, err := serverVT.Listen(
	context.Background(),
	"tcp6",
	net.JoinHostPort(serverCore.Address().String(), "0"),
)
if err != nil {
	return err
}

conn, err := clientVT.Dial(context.Background(), "tcp6", listener.Addr().String())
if err != nil {
	return err
}
```

The full example accepts a TCP connection, reads `ping`, and replies with
`tcp:ping`.

## Step 7: Run UDP Over Core

Use `ListenPacket` on the server VTun and `Dial` with a UDP network on the
client VTun:

```go
packetConn, err := serverVT.ListenPacket(
	context.Background(),
	"udp6",
	net.JoinHostPort(serverCore.Address().String(), "0"),
)
if err != nil {
	return err
}

conn, err := clientVT.Dial(context.Background(), "udp6", packetConn.LocalAddr().String())
if err != nil {
	return err
}
```

The compiled example sends `ping` and receives `udp:ping`.

## Step 8: Run HTTP Over Core

HTTP needs only a VTun-backed listener and a client transport whose
`DialContext` uses the client VTun:

```go
listener, err := serverVT.Listen(
	context.Background(),
	"tcp6",
	net.JoinHostPort(serverCore.Address().String(), "0"),
)
if err != nil {
	return err
}

server := &http.Server{
	Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "http:pong")
	}),
	ReadHeaderTimeout: 10 * time.Second,
}
go server.Serve(listener)

client := http.Client{
	Transport: &http.Transport{
		DialContext: clientVT.Dial,
	},
	Timeout: 10 * time.Second,
}
```

Build the request URL from the server Core address and listener port:

```go
target := fmt.Sprintf("http://[%s]:%s", serverCore.Address().String(), port)
resp, err := client.Get(target)
```

## Step 9: Set Up Autopeering With Public Peers

`autopeer.Manager` combines a public-peer fetcher with a peer-management policy.
Use `BUILTIN` for embedded public peers list, or add remote public-peer sources
URLs when the selected network can reach them.

```go
fetcher := autopeer.NewFetcher(ygglogger.Discard(), time.Hour)
fetcher.SetDefaultNetwork(nativeNetwork)
fetcher.SetSources([]string{autopeer.BuiltinSource})

manager := autopeer.NewManager(fetcher)
manager.SetPeerManager(coreNode)
manager.SetConfig(autopeer.ManagerConfig{
	CheckInterval:    time.Minute,
	MinimumConnected: 2,
	Countries:        []string{"germany", "france", "netherlands"},
	TransportSchemes: []string{"tcp", "tls"},
})

manager.Start()
defer manager.Close()
```

The manager stays idle unless both country filters and transport-scheme filters
are configured. It uses `core.AddPeer` so selected peers become persistent.

## Step 10: Set Up Link-Local Autopeering

Link-local autopeering is provided by `ygglib/multicast`. It listens for local
beacons and calls `core.CallPeer` for discovered nodes.

This works only with a native carrier network. Link-local IPv6 addresses and
interface-scoped listeners rely on OS network interfaces, which emulated or
proxied `gonnect.Network` implementations do not provide.

```go
if network == nil || !network.IsNative() {
	return fmt.Errorf("link-local autopeering requires a native carrier network")
}

mc, err := multicast.New(
	multicastCoreAdapter{core: coreNode},
	ygglogger.Discard(),
	multicast.ProtocolVersion{
		Major: core.ProtocolVersionMajor,
		Minor: core.ProtocolVersionMinor,
	},
	multicast.MulticastInterface{
		Regex:  regexp.MustCompile("^(eth|en|wlan|wl).*"),
		Beacon: true,
		Listen: true,
		Port:   0,
	},
)
if err != nil {
	return err
}
defer mc.Stop()
```

The adapter is small and mirrors the daemon:

```go
type multicastCoreAdapter struct {
	core *core.Core
}

func (a multicastCoreAdapter) ListenLocal(u *url.URL, sintf string) (multicast.Listener, error) {
	return a.core.ListenLocal(u, sintf)
}

func (a multicastCoreAdapter) CallPeer(u *url.URL, sintf string) error {
	return a.core.CallPeer(u, sintf)
}

func (a multicastCoreAdapter) PublicKey() ed25519.PublicKey {
	return a.core.PublicKey()
}
```

