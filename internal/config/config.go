package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// Config is the root configuration of FakeWebCamera.
type Config struct {
	Server ServerConfig `yaml:"server" json:"server"`
	Device DeviceConfig `yaml:"device" json:"device"`
	Auth   AuthConfig   `yaml:"auth" json:"auth"`
	Video  VideoConfig  `yaml:"video" json:"video"`
	MJPEG  MJPEGConfig  `yaml:"mjpeg" json:"mjpeg"`
	ONVIF  ONVIFConfig  `yaml:"onvif" json:"onvif"`
	Web    WebConfig    `yaml:"web" json:"web"`
}

type ServerConfig struct {
	// RTSPPort is the RTSP listener port. 554 requires root.
	RTSPPort int `yaml:"rtsp_port" json:"rtsp_port"`
	// HTTPPort serves ONVIF SOAP and the local control panel.
	HTTPPort int `yaml:"http_port" json:"http_port"`
	// Bind is the address the HTTP/ONVIF server listens on.
	Bind string `yaml:"bind" json:"bind"`
	// Interface selects the LAN interface used for discovery and advertised URLs.
	// "auto" picks the first non-loopback IPv4 interface.
	Interface string `yaml:"interface" json:"interface"`
	// UDPRTPPort / UDPRTCPPort enable RTSP over UDP. 0 disables UDP transport.
	UDPRTPPort  int `yaml:"udp_rtp_port" json:"udp_rtp_port"`
	UDPRTCPPort int `yaml:"udp_rtcp_port" json:"udp_rtcp_port"`
}

type DeviceConfig struct {
	Name            string `yaml:"name" json:"name"`
	Manufacturer    string `yaml:"manufacturer" json:"manufacturer"`
	Model           string `yaml:"model" json:"model"`
	FirmwareVersion string `yaml:"firmware_version" json:"firmware_version"`
	SerialNumber    string `yaml:"serial_number" json:"serial_number"`
	HardwareID      string `yaml:"hardware_id" json:"hardware_id"`
	Location        string `yaml:"location" json:"location"`
	UUID            string `yaml:"uuid" json:"uuid"`
}

type AuthConfig struct {
	Enabled  bool   `yaml:"enabled" json:"enabled"`
	Username string `yaml:"username" json:"username"`
	Password string `yaml:"password" json:"password"`
	// AllowAnonymousInfo keeps read-only discovery calls (GetSystemDateAndTime,
	// GetCapabilities, GetServices, GetDeviceInformation) available without
	// credentials. Many phone apps and NVRs probe these before authenticating.
	AllowAnonymousInfo bool `yaml:"allow_anonymous_info" json:"allow_anonymous_info"`
	// ClockSkewMinutes is the accepted difference for WS-UsernameToken Created.
	ClockSkewMinutes int `yaml:"clock_skew_minutes" json:"clock_skew_minutes"`
}

type VideoConfig struct {
	Source      string `yaml:"source" json:"source"`
	Width       int    `yaml:"width" json:"width"`
	Height      int    `yaml:"height" json:"height"`
	FPS         int    `yaml:"fps" json:"fps"`
	BitrateKbps int    `yaml:"bitrate_kbps" json:"bitrate_kbps"`
	GOP         int    `yaml:"gop" json:"gop"`
	// Encoder is "auto", "libx264" or "videotoolbox".
	Encoder string `yaml:"encoder" json:"encoder"`
	Loop    bool   `yaml:"loop" json:"loop"`
}

type MJPEGConfig struct {
	// Enabled exposes /stream2 and a second ONVIF profile. A low-rate JPEG
	// pipeline always runs so the snapshot endpoint stays available.
	Enabled bool `yaml:"enabled" json:"enabled"`
	Width   int  `yaml:"width" json:"width"`
	Height  int  `yaml:"height" json:"height"`
	FPS     int  `yaml:"fps" json:"fps"`
	Quality int  `yaml:"quality" json:"quality"`
}

type ONVIFConfig struct {
	Events                bool `yaml:"events" json:"events"`
	MotionSimulation      bool `yaml:"motion_simulation" json:"motion_simulation"`
	MotionIntervalSeconds int  `yaml:"motion_interval_seconds" json:"motion_interval_seconds"`
}

type WebConfig struct {
	// AllowLAN exposes the control panel to non-loopback clients. When enabled,
	// the panel requires the same credentials as ONVIF/RTSP.
	AllowLAN bool `yaml:"allow_lan" json:"allow_lan"`
}

// Default returns a configuration that works out of the box.
func Default() *Config {
	return &Config{
		Server: ServerConfig{
			RTSPPort:    8554,
			HTTPPort:    8080,
			Bind:        "0.0.0.0",
			Interface:   "auto",
			UDPRTPPort:  8000,
			UDPRTCPPort: 8001,
		},
		Device: DeviceConfig{
			Name:            "Fake Web Camera",
			Manufacturer:    "FakeCam",
			Model:           "FakeWebCamera",
			FirmwareVersion: "1.0.0",
			SerialNumber:    "FC-0001",
			HardwareID:      "FC-0001",
			Location:        "LAN",
			UUID:            uuid.NewString(),
		},
		Auth: AuthConfig{
			Enabled:            true,
			Username:           "admin",
			Password:           "admin123",
			AllowAnonymousInfo: true,
			ClockSkewMinutes:   5,
		},
		Video: VideoConfig{
			Width:       1920,
			Height:      1080,
			FPS:         25,
			BitrateKbps: 2500,
			GOP:         50,
			Encoder:     "auto",
			Loop:        true,
		},
		MJPEG: MJPEGConfig{
			Enabled: true,
			Width:   1280,
			Height:  720,
			FPS:     10,
			Quality: 5,
		},
		ONVIF: ONVIFConfig{
			Events:                true,
			MotionSimulation:      false,
			MotionIntervalSeconds: 20,
		},
		Web: WebConfig{AllowLAN: false},
	}
}

// Load reads a YAML configuration. Missing values keep their defaults.
func Load(path string) (*Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	cfg.normalize()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, nil
}

// Save writes the configuration atomically with owner-only permissions.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func (c *Config) normalize() {
	c.Server.Bind = strings.TrimSpace(c.Server.Bind)
	if c.Server.Bind == "" {
		c.Server.Bind = "0.0.0.0"
	}
	if c.Server.Interface == "" {
		c.Server.Interface = "auto"
	}
	c.Video.Source = ExpandPath(c.Video.Source)
	c.Video.Encoder = strings.ToLower(strings.TrimSpace(c.Video.Encoder))
	if c.Video.Encoder == "" {
		c.Video.Encoder = "auto"
	}
	if c.Device.UUID == "" {
		c.Device.UUID = uuid.NewString()
	}
	if c.Device.Name == "" {
		c.Device.Name = "Fake Web Camera"
	}
	if c.Device.Manufacturer == "" {
		c.Device.Manufacturer = "FakeCam"
	}
	if c.Device.Model == "" {
		c.Device.Model = "FakeWebCamera"
	}
	if c.Device.FirmwareVersion == "" {
		c.Device.FirmwareVersion = "1.0.0"
	}
	if c.Device.SerialNumber == "" {
		c.Device.SerialNumber = "FC-0001"
	}
	if c.Device.HardwareID == "" {
		c.Device.HardwareID = c.Device.SerialNumber
	}
	if c.Device.Location == "" {
		c.Device.Location = "LAN"
	}
	if c.Auth.ClockSkewMinutes <= 0 {
		c.Auth.ClockSkewMinutes = 5
	}
	if c.ONVIF.MotionIntervalSeconds <= 0 {
		c.ONVIF.MotionIntervalSeconds = 20
	}
	if c.MJPEG.Quality <= 0 {
		c.MJPEG.Quality = 5
	}
	if c.MJPEG.Width <= 0 {
		c.MJPEG.Width = 1280
	}
	if c.MJPEG.Height <= 0 {
		c.MJPEG.Height = 720
	}
	if c.MJPEG.FPS <= 0 {
		c.MJPEG.FPS = 10
	}
}

// Validate checks values that would otherwise fail at runtime.
func (c *Config) Validate() error {
	if c.Server.RTSPPort < 1 || c.Server.RTSPPort > 65535 {
		return fmt.Errorf("server.rtsp_port must be 1-65535")
	}
	if c.Server.HTTPPort < 1 || c.Server.HTTPPort > 65535 {
		return fmt.Errorf("server.http_port must be 1-65535")
	}
	if c.Server.HTTPPort == c.Server.RTSPPort {
		return fmt.Errorf("server.http_port and server.rtsp_port must differ")
	}
	if (c.Server.UDPRTPPort == 0) != (c.Server.UDPRTCPPort == 0) {
		return fmt.Errorf("server.udp_rtp_port and server.udp_rtcp_port must both be set or both be 0")
	}
	if c.Video.Width < 16 || c.Video.Height < 16 || c.Video.Width%2 != 0 || c.Video.Height%2 != 0 {
		return fmt.Errorf("video.width/height must be even and >= 16")
	}
	if c.Video.FPS < 1 || c.Video.FPS > 60 {
		return fmt.Errorf("video.fps must be 1-60")
	}
	if c.Video.BitrateKbps < 64 {
		return fmt.Errorf("video.bitrate_kbps must be >= 64")
	}
	if c.Video.GOP < 1 {
		c.Video.GOP = c.Video.FPS * 2
	}
	switch c.Video.Encoder {
	case "auto", "libx264", "videotoolbox":
	default:
		return fmt.Errorf("video.encoder must be auto, libx264 or videotoolbox")
	}
	// RFC 2435 (RTP/JPEG) requires dimensions that are multiples of 8.
	if c.MJPEG.Width < 16 || c.MJPEG.Height < 16 || c.MJPEG.Width%8 != 0 || c.MJPEG.Height%8 != 0 {
		return fmt.Errorf("mjpeg.width/height must be multiples of 8 and >= 16")
	}
	if c.MJPEG.FPS < 1 || c.MJPEG.FPS > 30 {
		return fmt.Errorf("mjpeg.fps must be 1-30")
	}
	if c.MJPEG.Quality < 2 || c.MJPEG.Quality > 31 {
		return fmt.Errorf("mjpeg.quality must be 2-31")
	}
	if c.Auth.Enabled {
		if c.Auth.Username == "" {
			return fmt.Errorf("auth.username must not be empty")
		}
		if c.Auth.Password == "" {
			return fmt.Errorf("auth.password must not be empty")
		}
	}
	return nil
}

// ExpandPath expands a leading ~ and converts relative paths to absolute.
func ExpandPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// DefaultPath returns ~/Library/Application Support/FakeWebCamera/config.yaml.
func DefaultPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "FakeWebCamera", "config.yaml")
}

// LogPath returns the menu bar application's log file path.
func LogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Logs", "FakeWebCamera.log")
}
