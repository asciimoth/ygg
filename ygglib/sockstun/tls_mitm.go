package sockstun

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	gonnecthelpers "github.com/asciimoth/gonnect/helpers"
	"github.com/asciimoth/socksgo"
	"github.com/asciimoth/socksgo/protocol"
)

var defaultTLSMITMHostnames = []string{"*.ygg", "*.meshname", "*.meship", "*.onion", "*.i2p"}

type TLSMITMConfig struct {
	CAFile    string
	KeyFile   string
	Hostnames []string
}

type tlsMITM struct {
	caCert   *x509.Certificate
	caKey    crypto.Signer
	hosts    []string
	network  *routeNetwork
	log      Logger
	certMu   sync.Mutex
	certByCN map[string]*tls.Certificate
}

func newTLSMITM(cfg TLSMITMConfig, network *routeNetwork, log Logger) (*tlsMITM, error) {
	cfg.CAFile = strings.TrimSpace(cfg.CAFile)
	cfg.KeyFile = strings.TrimSpace(cfg.KeyFile)
	if cfg.CAFile == "" && cfg.KeyFile == "" {
		return nil, nil
	}
	if cfg.CAFile == "" || cfg.KeyFile == "" {
		return nil, fmt.Errorf("sockstun TLS MITM requires both CAFile and KeyFile")
	}
	caPEM, err := os.ReadFile(cfg.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read sockstun TLS MITM CA file: %w", err)
	}
	keyPEM, err := os.ReadFile(cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("read sockstun TLS MITM key file: %w", err)
	}
	ca, err := tls.X509KeyPair(caPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("load sockstun TLS MITM CA pair: %w", err)
	}
	caCert, err := x509.ParseCertificate(ca.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse sockstun TLS MITM CA certificate: %w", err)
	}
	caKey, ok := ca.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("sockstun TLS MITM CA private key does not implement crypto.Signer")
	}

	hosts := normalizeStringSlice(cfg.Hostnames)
	if len(hosts) == 0 {
		hosts = append([]string{}, defaultTLSMITMHostnames...)
	}
	return &tlsMITM{
		caCert:   caCert,
		caKey:    caKey,
		hosts:    hosts,
		network:  network,
		log:      log,
		certByCN: make(map[string]*tls.Certificate),
	}, nil
}

func (m *tlsMITM) handlers() map[protocol.Cmd]socksgo.CommandHandler {
	handlers := make(map[protocol.Cmd]socksgo.CommandHandler, len(socksgo.DefaultCommandHandlers))
	for cmd, handler := range socksgo.DefaultCommandHandlers {
		handlers[cmd] = handler
	}
	handlers[protocol.CmdConnect] = socksgo.CommandHandler{
		Socks4:    true,
		Socks5:    true,
		TLSCompat: true,
		Handler: func(ctx context.Context, server *socksgo.Server, conn net.Conn, ver string, info protocol.AuthInfo, cmd protocol.Cmd, addr protocol.Addr) error {
			if !m.shouldIntercept(addr) {
				return socksgo.DefaultConnectHandler.Handler(ctx, server, conn, ver, info, cmd, addr)
			}
			return m.handle(ctx, server, conn, ver, addr)
		},
	}
	return handlers
}

func (m *tlsMITM) shouldIntercept(addr protocol.Addr) bool {
	if !strings.HasPrefix(addr.Network(), "tcp") || addr.Port != 443 {
		return false
	}
	host := normalizeHost(addr.ToFQDN())
	if host == "" || isIPLiteral(host) {
		return false
	}
	for _, pattern := range m.hosts {
		if matchHostnamePattern(host, pattern) {
			return true
		}
	}
	return false
}

func (m *tlsMITM) handle(ctx context.Context, server *socksgo.Server, conn net.Conn, ver string, addr protocol.Addr) error {
	pool := server.GetPool()
	if err := server.CheckRaddr(&addr); err != nil {
		protocol.Reject(ver, conn, protocol.DisallowReply, pool)
		return err
	}
	host := normalizeHost(addr.ToFQDN())
	target := net.JoinHostPort(host, "80")
	conn2, err := m.network.dialUnresolved(ctx, addr.Network(), target)
	if err != nil {
		protocol.Reject(ver, conn, protocol.HostUnreachReply, pool)
		return err
	}
	defer func() { _ = conn2.Close() }()
	if err := protocol.Reply(ver, conn, protocol.SuccReply, protocol.AddrFromNetAddr(conn2.RemoteAddr()), pool); err != nil {
		return err
	}
	cert, err := m.leafCert(host)
	if err != nil {
		return err
	}
	tlsConn := tls.Server(conn, &tls.Config{
		Certificates: []tls.Certificate{*cert},
		NextProtos:   []string{"http/1.1"},
		MinVersion:   tls.VersionTLS12,
	})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return err
	}
	if m.log != nil {
		m.log.Debugf("sockstun TLS MITM intercepted host=%q upstream=%q", host, target)
	}
	return gonnecthelpers.PipeConn(tlsConn, conn2)
}

func (m *tlsMITM) leafCert(host string) (*tls.Certificate, error) {
	m.certMu.Lock()
	defer m.certMu.Unlock()
	if cert := m.certByCN[host]; cert != nil {
		return cert, nil
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{host},
	}
	certBytes, err := x509.CreateCertificate(rand.Reader, &template, m.caCert, &key.PublicKey, m.caKey)
	if err != nil {
		return nil, err
	}
	cert := &tls.Certificate{
		Certificate: [][]byte{certBytes, m.caCert.Raw},
		PrivateKey:  key,
	}
	m.certByCN[host] = cert
	return cert, nil
}

func normalizeStringSlice(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func matchHostnamePattern(host, pattern string) bool {
	host = strings.TrimSuffix(normalizeHost(host), ".")
	pattern = strings.TrimSuffix(normalizeHost(pattern), ".")
	if host == "" || pattern == "" {
		return false
	}
	if strings.HasPrefix(pattern, "*.") {
		suffix := strings.TrimPrefix(pattern, "*")
		return strings.HasSuffix(host, suffix) && host != strings.TrimPrefix(suffix, ".")
	}
	return host == pattern
}
