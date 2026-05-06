package admin

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

//go:embed webstatic/*
var builtinWebStatic embed.FS

type webAdminServer struct {
	server   *http.Server
	listener net.Listener
}

func (a *AdminSocket) startWebAdmin() error {
	listener, err := listenWebAdmin(string(a.config.webListenaddr))
	if err != nil {
		a.log.Errf("Admin web failed to listen: %v", err)
		return err
	}
	static, err := a.webStaticFS()
	if err != nil {
		_ = listener.Close()
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/.yggapi", a.webAPIHandler())
	mux.Handle("/.yggapi/", a.webAPIHandler())
	mux.Handle("/", spaFileServer(static))

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return context.Background() },
	}
	a.web = &webAdminServer{server: server, listener: listener}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.log.Errf("Admin web server stopped: %v", err)
		}
	}()
	a.log.Infof("HTTP admin panel listening on %s", listener.Addr().String())
	return nil
}

func (a *AdminSocket) stopWebAdmin() error {
	if a.web == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := a.web.server.Shutdown(ctx)
	a.web = nil
	return err
}

func listenWebAdmin(listenaddr string) (net.Listener, error) {
	u, err := url.Parse(listenaddr)
	if err == nil && strings.EqualFold(u.Scheme, "tcp") {
		return net.Listen("tcp", u.Host)
	}
	if err == nil && u.Scheme != "" {
		return nil, fmt.Errorf("unsupported admin web listen scheme %q", u.Scheme)
	}
	return net.Listen("tcp", listenaddr)
}

func (a *AdminSocket) webStaticFS() (fs.FS, error) {
	if strings.TrimSpace(string(a.config.webStaticDir)) != "" {
		return os.DirFS(string(a.config.webStaticDir)), nil
	}
	return fs.Sub(builtinWebStatic, "webstatic")
}

func (a *AdminSocket) webAPIHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
			return
		}

		req := AdminSocketRequest{Name: strings.Trim(strings.TrimPrefix(r.URL.Path, "/.yggapi"), "/")}
		req.Arguments = []byte("{}")
		if r.Method == http.MethodGet {
			if req.Name == "" {
				req.Name = "list"
			}
			a.writeWebAPIResponse(w, a.Dispatch(req))
			return
		}

		if req.Name == "" {
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&req); err != nil {
				a.writeWebAPIResponse(w, AdminSocketResponse{
					Status: "error",
					Error:  fmt.Sprintf("failed to unmarshal request: %v", err),
				})
				return
			}
		} else {
			var raw json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				if errors.Is(err, io.EOF) {
					raw = json.RawMessage(`{}`)
				} else {
					a.writeWebAPIResponse(w, AdminSocketResponse{
						Status:  "error",
						Error:   fmt.Sprintf("failed to unmarshal arguments: %v", err),
						Request: req,
					})
					return
				}
			}
			req.Arguments = raw
		}
		a.writeWebAPIResponse(w, a.Dispatch(req))
	})
}

func (a *AdminSocket) writeWebAPIResponse(w http.ResponseWriter, resp AdminSocketResponse) {
	if resp.Status == "error" {
		w.WriteHeader(http.StatusBadRequest)
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(resp)
}

func spaFileServer(root fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(root, name); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/index.html"
		}
		fileServer.ServeHTTP(w, r)
	})
}
