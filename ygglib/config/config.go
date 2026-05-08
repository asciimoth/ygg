/*
The config package contains structures related to the configuration of an
Yggdrasil node.

The configuration contains, amongst other things, encryption keys which are used
to derive a node's identity, information about peerings and node information
that is shared with the network. There are also some module-specific options
related to TUN, multicast and the admin socket.

In order for a node to maintain the same identity across restarts, you should
persist the configuration onto the filesystem or into some configuration storage
so that the encryption keys (and therefore the node ID) do not change.

Note that Yggdrasil will automatically populate sane defaults for any
configuration option that is not provided.
*/
package config

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/hjson/hjson-go/v4"
	"golang.org/x/text/encoding/unicode"
)

// NodeConfig is the main configuration structure, containing configuration
// options that are necessary for an Yggdrasil node to run. You will need to
// supply one of these structs to the Yggdrasil core when starting a node.
type NodeConfig struct {
	PrivateKey           KeyBytes                   `json:",omitempty" comment:"Your private key. DO NOT share this with anyone!"`
	PrivateKeyPath       string                     `comment:"The path to your private key file in PEM format. If this is set,\nYggdrasil will load the private key from this file instead of using\nthe inline \"PrivateKey\" value above."`
	Certificate          *tls.Certificate           `json:"-"`
	Peers                []string                   `comment:"List of outbound peer connection strings (e.g. tls://a.b.c.d:e,\nquic://a.b.c.d:e or socks://a.b.c.d:e/f.g.h.i:j).\nConnection strings can contain options,\nsee https://yggdrasil-network.github.io/configurationref.html#peers.\nYggdrasil has no concept of bootstrap nodes - all network traffic\nwill transit peer connections. Therefore make sure to only peer with\nnearby nodes that have good connectivity and low latency. Avoid adding\npeers to this list from distant countries as this will worsen your\nnode's connectivity and performance considerably."`
	InterfacePeers       map[string][]string        `comment:"List of connection strings for outbound peer connections in URI format,\narranged by source interface, e.g. { \"eth0\": [ \"tls://a.b.c.d:e\" ] }.\nYou should only use this option if your machine is multi-homed and you\nwant to establish outbound peer connections on different interfaces.\nOtherwise you should use \"Peers\"."`
	Listen               []string                   `comment:"Listen addresses for incoming connections. You will need to add\nlisteners in order to accept incoming peerings from non-local nodes.\nThis is not required if you wish to establish outbound peerings only.\nMulticast peer discovery will work regardless of any listeners set\nhere. Each listener should be specified in URI format as above.\nSupported daemon listener schemes are tcp, tls, ws, quic and unix.\nUse wss for outbound peers behind a secure WebSocket reverse proxy;\ndirect wss listeners are not supported. WebSocket listeners can allow\ncross-origin browser clients with origin=host-pattern or origin=*.\nExample listeners:\ntls://0.0.0.0:0, quic://[::]:0, ws://0.0.0.0:0, ws://0.0.0.0:0?origin=* or unix:///var/run/ygg.sock."`
	AdminListen          string                     `comment:"Listen address for admin connections. Default is to listen for local\nconnections either on TCP/9001 or a UNIX socket depending on your\nplatform. Use this value for yggdrasilctl -endpoint=X. To disable\nthe admin socket, use the value \"none\" instead.\n\nIf this is left at the platform default and startup TUN does not need\nnative OS TUN privileges (TunType \"none\", \"sockstun\" or \"outproxy\", or the\n\"socks\" alias), Yggdrasil falls back to tcp://localhost:9001 when it cannot\ncreate a privileged UNIX socket path such as /var/run/yggdrasil.sock.\nSet AdminListen explicitly to force a specific admin endpoint."`
	AdminWebListen       string                     `comment:"Optional TCP listen address for the HTTP admin API and web panel.\nWhen set, requests under /.yggapi are dispatched to the same admin\nhandlers as the local admin socket, while other paths serve static files.\nUse \"none\" or an empty value to disable it. Example:\nAdminWebListen: 127.0.0.1:9002"`
	AdminWebStaticDir    string                     `comment:"Optional directory of static files for the HTTP admin panel. When empty,\nYggdrasil serves its built-in minimal web interface. This only has effect\nwhen AdminWebListen is enabled."`
	LocalDNSListen       string                     `comment:"Optional local DNS listen address for mesh-name resolution. When set,\nYggdrasil starts a simple local DNS server that answers IN A and IN AAAA\nqueries through mnlib.Resolver. Unsupported record types are rejected.\nExample:\nLocalDNSListen: 127.0.0.1:5353"`
	MulticastInterfaces  []MulticastInterfaceConfig `comment:"Configuration for which interfaces multicast peer discovery should be\nenabled on. Regex is a regular expression which is matched against an\ninterface name, and interfaces use the first configuration that they\nmatch against. Beacon controls whether or not your node advertises its\npresence to others, whereas Listen controls whether or not your node\nlistens out for and tries to connect to other advertising nodes. See\nhttps://yggdrasil-network.github.io/configurationref.html#multicastinterfaces\nfor more supported options."`
	AllowedPublicKeys    []string                   `comment:"List of peer public keys to allow incoming peering connections\nfrom. If left empty/undefined then all connections will be allowed\nby default. This does not affect outgoing peerings, nor does it\naffect link-local peers discovered via multicast.\nWARNING: THIS IS NOT A FIREWALL and DOES NOT limit who can reach\nopen ports or services running on your machine!"`
	Transport            TransportConfig            `comment:"Configuration for the transport manager networks used by core.\nIf this block is omitted entirely, Yggdrasil uses the built-in\nnative network as the default network and installs nil host-based\nmappings for *.onion, *.i2p and *.loki so those peers stay disabled\nunless you enable them explicitly. Set DefaultNetwork to null to\ndisable the default network entirely. Set a NetworkMappings entry\nto null to keep the mapping but disable its network. Supported\nnon-null values today are \"native\" or a socks network object with a\nProxyURL such as \"socks5://proxy:1080\"."`
	AutoPeer             AutoPeerConfig             `comment:"Configuration for public-peer autopeering. When enabled, Yggdrasil\nwill periodically fetch peer candidates from configured sources and\nadd one matching peer when your runtime connectivity thresholds are\nnot met. Sources may be URLs returning public-peers JSON documents\nor the special value \"BUILTIN\" for the embedded list."`
	Jumper               JumperConfig               `comment:"Configuration for optional NodeInfo-based direct peering. When enabled,\nYggdrasil watches routed traffic, fetches the remote node's NodeInfo,\nand tries explicitly published jumper addresses as direct peer links.\nIt does not perform NAT traversal."`
	TunType              string                     `comment:"TUN implementation to attach at startup. Supported values are\n\"native\", \"sockstun\", \"outproxy\" and \"none\". \"native\" creates an OS\nTUN device. \"sockstun\" creates a VTun netstack and exposes it through a\nlocal SOCKS server. \"outproxy\" creates a VTun netstack, listens for\nSOCKS clients inside Yggdrasil, and proxies them to the outer network.\n\"none\", \"sockstun\" and \"outproxy\" can start without root or escalated\nprivileges when no other configured option needs them."`
	IfName               string                     `comment:"Local network interface name for TUN adapter, or \"auto\" to select\nan interface automatically, or \"none\" to run without TUN. For\nTunType \"sockstun\" or \"outproxy\", this is the VTun name."`
	IfMTU                uint64                     `comment:"Maximum Transmission Unit (MTU) size for your local TUN interface.\nDefault is the largest supported size for your platform. The lowest\npossible value is 1280."`
	TunSocksListen       string                     `comment:"SOCKS TCP listen address for TunType \"sockstun\" or \"outproxy\".\nSockstun listens on the local network and proxies through VTun. Outproxy\nlistens on VTun for Yggdrasil clients and proxies to the outer network;\nloopback or unspecified listen hosts are replaced with the node's\nYggdrasil address."`
	TunSocksProxies      []TunSocksProxyConfig      `comment:"Optional SOCKS proxy routing for TunType \"sockstun\" or \"outproxy\".\nEach entry has a Filter in socksgo.BuildFilter format and a ProxyURL.\nSockstun reaches matching proxies through VTun. Outproxy reaches matching\nproxies through the outer network. Explicit rules are checked before\nTunSocksDefaultProxy.\n\nExample:\nTunSocksProxies: [\n  { Filter: \"*.onion,*.i2p\", proxy_url: \"socks5://[200::1]:9050\" },\n  { Filter: \"10.0.0.0/8,192.168.0.0/16\", proxy_url: \"socks5://[300::1]:1080\" },\n]\nRecommended community public Ygg-to-clearnet SOCKS proxies include:\n- socks5://[324:71e:281a:9ed3::fa11]:1080\n- socks5://[200:c0fc:de66:7a1:443a:ddd:df92:e7db]:1080\n\nMinimal outproxy host example with Tor/I2P routed through local proxies:\nTunType: outproxy\nTunSocksListen: 127.0.0.1:1080\nTunSocksProxies: [\n  { Filter: \"*.onion\", proxy_url: \"socks5://127.0.0.1:9050\" },\n  { Filter: \"*.i2p\", proxy_url: \"socks5://127.0.0.1:4447\" },\n]"`
	TunSocksDefaultProxy string                     `comment:"Optional fallback SOCKS proxy URL for TunType \"sockstun\" or \"outproxy\". If\nset for sockstun, unmatched destinations outside the Yggdrasil 200::/7\naddress range are sent to this proxy through VTun while Yggdrasil addresses\nstay direct. If set for outproxy, all unmatched destinations are sent to\nthis proxy through the outer network.\nExample:\nTunSocksDefaultProxy: socks5://[324:71e:281a:9ed3::fa11]:1080"`
	TunSocksDNSFallback  string                     `comment:"Optional fallback DNS server for TunType \"sockstun\". Sockstun first uses\nmnlib mesh-name resolution and, when a lookup succeeds, routes the request\nusing the resolved address. If this DNS server is set, fallback DNS queries\nthemselves travel through the same sockstun routing pipeline, including\nTunSocksProxies and TunSocksDefaultProxy. The built-in protected zones\n*.onion, *.i2p and *.loki are never resolved and are routed as hostnames.\nCommunity-hosted fallback DNS servers include:\n- [324:71e:281a:9ed3::53]:53\n- [302:db60::53]:53\n- [300:6223::53]:53\n- [302:7991::53]:53\n- [202:1d4e:724e:de52:8273:e2b5:4988:a9ba]:53"`
	TunSocksNoResolve    []string                   `comment:"Additional DNS zones that TunType \"sockstun\" must never resolve before\nrouting. Entries may be written as \"example\", \".example\" or \"*.example\"."`
	TunSocksTLSMITM      TunSocksTLSMITMConfig      `comment:"Optional selective TLS MITM for TunType \"sockstun\". When ca_file and\nkey_file point to a pre-generated local CA certificate and private key,\nsockstun intercepts matching TCP/443 CONNECT requests before DNS\nresolution or proxy routing, terminates client TLS using certificates\ngenerated from that CA, and forwards plaintext TCP to port 80 on the same\nhostname through normal sockstun routing. Clients must trust the CA.\nIf hostnames is empty, the defaults are:\n*.ygg, *.meshname, *.meship, *.onion and *.i2p.\n\nExample:\nTunSocksTLSMITM: {\n  ca_file: \"/etc/yggd/sockstun-mitm-ca.crt\",\n  key_file: \"/etc/yggd/sockstun-mitm-ca.key\",\n  hostnames: [\"*.ygg\", \"*.meshname\", \"*.meship\", \"*.onion\", \"*.i2p\"]\n}\n\nCA generation example:\nopenssl genrsa -out ca.key 2048\nopenssl req -x509 -new -nodes -key ca.key -sha256 -days 1024 -out ca.crt -subj \"/CN=MyTestCA/O=MyOrg/C=US\""`
	TunFirewall          TunFirewallConfig          `comment:"Optional IP firewall for packets between the attached TUN implementation\nand core. When Enabled is null, the daemon enables it for TunType \"native\"\nand disables it for other TUN types. ICMPv6 is always allowed. Outgoing TCP\nand UDP create temporary return-flow entries. Unsolicited incoming TCP and\nUDP are allowed only for the listed destination ports.\nExample:\nTunFirewall: { enabled: true, allowed_tcp_ports: [22, 80], allowed_udp_ports: [53] }"`
	TunMWO               int                        `comment:"Minimum write offset for VTun-backed TUN implementations. Leave at 0\nunless a custom packet path needs reserved headroom.\nIt is mostly debug config option."`
	TunMRO               int                        `comment:"Minimum read offset for VTun-backed TUN implementations. Leave at 0\nunless a custom packet path needs reserved headroom.\nIt is mostly debug config option."`
	LogLookups           bool                       `comment:"Enables the \"lookups\" admin API handler, which records lookup activity\nfor later inspection over the admin socket."`
	NodeInfoPrivacy      bool                       `comment:"By default, nodeinfo contains some defaults including the platform,\narchitecture and Yggdrasil version. These can help when surveying\nthe network and diagnosing network routing problems. Enabling\nnodeinfo privacy prevents this, so that only items specified in\n\"NodeInfo\" are sent back if specified."`
	NodeInfo             map[string]interface{}     `comment:"Optional nodeinfo. This must be a { \"key\": \"value\", ... } map\nor set as null. This is entirely optional but, if set, is visible\nto the whole network on request.\nE.g: { \"jumper\": { \"addresses\": [ \"tls://example.net:12345\" ] } }"`
}

type AutoPeerConfig struct {
	Enabled                   bool     `comment:"Enable public-peer autopeering."`
	Sources                   []string `comment:"Ordered list of public-peer sources. Entries may be HTTPS URLs\nreturning the public-peers JSON document format or the special value\n\"BUILTIN\" for the embedded peer list."`
	FetchInterval             string   `comment:"How often to refresh configured public-peer sources. Uses Go duration\nsyntax such as \"30m\" or \"1h\"."`
	CheckInterval             string   `comment:"How often runtime autopeering policy should be evaluated. Uses Go\nduration syntax such as \"1m\"."`
	MinimumConnected          int      `comment:"Minimum number of connected peers before autopeering remains idle."`
	MinimumConnectedFromFetch int      `comment:"Minimum number of connected peers whose URIs are present in the\nfiltered autopeer source set before autopeering remains idle."`
	Countries                 []string `comment:"Optional country filters for peer selection, matched case-insensitively\nagainst public-peer metadata."`
	TransportSchemes          []string `comment:"Transport scheme filters for peer selection, e.g. [\"tcp\", \"tls\"].\nAutopeering stays idle unless both this and Countries are configured."`
}

type JumperConfig struct {
	Enabled       bool     `comment:"Enable NodeInfo-based direct peering."`
	Addresses     []string `comment:"Public peering addresses to publish in NodeInfo for other jumper nodes,\nfor example [\"tls://example.net:12345\"]. These must be reachable by\nremote nodes; jumper does not perform NAT traversal."`
	CheckInterval string   `comment:"How often queued jumper targets and owned link cleanup should be checked.\nUses Go duration syntax such as \"10s\"."`
	LinkTimeout   string   `comment:"How long a jumper-added link may remain disconnected before jumper removes\nit and tries another published address. Uses Go duration syntax such as\n\"30s\"."`
}

type TransportConfig struct {
	DefaultNetwork  TransportNetworkConfig            `json:",omitempty" comment:"Default gonnect.Network used for transport hosts that do not match\nany optional host pattern in NetworkMappings. Set this to null to\nmake unmatched transport connections unavailable. Supported values are\n\"native\" or an object such as { Type: socks, ProxyURL: \"socks5://proxy:1080\" }."`
	NetworkMappings map[string]TransportNetworkConfig `json:",omitempty" comment:"Optional host-pattern to gonnect.Network overrides for transport\nconnections. Patterns use the same matching rules as transport.Manager,\nfor example \"*.example\" or \"node.example\". Default config installs\nnil mappings for *.onion, *.i2p and *.loki so those peers are blocked\nunless you explicitly assign a network. Set a value to null to keep\nthe mapping but disable its network. Remove the entry entirely to\nunset the mapping. Supported values are \"native\" or an object such as\n{ Type: socks, ProxyURL: \"socks5://proxy:1080\" }."`
}

type TransportNetworkConfig struct {
	set      bool
	null     bool
	name     string
	proxyURL string
}

type TunSocksProxyConfig struct {
	Filter   string `json:"filter,omitempty" comment:"Destination filter in socksgo.BuildFilter format, for example\n\"*.onion,*.i2p,0.0.0.0/0\" or \"example.com:443\"."`
	ProxyURL string `json:"proxy_url,omitempty" comment:"SOCKS proxy URL reachable inside Yggdrasil, for example\n\"socks5://[200::1]:1080\"."`
}

type TunSocksTLSMITMConfig struct {
	CAFile    string   `json:"ca_file,omitempty" comment:"Path to a PEM encoded CA certificate trusted by local clients."`
	KeyFile   string   `json:"key_file,omitempty" comment:"Path to the PEM encoded CA private key."`
	Hostnames []string `json:"hostnames,omitempty" comment:"Hostname patterns to intercept on TCP/443, for example [\"*.ygg\"]."`
}

func (cfg *TunSocksTLSMITMConfig) normalize() {
	cfg.CAFile = strings.TrimSpace(cfg.CAFile)
	cfg.KeyFile = strings.TrimSpace(cfg.KeyFile)
	cfg.Hostnames = normalizeStringSlice(cfg.Hostnames)
}

type TunFirewallConfig struct {
	Enabled         *bool    `json:"enabled,omitempty" comment:"Set to true or false to force the firewall state, or null/omit to use the TunType default."`
	AllowedTCPPorts []uint16 `json:"allowed_tcp_ports,omitempty" comment:"Unsolicited incoming TCP destination ports to allow."`
	AllowedUDPPorts []uint16 `json:"allowed_udp_ports,omitempty" comment:"Unsolicited incoming UDP destination ports to allow."`
}

func (cfg *TunFirewallConfig) normalize() {
	cfg.AllowedTCPPorts = normalizePortSlice(cfg.AllowedTCPPorts)
	cfg.AllowedUDPPorts = normalizePortSlice(cfg.AllowedUDPPorts)
}

type MulticastInterfaceConfig struct {
	Regex    string
	Beacon   bool
	Listen   bool
	Port     uint16 `comment:"Optional TCP/TLS listen port to advertise for multicast-discovered\npeers. Leave this as 0 to advertise the port chosen by your listener."`
	Priority uint64 `comment:"Link priority for multicast-discovered peers. Lower values are\npreferred when there are multiple links to the same node."` // really uint8, but gobind won't export it
	Password string `comment:"Optional password used to restrict multicast peering to nodes using\nthe same value."`
}

// Generates default configuration and returns a pointer to the resulting
// NodeConfig. This is used when outputting the -genconf parameter and also when
// using -autoconf.
func GenerateConfig() *NodeConfig {
	// Get the defaults for the platform.
	defaults := GetDefaults()
	// Create a node configuration and populate it.
	cfg := new(NodeConfig)
	cfg.NewPrivateKey()
	cfg.Listen = []string{}
	cfg.AdminListen = defaults.DefaultAdminListen
	cfg.AdminWebListen = ""
	cfg.AdminWebStaticDir = ""
	cfg.LocalDNSListen = ""
	cfg.Peers = []string{}
	cfg.InterfacePeers = map[string][]string{}
	cfg.AllowedPublicKeys = []string{}
	cfg.Transport = TransportConfig{
		DefaultNetwork:  NewTransportNetworkConfig("native"),
		NetworkMappings: defaultTransportNetworkMappings(),
	}
	cfg.AutoPeer = AutoPeerConfig{
		Sources:       []string{"BUILTIN"},
		FetchInterval: "1h",
		CheckInterval: "1m",
	}
	cfg.Jumper = JumperConfig{
		Addresses:     []string{},
		CheckInterval: "10s",
		LinkTimeout:   "30s",
	}
	cfg.MulticastInterfaces = defaults.DefaultMulticastInterfaces
	cfg.TunType = "native"
	cfg.IfName = defaults.DefaultIfName
	cfg.IfMTU = defaults.DefaultIfMTU
	cfg.TunSocksListen = "127.0.0.1:1080"
	cfg.TunSocksProxies = []TunSocksProxyConfig{}
	cfg.TunSocksDefaultProxy = ""
	cfg.TunSocksDNSFallback = ""
	cfg.TunSocksNoResolve = []string{}
	cfg.TunSocksTLSMITM = TunSocksTLSMITMConfig{}
	cfg.NodeInfoPrivacy = false
	if err := cfg.postprocessConfig(); err != nil {
		panic(err)
	}
	return cfg
}

func (cfg *NodeConfig) ReadFrom(r io.Reader) (int64, error) {
	conf, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}
	n := int64(len(conf))
	// If there's a byte order mark - which Windows 10 is now incredibly fond of
	// throwing everywhere when it's converting things into UTF-16 for the hell
	// of it - remove it and decode back down into UTF-8. This is necessary
	// because hjson doesn't know what to do with UTF-16 and will panic
	if bytes.Equal(conf[0:2], []byte{0xFF, 0xFE}) ||
		bytes.Equal(conf[0:2], []byte{0xFE, 0xFF}) {
		utf := unicode.UTF16(unicode.BigEndian, unicode.UseBOM)
		decoder := utf.NewDecoder()
		conf, err = decoder.Bytes(conf)
		if err != nil {
			return n, err
		}
	}
	// Generate a new configuration - this gives us a set of sane defaults -
	// then parse the configuration we loaded above on top of it. The effect
	// of this is that any configuration item that is missing from the provided
	// configuration will use a sane default.
	*cfg = *GenerateConfig()
	if err := cfg.UnmarshalHJSON(conf); err != nil {
		return n, err
	}
	return n, nil
}

func (cfg *NodeConfig) UnmarshalHJSON(b []byte) error {
	if err := hjson.Unmarshal(b, cfg); err != nil {
		return err
	}
	return cfg.postprocessConfig()
}

func (cfg *NodeConfig) postprocessConfig() error {
	cfg.Transport.normalize()
	if err := cfg.AutoPeer.normalize(); err != nil {
		return err
	}
	if err := cfg.Jumper.normalize(); err != nil {
		return err
	}
	cfg.TunType = NormalizeTunType(cfg.TunType)
	cfg.IfName = strings.TrimSpace(cfg.IfName)
	if cfg.IfName == "" {
		cfg.IfName = "auto"
	}
	cfg.TunSocksListen = strings.TrimSpace(cfg.TunSocksListen)
	if cfg.TunSocksListen == "" {
		cfg.TunSocksListen = "127.0.0.1:1080"
	}
	cfg.TunSocksProxies = normalizeTunSocksProxies(cfg.TunSocksProxies)
	cfg.TunSocksDefaultProxy = strings.TrimSpace(cfg.TunSocksDefaultProxy)
	cfg.TunSocksDNSFallback = strings.TrimSpace(cfg.TunSocksDNSFallback)
	cfg.TunSocksNoResolve = normalizeStringSlice(cfg.TunSocksNoResolve)
	cfg.TunSocksTLSMITM.normalize()
	cfg.TunFirewall.normalize()
	cfg.LocalDNSListen = strings.TrimSpace(cfg.LocalDNSListen)
	cfg.AdminWebListen = strings.TrimSpace(cfg.AdminWebListen)
	cfg.AdminWebStaticDir = strings.TrimSpace(cfg.AdminWebStaticDir)
	if cfg.PrivateKeyPath != "" {
		cfg.PrivateKey = nil
		f, err := os.ReadFile(cfg.PrivateKeyPath)
		if err != nil {
			return err
		}
		if err := cfg.UnmarshalPEMPrivateKey(f); err != nil {
			return err
		}
	}
	switch {
	case cfg.Certificate == nil:
		// No self-signed certificate has been generated yet.
		fallthrough
	case !bytes.Equal(cfg.Certificate.PrivateKey.(ed25519.PrivateKey), cfg.PrivateKey):
		// A self-signed certificate was generated but the private
		// key has changed since then, possibly because a new config
		// was parsed.
		if err := cfg.GenerateSelfSignedCertificate(); err != nil {
			return err
		}
	}
	return nil
}

func NewTransportNetworkConfig(name string) TransportNetworkConfig {
	return TransportNetworkConfig{
		set:  true,
		name: strings.ToLower(strings.TrimSpace(name)),
	}
}

func NullTransportNetworkConfig() TransportNetworkConfig {
	return TransportNetworkConfig{
		set:  true,
		null: true,
	}
}

func NewSocksTransportNetworkConfig(proxyURL string) TransportNetworkConfig {
	return TransportNetworkConfig{
		set:      true,
		name:     "socks",
		proxyURL: strings.TrimSpace(proxyURL),
	}
}

func (cfg TransportNetworkConfig) IsSet() bool {
	return cfg.set
}

func (cfg TransportNetworkConfig) IsNull() bool {
	return cfg.set && cfg.null
}

func (cfg TransportNetworkConfig) Name() string {
	return cfg.name
}

func (cfg TransportNetworkConfig) ProxyURL() string {
	return cfg.proxyURL
}

func (cfg *TransportNetworkConfig) UnmarshalJSON(b []byte) error {
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		*cfg = NullTransportNetworkConfig()
		return nil
	}

	var name string
	if err := json.Unmarshal(b, &name); err == nil {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			return fmt.Errorf("transport network must not be empty")
		}

		*cfg = NewTransportNetworkConfig(name)
		return nil
	}

	var raw struct {
		Type        string `json:"type"`
		Name        string `json:"name"`
		ProxyURL    string `json:"proxy_url"`
		ProxyURLAlt string `json:"ProxyURL"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}

	name = raw.Type
	if strings.TrimSpace(name) == "" {
		name = raw.Name
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return fmt.Errorf("transport network must not be empty")
	}

	*cfg = TransportNetworkConfig{
		set:      true,
		name:     name,
		proxyURL: strings.TrimSpace(firstNonEmpty(raw.ProxyURL, raw.ProxyURLAlt)),
	}
	return nil
}

func (cfg TransportNetworkConfig) MarshalJSON() ([]byte, error) {
	if cfg.null {
		return []byte("null"), nil
	}
	if cfg.proxyURL != "" {
		return json.Marshal(struct {
			Type     string `json:"type"`
			ProxyURL string `json:"proxy_url,omitempty"`
		}{
			Type:     cfg.name,
			ProxyURL: cfg.proxyURL,
		})
	}
	return json.Marshal(cfg.name)
}

func (cfg *TransportConfig) normalize() {
	if cfg.NetworkMappings == nil {
		cfg.NetworkMappings = map[string]TransportNetworkConfig{}
	}
	cfg.DefaultNetwork.name = strings.ToLower(strings.TrimSpace(cfg.DefaultNetwork.name))
	cfg.DefaultNetwork.proxyURL = strings.TrimSpace(cfg.DefaultNetwork.proxyURL)
	for pattern, network := range cfg.NetworkMappings {
		if network.null {
			cfg.NetworkMappings[pattern] = NullTransportNetworkConfig()
			continue
		}
		network.name = strings.ToLower(strings.TrimSpace(network.name))
		network.proxyURL = strings.TrimSpace(network.proxyURL)
		cfg.NetworkMappings[pattern] = network
	}
}

func (cfg *AutoPeerConfig) normalize() error {
	cfg.Sources = normalizeStringSlice(cfg.Sources)
	cfg.Countries = normalizeStringSlice(cfg.Countries)
	cfg.TransportSchemes = normalizeStringSlice(cfg.TransportSchemes)

	if strings.TrimSpace(cfg.FetchInterval) == "" {
		cfg.FetchInterval = "1h"
	}
	if _, err := time.ParseDuration(cfg.FetchInterval); err != nil {
		return fmt.Errorf("invalid AutoPeer.FetchInterval: %w", err)
	}

	if strings.TrimSpace(cfg.CheckInterval) == "" {
		cfg.CheckInterval = "1m"
	}
	if _, err := time.ParseDuration(cfg.CheckInterval); err != nil {
		return fmt.Errorf("invalid AutoPeer.CheckInterval: %w", err)
	}

	return nil
}

func (cfg *JumperConfig) normalize() error {
	cfg.Addresses = normalizeStringSlice(cfg.Addresses)

	if strings.TrimSpace(cfg.CheckInterval) == "" {
		cfg.CheckInterval = "10s"
	}
	if _, err := time.ParseDuration(cfg.CheckInterval); err != nil {
		return fmt.Errorf("invalid Jumper.CheckInterval: %w", err)
	}

	if strings.TrimSpace(cfg.LinkTimeout) == "" {
		cfg.LinkTimeout = "30s"
	}
	if _, err := time.ParseDuration(cfg.LinkTimeout); err != nil {
		return fmt.Errorf("invalid Jumper.LinkTimeout: %w", err)
	}

	return nil
}

func normalizeStringSlice(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func normalizeTunSocksProxies(values []TunSocksProxyConfig) []TunSocksProxyConfig {
	if len(values) == 0 {
		return []TunSocksProxyConfig{}
	}
	out := make([]TunSocksProxyConfig, 0, len(values))
	for _, value := range values {
		value.Filter = strings.TrimSpace(value.Filter)
		value.ProxyURL = strings.TrimSpace(value.ProxyURL)
		if value.Filter == "" && value.ProxyURL == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func normalizePortSlice(values []uint16) []uint16 {
	if len(values) == 0 {
		return []uint16{}
	}
	seen := make(map[uint16]struct{}, len(values))
	out := make([]uint16, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func defaultTransportNetworkMappings() map[string]TransportNetworkConfig {
	return map[string]TransportNetworkConfig{
		"*.i2p":   NullTransportNetworkConfig(),
		"*.loki":  NullTransportNetworkConfig(),
		"*.onion": NullTransportNetworkConfig(),
	}
}

// RFC5280 section 4.1.2.5
var notAfterNeverExpires = time.Date(9999, time.December, 31, 23, 59, 59, 0, time.UTC)

func (cfg *NodeConfig) GenerateSelfSignedCertificate() error {
	key, err := cfg.MarshalPEMPrivateKey()
	if err != nil {
		return err
	}
	cert, err := cfg.MarshalPEMCertificate()
	if err != nil {
		return err
	}
	tlsCert, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return err
	}
	cfg.Certificate = &tlsCert
	return nil
}

func (cfg *NodeConfig) MarshalPEMCertificate() ([]byte, error) {
	privateKey := ed25519.PrivateKey(cfg.PrivateKey)
	publicKey := privateKey.Public().(ed25519.PublicKey)

	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: hex.EncodeToString(publicKey),
		},
		NotBefore:             time.Now(),
		NotAfter:              notAfterNeverExpires,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	certbytes, err := x509.CreateCertificate(rand.Reader, cert, cert, publicKey, privateKey)
	if err != nil {
		return nil, err
	}

	block := &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certbytes,
	}
	return pem.EncodeToMemory(block), nil
}

func (cfg *NodeConfig) NewPrivateKey() {
	_, spriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		panic(err)
	}
	cfg.PrivateKey = KeyBytes(spriv)
}

func (cfg *NodeConfig) MarshalPEMPrivateKey() ([]byte, error) {
	b, err := x509.MarshalPKCS8PrivateKey(ed25519.PrivateKey(cfg.PrivateKey))
	if err != nil {
		return nil, fmt.Errorf("failed to marshal PKCS8 key: %w", err)
	}
	block := &pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: b,
	}
	return pem.EncodeToMemory(block), nil
}

func (cfg *NodeConfig) UnmarshalPEMPrivateKey(b []byte) error {
	p, _ := pem.Decode(b)
	if p == nil {
		return fmt.Errorf("failed to parse PEM file")
	}
	if p.Type != "PRIVATE KEY" {
		return fmt.Errorf("unexpected PEM type %q", p.Type)
	}
	k, err := x509.ParsePKCS8PrivateKey(p.Bytes)
	if err != nil {
		return fmt.Errorf("failed to unmarshal PKCS8 key: %w", err)
	}
	key, ok := k.(ed25519.PrivateKey)
	if !ok {
		return fmt.Errorf("private key must be ed25519 key")
	}
	if len(key) != ed25519.PrivateKeySize {
		return fmt.Errorf("unexpected ed25519 private key length")
	}
	cfg.PrivateKey = KeyBytes(key)
	return nil
}

type KeyBytes []byte

func (k KeyBytes) MarshalJSON() ([]byte, error) {
	return json.Marshal(hex.EncodeToString(k))
}

func (k *KeyBytes) UnmarshalJSON(b []byte) error {
	var s string
	var err error
	if err = json.Unmarshal(b, &s); err != nil {
		return err
	}
	*k, err = hex.DecodeString(s)
	return err
}
