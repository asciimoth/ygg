package tun

func (m *TunAdapter) _applyOption(opt SetupOption) {
	switch v := opt.(type) {
	case InterfaceMTU:
		m.config.mtu = v
	case FirewallConfig:
		m.config.firewall = NewFirewall(v)
	}
}

type SetupOption interface {
	isSetupOption()
}

type InterfaceMTU uint64

func (a InterfaceMTU) isSetupOption() {}

func (a FirewallConfig) isSetupOption() {}
