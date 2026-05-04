package admin

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/asciimoth/ygg/internal/transportcfg"
	"github.com/asciimoth/ygg/src/config"
	"github.com/asciimoth/ygg/transport"
)

type GetTransportResponse struct {
	DefaultNetwork        *string            `json:"default_network"`
	DefaultNetworkConfig  any                `json:"default_network_config"`
	NetworkMappings       map[string]*string `json:"network_mappings"`
	NetworkMappingConfigs map[string]any     `json:"network_mapping_configs"`
}

type SetTransportRequest struct {
	DefaultNetwork       string `json:"default_network,omitempty"`
	DefaultNetworkConfig string `json:"default_network_config,omitempty"`
	NetworkMappings      string `json:"network_mappings,omitempty"`
	UnsetNetworkMappings string `json:"unset_network_mappings,omitempty"`
}

func (a *AdminSocket) getTransportHandler() (*GetTransportResponse, error) {
	manager := a.core.TransportManager()
	res := &GetTransportResponse{
		DefaultNetwork:        adminNetworkName(manager.DefaultNetwork()),
		DefaultNetworkConfig:  adminNetworkConfig(manager.DefaultNetwork()),
		NetworkMappings:       make(map[string]*string),
		NetworkMappingConfigs: make(map[string]any),
	}
	for pattern, network := range manager.NetworkMappings() {
		res.NetworkMappings[pattern] = adminNetworkName(network)
		res.NetworkMappingConfigs[pattern] = adminNetworkConfig(network)
	}
	return res, nil
}

func (a *AdminSocket) setTransportHandler(req *SetTransportRequest) error {
	if strings.TrimSpace(req.DefaultNetwork) != "" && strings.TrimSpace(req.DefaultNetworkConfig) != "" {
		return fmt.Errorf("default_network and default_network_config are mutually exclusive")
	}

	if strings.TrimSpace(req.DefaultNetwork) != "" {
		network, err := networkFromAdminValue(req.DefaultNetwork, true)
		if err != nil {
			return fmt.Errorf("default_network: %w", err)
		}
		a.core.SetTransportDefaultNetwork(network)
	}
	if strings.TrimSpace(req.DefaultNetworkConfig) != "" {
		networkCfg, err := parseAdminNetworkConfig(req.DefaultNetworkConfig)
		if err != nil {
			return fmt.Errorf("default_network_config: %w", err)
		}
		network, err := networkFromConfig(networkCfg, true)
		if err != nil {
			return fmt.Errorf("default_network_config: %w", err)
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
			return transportcfg.NetworkFromConfig(config.NewTransportNetworkConfig(transport.NetworkKindNative))
		}
		return nil, fmt.Errorf("unset is not supported here")
	default:
		return transportcfg.NetworkFromConfig(config.NewTransportNetworkConfig(value))
	}
}

func networkFromConfig(cfg config.TransportNetworkConfig, allowUnset bool) (transport.Network, error) {
	if cfg.IsNull() {
		return nil, nil
	}
	if !cfg.IsSet() && allowUnset {
		return transportcfg.NetworkFromConfig(config.NewTransportNetworkConfig(transport.NetworkKindNative))
	}
	return transportcfg.NetworkFromConfig(cfg)
}

func adminNetworkName(network transport.Network) *string {
	if network == nil {
		return nil
	}
	cfg := transportcfg.ConfigFromNetwork(network)
	if cfg.Name() != "" {
		name := cfg.Name()
		return &name
	}
	if name, ok := transport.BuiltinNetworkName(network); ok {
		return &name
	}
	name := fmt.Sprintf("%T", network)
	return &name
}

func adminNetworkConfig(network transport.Network) any {
	cfg := transportcfg.ConfigFromNetwork(network)
	if cfg.IsNull() {
		return nil
	}
	if !cfg.IsSet() {
		return nil
	}
	if cfg.ProxyURL() == "" {
		return cfg.Name()
	}
	return map[string]any{
		"type":      cfg.Name(),
		"proxy_url": cfg.ProxyURL(),
	}
}

func parseAdminNetworkConfig(value string) (config.TransportNetworkConfig, error) {
	var cfg config.TransportNetworkConfig
	if err := json.Unmarshal([]byte(value), &cfg); err != nil {
		return config.TransportNetworkConfig{}, err
	}
	return cfg, nil
}
