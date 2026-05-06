package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/asciimoth/ygg/ygglib/core"
)

// TODO: Add authentication

type AdminSocket struct {
	core       *core.Core
	log        core.Logger
	listener   net.Listener
	web        *webAdminServer
	handlersMu sync.RWMutex
	handlers   map[string]handler
	done       chan struct{}
	config     struct {
		listenaddr    ListenAddress
		webListenaddr WebListenAddress
		webStaticDir  WebStaticDir
	}
}

type AdminSocketRequest struct {
	Name      string          `json:"request"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	KeepAlive bool            `json:"keepalive,omitempty"`
}

type AdminSocketResponse struct {
	Status   string             `json:"status"`
	Error    string             `json:"error,omitempty"`
	Request  AdminSocketRequest `json:"request"`
	Response json.RawMessage    `json:"response"`
}

type handler struct {
	desc    string      // What does the endpoint do?
	args    []string    // List of human-readable argument names
	handler HandlerFunc // First is input map, second is output
}

type HandlerFunc func(json.RawMessage) (interface{}, error)

type ListResponse struct {
	List []ListEntry `json:"list"`
}

type ListEntry struct {
	Command     string   `json:"command"`
	Description string   `json:"description"`
	Fields      []string `json:"fields,omitempty"`
}

// AddHandler is called for each admin function to add the handler and help documentation to the API.
func (a *AdminSocket) AddHandler(name, desc string, args []string, handlerfunc HandlerFunc) error {
	a.handlersMu.Lock()
	defer a.handlersMu.Unlock()
	if _, ok := a.handlers[strings.ToLower(name)]; ok {
		return errors.New("handler already exists")
	}
	a.handlers[strings.ToLower(name)] = handler{
		desc:    desc,
		args:    args,
		handler: handlerfunc,
	}
	return nil
}

// Init runs the initial admin setup.
func New(c *core.Core, log core.Logger, opts ...SetupOption) (*AdminSocket, error) {
	a := &AdminSocket{
		core:     c,
		log:      log,
		handlers: make(map[string]handler),
	}
	for _, opt := range opts {
		a._applyOption(opt)
	}
	if a.config.listenaddr == "none" || a.config.listenaddr == "" {
		if a.config.webListenaddr == "" || a.config.webListenaddr == "none" {
			return nil, nil
		}
		a.done = make(chan struct{})
	} else {
		if err := a.startSocket(); err != nil {
			return nil, err
		}
	}

	_ = a.AddHandler("list", "List available commands", []string{}, func(_ json.RawMessage) (interface{}, error) {
		res := &ListResponse{}
		a.handlersMu.RLock()
		for name, handler := range a.handlers {
			res.List = append(res.List, ListEntry{
				Command:     name,
				Description: handler.desc,
				Fields:      handler.args,
			})
		}
		a.handlersMu.RUnlock()
		sort.SliceStable(res.List, func(i, j int) bool {
			return strings.Compare(res.List[i].Command, res.List[j].Command) < 0
		})
		return res, nil
	})
	if a.config.webListenaddr != "" && a.config.webListenaddr != "none" {
		if err := a.startWebAdmin(); err != nil {
			_ = a.Stop()
			return nil, err
		}
	}
	return a, nil
}

func (a *AdminSocket) startSocket() error {
	listenaddr := string(a.config.listenaddr)
	u, err := url.Parse(listenaddr)
	if err == nil {
		switch strings.ToLower(u.Scheme) {
		case "unix":
			if _, err := os.Stat(u.Path); err == nil {
				a.log.Debug("Admin socket", u.Path, "already exists, trying to clean up")
				if _, err := net.DialTimeout("unix", u.Path, time.Second*2); err == nil || err.(net.Error).Timeout() {
					a.log.Err("Admin socket", u.Path, "already exists and is in use by another process")
					return fmt.Errorf("admin socket %q already exists and is in use", u.Path)
				} else {
					if err := os.Remove(u.Path); err == nil {
						a.log.Debug(u.Path, "was cleaned up")
					} else {
						a.log.Err(u.Path, "already exists and was not cleaned up:", err)
						return fmt.Errorf("remove stale admin socket %q: %w", u.Path, err)
					}
				}
			}
			a.listener, err = net.Listen("unix", u.Path)
			if err == nil {
				switch u.Path[:1] {
				case "@": // maybe abstract namespace
				default:
					if err := os.Chmod(u.Path, 0660); err != nil {
						a.log.Warn("WARNING:", u.Path, "may have unsafe permissions!")
					}
				}
			}
		case "tcp":
			a.listener, err = net.Listen("tcp", u.Host)
		default:
			a.listener, err = net.Listen("tcp", listenaddr)
		}
	} else {
		a.listener, err = net.Listen("tcp", listenaddr)
	}
	if err != nil {
		a.log.Errf("Admin socket failed to listen: %v", err)
		return err
	}
	a.log.Infof("%s admin socket listening on %s",
		strings.ToUpper(a.listener.Addr().Network()),
		a.listener.Addr().String())

	a.done = make(chan struct{})
	go a.listen()
	return nil
}

func (a *AdminSocket) SetupCoreHandlers() {
	_ = a.AddHandler(
		"getSelf", "Show details about this node", []string{},
		func(in json.RawMessage) (interface{}, error) {
			req := &GetSelfRequest{}
			res := &GetSelfResponse{}
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, err
			}
			if err := a.getSelfHandler(req, res); err != nil {
				return nil, err
			}
			return res, nil
		},
	)
	_ = a.AddHandler(
		"getNodeInfo", "Request nodeinfo from a remote node by its public key", []string{"key"},
		func(in json.RawMessage) (interface{}, error) {
			req := &GetNodeInfoRequest{}
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, err
			}
			info, err := a.core.GetNodeInfo(req.Key)
			if err != nil {
				return nil, err
			}
			return GetNodeInfoResponse{req.Key: info}, nil
		},
	)
	_ = a.AddHandler(
		"debug_remoteGetSelf", "Debug use only", []string{"key"},
		func(in json.RawMessage) (interface{}, error) {
			req := &DebugRemoteGetRequest{}
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, err
			}
			info, err := a.core.DebugRemoteGetSelf(req.Key)
			if err != nil {
				return nil, err
			}
			return DebugGetSelfResponse{keyToIP(req.Key): json.RawMessage(info)}, nil
		},
	)
	_ = a.AddHandler(
		"debug_remoteGetPeers", "Debug use only", []string{"key"},
		func(in json.RawMessage) (interface{}, error) {
			req := &DebugRemoteGetRequest{}
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, err
			}
			keys, err := a.core.DebugRemoteGetPeers(req.Key)
			if err != nil {
				return nil, err
			}
			return DebugGetPeersResponse{keyToIP(req.Key): DebugKeys{Keys: keys}}, nil
		},
	)
	_ = a.AddHandler(
		"debug_remoteGetTree", "Debug use only", []string{"key"},
		func(in json.RawMessage) (interface{}, error) {
			req := &DebugRemoteGetRequest{}
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, err
			}
			keys, err := a.core.DebugRemoteGetTree(req.Key)
			if err != nil {
				return nil, err
			}
			return DebugGetTreeResponse{keyToIP(req.Key): DebugKeys{Keys: keys}}, nil
		},
	)
	_ = a.AddHandler(
		"getPeers", "Show directly connected peers", []string{"sort"},
		func(in json.RawMessage) (interface{}, error) {
			req := &GetPeersRequest{}
			res := &GetPeersResponse{}
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, err
			}
			if err := a.getPeersHandler(req, res); err != nil {
				return nil, err
			}
			return res, nil
		},
	)
	_ = a.AddHandler(
		"getTree", "Show known Tree entries", []string{},
		func(in json.RawMessage) (interface{}, error) {
			req := &GetTreeRequest{}
			res := &GetTreeResponse{}
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, err
			}
			if err := a.getTreeHandler(req, res); err != nil {
				return nil, err
			}
			return res, nil
		},
	)
	_ = a.AddHandler(
		"getPaths", "Show established paths through this node", []string{},
		func(in json.RawMessage) (interface{}, error) {
			req := &GetPathsRequest{}
			res := &GetPathsResponse{}
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, err
			}
			if err := a.getPathsHandler(req, res); err != nil {
				return nil, err
			}
			return res, nil
		},
	)
	_ = a.AddHandler(
		"getSessions", "Show established traffic sessions with remote nodes", []string{},
		func(in json.RawMessage) (interface{}, error) {
			req := &GetSessionsRequest{}
			res := &GetSessionsResponse{}
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, err
			}
			if err := a.getSessionsHandler(req, res); err != nil {
				return nil, err
			}
			return res, nil
		},
	)
	_ = a.AddHandler(
		"getTransport", "Show transport manager network configuration", []string{},
		func(_ json.RawMessage) (interface{}, error) {
			return a.getTransportHandler()
		},
	)
	_ = a.AddHandler(
		"setTransport", "Update runtime transport manager networks", []string{
			"default_network", "default_network_config", "network_mappings", "unset_network_mappings",
		},
		func(in json.RawMessage) (interface{}, error) {
			req := &SetTransportRequest{}
			if err := json.Unmarshal(in, req); err != nil {
				return nil, err
			}
			if err := a.setTransportHandler(req); err != nil {
				return nil, err
			}
			return a.getTransportHandler()
		},
	)
	_ = a.AddHandler(
		"addPeer", "Add a peer to the peer list", []string{"uri", "interface"},
		func(in json.RawMessage) (interface{}, error) {
			req := &AddPeerRequest{}
			res := &AddPeerResponse{}
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, err
			}
			if err := a.addPeerHandler(req, res); err != nil {
				return nil, err
			}
			return res, nil
		},
	)
	_ = a.AddHandler(
		"removePeer", "Remove a peer from the peer list", []string{"uri", "interface"},
		func(in json.RawMessage) (interface{}, error) {
			req := &RemovePeerRequest{}
			res := &RemovePeerResponse{}
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, err
			}
			if err := a.removePeerHandler(req, res); err != nil {
				return nil, err
			}
			return res, nil
		},
	)
}

// IsStarted returns true if the module has been started.
func (a *AdminSocket) IsStarted() bool {
	select {
	case <-a.done:
		// Not blocking, so we're not currently running
		return false
	default:
		// Blocked, so we must have started
		return true
	}
}

// Stop will stop the admin API and close the socket.
func (a *AdminSocket) Stop() error {
	if a == nil {
		return nil
	}
	if a.listener != nil {
		select {
		case <-a.done:
		default:
			close(a.done)
		}
		return errors.Join(a.listener.Close(), a.stopWebAdmin())
	}
	select {
	case <-a.done:
	default:
		close(a.done)
	}
	return a.stopWebAdmin()
}

// listen is run by start and manages API connections.
func (a *AdminSocket) listen() {
	defer a.listener.Close()
	for {
		conn, err := a.listener.Accept()
		if err == nil {
			go a.handleRequest(conn)
		} else {
			select {
			case <-a.done:
				// Not blocked, so we havent started or already stopped
				return
			default:
				// Blocked, so we're supposed to keep running
			}
		}
	}
}

// handleRequest calls the request handler for each request sent to the admin API.
func (a *AdminSocket) handleRequest(conn net.Conn) {
	decoder := json.NewDecoder(conn)
	decoder.DisallowUnknownFields()

	encoder := json.NewEncoder(conn)
	encoder.SetIndent("", "  ")

	defer conn.Close()

	for {
		var err error
		var buf json.RawMessage
		var req AdminSocketRequest
		req.Arguments = []byte("{}")
		if err = func() error {
			if err = decoder.Decode(&buf); err != nil {
				return fmt.Errorf("failed to find request")
			}
			if err = json.Unmarshal(buf, &req); err != nil {
				return fmt.Errorf("failed to unmarshal request")
			}
			return nil
		}(); err != nil {
			req.Name = ""
		}
		resp := a.Dispatch(req)
		if err != nil {
			resp.Status = "error"
			resp.Error = err.Error()
		}
		if err = encoder.Encode(resp); err != nil {
			a.log.Debug("Encode error:", err)
		}
		if !req.KeepAlive {
			break
		} else {
			continue
		}
	}
}

// Dispatch calls a registered admin API handler and returns the same response
// envelope used by the socket protocol.
func (a *AdminSocket) Dispatch(req AdminSocketRequest) AdminSocketResponse {
	if req.Arguments == nil {
		req.Arguments = []byte("{}")
	}
	resp := AdminSocketResponse{Request: req}
	if req.Name == "" {
		resp.Status = "error"
		resp.Error = "no request specified"
		return resp
	}
	reqname := strings.ToLower(req.Name)
	a.handlersMu.RLock()
	handler, ok := a.handlers[reqname]
	a.handlersMu.RUnlock()
	if !ok {
		resp.Status = "error"
		resp.Error = fmt.Sprintf("unknown action '%s', try 'list' for help", reqname)
		return resp
	}
	res, err := handler.handler(req.Arguments)
	if err != nil {
		resp.Status = "error"
		resp.Error = err.Error()
		return resp
	}
	if resp.Response, err = json.Marshal(res); err != nil {
		resp.Status = "error"
		resp.Error = fmt.Sprintf("failed to marshal response: %v", err)
		return resp
	}
	resp.Status = "success"
	return resp
}

type DataUnit uint64

func (d DataUnit) String() string {
	switch {
	case d >= 1024*1024*1024*1024:
		return fmt.Sprintf("%2.1fTB", float64(d)/1024/1024/1024/1024)
	case d >= 1024*1024*1024:
		return fmt.Sprintf("%2.1fGB", float64(d)/1024/1024/1024)
	case d >= 1024*1024:
		return fmt.Sprintf("%2.1fMB", float64(d)/1024/1024)
	case d >= 100:
		return fmt.Sprintf("%2.1fKB", float64(d)/1024)
	default:
		return fmt.Sprintf("%dB", d)
	}
}
