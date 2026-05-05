package main

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"github.com/miekg/dns"
)

func TestLocalDNSServerAnswersAAndAAAA(t *testing.T) {
	server := &localDNSServer{
		resolver: fakeDNSResolver{
			ips: []net.IP{
				net.ParseIP("192.0.2.1"),
				net.ParseIP("200:db8::1"),
			},
		},
	}
	req := new(dns.Msg)
	req.SetQuestion("node.meshname.", dns.TypeA)
	req.Question = append(req.Question, dns.Question{Name: "node.meshname.", Qtype: dns.TypeAAAA, Qclass: dns.ClassINET})

	rec := &dnsRecorder{}
	server.handleDNS(rec, req)

	if rec.msg == nil {
		t.Fatal("expected response")
	}
	if rec.msg.Rcode != dns.RcodeSuccess {
		t.Fatalf("unexpected rcode: %s", dns.RcodeToString[rec.msg.Rcode])
	}
	if len(rec.msg.Answer) != 2 {
		t.Fatalf("expected 2 answers, got %d: %#v", len(rec.msg.Answer), rec.msg.Answer)
	}
	if rr, ok := rec.msg.Answer[0].(*dns.A); !ok || !rr.A.Equal(net.ParseIP("192.0.2.1")) {
		t.Fatalf("unexpected A answer: %#v", rec.msg.Answer[0])
	}
	if rr, ok := rec.msg.Answer[1].(*dns.AAAA); !ok || !rr.AAAA.Equal(net.ParseIP("200:db8::1")) {
		t.Fatalf("unexpected AAAA answer: %#v", rec.msg.Answer[1])
	}
}

func TestLocalDNSServerRejectsUnsupportedTypes(t *testing.T) {
	server := &localDNSServer{resolver: fakeDNSResolver{}}
	req := new(dns.Msg)
	req.SetQuestion("node.meshname.", dns.TypeMX)

	rec := &dnsRecorder{}
	server.handleDNS(rec, req)

	if rec.msg == nil {
		t.Fatal("expected response")
	}
	if rec.msg.Rcode != dns.RcodeNotImplemented {
		t.Fatalf("unexpected rcode: %s", dns.RcodeToString[rec.msg.Rcode])
	}
}

type dnsRecorder struct {
	msg *dns.Msg
}

func (r *dnsRecorder) LocalAddr() net.Addr         { return &net.UDPAddr{} }
func (r *dnsRecorder) RemoteAddr() net.Addr        { return &net.UDPAddr{} }
func (r *dnsRecorder) WriteMsg(msg *dns.Msg) error { r.msg = msg; return nil }
func (r *dnsRecorder) Write([]byte) (int, error)   { return 0, nil }
func (r *dnsRecorder) Close() error                { return nil }
func (r *dnsRecorder) TsigStatus() error           { return nil }
func (r *dnsRecorder) TsigTimersOnly(bool)         {}
func (r *dnsRecorder) Hijack()                     {}

type fakeDNSResolver struct {
	ips []net.IP
	err error
}

func (r fakeDNSResolver) LookupIP(context.Context, string, string) ([]net.IP, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.ips, nil
}

func (r fakeDNSResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return nil, nil
}

func (r fakeDNSResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return nil, nil
}

func (r fakeDNSResolver) LookupHost(context.Context, string) ([]string, error) {
	return nil, nil
}

func (r fakeDNSResolver) LookupAddr(context.Context, string) ([]string, error) {
	return nil, nil
}

func (r fakeDNSResolver) LookupCNAME(_ context.Context, host string) (string, error) {
	return host, nil
}

func (r fakeDNSResolver) LookupPort(context.Context, string, string) (int, error) {
	return 0, nil
}

func (r fakeDNSResolver) LookupNS(context.Context, string) ([]*net.NS, error) {
	return nil, nil
}

func (r fakeDNSResolver) LookupMX(context.Context, string) ([]*net.MX, error) {
	return nil, nil
}

func (r fakeDNSResolver) LookupSRV(context.Context, string, string, string) (string, []*net.SRV, error) {
	return "", nil, nil
}

func (r fakeDNSResolver) LookupTXT(context.Context, string) ([]string, error) {
	return nil, nil
}
