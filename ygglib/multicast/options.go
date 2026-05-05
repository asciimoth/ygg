package multicast

import "regexp"

func (m *Multicast) _applyOption(opt SetupOption) {
	switch v := opt.(type) {
	case MulticastInterface:
		m.config._interfaces[v] = struct{}{}
	case GroupAddress:
		m.config._groupAddr = v
	case ProtocolVersion:
		m.config._protocolVersion = v
	}
}

type SetupOption interface {
	isSetupOption()
}

type MulticastInterface struct {
	Regex    *regexp.Regexp
	Beacon   bool
	Listen   bool
	Port     uint16
	Priority uint8
	Password string
}

type GroupAddress string
type ProtocolVersion struct {
	Major uint16
	Minor uint16
}

func (a MulticastInterface) isSetupOption() {}
func (a GroupAddress) isSetupOption()       {}
func (a ProtocolVersion) isSetupOption()    {}
