package config

import "testing"

func TestEffectiveAdminListenUsesUnprivilegedDefaultWithoutNativeTun(t *testing.T) {
	defaultAdmin := GetDefaults().DefaultAdminListen

	for _, tc := range []struct {
		name    string
		tunType string
		ifName  string
	}{
		{name: "none", tunType: "none", ifName: "auto"},
		{name: "dummy", tunType: "dummy", ifName: "auto"},
		{name: "sockstun", tunType: "sockstun", ifName: "auto"},
		{name: "socks alias", tunType: "socks", ifName: "auto"},
		{name: "legacy ifname none", tunType: "native", ifName: "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := EffectiveAdminListenFor(defaultAdmin, tc.tunType, tc.ifName)
			if got != UnprivilegedAdminListen {
				t.Fatalf("expected unprivileged admin listen, got %q", got)
			}
		})
	}
}

func TestEffectiveAdminListenKeepsNativeAndExplicitValues(t *testing.T) {
	defaultAdmin := GetDefaults().DefaultAdminListen
	if got := EffectiveAdminListenFor(defaultAdmin, "native", "auto"); got != defaultAdmin {
		t.Fatalf("expected native default admin listen to stay %q, got %q", defaultAdmin, got)
	}

	const explicit = "unix:///tmp/yggdrasil.sock"
	if got := EffectiveAdminListenFor(explicit, "none", "auto"); got != explicit {
		t.Fatalf("expected explicit admin listen to stay %q, got %q", explicit, got)
	}
}

func TestUseUnprivilegedAdminFallback(t *testing.T) {
	cfg := GenerateConfig()
	cfg.TunType = "none"
	if !cfg.UseUnprivilegedAdminFallback() {
		t.Fatal("expected default admin listen without native TUN to allow unprivileged fallback")
	}

	cfg.AdminListen = "unix:///tmp/yggdrasil.sock"
	if cfg.UseUnprivilegedAdminFallback() {
		t.Fatal("expected explicit non-default admin listen to disable unprivileged fallback")
	}
}
