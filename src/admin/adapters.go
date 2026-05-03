package admin

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net"

	"github.com/asciimoth/ygg/src/address"
	"github.com/asciimoth/ygg/src/multicast"
	"github.com/asciimoth/ygg/src/tun"
)

type GetNodeInfoRequest struct {
	Key string `json:"key"`
}

type GetNodeInfoResponse map[string]json.RawMessage

type DebugRemoteGetRequest struct {
	Key string `json:"key"`
}

type DebugKeys struct {
	Keys []string `json:"keys"`
}

type DebugGetSelfResponse map[string]json.RawMessage
type DebugGetPeersResponse map[string]DebugKeys
type DebugGetTreeResponse map[string]DebugKeys

type GetMulticastInterfacesResponse struct {
	Interfaces []multicast.InterfaceState `json:"multicast_interfaces"`
}

func (a *AdminSocket) SetupMulticastHandlers(m *multicast.Multicast) {
	_ = a.AddHandler(
		"getMulticastInterfaces", "Show which interfaces multicast is enabled on", []string{},
		func(_ json.RawMessage) (interface{}, error) {
			return &GetMulticastInterfacesResponse{
				Interfaces: m.InterfaceStates(),
			}, nil
		},
	)
}

func (a *AdminSocket) SetupTunHandlers(t *tun.TunAdapter) {
	_ = a.AddHandler(
		"getTun", "Show information about the node's TUN interface", []string{},
		func(_ json.RawMessage) (interface{}, error) {
			return t.Status(), nil
		},
	)
}

func keyToIP(keyHex string) string {
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return keyHex
	}
	addr := address.AddrForKey(ed25519.PublicKey(key))
	return net.IP(addr[:]).String()
}
