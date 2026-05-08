# Ygg

> [!CAUTION]
> This project is a fork of the original
> [yggdrasil-go](https://github.com/yggdrasil-network/yggdrasil-go) project
> with substantial refactoring and experimental features.
> Both ygg and yggdrasil-go are *EXPERIMENTAL* software and *should NOT* be
> used for production or security-critical purposes.

This project has two main goals:
- Make Yggdrasil easier to embed and reuse as a Go library.
- Provide a place to experiment with features that are outside the current
  scope of upstream yggdrasil-go, including ideas often rejected as
  ["not a goal of Yggdrasil"](https://github.com/yggdrasil-network/yggdrasil-go/issues/1060#issuecomment-1712613462).

The repository keeps compatibility with the Yggdrasil network where practical,
but it is intentionally more willing to move code around, split runtime pieces
into libraries, and carry experimental daemon features.

## Useful Links
- [About Yggdrasil network](https://yggdrasil-network.github.io/)
- Public peers:
  - [Human-readable list](https://github.com/yggdrasil-network/public-peers)
  - [Machine-readable JSON](https://asciimoth.github.io/yggpeers/peers.json)
- [Public services inside yggdrasil](https://yggdrasil-network.github.io/services.html)
- [ygglib tutorial](lib-tutorial.md)
- [Web demo](https://asciimoth.github.io/ygg/)

## What Changed From Upstream
### Library-first layout
Reusable code moved into the `ygglib` submodule and the daemon/CLI moved into
`yggd`. Tightly coupled areas have been split so embedders can compose `core`,
transport setup, VTun, admin handlers, multicast, autopeering, and logging
without copying daemon internals.

### Pluggable transports
Transport implementations are registered at runtime through
`ygglib/transport`. The lightweight library side keeps `tcp` and `tls`
transports, while the daemon wires heavier schemes such as `quic`, `ws`,
`wss` dialing, and `unix`.

Transport setup now runs over the `gonnect.Network` abstraction instead of
using standard-library network primitives directly. This makes it possible to
wrap carrier traffic, test with in-memory networks, or route selected hosts
through other networks.

Host-scoped network mappings are also supported. For example, `.onion`
transport hosts can be sent through Tor without encoding the proxy into a
special transport URI:

```hjson
Transport: {
  DefaultNetwork: native
  NetworkMappings: {
    "*.onion": { Type: socks, ProxyURL: "socks5://127.0.0.1:9050" }
    "*.i2p": { Type: socks, ProxyURL: "socks5://127.0.0.1:4447" }
    "*.loki": null
  }
}
```

### Public-peer autopeering
The `autopeer` module can periodically fetch public peer lists and add matching
peers when configured connectivity thresholds are not met. Sources may be URLs
returning public-peers JSON or the special `BUILTIN` source embedded in the
binary.

```hjson
AutoPeer: {
  Enabled: true
  Sources: [BUILTIN]
  FetchInterval: 1h
  CheckInterval: 1m
  MinimumConnected: 1
  MinimumConnectedFromFetch: 1
  Countries: ["germany", "france", "netherlands"]
  TransportSchemes: ["tls", "tcp"]
}
```

### Browser WASM demo
The `web` module runs an
[Yggdrasil node in the browser](https://asciimoth.github.io/ygg/),
attaches a userspace
VTun stack, dials public peers through browser WebSocket APIs, and exposes
built-in HTTP and IRC clients over VTun.

You can also run it locally with:
```sh
just web-serve
```

Then open `http://127.0.0.1:8000/`.

### More TUN modes
In addition to native OS TUN devices, the daemon can use virtual TUN
implementations backed by an embedded userspace TCP/IP stack:
- `native` creates and configures an OS TUN device.
- `sockstun` creates a VTun netstack and exposes it through a local SOCKS
  server.
- `outproxy` creates a VTun netstack, listens for SOCKS clients inside
  Yggdrasil, and proxies them to the outer network.
- `none` starts without attaching a TUN implementation.

The `sockstun` and `outproxy` modes can run without native TUN privileges when
no other configured option needs them.

### Local SOCKS TUN
`sockstun` provides local SOCKS access to Yggdrasil without an OS TUN device.
It supports [standard and extended SOCKS features](https://github.com/asciimoth/socksgo#features), including bind, UDP, and Tor
extensions through [socksgo lib](https://github.com/asciimoth/socksgo).

Name resolution uses [mnlib](https://github.com/asciimoth/mnlib) for mesh names and related Yggdrasil naming
schemes. Optional fallback DNS ([like alfis](https://alfis.name/)) can be configured for other names, for example
through a public DNS service inside Yggdrasil.

```hjson
TunType: sockstun
IfName: auto
TunSocksListen: 127.0.0.1:1080
TunSocksDNSFallback: "[300:6223::53]:53"
TunSocksDefaultProxy: "socks5://[324:71e:281a:9ed3::fa11]:1080"
```

`sockstun` also has optional selective TLS MITM support. When configured with a
local CA that the client trusts, matching HTTPS requests can be converted to
plain HTTP inside Yggdrasil. This is useful for browsing HTTP-only Yggdrasil
sites from HTTPS-only browser environments.

```hjson
TunSocksTLSMITM: {
  ca_file: "/etc/yggd/sockstun-mitm-ca.crt"
  key_file: "/etc/yggd/sockstun-mitm-ca.key"
  hostnames: ["*.ygg", "*.meshname", "*.meship", "*.onion", "*.i2p"]
}
```

### Yggdrasil-hosted outproxy mode
`outproxy` makes it easier to host Yggdrasil-to-something SOCKS proxies. The
SOCKS listener is reachable on the node's Yggdrasil address, while selected
destinations can be forwarded to local or external proxy services.

```hjson
TunType: outproxy
TunSocksListen: 127.0.0.1:1080 # Will use node's IPv6 addr instead of 127.0.0.1
TunSocksProxies: [
  { Filter: "*.onion", proxy_url: "socks5://127.0.0.1:9050" }
  { Filter: "*.i2p", proxy_url: "socks5://127.0.0.1:4447" }
]
TunSocksDefaultProxy: ""
```

### NodeInfo jumper
The optional `jumper` module is inspired by
[one-d-wide/yggdrasil-jumper](https://github.com/one-d-wide/yggdrasil-jumper).
It watches routed traffic, reads remote NodeInfo, and tries advertised public
peer addresses as direct links.

Unlike the original jumper idea, this implementation publishes addresses in
NodeInfo instead of using a locally hosted discovery service. It does not
perform NAT traversal yet.

```hjson
Jumper: {
  Enabled: true
  Addresses: ["tls://example.net:12345"]
  CheckInterval: 10s
  LinkTimeout: 30s
}
```

### TUN firewall
The daemon restores a built-in TUN firewall feature that was removed from
upstream yggdrasil-go. The default behavior is conservative for native TUN
usage because [many people run Yggdrasil is production](https://habr.com/ru/companies/radcop/articles/771776/) despite its
experimental status.

When `enabled` is left unset, the daemon enables the firewall for `native` TUN
and disables it for VTun-backed modes. ICMPv6 is always allowed. Outgoing TCP
and UDP create temporary return-flow entries. Unsolicited incoming TCP and UDP
are allowed only for configured destination ports.

```hjson
TunFirewall: {
  enabled: true
  allowed_tcp_ports: [22, 80, 443]
  allowed_udp_ports: [53]
}
```

### Daemon and admin improvements
- `yggd` can auto-create a config file when it is missing.
- An optional HTTP admin API and minimal web admin panel can be enabled with
  `AdminWebListen`.
- Runtime admin handlers cover transport mappings, autopeering, jumper,
  firewall, and TUN attach/detach/replace operations.
- A full commented example config is available in [example.conf](example.conf).

### More tests
The test suite includes package tests, upstream compatibility tests, transport
manager tests, autopeering tests, jumper tests, sockstun/outproxy tests, TUN
firewall tests, and complex multi-node topology tests.

```sh
just test-total
```
> [!NOTE]
> The Docker-based integration suites are Linux-focused and use `sudo`.
> See [TESTING.md](TESTING.md).

## Minimal Library Usage
For a complete runnable example, see
[examples](./examples) and
[lib-tutorial.md](./lib-tutorial.md). A minimal core setup looks like this:

```go
package main

import (
	"fmt"

	"github.com/asciimoth/gonnect/native"
	"github.com/asciimoth/ygg/ygglib/config"
	"github.com/asciimoth/ygg/ygglib/core"
	ygglogger "github.com/asciimoth/ygg/ygglib/logger"
	"github.com/asciimoth/ygg/ygglib/transport"
)

func main() {
	cfg := config.GenerateConfig()
	if err := cfg.GenerateSelfSignedCertificate(); err != nil {
		panic(err)
	}

	network := &native.Network{}
	if err := network.Up(); err != nil {
		panic(err)
	}
	defer network.Down()

	manager := transport.NewManager(network)
	if err := manager.RegisterTransport(transport.NewTCPTransport()); err != nil {
		panic(err)
	}

	tlsConfig, err := core.GenerateTLSConfig(cfg.Certificate)
	if err != nil {
		panic(err)
	}
	if err := manager.RegisterTransport(transport.NewTLSTransport(tlsConfig)); err != nil {
		panic(err)
	}

	node, err := core.New(
		cfg.Certificate,
		ygglogger.Discard(),
		core.TransportManager{Manager: manager},
	)
	if err != nil {
		panic(err)
	}
	defer node.Stop()

	fmt.Println(node.Address())
}
```

## Installing yggd
### Nix
Build or run directly from the flake:
```sh
nix build github:asciimoth/ygg#yggd
nix run github:asciimoth/ygg#yggd
nix run github:asciimoth/ygg#yggctl -- -endpoint tcp://localhost:9001 getSelf
nix profile add github:asciimoth/ygg#yggd
```

NixOS users can import the module:
```nix
{
  inputs.ygg.url = "github:asciimoth/ygg";

  outputs = { self, nixpkgs, ygg, ... }: {
    nixosConfigurations.example = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        ygg.nixosModules.yggd
        {
          services.yggd.enable = true;
          services.yggd.settings = {
            Peers = [ "tls://example.net:12345" ];
            TunType = "native";
          };
        }
      ];
    };
  };
}
```

### Deb, and rpm-based systems
Packages are published to [my deb/rpm repository](https://repo.moth.contact):

Setup it for your sytstem via script (or manually):
```sh
curl https://repo.moth.contact/setup.sh | bash
```

Then install with your system package manager:
```sh
sudo apt install yggd
# or
sudo dnf install yggd
# or
sudo yum install yggd
```

### GitHub Releases
Release archives and package artifacts are published on the
[GitHub releases page](https://github.com/asciimoth/ygg/releases). Archives
include `yggd`, `yggctl`, and `genkeys`.

### Arch
[AUR](https://aur.archlinux.org/packages/yggd-bin) is available

## Configuration
Generate a config:
```sh
yggd -genconf > ygg.conf
```

Start with an explicit config:
```sh
sudo yggd -useconffile ygg.conf -logto stdout
```

The daemon also supports auto-config mode:
```sh
sudo yggd -autoconf -logto stdout
```

For a full commented config, see [example.conf](example.conf).

## TODO
- Add an IRC server to the web demo so two browser users can connect to each
  other directly
- Add NAT traversal to jumper
- Add a WebRTC transport
- Build a desktop admin panel
- Build an Android app
- Add a built-in pretty-address miner
- Improve [test coverage](https://coveralls.io/github/asciimoth/ygg)
- Optimize hot paths:
  - Use [bufpool](https://github.com/asciimoth/bufpool) where it matter
  - Reduce lock and synchronization overhead
- Add man pages
- Package for more platforms

## License
This code is released under the terms of the LGPLv3, but with an added
exception that was taken from [godeb](https://github.com/niemeyer/godeb).
Under certain circumstances, this exception permits distribution of binaries
that are statically or dynamically linked with this code, without requiring the
distribution of Minimal Corresponding Source or Minimal Application Code.
For more details, see [LICENSE](LICENSE).
