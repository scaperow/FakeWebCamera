package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultIsValid(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config must be valid: %v", err)
	}
	if cfg.Server.RTSPPort != 8554 || cfg.Server.HTTPPort != 8080 {
		t.Fatalf("unexpected default ports: %+v", cfg.Server)
	}
	if !cfg.Auth.Enabled || cfg.Auth.Username != "admin" || cfg.Auth.Password != "admin123" {
		t.Fatalf("unexpected default auth: %+v", cfg.Auth)
	}
	if cfg.MJPEG.Width%8 != 0 || cfg.MJPEG.Height%8 != 0 {
		t.Fatalf("default MJPEG size must satisfy RFC 2435: %dx%d", cfg.MJPEG.Width, cfg.MJPEG.Height)
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"same ports", func(c *Config) { c.Server.HTTPPort = c.Server.RTSPPort }, "must differ"},
		{"zero rtsp", func(c *Config) { c.Server.RTSPPort = 0 }, "rtsp_port"},
		{"odd video", func(c *Config) { c.Video.Width = 1919 }, "video.width"},
		{"bad fps", func(c *Config) { c.Video.FPS = 0 }, "video.fps"},
		{"bad encoder", func(c *Config) { c.Video.Encoder = "nvenc" }, "video.encoder"},
		{"mjpeg not multiple of 8", func(c *Config) { c.MJPEG.Height = 540 }, "multiples of 8"},
		{"mjpeg odd", func(c *Config) { c.MJPEG.Width = 961 }, "multiples of 8"},
		{"bad quality", func(c *Config) { c.MJPEG.Quality = 1 }, "mjpeg.quality"},
		{"empty user", func(c *Config) { c.Auth.Username = "" }, "auth.username"},
		{"empty password", func(c *Config) { c.Auth.Password = "" }, "auth.password"},
		{"half udp", func(c *Config) { c.Server.UDPRTCPPort = 0 }, "both"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			tc.mutate(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "config.yaml")

	cfg := Default()
	cfg.Device.Name = "客厅摄像头"
	cfg.Video.Source = filepath.Join(dir, "movie.mp4")
	cfg.MJPEG.Height = 544
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config permissions = %o, want 600", perm)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Device.Name != cfg.Device.Name {
		t.Fatalf("name = %q, want %q", loaded.Device.Name, cfg.Device.Name)
	}
	if loaded.Device.UUID != cfg.Device.UUID {
		t.Fatalf("uuid = %q, want %q", loaded.Device.UUID, cfg.Device.UUID)
	}
	if loaded.MJPEG.Height != 544 {
		t.Fatalf("mjpeg height = %d, want 544", loaded.MJPEG.Height)
	}
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Device.Name == "" || cfg.Server.RTSPPort != 8554 {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
}

func TestLoadAppliesDefaultsForPartialFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := "video:\n  fps: 15\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Video.FPS != 15 {
		t.Fatalf("fps = %d, want 15", cfg.Video.FPS)
	}
	if cfg.Server.RTSPPort != 8554 || cfg.Auth.Username != "admin" {
		t.Fatalf("defaults lost: %+v", cfg)
	}
}

func TestNormalizeExpandsSource(t *testing.T) {
	cfg := Default()
	cfg.Video.Source = "~/Movies/fakecam.mp4"
	cfg.Video.Encoder = "  AUTO "
	cfg.normalize()

	home, _ := os.UserHomeDir()
	if cfg.Video.Source != filepath.Join(home, "Movies/fakecam.mp4") {
		t.Fatalf("source = %q", cfg.Video.Source)
	}
	if cfg.Video.Encoder != "auto" {
		t.Fatalf("encoder = %q, want auto", cfg.Video.Encoder)
	}
}

func TestExpandPath(t *testing.T) {
	if got := ExpandPath(""); got != "" {
		t.Fatalf("empty = %q", got)
	}
	home, _ := os.UserHomeDir()
	if got := ExpandPath("~/x"); got != filepath.Join(home, "x") {
		t.Fatalf("home path = %q", got)
	}
	abs := filepath.Join(t.TempDir(), "a.mp4")
	if got := ExpandPath(abs); got != abs {
		t.Fatalf("absolute path changed: %q", got)
	}
}
