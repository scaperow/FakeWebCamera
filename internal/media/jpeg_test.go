package media

import (
	"bytes"
	"io"
	"testing"
	"time"
)

func jpegFrame(payload string) []byte {
	out := []byte{0xFF, 0xD8}
	out = append(out, []byte(payload)...)
	out = append(out, 0xFF, 0xD9)
	return out
}

func TestReadJPEGSplitsFrames(t *testing.T) {
	first := jpegFrame("first-frame")
	second := jpegFrame("second-frame")

	var stream bytes.Buffer
	stream.Write(first)
	stream.Write(second)

	var got [][]byte
	err := readJPEG(bytes.NewReader(stream.Bytes()), func(frame []byte) {
		got = append(got, append([]byte(nil), frame...))
	})
	if err != nil && err != io.EOF {
		t.Fatalf("readJPEG: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d frames, want 2", len(got))
	}
	if !bytes.Equal(got[0], first) {
		t.Fatalf("first frame = %x, want %x", got[0], first)
	}
	if !bytes.Equal(got[1], second) {
		t.Fatalf("second frame = %x, want %x", got[1], second)
	}
}

func TestReadJPEGHandlesSplitReads(t *testing.T) {
	frame := jpegFrame("split-across-reads")

	var got [][]byte
	err := readJPEG(&iotest{data: frame, chunk: 3}, func(f []byte) {
		got = append(got, append([]byte(nil), f...))
	})
	if err != nil && err != io.EOF {
		t.Fatalf("readJPEG: %v", err)
	}
	if len(got) != 1 || !bytes.Equal(got[0], frame) {
		t.Fatalf("unexpected frames: %v", got)
	}
}

func TestReadJPEGDropsGarbageBeforeSOI(t *testing.T) {
	frame := jpegFrame("payload")
	stream := append([]byte("noise-before-frame"), frame...)

	var got [][]byte
	_ = readJPEG(bytes.NewReader(stream), func(f []byte) {
		got = append(got, append([]byte(nil), f...))
	})
	if len(got) != 1 || !bytes.Equal(got[0], frame) {
		t.Fatalf("unexpected frames: %v", got)
	}
}

func TestFrameStore(t *testing.T) {
	store := NewFrameStore()
	if _, _, seq := store.Latest(); seq != 0 {
		t.Fatalf("fresh store seq = %d", seq)
	}

	store.Set([]byte{0xFF, 0xD8, 0xFF, 0xD9})
	store.Set(nil) // ignored
	jpeg, at, seq := store.Latest()
	if len(jpeg) != 4 || seq != 1 {
		t.Fatalf("latest = %d bytes, seq %d", len(jpeg), seq)
	}
	if time.Since(at) > time.Minute {
		t.Fatalf("timestamp looks wrong: %v", at)
	}
}

// iotest returns at most chunk bytes per Read to exercise buffering.
type iotest struct {
	data  []byte
	chunk int
}

func (r *iotest) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := r.chunk
	if n > len(p) {
		n = len(p)
	}
	if n > len(r.data) {
		n = len(r.data)
	}
	copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}
