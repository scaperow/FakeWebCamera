package logging

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Ring is a small in-memory log buffer used by the control panel.
type Ring struct {
	mu    sync.RWMutex
	lines []string
	max   int
}

func NewRing(max int) *Ring {
	if max <= 0 {
		max = 500
	}
	return &Ring{max: max}
}

func (r *Ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line == "" {
			continue
		}
		r.lines = append(r.lines, line)
	}
	if len(r.lines) > r.max {
		r.lines = r.lines[len(r.lines)-r.max:]
	}
	return len(p), nil
}

// Tail returns the last n lines.
func (r *Ring) Tail(n int) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if n <= 0 || n > len(r.lines) {
		n = len(r.lines)
	}
	out := make([]string, n)
	copy(out, r.lines[len(r.lines)-n:])
	return out
}

// Setup configures the standard logger and returns the in-memory buffer.
func Setup(logPath string) *Ring {
	ring := NewRing(800)
	writers := []io.Writer{os.Stderr, ring}

	if logPath != "" {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err == nil {
			rotate(logPath)
			if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
				writers = append(writers, f)
			}
		}
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetOutput(io.MultiWriter(writers...))
	return ring
}

func rotate(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() < 5*1024*1024 {
		return
	}
	_ = os.Rename(path, path+".old")
}
