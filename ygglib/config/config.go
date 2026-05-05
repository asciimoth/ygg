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
	PrivateKeyPath       string                     `json:",omitempty" comment:"The path to your private key file in PEM format."`
	Certificate          *tls.Certificate           `json:"-"`
	Peers                []string                   `comment:"List of outbound peer connection strings (e.g. tls://a.b.c.d:e or\nsocks://a.b.c.d:e/f.g.h.i:j). Connection strings can contain options,\nsee https://yggdrasil-network.github.io/configurationref.html#peers.\nYggdrasil has no concept of bootstrap nodes - all network traffic\nwill transit peer connections. Therefore make sure to only peer with\nnearby nodes that have good connectivity and low latency. Avoid adding\npeers to this list from distant countries as this will worsen your\nnode's connectivity and performance considerably."`
	InterfacePeers       map[string][]string        `comment:"List of connection strings for outbound peer connections in URI format,\narranged by source interface, e.g. { \"eth0\": [ \"tls://a.b.c.d:e\" ] }.\nYou should only use this option if your machine is multi-homed and you\nwant to establish outbound peer connections on different interfaces.\nOtherwise you should use \"Peers\"."`
	Listen               []string                   `comment:"Listen addresses for incoming connections. You will need to add\nlisteners in order to accept incoming peerings from non-local nodes.\nThis is not required if you wish to establish outbound peerings only.\nMulticast peer discovery will work regardless of any listeners set\nhere. Each listener should be specified in URI format as above, e.g.\ntls://0.0.0.0:0 or tls://[::]:0 to listen on all interfaces."`
	AdminListen          string                     `json:",omitempty" comment:"Listen address for admin connections. Default is to listen for local\nconnections either on TCP/9001 or a UNIX socket depending on your\nplatform. Use this value for yggdrasilctl -endpoint=X. To disable\nthe admin socket, use the value \"none\" instead."`
	LocalDNSListen       string                     `json:",omitempty" comment:"Optional local DNS listen address for mesh-name resolution. When set,\nthe daemon starts a simple local DNS server answering IN A and IN AAAA\nqueries through mnlib.Resolver, for example \"127.0.0.1:5353\"."`
	MulticastInterfaces  []MulticastInterfaceConfig `comment:"Configuration for which interfaces multicast peer discovery should be\nenabled on. Regex is a regular expression which is matched against an\ninterface name, and interfaces use the first configuration that they\nmatch against. Beacon controls whether or not your node advertises its\npresence to others, whereas Listen controls whether or not your node\nlistens out for and tries to connect to other advertising nodes. See\nhttps://yggdrasil-network.github.io/configurationref.html#multicastinterfaces\nfor more supported options."`
	AllowedPublicKeys    []string                   `comment:"List of peer public keys to allow incoming peering connections\nfrom. If left empty/undefined then all connections will be allowed\nby default. This does not affect outgoing peerings, nor does it\naffect link-local peers discovered via multicast.\nWARNING: THIS IS NOT A FIREWALL and DOES NOT limit who can reach\nopen ports or services running on your machine!"`
	Transport            TransportConfig            `comment:"Configuration for the transport manager networks used by core.\nIf this block is omitted entirely, Yggdrasil uses the built-in\nnative network as the default network and installs nil host-based\nmappings for *.onion, *.i2p and *.loki so those peers stay disabled\nunless you enable them explicitly. Set DefaultNetwork to null to\ndisable the default network entirely. Set a NetworkMappings entry\nto null to keep the mapping but disable its network. Supported\nnon-null values today are \"native\" or a socks network object with a\nProxyURL such as \"socks5://proxy:1080\"."`
	AutoPeer             AutoPeerConfig             `comment:"Configuration for public-peer autopeering. When enabled, Yggdrasil\nwill periodically fetch peer candidates from configured sources and\nadd one matching peer when your runtime connectivity thresholds are\nnot met. Sources may be URLs returning public-peers JSON documents\nor the special value \"BUILTIN\" for the embedded list."`
	TunType              string                     `comment:"TUN implementation to attach at startup. Supported values are\n\"native\", \"sockstun\" and \"none\". \"native\" creates an OS TUN device.\n\"sockstun\" creates a VTun netstack and exposes it through a local SOCKS\nserver with the default socksgo command set."`
	IfName               string                     `comment:"Local network interface name for TUN adapter, or \"auto\" to select\nan interface automatically, or \"none\" to run without TUN."`
	IfMTU                uint64                     `comment:"Maximum Transmission Unit (MTU) size for your local TUN interface.\nDefault is the largest supported size for your platform. The lowest\npossible value is 1280."`
	TunSocksListen       string                     `json:",omitempty" comment:"Local TCP listen address for TunType \"sockstun\". The SOCKS server\nproxies the default socksgo command set through the attached VTun."`
	TunSocksProxies      []TunSocksProxyConfig      `json:",omitempty" comment:"Optional second-hop SOCKS proxies used by TunType \"sockstun\". Each\nentry has a Filter in socksgo.BuildFilter format and a ProxyURL such as\n\"socks5://[200::1]:1080\". Matching traffic is sent to that proxy through\nVTun."`
	TunSocksDefaultProxy string                     `json:",omitempty" comment:"Optional fallback SOCKS proxy URL for TunType \"sockstun\". If set,\nunmatched destinations outside the Yggdrasil 200::/7 address range are\nsent to this proxy through VTun. Unmatched Yggdrasil node and subnet\naddresses stay direct through VTun."`
	TunSocksDNSFallback  string                     `json:",omitempty" comment:"Optional fallback DNS server for TunType \"sockstun\", for example\n\"[300:6223::53]:53\". Sockstun first tries mnlib mesh-name resolution\nfor SOCKS CONNECT/BIND and packet operations. If this fallback is set,\nother names are resolved by DNS requests sent through the same sockstun\nrouting pipeline, including second-hop and default proxy routing."`
	TunSocksNoResolve    []string                   `json:",omitempty" comment:"Additional DNS zones that TunType \"sockstun\" must never resolve before\nrouting. The built-in protected zones are *.onion, *.i2p and *.loki.\nEntries may be written as \"example\", \".example\" or \"*.example\"."`
	TunMWO               int                        `json:",omitempty" comment:"Minimum write offset for VTun-backed TUN implementations. Leave at 0\nunless a custom packet path needs reserved headroom."`
	TunMRO               int                        `json:",omitempty" comment:"Minimum read offset for VTun-backed TUN implementations. Leave at 0\nunless a custom packet path needs reserved headroom."`
	LogLookups           bool                       `json:",omitempty"`
	NodeInfoPrivacy      bool                       `comment:"By default, nodeinfo contains some defaults including the platform,\narchitecture and Yggdrasil version. These can help when surveying\nthe network and diagnosing network routing problems. Enabling\nnodeinfo privacy prevents this, so that only items specified in\n\"NodeInfo\" are sent back if specified."`
	NodeInfo             map[string]interface{}     `comment:"Optional nodeinfo. This must be a { \"key\": \"value\", ... } map\nor set as null. This is entirely optional but, if set, is visible\nto the whole network on request."`
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

type MulticastInterfaceConfig struct {
	Regex    string
	Beacon   bool
	Listen   bool
	Port     uint16 `json:",omitempty"`
	Priority uint64 `json:",omitempty"` // really uint8, but gobind won't export it
	Password string
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
	cfg.MulticastInterfaces = defaults.DefaultMulticastInterfaces
	cfg.TunType = "native"
	cfg.IfName = defaults.DefaultIfName
	cfg.IfMTU = defaults.DefaultIfMTU
	cfg.TunSocksListen = "127.0.0.1:1080"
	cfg.TunSocksProxies = []TunSocksProxyConfig{}
	cfg.TunSocksDefaultProxy = ""
	cfg.TunSocksDNSFallback = ""
	cfg.TunSocksNoResolve = []string{}
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
	cfg.LocalDNSListen = strings.TrimSpace(cfg.LocalDNSListen)
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
