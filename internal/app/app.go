package app

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"runtime"
	"sync"
	"time"

	"fakewebcamera/internal/config"
	"fakewebcamera/internal/discovery"
	"fakewebcamera/internal/httpapi"
	"fakewebcamera/internal/logging"
	"fakewebcamera/internal/media"
	"fakewebcamera/internal/netutil"
	"fakewebcamera/internal/onvif"
	"fakewebcamera/internal/rtsp"
)

// Status is the JSON payload of /api/status.
type Status struct {
	Running         bool          `json:"running"`
	Ready           bool          `json:"ready"`
	Uptime          string        `json:"uptime"`
	Interface       string        `json:"interface"`
	IP              string        `json:"ip"`
	RTSPURL         string        `json:"rtsp_url"`
	RTSPMJPEGURL    string        `json:"rtsp_mjpeg_url"`
	ONVIFURL        string        `json:"onvif_url"`
	SnapshotURL     string        `json:"snapshot_url"`
	WebURL          string        `json:"web_url"`
	Resolution      string        `json:"resolution"`
	MJPEGResolution string        `json:"mjpeg_resolution"`
	Media           media.Stats   `json:"media"`
	Clients         []rtsp.Client `json:"clients"`
	Motion          bool          `json:"motion"`
	Discovery       bool          `json:"discovery"`
	DiscoveryErr    string        `json:"discovery_error,omitempty"`
	ConfigPath      string        `json:"config_path"`
	AppVersion      string        `json:"app_version"`
}

// App owns all runtime components.
type App struct {
	rootCtx context.Context

	cfgMu   sync.RWMutex
	cfg     *config.Config
	cfgPath string
	ring    *logging.Ring

	restartMu sync.Mutex
	stateMu   sync.RWMutex

	runCtx    context.Context
	runCancel context.CancelFunc

	store    *media.FrameStore
	pipeline *media.Pipeline
	rtsp     *rtsp.Server
	onvif    *onvif.Server
	http     *httpapi.Server
	disc     *discovery.Server

	iface *net.Interface
	ip    net.IP

	startedAt time.Time
	running   bool

	discoveryErr string

	motionMu     sync.Mutex
	motion       bool
	motionCancel context.CancelFunc
}

func New(root context.Context, cfg *config.Config, cfgPath string, ring *logging.Ring) *App {
	return &App{
		rootCtx: root,
		cfg:     cfg,
		cfgPath: cfgPath,
		ring:    ring,
	}
}

// Start brings up RTSP, ffmpeg, ONVIF, HTTP and discovery.
func (a *App) Start(ctx context.Context) error {
	a.restartMu.Lock()
	defer a.restartMu.Unlock()
	return a.startLocked(ctx)
}

func (a *App) startLocked(parent context.Context) error {
	cfg := a.Config()

	ifi, err := netutil.Select(cfg.Server.Interface)
	if err != nil {
		return err
	}
	ip, err := netutil.PrimaryIPv4(ifi)
	if err != nil {
		return err
	}

	a.runCtx, a.runCancel = context.WithCancel(parent)

	store := media.NewFrameStore()

	rtspServer := rtsp.New(cfg)
	if err := rtspServer.Start(a.runCtx); err != nil {
		a.runCancel()
		return err
	}

	pipeline := media.NewPipeline(cfg, store)
	pipeline.OnH264 = rtspServer.WriteH264
	pipeline.OnJPEG = rtspServer.WriteMJPEG
	pipeline.Start(a.runCtx)

	onvifServer := onvif.New(cfg, store, ifi, ip)

	httpServer := httpapi.New(cfg, a, onvifServer, store, a.ring)
	if err := httpServer.Start(a.runCtx); err != nil {
		pipeline.Stop()
		rtspServer.Close()
		a.runCancel()
		return err
	}

	disc := discovery.New(cfg, ifi, ip)
	if err := disc.Start(a.runCtx); err != nil {
		a.stateMu.Lock()
		a.discoveryErr = err.Error()
		a.stateMu.Unlock()
		log.Printf("discovery: disabled: %v", err)
		disc = nil
	} else {
		a.stateMu.Lock()
		a.discoveryErr = ""
		a.stateMu.Unlock()
	}

	a.stateMu.Lock()
	a.store = store
	a.pipeline = pipeline
	a.rtsp = rtspServer
	a.onvif = onvifServer
	a.http = httpServer
	a.disc = disc
	a.iface = ifi
	a.ip = ip
	a.startedAt = time.Now()
	a.running = true
	a.stateMu.Unlock()

	a.startMotionLoop()
	log.Printf("app: ready on %s (%s), RTSP %d, HTTP %d", ip, ifi.Name, cfg.Server.RTSPPort, cfg.Server.HTTPPort)
	return nil
}

// Stop shuts down all components.
func (a *App) Stop() {
	a.restartMu.Lock()
	defer a.restartMu.Unlock()
	a.stopLocked()
}

func (a *App) stopLocked() {
	a.stopMotionLoop()

	a.stateMu.Lock()
	disc, httpServer, pipeline, rtspServer := a.disc, a.http, a.pipeline, a.rtsp
	a.disc, a.http, a.pipeline, a.rtsp = nil, nil, nil, nil
	a.running = false
	a.stateMu.Unlock()

	if disc != nil {
		disc.Close()
	}
	if httpServer != nil {
		httpServer.Close()
	}
	if pipeline != nil {
		pipeline.Stop()
	}
	if rtspServer != nil {
		rtspServer.Close()
	}
	if a.runCancel != nil {
		a.runCancel()
		a.runCancel = nil
	}
}

// Restart stops and starts every component with the current configuration.
func (a *App) Restart() error {
	a.restartMu.Lock()
	defer a.restartMu.Unlock()

	log.Printf("app: restarting")
	a.stopLocked()
	select {
	case <-time.After(300 * time.Millisecond):
	case <-a.rootCtx.Done():
		return a.rootCtx.Err()
	}
	return a.startLocked(a.rootCtx)
}

// Config returns the current configuration.
func (a *App) Config() *config.Config {
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	return a.cfg
}

// UpdateConfig validates, persists and asynchronously applies a new configuration.
func (a *App) UpdateConfig(next *config.Config) error {
	if next == nil {
		return fmt.Errorf("empty configuration")
	}
	next.Video.Source = config.ExpandPath(next.Video.Source)
	if err := next.Validate(); err != nil {
		return err
	}
	if next.Device.UUID == "" {
		next.Device.UUID = a.Config().Device.UUID
	}

	if a.cfgPath != "" {
		if err := next.Save(a.cfgPath); err != nil {
			return fmt.Errorf("save config: %w", err)
		}
	}

	a.cfgMu.Lock()
	a.cfg = next
	a.cfgMu.Unlock()

	go func() {
		time.Sleep(250 * time.Millisecond)
		if err := a.Restart(); err != nil {
			log.Printf("app: apply configuration: %v", err)
		}
	}()
	return nil
}

// Status returns the current state for the control panel.
func (a *App) Status() any {
	cfg := a.Config()

	a.stateMu.RLock()
	pipeline, rtspServer, disc := a.pipeline, a.rtsp, a.disc
	ipAddr, ifaceName := a.ip, ""
	if a.iface != nil {
		ifaceName = a.iface.Name
	}
	startedAt := a.startedAt
	running := a.running
	discRunning := disc != nil
	discoveryErr := a.discoveryErr
	a.stateMu.RUnlock()

	if disc != nil {
		if e := disc.LastError(); e != "" {
			discoveryErr = e
		}
	}

	ip := ""
	if ipAddr != nil {
		ip = ipAddr.String()
	}
	iface := ifaceName

	var mstats media.Stats
	if pipeline != nil {
		mstats = pipeline.Stats()
	}
	var clients []rtsp.Client
	if rtspServer != nil {
		clients = rtspServer.Clients()
	}

	rtspURL := ""
	mjpegURL := ""
	onvifURL := ""
	snapshotURL := ""
	webURL := ""
	if ip != "" {
		rtspURL = fmt.Sprintf("rtsp://%s:%d/stream1", ip, cfg.Server.RTSPPort)
		if cfg.MJPEG.Enabled {
			mjpegURL = fmt.Sprintf("rtsp://%s:%d/stream2", ip, cfg.Server.RTSPPort)
		}
		onvifURL = fmt.Sprintf("http://%s:%d/onvif/device_service", ip, cfg.Server.HTTPPort)
		snapshotURL = fmt.Sprintf("http://%s:%d/snapshot.jpg", ip, cfg.Server.HTTPPort)
		webURL = fmt.Sprintf("http://%s:%d", ip, cfg.Server.HTTPPort)
	}

	uptime := ""
	if !startedAt.IsZero() {
		uptime = time.Since(startedAt).Round(time.Second).String()
	}

	a.motionMu.Lock()
	motion := a.motion
	a.motionMu.Unlock()

	return Status{
		Running:         running && mstats.Running,
		Ready:           mstats.H264Frames > 0,
		Uptime:          uptime,
		Interface:       iface,
		IP:              ip,
		RTSPURL:         rtspURL,
		RTSPMJPEGURL:    mjpegURL,
		ONVIFURL:        onvifURL,
		SnapshotURL:     snapshotURL,
		WebURL:          webURL,
		Resolution:      fmt.Sprintf("%dx%d@%d", cfg.Video.Width, cfg.Video.Height, cfg.Video.FPS),
		MJPEGResolution: fmt.Sprintf("%dx%d@%d", cfg.MJPEG.Width, cfg.MJPEG.Height, cfg.MJPEG.FPS),
		Media:           mstats,
		Clients:         clients,
		Motion:          motion,
		Discovery:       discRunning,
		DiscoveryErr:    discoveryErr,
		ConfigPath:      a.cfgPath,
		AppVersion:      "1.0.0",
	}
}

func (a *App) Snapshot() ([]byte, time.Time) {
	a.stateMu.RLock()
	store := a.store
	a.stateMu.RUnlock()
	if store == nil {
		return nil, time.Time{}
	}
	jpeg, at, _ := store.Latest()
	return jpeg, at
}

func (a *App) Logs(n int) []string {
	if a.ring == nil {
		return nil
	}
	return a.ring.Tail(n)
}

func (a *App) Diag() any {
	interfaces, _ := netutil.List()
	cfg := a.Config()
	a.stateMu.RLock()
	iface, ipAddr, discRunning, discoveryErr := a.iface, a.ip, a.disc != nil, a.discoveryErr
	a.stateMu.RUnlock()
	return map[string]any{
		"os":            runtime.GOOS + "/" + runtime.GOARCH,
		"pid":           os.Getpid(),
		"config_path":   a.cfgPath,
		"ffmpeg":        media.FFmpegPath(),
		"interfaces":    interfaces,
		"selected":      iface,
		"ip":            ipAddr,
		"rtsp_port":     cfg.Server.RTSPPort,
		"http_port":     cfg.Server.HTTPPort,
		"rtp_ports":     []int{cfg.Server.UDPRTPPort, cfg.Server.UDPRTCPPort},
		"discovery":     discRunning,
		"discovery_err": discoveryErr,
		"multicast":     "239.255.255.250:3702",
	}
}

// TriggerMotion emits a motion event and clears it after a short delay.
func (a *App) TriggerMotion(on bool) {
	a.motionMu.Lock()
	a.motion = on
	a.motionMu.Unlock()

	a.stateMu.RLock()
	onvifServer := a.onvif
	a.stateMu.RUnlock()
	if onvifServer != nil {
		onvifServer.TriggerMotion(on)
	}

	if on {
		go func() {
			select {
			case <-time.After(3 * time.Second):
			case <-a.rootCtx.Done():
				return
			}
			a.motionMu.Lock()
			a.motion = false
			a.motionMu.Unlock()
			if srv := a.currentONVIF(); srv != nil {
				srv.TriggerMotion(false)
			}
		}()
	}
}

func (a *App) currentONVIF() *onvif.Server {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.onvif
}

func (a *App) startMotionLoop() {
	a.stopMotionLoop()
	if a.runCtx == nil {
		return
	}
	cfg := a.Config()
	if !cfg.ONVIF.MotionSimulation {
		return
	}
	interval := time.Duration(cfg.ONVIF.MotionIntervalSeconds) * time.Second
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	ctx, cancel := context.WithCancel(a.runCtx)
	a.motionCancel = cancel
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.TriggerMotion(true)
			}
		}
	}()
	log.Printf("app: motion simulation every %s", interval)
}

// SetMotionSimulation toggles periodic motion events without restarting streams.
func (a *App) SetMotionSimulation(on bool) error {
	a.cfgMu.Lock()
	a.cfg.ONVIF.MotionSimulation = on
	cfg := a.cfg
	a.cfgMu.Unlock()

	if a.cfgPath != "" {
		if err := cfg.Save(a.cfgPath); err != nil {
			return err
		}
	}
	a.startMotionLoop()
	return nil
}

func (a *App) stopMotionLoop() {
	if a.motionCancel != nil {
		a.motionCancel()
		a.motionCancel = nil
	}
}
