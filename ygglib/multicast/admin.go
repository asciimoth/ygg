package multicast

import (
	"slices"
	"strings"

	"github.com/Arceliar/phony"
)

type InterfaceState struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Beacon   bool   `json:"beacon"`
	Listen   bool   `json:"listen"`
	Password bool   `json:"password"`
}

func (m *Multicast) InterfaceStates() []InterfaceState {
	states := []InterfaceState{}
	phony.Block(m, func() {
		for name, intf := range m._interfaces {
			is := InterfaceState{
				Name:     intf.iface.Name(),
				Beacon:   intf.beacon,
				Listen:   intf.listen,
				Password: len(intf.password) > 0,
			}
			if li := m._listeners[name]; li != nil && li.listener != nil {
				is.Address = li.listener.Addr().String()
			} else {
				is.Address = "-"
			}
			states = append(states, is)
		}
	})
	slices.SortStableFunc(states, func(a, b InterfaceState) int {
		return strings.Compare(a.Name, b.Name)
	})
	return states
}
