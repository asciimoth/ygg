package admin

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/asciimoth/ygg/src/config"
	"github.com/asciimoth/ygg/transport"
)

type GetTransportResponse struct {
	DefaultNetwork  *string            `json:"default_network"`
	NetworkMappings map[string]*string `json:"network_mappings"`
}

type SetTransportRequest struct {
	DefaultNetwork       string `json:"default_network,omitempty"`
	NetworkMappings      string `json:"network_mappings,omitempty"`
	UnsetNetworkMappings string `json:"unset_network_mappings,omitempty"`
}

func (a *AdminSocket) getTransportHandler() (*GetTransportResponse, error) {
	manager := a.core.TransportManager()
	res := &GetTransportResponse{
		DefaultNetwork:  adminNetworkName(manager.DefaultNetwork()),
		NetworkMappings: make(map[string]*string),
	}
	for pattern, network := range manager.NetworkMappings() {
		res.NetworkMappings[pattern] = adminNetworkName(network)
	}
	return res, nil
}

func (a *AdminSocket) setTransportHandler(req *SetTransportRequest) error {
	if strings.TrimSpace(req.DefaultNetwork) != "" {
		network, err := networkFromAdminValue(req.DefaultNetwork, true)
		if err != nil {
			return fmt.Errorf("default_network: %w", err)
		}
		a.core.SetTransportDefaultNetwork(network)
	}

	if strings.TrimSpace(req.NetworkMappings) != "" {
		var mappings map[string]config.TransportNetworkConfig
		if err := json.Unmarshal([]byte(req.NetworkMappings), &mappings); err != nil {
			return fmt.Errorf("network_mappings: %w", err)
		}
		for pattern, networkCfg := range mappings {
			network, err := networkFromConfig(networkCfg, false)
			if err != nil {
				return fmt.Errorf("network_mappings[%q]: %w", pattern, err)
			}
			if err := a.core.MapTransportNetwork(pattern, network); err != nil {
				return err
			}
		}
	}

	for _, pattern := range splitAdminCSV(req.UnsetNetworkMappings) {
		if err := a.core.UnmapTransportNetwork(pattern); err != nil {
			return err
		}
	}

	return nil
}

func networkFromAdminValue(value string, allowUnset bool) (transport.Network, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	switch value {
	case "null", "nil":
		return nil, nil
	case "unset":
		if allowUnset {
			return transport.NewBuiltinNetwork(transport.NetworkKindNative)
		}
		return nil, fmt.Errorf("unset is not supported here")
	default:
		return transport.NewBuiltinNetwork(value)
	}
}

func networkFromConfig(cfg config.TransportNetworkConfig, allowUnset bool) (transport.Network, error) {
	if cfg.IsNull() {
		return nil, nil
	}
	if !cfg.IsSet() && allowUnset {
		return transport.NewBuiltinNetwork(transport.NetworkKindNative)
	}
	return transport.NewBuiltinNetwork(cfg.Name())
}

func adminNetworkName(network transport.Network) *string {
	if network == nil {
		return nil
	}
	if name, ok := transport.BuiltinNetworkName(network); ok {
		return &name
	}
	name := fmt.Sprintf("%T", network)
	return &name
}
