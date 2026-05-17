package main

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/mnlib"
	"github.com/asciimoth/ygg/ygglib/core"
	"github.com/miekg/dns"
)

const localDNSTTL = 60

type localDNSServer struct {
	addr string
	log  core.Logger

	resolver gonnect.Resolver
	udp      *dns.Server
	tcp      *dns.Server
	once     sync.Once
}

func newLocalDNSServer(addr string, network gonnect.Network, log core.Logger) (*localDNSServer, error) {
	if network == nil {
		return nil, errors.New("local DNS network is nil")
	}
	server := &localDNSServer{
		addr:     addr,
		log:      log,
		resolver: mnlib.NewResolver(network),
	}
	handler := dns.HandlerFunc(server.handleDNS)
	server.udp = &dns.Server{Addr: addr, Net: "udp", Handler: handler}
	server.tcp = &dns.Server{Addr: addr, Net: "tcp", Handler: handler}
	return server, nil
}

func (s *localDNSServer) Start() error {
	udpReady := make(chan error, 1)
	tcpReady := make(chan error, 1)
	s.udp.NotifyStartedFunc = func() {
		udpReady <- nil
	}
	s.tcp.NotifyStartedFunc = func() {
		tcpReady <- nil
	}

	go s.startServer(s.udp, udpReady)
	if err := <-udpReady; err != nil {
		return err
	}

	go s.startServer(s.tcp, tcpReady)
	if err := <-tcpReady; err != nil {
		_ = s.Stop()
		return err
	}

	if s.log != nil {
		s.log.Infof("Local DNS server listening on %s", s.addr)
	}
	return nil
}

func (s *localDNSServer) Stop() error {
	if s == nil {
		return nil
	}
	var err error
	s.once.Do(func() {
		if s.udp != nil {
			err = errors.Join(err, s.udp.Shutdown())
		}
		if s.tcp != nil {
			err = errors.Join(err, s.tcp.Shutdown())
		}
	})
	return err
}

func (s *localDNSServer) startServer(server *dns.Server, ready chan<- error) {
	if err := server.ListenAndServe(); err != nil {
		select {
		case ready <- err:
		default:
		}
		return
	}
}

func localDNSNetwork(controller *daemonTunController) gonnect.Network {
	if controller != nil {
		controller.mu.RLock()
		active := controller.activeSocks
		controller.mu.RUnlock()
		if active != nil {
			return active.Network()
		}
	}
	return gonnect.NativeConfig{}.Build()
}

func (s *localDNSServer) handleDNS(w dns.ResponseWriter, req *dns.Msg) {
	resp := new(dns.Msg)
	resp.SetReply(req)
	resp.Authoritative = false
	resp.RecursionAvailable = false

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	supported := false
	for _, question := range req.Question {
		if question.Qclass != dns.ClassINET {
			continue
		}
		switch question.Qtype {
		case dns.TypeA:
			supported = true
			s.addAddressAnswers(ctx, resp, question, "ip4")
		case dns.TypeAAAA:
			supported = true
			s.addAddressAnswers(ctx, resp, question, "ip6")
		}
	}
	if !supported && len(req.Question) > 0 {
		resp.Rcode = dns.RcodeNotImplemented
	}
	if err := w.WriteMsg(resp); err != nil && s.log != nil {
		s.log.Debugf("local DNS response write failed: %v", err)
	}
}

func (s *localDNSServer) addAddressAnswers(ctx context.Context, resp *dns.Msg, question dns.Question, network string) {
	ips, err := s.resolver.LookupIP(ctx, network, question.Name)
	if err != nil {
		if dnsErr, ok := err.(*net.DNSError); ok && dnsErr.IsNotFound {
			resp.Rcode = dns.RcodeNameError
		} else if resp.Rcode == dns.RcodeSuccess {
			resp.Rcode = dns.RcodeServerFailure
		}
		if s.log != nil {
			s.log.Debugf("local DNS lookup failed name=%q network=%q error=%v", question.Name, network, err)
		}
		return
	}
	for _, ip := range ips {
		switch network {
		case "ip4":
			if ip4 := ip.To4(); ip4 != nil {
				resp.Answer = append(resp.Answer, &dns.A{
					Hdr: dns.RR_Header{Name: question.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: localDNSTTL},
					A:   ip4,
				})
			}
		case "ip6":
			if ip.To4() == nil {
				if ip16 := ip.To16(); ip16 != nil {
					resp.Answer = append(resp.Answer, &dns.AAAA{
						Hdr:  dns.RR_Header{Name: question.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: localDNSTTL},
						AAAA: ip16,
					})
				}
			}
		}
	}
}
