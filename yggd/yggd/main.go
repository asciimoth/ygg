package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	stdlog "log"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"suah.dev/protect"

	gsyslog "github.com/hashicorp/go-syslog"
	"github.com/hjson/hjson-go/v4"
	"github.com/kardianos/minwinsvc"

	"github.com/asciimoth/gonnect"
	gtun "github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/ygg/yggd/linktransport"
	"github.com/asciimoth/ygg/yggd/tunnative"
	"github.com/asciimoth/ygg/ygglib/address"
	"github.com/asciimoth/ygg/ygglib/admin"
	"github.com/asciimoth/ygg/ygglib/autopeer"
	"github.com/asciimoth/ygg/ygglib/config"
	"github.com/asciimoth/ygg/ygglib/core"
	"github.com/asciimoth/ygg/ygglib/ipv6rwc"
	"github.com/asciimoth/ygg/ygglib/jumper"
	"github.com/asciimoth/ygg/ygglib/logger"
	"github.com/asciimoth/ygg/ygglib/multicast"
	"github.com/asciimoth/ygg/ygglib/outproxy"
	"github.com/asciimoth/ygg/ygglib/sockstun"
	"github.com/asciimoth/ygg/ygglib/transport"
	"github.com/asciimoth/ygg/ygglib/transportcfg"
	yggtun "github.com/asciimoth/ygg/ygglib/tun"
	"github.com/asciimoth/ygg/ygglib/version"
)

type node struct {
	core      *core.Core
	tun       *yggtun.TunAdapter
	multicast *multicast.Multicast
	autopeer  *autopeer.Manager
	jumper    *jumper.Manager
	admin     *admin.AdminSocket
	localDNS  *localDNSServer
}

type daemonTunController struct {
	node        *node
	cfg         *config.NodeConfig
	log         core.Logger
	mu          sync.RWMutex
	activeSocks socksTun
}

type socksTun interface {
	Network() gonnect.Network
	SetProxies([]sockstun.ProxyConfig, string) error
	Proxies() []sockstun.ProxyConfig
	DefaultProxyURL() string
}

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

// The main function is responsible for configuring and starting Yggdrasil.
func main() {
	gonnect.UnfuckGoDns()

	genconf := flag.Bool("genconf", false, "print a new config to stdout")
	useconf := flag.Bool("useconf", false, "read HJSON/JSON config from stdin")
	useconffile := flag.String("useconffile", "", "read HJSON/JSON config from specified file path")
	normaliseconf := flag.Bool("normaliseconf", false, "use in combination with either -useconf or -useconffile, outputs your configuration normalised")
	exportkey := flag.Bool("exportkey", false, "use in combination with either -useconf or -useconffile, outputs your private key in PEM format")
	confjson := flag.Bool("json", false, "print configuration from -genconf or -normaliseconf as JSON instead of HJSON")
	autoconf := flag.Bool("autoconf", false, "automatic mode (dynamic IP, peer with IPv6 neighbors)")
	ver := flag.Bool("version", false, "prints the version of this build")
	logto := flag.String("logto", "stdout", "file path to log to, \"syslog\" or \"stdout\"")
	getaddr := flag.Bool("address", false, "use in combination with either -useconf or -useconffile, outputs your IPv6 address")
	getsnet := flag.Bool("subnet", false, "use in combination with either -useconf or -useconffile, outputs your IPv6 subnet")
	getpkey := flag.Bool("publickey", false, "use in combination with either -useconf or -useconffile, outputs your public key")
	loglevel := flag.String("loglevel", "info", "loglevel to enable")
	chuserto := flag.String("user", "", "user (and, optionally, group) to set UID/GID to")
	notifyFd := flag.Int("notifyfd", -1, "write a newline to this file-descriptor to indicate readiness to a service manager")
	flag.Parse()

	done := make(chan struct{})
	defer close(done)

	// Catch interrupts from the operating system to exit gracefully.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	// Create a new logger that logs output to stdout.
	var logWriter io.Writer = os.Stdout
	logFlags := stdlog.Flags()
	logFallback := false
	switch *logto {
	case "stdout":

	case "syslog":
		if syslogger, err := gsyslog.NewLogger(gsyslog.LOG_NOTICE, "DAEMON", version.BuildName()); err == nil {
			logWriter = syslogger
			logFlags = stdlog.Flags() & ^(stdlog.Ldate | stdlog.Ltime)
		} else {
			logFallback = true
		}

	default:
		if logfd, err := os.OpenFile(*logto, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
			logWriter = logfd
		} else {
			logFallback = true
		}
	}
	logger := logger.New(logWriter, "", logFlags)
	if logFallback {
		logger.Warn("Logging defaulting to stdout")
	}
	if *normaliseconf {
		setLogLevel("error", logger)
	} else {
		setLogLevel(*loglevel, logger)
	}

	cfg := config.GenerateConfig()
	var err error
	switch {
	case *ver:
		fmt.Println("Build name:", version.BuildName())
		fmt.Println("Build version:", version.BuildVersion())
		return

	case *autoconf:
		// Use an autoconf-generated config, this will give us random keys and
		// port numbers, and will use an automatically selected TUN interface.

	case *useconf:
		if _, err := cfg.ReadFrom(os.Stdin); err != nil {
			panic(err)
		}

	case *useconffile != "":
		f, err := os.Open(*useconffile)
		if err != nil {
			panic(err)
		}
		if _, err := cfg.ReadFrom(f); err != nil {
			panic(err)
		}
		_ = f.Close()

	case *genconf:
		var bs []byte
		if *confjson {
			bs, err = json.MarshalIndent(cfg, "", "  ")
		} else {
			bs, err = hjson.Marshal(cfg)
		}
		if err != nil {
			panic(err)
		}
		fmt.Println(string(bs))
		return

	default:
		if !*getaddr && !*getsnet && !*getpkey {
			if err := readOrGenerateDefaultConfig(cfg); err != nil {
				panic(err)
			}
			break
		}

		fmt.Println("Usage:")
		flag.PrintDefaults()

		if *getaddr || *getsnet {
			fmt.Println("\nError: You need to specify some config data using -useconf or -useconffile.")
		}
		return
	}

	privateKey := ed25519.PrivateKey(cfg.PrivateKey)
	publicKey := privateKey.Public().(ed25519.PublicKey)

	switch {
	case *getaddr:
		addr := address.AddrForKey(publicKey)
		ip := net.IP(addr[:])
		fmt.Println(ip.String())
		return

	case *getsnet:
		snet := address.SubnetForKey(publicKey)
		ipnet := net.IPNet{
			IP:   append(snet[:], 0, 0, 0, 0, 0, 0, 0, 0),
			Mask: net.CIDRMask(len(snet)*8, 128),
		}
		fmt.Println(ipnet.String())
		return

	case *getpkey:
		fmt.Println(hex.EncodeToString(publicKey))
		return

	case *normaliseconf:
		cfg.AdminListen = ""
		if cfg.PrivateKeyPath != "" {
			cfg.PrivateKey = nil
		}
		var bs []byte
		if *confjson {
			bs, err = json.MarshalIndent(cfg, "", "  ")
		} else {
			bs, err = hjson.Marshal(cfg)
		}
		if err != nil {
			panic(err)
		}
		fmt.Println(string(bs))
		return

	case *exportkey:
		pem, err := cfg.MarshalPEMPrivateKey()
		if err != nil {
			panic(err)
		}
		fmt.Println(string(pem))
		return
	}

	n := &node{}
	var tunController *daemonTunController
	var defaultNetwork transport.Network

	// Set up the Yggdrasil node itself.
	{
		manager, defaultNet, err := newTransportManager(cfg)
		if err != nil {
			panic(err)
		}
		defaultNetwork = defaultNet

		iprange := net.IPNet{
			IP:   net.ParseIP("200::"),
			Mask: net.CIDRMask(7, 128),
		}
		nodeInfo := cfg.NodeInfo
		if cfg.Jumper.Enabled {
			nodeInfo = mergeNodeInfo(nodeInfo, jumper.AdvertisedNodeInfo(cfg.Jumper.Addresses))
		}

		options := []core.SetupOption{
			core.TransportManager{Manager: manager},
			core.NodeInfo(nodeInfo),
			core.NodeInfoPrivacy(cfg.NodeInfoPrivacy),
			core.PeerFilter(func(ip net.IP) bool {
				return !iprange.Contains(ip)
			}),
		}
		for _, addr := range cfg.Listen {
			options = append(options, core.ListenAddress(addr))
		}
		for _, peer := range cfg.Peers {
			options = append(options, core.Peer{URI: peer})
		}
		for intf, peers := range cfg.InterfacePeers {
			for _, peer := range peers {
				options = append(options, core.Peer{URI: peer, SourceInterface: intf})
			}
		}
		for _, allowed := range cfg.AllowedPublicKeys {
			k, err := hex.DecodeString(allowed)
			if err != nil {
				panic(err)
			}
			options = append(options, core.AllowedPublicKey(k[:]))
		}
		if n.core, err = core.New(cfg.Certificate, logger, options...); err != nil {
			panic(err)
		}
		address, subnet := n.core.Address(), n.core.Subnet()
		logger.Infof("Your public key is %s", hex.EncodeToString(n.core.PublicKey()))
		logger.Infof("Your IPv6 address is %s", address.String())
		logger.Infof("Your IPv6 subnet is %s", subnet.String())

		fetchInterval, err := time.ParseDuration(cfg.AutoPeer.FetchInterval)
		if err != nil {
			panic(err)
		}
		checkInterval, err := time.ParseDuration(cfg.AutoPeer.CheckInterval)
		if err != nil {
			panic(err)
		}
		fetcher := autopeer.NewFetcher(logger, fetchInterval)
		fetcher.SetDefaultNetwork(defaultNetwork)
		fetcher.SetSources(cfg.AutoPeer.Sources)
		n.autopeer = autopeer.NewManager(fetcher)
		n.autopeer.SetPeerManager(n.core)
		n.autopeer.SetConfig(autopeer.ManagerConfig{
			CheckInterval:             checkInterval,
			MinimumConnected:          cfg.AutoPeer.MinimumConnected,
			MinimumConnectedFromFetch: cfg.AutoPeer.MinimumConnectedFromFetch,
			Countries:                 cfg.AutoPeer.Countries,
			TransportSchemes:          cfg.AutoPeer.TransportSchemes,
		})
		if cfg.AutoPeer.Enabled {
			n.autopeer.Start()
		}

		jumperCheckInterval, err := time.ParseDuration(cfg.Jumper.CheckInterval)
		if err != nil {
			panic(err)
		}
		jumperLinkTimeout, err := time.ParseDuration(cfg.Jumper.LinkTimeout)
		if err != nil {
			panic(err)
		}
		n.jumper = jumper.NewManager(n.core, logger, jumper.Config{
			CheckInterval: jumperCheckInterval,
			LinkTimeout:   jumperLinkTimeout,
		})
	}

	// Set up the admin socket.
	{
		options := []admin.SetupOption{
			admin.ListenAddress(cfg.AdminListen),
			admin.WebListenAddress(cfg.AdminWebListen),
			admin.WebStaticDir(cfg.AdminWebStaticDir),
		}
		if cfg.LogLookups {
			options = append(options, admin.LogLookups{})
		}
		if n.admin, err = admin.New(n.core, logger, options...); err != nil {
			if !cfg.UseUnprivilegedAdminFallback() || !isPermissionError(err) {
				panic(err)
			}
			cfg.AdminListen = config.UnprivilegedAdminListen
			options[0] = admin.ListenAddress(cfg.AdminListen)
			if n.admin, err = admin.New(n.core, logger, options...); err != nil {
				panic(err)
			}
		}
		if n.admin != nil {
			n.admin.SetupConfigHandlers(admin.NewConfigController(cfg))
			n.admin.SetupCoreHandlers()
			n.admin.SetupAutoPeerHandlers(admin.NewAutoPeerController(n.autopeer, cfg.AutoPeer.Enabled))
			n.admin.SetupJumperHandlers(admin.NewJumperController(
				n.jumper,
				n.core,
				cfg.NodeInfo,
				cfg.NodeInfoPrivacy,
				cfg.Jumper.Enabled,
				cfg.Jumper.Addresses,
			))
		}
	}

	// Set up the multicast module.
	{
		options := []multicast.SetupOption{}
		for _, intf := range cfg.MulticastInterfaces {
			options = append(options, multicast.MulticastInterface{
				Regex:    regexp.MustCompile(intf.Regex),
				Beacon:   intf.Beacon,
				Listen:   intf.Listen,
				Port:     intf.Port,
				Priority: uint8(intf.Priority),
				Password: intf.Password,
			})
		}
		options = append(options, multicast.ProtocolVersion{
			Major: core.ProtocolVersionMajor,
			Minor: core.ProtocolVersionMinor,
		})
		if defaultNetwork != nil {
			options = append(options, multicast.Network{Network: defaultNetwork})
		}
		if n.multicast, err = multicast.New(multicastCoreAdapter{core: n.core}, logger, options...); err != nil {
			panic(err)
		}
		if n.admin != nil && n.multicast != nil {
			n.admin.SetupMulticastHandlers(n.multicast)
		}
	}

	// Set up the TUN module.
	{
		options := []yggtun.SetupOption{
			yggtun.InterfaceMTU(cfg.IfMTU),
		}
		if n.tun, err = yggtun.New(ipv6rwc.NewReadWriteCloser(n.core), logger, options...); err != nil {
			panic(err)
		}
		controller := &daemonTunController{node: n, cfg: cfg, log: logger}
		tunController = controller
		if shouldAttachTun(cfg) {
			if err := controller.Attach(admin.AttachTunRequest{
				Type:              cfg.TunType,
				Name:              cfg.IfName,
				MTU:               fmt.Sprintf("%d", cfg.IfMTU),
				SocksListen:       cfg.TunSocksListen,
				SocksProxies:      mustMarshalTunSocksProxies(cfg.TunSocksProxies),
				SocksDefaultProxy: cfg.TunSocksDefaultProxy,
				SocksDNSFallback:  cfg.TunSocksDNSFallback,
				SocksNoResolve:    mustMarshalStrings(cfg.TunSocksNoResolve),
				SocksTLSMITMCA:    cfg.TunSocksTLSMITM.CAFile,
				SocksTLSMITMKey:   cfg.TunSocksTLSMITM.KeyFile,
				SocksTLSMITMHosts: mustMarshalStrings(cfg.TunSocksTLSMITM.Hostnames),
				MWO:               fmt.Sprintf("%d", cfg.TunMWO),
				MRO:               fmt.Sprintf("%d", cfg.TunMRO),
				FirewallTCPPorts:  mustMarshalUint16s(cfg.TunFirewall.AllowedTCPPorts),
				FirewallUDPPorts:  mustMarshalUint16s(cfg.TunFirewall.AllowedUDPPorts),
			}, false); err != nil {
				panic(err)
			}
		}
		if n.admin != nil && n.tun != nil {
			n.admin.SetupTunHandlers(n.tun, controller)
		}
	}

	// Set up the optional jumper module after TUN so both modules can subscribe
	// to path notifications.
	if n.jumper != nil {
		n.core.AddPathNotify(n.jumper.NotifyTraffic)
		if cfg.Jumper.Enabled {
			n.jumper.Start()
		}
	}

	// Set up optional local DNS server.
	if cfg.LocalDNSListen != "" {
		if n.localDNS, err = newLocalDNSServer(cfg.LocalDNSListen, localDNSNetwork(tunController), logger); err != nil {
			panic(err)
		}
		if err := n.localDNS.Start(); err != nil {
			panic(err)
		}
	}

	//Windows service shutdown
	minwinsvc.SetOnExit(func() {
		logger.Infof("Shutting down service ...")
		cancel()
		// Wait for all parts to shutdown properly
		<-done
	})

	// Change user if requested
	if *chuserto != "" {
		err = chuser(*chuserto)
		if err != nil {
			panic(err)
		}
	}

	// Promise final modes of operation.  At this point, if at all:
	// - raw socket is created/open
	// - admin socket is created/open
	// - privileges are dropped to non-root user
	//
	// Peers, InterfacePeers, Listen can be UNIX sockets;
	// Go's net.Listen.Close() deletes files on shutdown.
	promises := []string{"stdio", "rpath", "cpath", "inet", "unix", "dns"}
	if len(cfg.MulticastInterfaces) > 0 {
		promises = append(promises, "mcast")
	}
	if err := protect.Pledge(strings.Join(promises, " ")); err != nil {
		panic(fmt.Sprintf("pledge: %v: %v", promises, err))
	}

	if notifyFd != nil && *notifyFd > 0 {
		f := os.NewFile(uintptr(*notifyFd), "notifyfd")
		_, _ = f.Write([]byte{0x0a})
		f.Close()
	}

	// Block until we are told to shut down.
	<-ctx.Done()

	// Shut down the node.
	_ = n.localDNS.Stop()
	_ = n.admin.Stop()
	_ = n.jumper.Close()
	_ = n.autopeer.Close()
	_ = n.multicast.Stop()
	_ = n.tun.Stop()
	n.core.Stop()
}

func isPermissionError(err error) bool {
	return os.IsPermission(err) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM)
}

func readOrGenerateDefaultConfig(cfg *config.NodeConfig) error {
	configPath := config.GetDefaults().DefaultConfigFile
	if strings.TrimSpace(configPath) == "" {
		return nil
	}

	f, err := os.Open(configPath)
	if err == nil {
		defer f.Close()
		_, err = cfg.ReadFrom(f)
		return err
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	tryWriteGeneratedConfig(configPath, cfg)
	return nil
}

func tryWriteGeneratedConfig(configPath string, cfg *config.NodeConfig) {
	bs, err := hjson.Marshal(cfg)
	if err != nil {
		return
	}
	if dir := filepath.Dir(configPath); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return
		}
	}
	f, err := os.OpenFile(configPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(bs, '\n'))
}

func mergeNodeInfo(base map[string]interface{}, overlay map[string]interface{}) map[string]interface{} {
	if len(overlay) == 0 {
		return base
	}
	merged := make(map[string]interface{}, len(base)+len(overlay))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range overlay {
		merged[key] = value
	}
	return merged
}

func newTransportManager(cfg *config.NodeConfig) (*transport.Manager, transport.Network, error) {
	defaultNetwork, err := transportNetworkFromConfig(cfg.Transport.DefaultNetwork)
	if err != nil {
		return nil, nil, fmt.Errorf("default transport network: %w", err)
	}

	manager := transport.NewManager(defaultNetwork)
	if err := manager.RegisterTransport(transport.NewTCPTransport()); err != nil {
		return nil, nil, err
	}
	tlsConfig, err := core.GenerateTLSConfig(cfg.Certificate)
	if err != nil {
		return nil, nil, err
	}
	if err := manager.RegisterTransport(transport.NewTLSTransport(tlsConfig.Clone())); err != nil {
		return nil, nil, err
	}
	for _, t := range []transport.Transport{
		linktransport.NewUNIXTransport(),
		linktransport.NewWebSocketTransport(),
		linktransport.NewSecureWebSocketTransport(tlsConfig.Clone()),
		linktransport.NewQUICTransport(tlsConfig.Clone()),
	} {
		if err := manager.RegisterTransport(t); err != nil {
			return nil, nil, err
		}
	}

	for pattern, networkCfg := range cfg.Transport.NetworkMappings {
		network, err := transportNetworkFromConfig(networkCfg)
		if err != nil {
			return nil, nil, fmt.Errorf("transport network mapping %q: %w", pattern, err)
		}
		if err := manager.MapNetwork(pattern, network); err != nil {
			return nil, nil, err
		}
	}

	return manager, defaultNetwork, nil
}

func transportNetworkFromConfig(cfg config.TransportNetworkConfig) (transport.Network, error) {
	return transportcfg.NetworkFromConfig(cfg)
}

func shouldAttachTun(cfg *config.NodeConfig) bool {
	switch config.NormalizeTunType(cfg.TunType) {
	case "none":
		return false
	default:
		return cfg.IfName != "none" && cfg.IfName != "dummy"
	}
}

func (c *daemonTunController) Attach(req admin.AttachTunRequest, replace bool) error {
	typ := strings.TrimSpace(req.Type)
	if typ == "" {
		typ = config.NormalizeTunType(c.cfg.TunType)
	} else {
		typ = config.NormalizeTunType(typ)
	}
	if typ == "none" {
		return c.Detach()
	}

	mtu := c.cfg.IfMTU
	if parsed, ok, err := parseOptionalUint(req.MTU); err != nil {
		return fmt.Errorf("mtu: %w", err)
	} else if ok {
		mtu = parsed
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = c.cfg.IfName
	}

	var device gtun.Tun
	var activeSocks socksTun
	var err error

	switch typ {
	case "native":
		native, err := tunnative.Create(c.log, tunnative.Config{
			Name:    name,
			Address: buildTunAddress(c.node.core.Address()),
			MTU:     mtu,
		})
		if err != nil {
			return err
		}
		device = native
	case "sockstun":
		mwo := c.cfg.TunMWO
		if parsed, ok, err := parseOptionalInt(req.MWO); err != nil {
			return fmt.Errorf("mwo: %w", err)
		} else if ok {
			mwo = parsed
		}
		mro := c.cfg.TunMRO
		if parsed, ok, err := parseOptionalInt(req.MRO); err != nil {
			return fmt.Errorf("mro: %w", err)
		} else if ok {
			mro = parsed
		}
		listen := strings.TrimSpace(req.SocksListen)
		if listen == "" {
			listen = c.cfg.TunSocksListen
		}
		if name == "" || name == "auto" {
			name = sockstun.DefaultName
		}
		proxies, defaultProxyURL, err := parseOptionalSocksRouting(req.SocksProxies, req.SocksDefaultProxy, c.cfg.TunSocksProxies, c.cfg.TunSocksDefaultProxy)
		if err != nil {
			return err
		}
		dns, err := parseOptionalSocksDNS(req.SocksDNSFallback, req.SocksNoResolve, c.cfg.TunSocksDNSFallback, c.cfg.TunSocksNoResolve)
		if err != nil {
			return err
		}
		tlsMITM, err := parseOptionalSocksTLSMITM(req.SocksTLSMITMCA, req.SocksTLSMITMKey, req.SocksTLSMITMHosts, c.cfg.TunSocksTLSMITM)
		if err != nil {
			return err
		}
		st, err := sockstun.Create(sockstun.Config{
			Name:            name,
			Listen:          listen,
			Address:         c.node.core.Address(),
			MTU:             mtu,
			MWO:             mwo,
			MRO:             mro,
			Proxies:         proxies,
			DefaultProxyURL: defaultProxyURL,
			DNS:             dns,
			TLSMITM:         tlsMITM,
			Log:             c.log,
		})
		if err != nil {
			return err
		}
		device = st
		activeSocks = st
	case "outproxy":
		mwo := c.cfg.TunMWO
		if parsed, ok, err := parseOptionalInt(req.MWO); err != nil {
			return fmt.Errorf("mwo: %w", err)
		} else if ok {
			mwo = parsed
		}
		mro := c.cfg.TunMRO
		if parsed, ok, err := parseOptionalInt(req.MRO); err != nil {
			return fmt.Errorf("mro: %w", err)
		} else if ok {
			mro = parsed
		}
		listen := strings.TrimSpace(req.SocksListen)
		if listen == "" {
			listen = c.cfg.TunSocksListen
		}
		if name == "" || name == "auto" {
			name = outproxy.DefaultName
		}
		proxies, defaultProxyURL, err := parseOptionalSocksRouting(req.SocksProxies, req.SocksDefaultProxy, c.cfg.TunSocksProxies, c.cfg.TunSocksDefaultProxy)
		if err != nil {
			return err
		}
		st, err := outproxy.Create(outproxy.Config{
			Name:            name,
			Listen:          listen,
			Address:         c.node.core.Address(),
			MTU:             mtu,
			MWO:             mwo,
			MRO:             mro,
			Proxies:         proxies,
			DefaultProxyURL: defaultProxyURL,
			Log:             c.log,
		})
		if err != nil {
			return err
		}
		device = st
		activeSocks = st
	default:
		return fmt.Errorf("unsupported tun type %q", typ)
	}

	firewallCfg, err := c.firewallConfigForAttach(typ, req)
	if err != nil {
		_ = device.Close()
		return err
	}
	previousFirewall := c.node.tun.FirewallConfig()
	c.node.tun.SetFirewallConfig(adminFirewallToTun(firewallCfg))
	if replace {
		err = c.node.tun.Replace(device, yggtun.AttachmentType(typ))
	} else {
		err = c.node.tun.Attach(device, yggtun.AttachmentType(typ))
	}
	if err != nil {
		c.node.tun.SetFirewallConfig(previousFirewall)
		_ = device.Close()
		return err
	}
	c.setActiveSocks(activeSocks)
	if typ == "native" {
		if status := c.node.tun.Status(); mtu != 0 && status.MTU != 0 && mtu != status.MTU {
			c.log.Warnf("Warning: Interface MTU %d automatically adjusted to %d", mtu, status.MTU)
		}
	}
	return nil
}

func (c *daemonTunController) Detach() error {
	if err := c.node.tun.Detach(); err != nil {
		return err
	}
	c.setActiveSocks(nil)
	return nil
}

func (c *daemonTunController) GetSocksProxies() admin.TunSocksProxyRouting {
	c.mu.RLock()
	active := c.activeSocks
	c.mu.RUnlock()
	if active == nil {
		return admin.TunSocksProxyRouting{
			Proxies:         configToAdminTunSocksProxies(c.cfg.TunSocksProxies),
			DefaultProxyURL: c.cfg.TunSocksDefaultProxy,
		}
	}
	return admin.TunSocksProxyRouting{
		Proxies:         sockstunToAdminProxies(active.Proxies()),
		DefaultProxyURL: active.DefaultProxyURL(),
	}
}

func (c *daemonTunController) SetSocksProxies(routing admin.TunSocksProxyRouting) error {
	cfgs := adminToSockstunProxies(routing.Proxies)
	c.mu.RLock()
	active := c.activeSocks
	c.mu.RUnlock()
	if active != nil {
		if err := active.SetProxies(cfgs, routing.DefaultProxyURL); err != nil {
			return err
		}
	}
	c.cfg.TunSocksProxies = adminToConfigTunSocksProxies(routing.Proxies)
	c.cfg.TunSocksDefaultProxy = strings.TrimSpace(routing.DefaultProxyURL)
	return nil
}

func (c *daemonTunController) GetSocksDNS() admin.TunSocksDNSConfig {
	c.mu.RLock()
	active := c.activeSocks
	c.mu.RUnlock()
	if active == nil {
		return admin.TunSocksDNSConfig{
			FallbackServer: c.cfg.TunSocksDNSFallback,
			NoResolveZones: append([]string{}, c.cfg.TunSocksNoResolve...),
		}
	}
	sock, ok := active.(*sockstun.Tun)
	if !ok {
		return admin.TunSocksDNSConfig{
			FallbackServer: c.cfg.TunSocksDNSFallback,
			NoResolveZones: append([]string{}, c.cfg.TunSocksNoResolve...),
		}
	}
	dns := sock.DNS()
	return admin.TunSocksDNSConfig{
		FallbackServer: dns.FallbackServer,
		NoResolveZones: dns.NoResolveZones,
	}
}

func (c *daemonTunController) SetSocksDNS(cfg admin.TunSocksDNSConfig) error {
	dns := sockstun.DNSConfig{
		FallbackServer: strings.TrimSpace(cfg.FallbackServer),
		NoResolveZones: append([]string{}, cfg.NoResolveZones...),
	}
	c.mu.RLock()
	active := c.activeSocks
	c.mu.RUnlock()
	if active == nil && config.NormalizeTunType(c.cfg.TunType) == "outproxy" && (dns.FallbackServer != "" || len(dns.NoResolveZones) > 0) {
		return fmt.Errorf("tun type outproxy does not support sockstun DNS resolution settings")
	}
	if active != nil {
		sock, ok := active.(*sockstun.Tun)
		if ok {
			if err := sock.SetDNS(dns); err != nil {
				return err
			}
			dns = sock.DNS()
		} else if dns.FallbackServer != "" || len(dns.NoResolveZones) > 0 {
			return fmt.Errorf("tun type outproxy does not support sockstun DNS resolution settings")
		}
		if !ok {
			c.cfg.TunSocksDNSFallback = dns.FallbackServer
			c.cfg.TunSocksNoResolve = dns.NoResolveZones
			return nil
		}
	}
	c.cfg.TunSocksDNSFallback = dns.FallbackServer
	c.cfg.TunSocksNoResolve = dns.NoResolveZones
	return nil
}

func (c *daemonTunController) GetFirewall() admin.TunFirewallConfig {
	cfg := c.node.tun.FirewallConfig()
	return admin.TunFirewallConfig{
		Enabled:         cfg.Enabled,
		AllowedTCPPorts: append([]uint16{}, cfg.AllowedTCPPorts...),
		AllowedUDPPorts: append([]uint16{}, cfg.AllowedUDPPorts...),
	}
}

func (c *daemonTunController) SetFirewall(cfg admin.TunFirewallConfig) error {
	cfg.AllowedTCPPorts = normalizeUint16s(cfg.AllowedTCPPorts)
	cfg.AllowedUDPPorts = normalizeUint16s(cfg.AllowedUDPPorts)
	c.node.tun.SetFirewallConfig(adminFirewallToTun(cfg))
	enabled := cfg.Enabled
	c.cfg.TunFirewall.Enabled = &enabled
	c.cfg.TunFirewall.AllowedTCPPorts = append([]uint16{}, cfg.AllowedTCPPorts...)
	c.cfg.TunFirewall.AllowedUDPPorts = append([]uint16{}, cfg.AllowedUDPPorts...)
	return nil
}

func (c *daemonTunController) firewallConfigForAttach(typ string, req admin.AttachTunRequest) (admin.TunFirewallConfig, error) {
	cfg := admin.TunFirewallConfig{
		Enabled:         effectiveTunFirewallEnabled(c.cfg, typ),
		AllowedTCPPorts: append([]uint16{}, c.cfg.TunFirewall.AllowedTCPPorts...),
		AllowedUDPPorts: append([]uint16{}, c.cfg.TunFirewall.AllowedUDPPorts...),
	}
	if strings.TrimSpace(req.FirewallEnabled) != "" {
		enabled, err := strconv.ParseBool(req.FirewallEnabled)
		if err != nil {
			return admin.TunFirewallConfig{}, fmt.Errorf("firewall_enabled: %w", err)
		}
		cfg.Enabled = enabled
	}
	if strings.TrimSpace(req.FirewallTCPPorts) != "" {
		ports, err := parseAdminUint16s(req.FirewallTCPPorts)
		if err != nil {
			return admin.TunFirewallConfig{}, fmt.Errorf("firewall_tcp_ports: %w", err)
		}
		cfg.AllowedTCPPorts = ports
	}
	if strings.TrimSpace(req.FirewallUDPPorts) != "" {
		ports, err := parseAdminUint16s(req.FirewallUDPPorts)
		if err != nil {
			return admin.TunFirewallConfig{}, fmt.Errorf("firewall_udp_ports: %w", err)
		}
		cfg.AllowedUDPPorts = ports
	}
	cfg.AllowedTCPPorts = normalizeUint16s(cfg.AllowedTCPPorts)
	cfg.AllowedUDPPorts = normalizeUint16s(cfg.AllowedUDPPorts)
	return cfg, nil
}

func (c *daemonTunController) setActiveSocks(active socksTun) {
	c.mu.Lock()
	c.activeSocks = active
	c.mu.Unlock()
}

func parseOptionalUint(value string) (uint64, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false, nil
	}
	out, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, false, err
	}
	return out, true, nil
}

func parseOptionalInt(value string) (int, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false, nil
	}
	out, err := strconv.Atoi(value)
	if err != nil {
		return 0, false, err
	}
	return out, true, nil
}

func parseOptionalSocksRouting(proxiesValue, defaultProxyValue string, fallbackProxies []config.TunSocksProxyConfig, fallbackDefaultProxy string) ([]sockstun.ProxyConfig, string, error) {
	proxiesValue = strings.TrimSpace(proxiesValue)
	if proxiesValue == "" {
		return configToSockstunProxies(fallbackProxies), strings.TrimSpace(firstNonEmpty(defaultProxyValue, fallbackDefaultProxy)), nil
	}
	var proxies []admin.TunSocksProxyConfig
	if err := json.Unmarshal([]byte(proxiesValue), &proxies); err != nil {
		return nil, "", fmt.Errorf("socks_proxies: %w", err)
	}
	return adminToSockstunProxies(proxies), strings.TrimSpace(firstNonEmpty(defaultProxyValue, fallbackDefaultProxy)), nil
}

func parseOptionalSocksDNS(fallbackValue, noResolveValue, fallbackServer string, fallbackZones []string) (sockstun.DNSConfig, error) {
	cfg := sockstun.DNSConfig{
		FallbackServer: strings.TrimSpace(firstNonEmpty(fallbackValue, fallbackServer)),
		NoResolveZones: append([]string{}, fallbackZones...),
	}
	noResolveValue = strings.TrimSpace(noResolveValue)
	if noResolveValue == "" {
		return cfg, nil
	}
	var zones []string
	if err := json.Unmarshal([]byte(noResolveValue), &zones); err != nil {
		return sockstun.DNSConfig{}, fmt.Errorf("socks_no_resolve: %w", err)
	}
	cfg.NoResolveZones = zones
	return cfg, nil
}

func parseOptionalSocksTLSMITM(caValue, keyValue, hostsValue string, fallback config.TunSocksTLSMITMConfig) (sockstun.TLSMITMConfig, error) {
	cfg := sockstun.TLSMITMConfig{
		CAFile:    strings.TrimSpace(firstNonEmpty(caValue, fallback.CAFile)),
		KeyFile:   strings.TrimSpace(firstNonEmpty(keyValue, fallback.KeyFile)),
		Hostnames: append([]string{}, fallback.Hostnames...),
	}
	hostsValue = strings.TrimSpace(hostsValue)
	if hostsValue == "" {
		return cfg, nil
	}
	var hosts []string
	if err := json.Unmarshal([]byte(hostsValue), &hosts); err != nil {
		return sockstun.TLSMITMConfig{}, fmt.Errorf("socks_tls_mitm_hosts: %w", err)
	}
	cfg.Hostnames = hosts
	return cfg, nil
}

func mustMarshalTunSocksProxies(proxies []config.TunSocksProxyConfig) string {
	if len(proxies) == 0 {
		return ""
	}
	bs, err := json.Marshal(configToAdminTunSocksProxies(proxies))
	if err != nil {
		panic(err)
	}
	return string(bs)
}

func mustMarshalStrings(values []string) string {
	if len(values) == 0 {
		return ""
	}
	bs, err := json.Marshal(values)
	if err != nil {
		panic(err)
	}
	return string(bs)
}

func mustMarshalUint16s(values []uint16) string {
	if len(values) == 0 {
		return ""
	}
	bs, err := json.Marshal(values)
	if err != nil {
		panic(err)
	}
	return string(bs)
}

func effectiveTunFirewallEnabled(cfg *config.NodeConfig, typ string) bool {
	if cfg.TunFirewall.Enabled != nil {
		return *cfg.TunFirewall.Enabled
	}
	return config.NormalizeTunType(typ) == "native"
}

func adminFirewallToTun(cfg admin.TunFirewallConfig) yggtun.FirewallConfig {
	return yggtun.FirewallConfig{
		Enabled:         cfg.Enabled,
		AllowedTCPPorts: append([]uint16{}, cfg.AllowedTCPPorts...),
		AllowedUDPPorts: append([]uint16{}, cfg.AllowedUDPPorts...),
	}
}

func parseAdminUint16s(value string) ([]uint16, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return []uint16{}, nil
	}
	var values []uint16
	if strings.HasPrefix(value, "[") {
		if err := json.Unmarshal([]byte(value), &values); err != nil {
			return nil, err
		}
		return normalizeUint16s(values), nil
	}
	parts := strings.Split(value, ",")
	values = make([]uint16, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := strconv.ParseUint(part, 10, 16)
		if err != nil {
			return nil, err
		}
		values = append(values, uint16(v))
	}
	return normalizeUint16s(values), nil
}

func normalizeUint16s(values []uint16) []uint16 {
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

func configToSockstunProxies(proxies []config.TunSocksProxyConfig) []sockstun.ProxyConfig {
	out := make([]sockstun.ProxyConfig, 0, len(proxies))
	for _, proxy := range proxies {
		out = append(out, sockstun.ProxyConfig{
			Filter:   proxy.Filter,
			ProxyURL: proxy.ProxyURL,
		})
	}
	return out
}

func configToAdminTunSocksProxies(proxies []config.TunSocksProxyConfig) []admin.TunSocksProxyConfig {
	out := make([]admin.TunSocksProxyConfig, 0, len(proxies))
	for _, proxy := range proxies {
		out = append(out, admin.TunSocksProxyConfig{
			Filter:   proxy.Filter,
			ProxyURL: proxy.ProxyURL,
		})
	}
	return out
}

func adminToConfigTunSocksProxies(proxies []admin.TunSocksProxyConfig) []config.TunSocksProxyConfig {
	out := make([]config.TunSocksProxyConfig, 0, len(proxies))
	for _, proxy := range proxies {
		out = append(out, config.TunSocksProxyConfig{
			Filter:   strings.TrimSpace(proxy.Filter),
			ProxyURL: strings.TrimSpace(proxy.ProxyURL),
		})
	}
	return out
}

func adminToSockstunProxies(proxies []admin.TunSocksProxyConfig) []sockstun.ProxyConfig {
	out := make([]sockstun.ProxyConfig, 0, len(proxies))
	for _, proxy := range proxies {
		out = append(out, sockstun.ProxyConfig{
			Filter:   proxy.Filter,
			ProxyURL: proxy.ProxyURL,
		})
	}
	return out
}

func sockstunToAdminProxies(proxies []sockstun.ProxyConfig) []admin.TunSocksProxyConfig {
	out := make([]admin.TunSocksProxyConfig, 0, len(proxies))
	for _, proxy := range proxies {
		out = append(out, admin.TunSocksProxyConfig{
			Filter:   proxy.Filter,
			ProxyURL: proxy.ProxyURL,
		})
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

func setLogLevel(loglevel string, log *logger.StdLogger) {
	level, ok := logger.ParseLevel(loglevel)
	if !ok {
		log.Info("Loglevel parse failed. Set default level(info)")
	}
	log.SetLevel(level)
}

func buildTunAddress(ip net.IP) string {
	prefix := address.GetPrefix()
	return fmt.Sprintf("%s/%d", ip.String(), 8*len(prefix[:])-1)
}
