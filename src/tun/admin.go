package tun

import (
	"encoding/json"

	"github.com/asciimoth/ygg/src/admin"
)

type GetTUNRequest struct{}
type GetTUNResponse struct {
	Enabled  bool   `json:"enabled"`
	Attached bool   `json:"attached"`
	State    State  `json:"state"`
	Type     string `json:"type,omitempty"`
	Name     string `json:"name,omitempty"`
	MTU      uint64 `json:"mtu,omitempty"`
	MRO      int    `json:"mro,omitempty"`
	MWO      int    `json:"mwo,omitempty"`
}

type TUNEntry struct {
	MTU uint64 `json:"mtu"`
}

func (t *TunAdapter) getTUNHandler(req *GetTUNRequest, res *GetTUNResponse) error {
	status := t.Status()
	res.Enabled = status.Enabled
	res.Attached = status.Attached
	res.State = status.State
	res.Type = status.Type
	res.Name = status.Name
	res.MTU = status.MTU
	res.MRO = status.MRO
	res.MWO = status.MWO
	return nil
}

func (t *TunAdapter) SetupAdminHandlers(a *admin.AdminSocket) {
	_ = a.AddHandler(
		"getTun", "Show information about the node's TUN interface", []string{},
		func(in json.RawMessage) (interface{}, error) {
			req := &GetTUNRequest{}
			res := &GetTUNResponse{}
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, err
			}
			if err := t.getTUNHandler(req, res); err != nil {
				return nil, err
			}
			return res, nil
		},
	)
}
