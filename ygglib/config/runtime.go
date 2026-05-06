package config

import "strings"

const UnprivilegedAdminListen = "tcp://localhost:9001"

func NormalizeTunType(tunType string) string {
	tunType = strings.ToLower(strings.TrimSpace(tunType))
	switch tunType {
	case "":
		return "native"
	case "socks", "vtun+socks":
		return "sockstun"
	case "socks+vtun":
		return "outproxy"
	case "dummy":
		return "none"
	default:
		return tunType
	}
}

func (cfg *NodeConfig) StartupTunNeedsPrivileges() bool {
	if cfg == nil {
		return true
	}
	return startupTunNeedsPrivileges(cfg.TunType, cfg.IfName)
}

func (cfg *NodeConfig) UseUnprivilegedAdminFallback() bool {
	if cfg == nil {
		return false
	}
	adminListen := strings.TrimSpace(cfg.AdminListen)
	return adminListen == GetDefaults().DefaultAdminListen && !cfg.StartupTunNeedsPrivileges()
}

func EffectiveAdminListenFor(adminListen, tunType, ifName string) string {
	adminListen = strings.TrimSpace(adminListen)
	if adminListen == "" || strings.EqualFold(adminListen, "none") {
		return adminListen
	}
	if adminListen == GetDefaults().DefaultAdminListen && !startupTunNeedsPrivileges(tunType, ifName) {
		return UnprivilegedAdminListen
	}
	return adminListen
}

func startupTunNeedsPrivileges(tunType, ifName string) bool {
	if NormalizeTunType(tunType) != "native" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(ifName)) {
	case "none", "dummy":
		return false
	default:
		return true
	}
}
