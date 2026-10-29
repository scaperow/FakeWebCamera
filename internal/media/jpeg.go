package media

import (
	"bytes"
	"io"
	"sync"
	"time"
)

// FrameStore keeps the most recent JPEG frame for snapshots and the web preview.
type FrameStore struct {
	mu   sync.RWMutex
	jpeg []byte
	at   time.Time
	seq  uint64
}

func NewFrameStore() *FrameStore { return &FrameStore{} }

func (s *FrameStore) Set(jpeg []byte) {
	if len(jpeg) == 0 {
		return
	}
	s.mu.Lock()
	s.jpeg = jpeg
	s.at = time.Now()
	s.seq++
	s.mu.Unlock()
}

// Latest returns the newest JPEG frame. The returned slice must not be modified.
func (s *FrameStore) Latest() ([]byte, time.Time, uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.jpeg, s.at, s.seq
}

// readJPEG splits a raw MJPEG byte stream into complete JPEG images.
func readJPEG(r io.Reader, cb func([]byte)) error {
	const maxBuffer = 8 << 20

	buf := make([]byte, 0, 512<<10)
	tmp := make([]byte, 64<<10)
	soi := []byte{0xFF, 0xD8}
	eoi := []byte{0xFF, 0xD9}

	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)

			for {
				start := bytes.Index(buf, soi)
				if start < 0 {
					if len(buf) > 1 {
						buf = buf[len(buf)-1:]
					}
					break
				}
				if start > 0 {
					buf = buf[start:]
				}
				if len(buf) < 2 {
					break
				}
				end := bytes.Index(buf[2:], eoi)
				if end < 0 {
					break
				}
				end += 2
				frame := make([]byte, end+2)
				copy(frame, buf[:end+2])
				cb(frame)
				buf = buf[end+2:]
			}

			if len(buf) > maxBuffer {
				buf = buf[len(buf)-2:]
			}
		}

		if err != nil {
			return err
		}
	}
}
