package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"fakewebcamera/internal/config"
	"fakewebcamera/internal/logging"
	"fakewebcamera/internal/media"
)

// Controller is implemented by the application.
type Controller interface {
	Status() any
	Config() *config.Config
	UpdateConfig(*config.Config) error
	Restart() error
	TriggerMotion(bool)
	Snapshot() ([]byte, time.Time)
	Logs(int) []string
	Diag() any
}

// Server exposes the ONVIF endpoints, snapshot, preview and control panel.
type Server struct {
	cfg    *config.Config
	ctrl   Controller
	onvif  http.Handler
	frames *media.FrameStore
	ring   *logging.Ring

	httpServer *http.Server
	listener   net.Listener

	mu     sync.Mutex
	closed bool
}

func New(cfg *config.Config, ctrl Controller, onvif http.Handler, frames *media.FrameStore, ring *logging.Ring) *Server {
	return &Server{cfg: cfg, ctrl: ctrl, onvif: onvif, frames: frames, ring: ring}
}

func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/onvif/", s.handleONVIF)
	mux.HandleFunc("/snapshot.jpg", s.handleSnapshot)
	mux.HandleFunc("/api/status", s.guard(s.handleStatus))
	mux.HandleFunc("/api/config", s.guard(s.handleConfig))
	mux.HandleFunc("/api/restart", s.guard(s.handleRestart))
	mux.HandleFunc("/api/motion", s.guard(s.handleMotion))
	mux.HandleFunc("/api/logs", s.guard(s.handleLogs))
	mux.HandleFunc("/api/diag", s.guard(s.handleDiag))
	mux.HandleFunc("/api/snapshot.jpg", s.guard(s.handleSnapshot))
	mux.HandleFunc("/api/preview.mjpg", s.guard(s.handlePreview))
	mux.HandleFunc("/", s.guard(s.handleIndex))

	addr := fmt.Sprintf("%s:%d", s.cfg.Server.Bind, s.cfg.Server.HTTPPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen HTTP on %s: %w", addr, err)
	}
	s.listener = listener
	s.httpServer = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		err := s.httpServer.Serve(listener)
		if err != nil && err != http.ErrServerClosed && ctx.Err() == nil {
			log.Printf("http: server stopped: %v", err)
		}
	}()

	log.Printf("http: control panel on http://127.0.0.1:%d (ONVIF on :%d)", s.cfg.Server.HTTPPort, s.cfg.Server.HTTPPort)
	return nil
}

func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()

	if s.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.httpServer.Shutdown(ctx)
	}
	if s.listener != nil {
		_ = s.listener.Close()
	}
}

func (s *Server) handleONVIF(w http.ResponseWriter, r *http.Request) {
	s.onvif.ServeHTTP(w, r)
}

func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg.Web.AllowLAN {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				host = r.RemoteAddr
			}
			ip := net.ParseIP(host)
			if ip == nil || !ip.IsLoopback() {
				http.Error(w, "control panel is limited to localhost; set web.allow_lan=true to allow LAN access", http.StatusForbidden)
				return
			}
		} else if s.cfg.Auth.Enabled {
			if user, pass, ok := r.BasicAuth(); !ok || user != s.cfg.Auth.Username || pass != s.cfg.Auth.Password {
				w.Header().Set("WWW-Authenticate", `Basic realm="FakeWebCamera"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, indexHTML)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.ctrl.Status())
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := s.ctrl.Config()
		copyCfg := *cfg
		if copyCfg.Auth.Password != "" {
			copyCfg.Auth.Password = "********"
		}
		writeJSON(w, &copyCfg)
	case http.MethodPost, http.MethodPut:
		var incoming config.Config
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&incoming); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if incoming.Auth.Password == "********" {
			incoming.Auth.Password = s.ctrl.Config().Auth.Password
		}
		if err := s.ctrl.UpdateConfig(&incoming); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "restarting": true})
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
	go func() {
		time.Sleep(200 * time.Millisecond)
		if err := s.ctrl.Restart(); err != nil {
			log.Printf("http: restart failed: %v", err)
		}
	}()
}

func (s *Server) handleMotion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	on := true
	if v := r.URL.Query().Get("on"); v != "" {
		on, _ = strconv.ParseBool(v)
	}
	s.ctrl.TriggerMotion(on)
	writeJSON(w, map[string]any{"ok": true, "motion": on})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	if lines <= 0 {
		lines = 200
	}
	writeJSON(w, map[string]any{"lines": s.ctrl.Logs(lines)})
}

func (s *Server) handleDiag(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.ctrl.Diag())
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	jpeg, at := s.ctrl.Snapshot()
	if len(jpeg) == 0 {
		http.Error(w, "no frame available yet", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Last-Modified", at.UTC().Format(http.TimeFormat))
	_, _ = w.Write(jpeg)
}

func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "close")
	flusher.Flush()

	var lastSeq uint64
	for {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(100 * time.Millisecond):
		}

		jpeg, _, seq := s.frames.Latest()
		if len(jpeg) == 0 || seq == lastSeq {
			continue
		}
		lastSeq = seq
		if _, err := fmt.Fprintf(w, "--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(jpeg)); err != nil {
			return
		}
		if _, err := w.Write(jpeg); err != nil {
			return
		}
		if _, err := io.WriteString(w, "\r\n"); err != nil {
			return
		}
		flusher.Flush()
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// SnapshotHandler is used by ONVIF GetSnapshotUri indirectly through the same route.
func SnapshotHandler(frames *media.FrameStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jpeg, at, _ := frames.Latest()
		if len(jpeg) == 0 {
			http.Error(w, "no frame", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Last-Modified", at.UTC().Format(http.TimeFormat))
		_, _ = w.Write(jpeg)
	}
}
