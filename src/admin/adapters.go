package admin

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asciimoth/ygg/autopeer"
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

type GetAutoPeerResponse struct {
	Enabled                   bool            `json:"enabled"`
	Active                    bool            `json:"active"`
	Sources                   []string        `json:"sources"`
	FetchInterval             string          `json:"fetch_interval"`
	CheckInterval             string          `json:"check_interval"`
	MinimumConnected          int             `json:"minimum_connected"`
	MinimumConnectedFromFetch int             `json:"minimum_connected_from_fetch"`
	Countries                 []string        `json:"countries"`
	TransportSchemes          []string        `json:"transport_schemes"`
	Peers                     []autopeer.Peer `json:"peers"`
}

type SetAutoPeerRequest struct {
	Enabled                   string `json:"enabled,omitempty"`
	Sources                   string `json:"sources,omitempty"`
	FetchInterval             string `json:"fetch_interval,omitempty"`
	CheckInterval             string `json:"check_interval,omitempty"`
	MinimumConnected          string `json:"minimum_connected,omitempty"`
	MinimumConnectedFromFetch string `json:"minimum_connected_from_fetch,omitempty"`
	Countries                 string `json:"countries,omitempty"`
	TransportSchemes          string `json:"transport_schemes,omitempty"`
}

type RefreshAutoPeerRequest struct {
	Source string `json:"source,omitempty"`
}

type AutoPeerController struct {
	mu      sync.RWMutex
	manager *autopeer.Manager
	enabled bool
}

func NewAutoPeerController(manager *autopeer.Manager, enabled bool) *AutoPeerController {
	if manager == nil {
		return nil
	}
	return &AutoPeerController{
		manager: manager,
		enabled: enabled,
	}
}

func (c *AutoPeerController) Enabled() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.enabled
}

func (c *AutoPeerController) Snapshot() *GetAutoPeerResponse {
	if c == nil || c.manager == nil {
		return nil
	}
	managerCfg := c.manager.Config()
	fetcher := c.manager.Fetcher()
	res := &GetAutoPeerResponse{
		Enabled:                   c.Enabled(),
		Active:                    c.manager.IsStarted(),
		CheckInterval:             managerCfg.CheckInterval.String(),
		MinimumConnected:          managerCfg.MinimumConnected,
		MinimumConnectedFromFetch: managerCfg.MinimumConnectedFromFetch,
		Countries:                 managerCfg.Countries,
		TransportSchemes:          managerCfg.TransportSchemes,
		Peers:                     c.manager.Peers(),
	}
	if fetcher != nil {
		res.Sources = fetcher.Sources()
		res.FetchInterval = fetcher.Interval().String()
	}
	return res
}

func (c *AutoPeerController) Apply(req *SetAutoPeerRequest) error {
	if c == nil || c.manager == nil {
		return fmt.Errorf("autopeer controller not configured")
	}

	fetcher := c.manager.Fetcher()
	if fetcher == nil {
		return fmt.Errorf("autopeer fetcher not configured")
	}

	managerCfg := c.manager.Config()
	if req.CheckInterval != "" {
		d, err := time.ParseDuration(req.CheckInterval)
		if err != nil {
			return fmt.Errorf("invalid check_interval: %w", err)
		}
		managerCfg.CheckInterval = d
	}
	if req.MinimumConnected != "" {
		v, err := strconv.Atoi(req.MinimumConnected)
		if err != nil {
			return fmt.Errorf("invalid minimum_connected: %w", err)
		}
		managerCfg.MinimumConnected = v
	}
	if req.MinimumConnectedFromFetch != "" {
		v, err := strconv.Atoi(req.MinimumConnectedFromFetch)
		if err != nil {
			return fmt.Errorf("invalid minimum_connected_from_fetch: %w", err)
		}
		managerCfg.MinimumConnectedFromFetch = v
	}
	if req.Countries != "" {
		managerCfg.Countries = splitAdminCSV(req.Countries)
	}
	if req.TransportSchemes != "" {
		managerCfg.TransportSchemes = splitAdminCSV(req.TransportSchemes)
	}

	if req.FetchInterval != "" {
		d, err := time.ParseDuration(req.FetchInterval)
		if err != nil {
			return fmt.Errorf("invalid fetch_interval: %w", err)
		}
		fetcher.SetInterval(d)
	}
	if req.Sources != "" {
		fetcher.SetSources(splitAdminCSV(req.Sources))
	}

	c.manager.SetConfig(managerCfg)

	if req.Enabled != "" {
		enabled, err := strconv.ParseBool(req.Enabled)
		if err != nil {
			return fmt.Errorf("invalid enabled: %w", err)
		}

		c.mu.Lock()
		defer c.mu.Unlock()

		if !enabled && c.enabled && c.manager.IsStarted() {
			return fmt.Errorf("disabling autopeer at runtime is not supported")
		}
		c.enabled = enabled
		if enabled && !c.manager.IsStarted() {
			c.manager.Start()
		}
	}

	return nil
}

func (c *AutoPeerController) Refresh(source string) error {
	if c == nil || c.manager == nil || c.manager.Fetcher() == nil {
		return fmt.Errorf("autopeer controller not configured")
	}
	ctx := context.Background()
	fetcher := c.manager.Fetcher()
	if strings.TrimSpace(source) != "" {
		return fetcher.FetchNow(ctx, strings.TrimSpace(source))
	}
	for _, source := range fetcher.Sources() {
		if err := fetcher.FetchNow(ctx, source); err != nil {
			return err
		}
	}
	return nil
}

func splitAdminCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return []string{}
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

func (a *AdminSocket) SetupAutoPeerHandlers(c *AutoPeerController) {
	if c == nil {
		return
	}
	_ = a.AddHandler(
		"getAutoPeer", "Show autopeer configuration, state and fetched peers", []string{},
		func(_ json.RawMessage) (interface{}, error) {
			res := c.Snapshot()
			if res == nil {
				return nil, fmt.Errorf("autopeer controller not configured")
			}
			return res, nil
		},
	)
	_ = a.AddHandler(
		"setAutoPeer", "Update runtime autopeer configuration", []string{
			"enabled", "sources", "fetch_interval", "check_interval",
			"minimum_connected", "minimum_connected_from_fetch", "countries", "transport_schemes",
		},
		func(in json.RawMessage) (interface{}, error) {
			req := &SetAutoPeerRequest{}
			if err := json.Unmarshal(in, req); err != nil {
				return nil, err
			}
			if err := c.Apply(req); err != nil {
				return nil, err
			}
			return c.Snapshot(), nil
		},
	)
	_ = a.AddHandler(
		"refreshAutoPeer", "Refresh autopeer sources immediately", []string{"source"},
		func(in json.RawMessage) (interface{}, error) {
			req := &RefreshAutoPeerRequest{}
			if err := json.Unmarshal(in, req); err != nil {
				return nil, err
			}
			if err := c.Refresh(req.Source); err != nil {
				return nil, err
			}
			return c.Snapshot(), nil
		},
	)
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
