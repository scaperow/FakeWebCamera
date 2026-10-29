package media

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/mpegts"
	tscodecs "github.com/bluenviron/mediacommon/v2/pkg/formats/mpegts/codecs"

	"fakewebcamera/internal/config"
)

// Stats describes the state of the ffmpeg pipeline.
type Stats struct {
	Running         bool      `json:"running"`
	Encoder         string    `json:"encoder"`
	Source          string    `json:"source"`
	UsingTestSource bool      `json:"using_test_source"`
	Restarts        int       `json:"restarts"`
	H264Frames      uint64    `json:"h264_frames"`
	JPEGFrames      uint64    `json:"jpeg_frames"`
	LastH264        time.Time `json:"last_h264"`
	LastJPEG        time.Time `json:"last_jpeg"`
	LastError       string    `json:"last_error"`
}

// Pipeline runs ffmpeg and feeds H.264 access units and JPEG frames to callbacks.
type Pipeline struct {
	cfg   *config.Config
	store *FrameStore

	// OnH264 is called for every access unit. sps/pps are set when present.
	OnH264 func(au [][]byte, sps, pps []byte)
	// OnJPEG is called for every JPEG frame.
	OnJPEG func(jpeg []byte)

	mu         sync.RWMutex
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	running    bool
	cmd        *exec.Cmd
	encoder    string
	source     string
	testSrc    bool
	restarts   int
	frames264  uint64
	framesJPEG uint64
	last264    time.Time
	lastJPEG   time.Time
	lastErr    string
}

func NewPipeline(cfg *config.Config, store *FrameStore) *Pipeline {
	return &Pipeline{cfg: cfg, store: store}
}

// Start launches the supervisor goroutine.
func (p *Pipeline) Start(parent context.Context) {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return
	}
	p.ctx, p.cancel = context.WithCancel(parent)
	p.done = make(chan struct{})
	p.running = true
	p.mu.Unlock()

	go p.loop()
}

// Stop terminates ffmpeg and waits for the supervisor to exit.
func (p *Pipeline) Stop() {
	p.mu.Lock()
	cancel := p.cancel
	done := p.done
	running := p.running
	p.mu.Unlock()
	if !running {
		return
	}
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
}

func (p *Pipeline) Stats() Stats {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return Stats{
		Running:         p.running,
		Encoder:         p.encoder,
		Source:          p.source,
		UsingTestSource: p.testSrc,
		Restarts:        p.restarts,
		H264Frames:      p.frames264,
		JPEGFrames:      p.framesJPEG,
		LastH264:        p.last264,
		LastJPEG:        p.lastJPEG,
		LastError:       p.lastErr,
	}
}

func (p *Pipeline) loop() {
	defer func() {
		p.mu.Lock()
		p.running = false
		p.mu.Unlock()
		close(p.done)
	}()

	encoder := chooseEncoder(p.cfg.Video.Encoder)
	p.mu.Lock()
	p.encoder = encoder
	p.mu.Unlock()
	log.Printf("media: using encoder %s", encoder)

	for {
		select {
		case <-p.ctx.Done():
			return
		default:
		}

		err := p.runOnce(p.ctx, encoder)
		if p.ctx.Err() != nil {
			return
		}

		p.mu.Lock()
		p.restarts++
		if err != nil {
			p.lastErr = err.Error()
		}
		p.mu.Unlock()

		if err != nil {
			log.Printf("media: ffmpeg stopped: %v (restarting in 2s)", err)
		} else {
			log.Printf("media: ffmpeg exited (restarting in 2s)")
		}

		select {
		case <-time.After(2 * time.Second):
		case <-p.ctx.Done():
			return
		}
	}
}

func (p *Pipeline) runOnce(ctx context.Context, encoder string) error {
	source := p.cfg.Video.Source
	testSource := false
	if source == "" {
		testSource = true
	} else if st, err := os.Stat(source); err != nil || st.IsDir() {
		log.Printf("media: source %q is not readable, falling back to test pattern", source)
		testSource = true
	}

	args, err := buildArgs(p.cfg, encoder, source, testSource)
	if err != nil {
		return err
	}

	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.ExtraFiles = []*os.File{pw}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return err
	}

	log.Printf("media: starting ffmpeg (%s, source=%s)", encoder, sourceLabel(source, testSource))
	if err := cmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return err
	}
	_ = pw.Close()

	p.mu.Lock()
	p.cmd = cmd
	p.source = source
	p.testSrc = testSource
	p.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		scanFFmpegLog(stderr)
	}()
	go func() {
		defer wg.Done()
		if err := readJPEG(pr, p.handleJPEG); err != nil && !errors.Is(err, io.EOF) && ctx.Err() == nil {
			log.Printf("media: jpeg reader: %v", err)
		}
	}()

	h264Err := p.readH264(ctx, stdout)

	waitErr := cmd.Wait()
	_ = pr.Close()
	wg.Wait()

	p.mu.Lock()
	p.cmd = nil
	p.mu.Unlock()

	if ctx.Err() != nil {
		return nil
	}
	if h264Err != nil && !errors.Is(h264Err, io.EOF) {
		return h264Err
	}
	return waitErr
}

func (p *Pipeline) readH264(ctx context.Context, r io.Reader) error {
	first := true
	for ctx.Err() == nil {
		mr := &mpegts.Reader{R: r}
		if err := mr.Initialize(); err != nil {
			return err
		}

		var track *mpegts.Track
		for _, candidate := range mr.Tracks() {
			if _, ok := candidate.Codec.(*tscodecs.H264); ok {
				track = candidate
				break
			}
		}
		if track == nil {
			return fmt.Errorf("ffmpeg output has no H.264 track")
		}

		mr.OnDataH264(track, func(_, _ int64, au [][]byte) error {
			if len(au) == 0 {
				return nil
			}
			if first {
				log.Printf("media: first H.264 access unit (%d NALUs)", len(au))
				first = false
			}
			sps, pps := parameterSets(au)
			p.handleH264(au, sps, pps)
			return nil
		})

		for {
			err := mr.Read()
			if err != nil {
				if errors.Is(err, io.EOF) {
					return err
				}
				return err
			}
		}
	}
	return ctx.Err()
}

func (p *Pipeline) handleH264(au [][]byte, sps, pps []byte) {
	p.mu.Lock()
	p.frames264++
	p.last264 = time.Now()
	p.mu.Unlock()

	if p.OnH264 != nil {
		p.OnH264(au, sps, pps)
	}
}

func (p *Pipeline) handleJPEG(jpeg []byte) {
	p.mu.Lock()
	p.framesJPEG++
	p.lastJPEG = time.Now()
	p.mu.Unlock()

	p.store.Set(jpeg)
	if p.OnJPEG != nil {
		p.OnJPEG(jpeg)
	}
}

func parameterSets(au [][]byte) (sps, pps []byte) {
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		switch h264.NALUType(nalu[0] & 0x1F) {
		case h264.NALUTypeSPS:
			sps = append([]byte(nil), nalu...)
		case h264.NALUTypePPS:
			pps = append([]byte(nil), nalu...)
		}
	}
	return sps, pps
}

func scanFFmpegLog(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		log.Printf("[ffmpeg] %s", line)
	}
}

func sourceLabel(source string, test bool) string {
	if test {
		return "testsrc2"
	}
	return source
}

func buildArgs(cfg *config.Config, encoder, source string, test bool) ([]string, error) {
	v := cfg.Video
	m := cfg.MJPEG

	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-nostdin",
		"-re",
	}

	if test {
		args = append(args,
			"-f", "lavfi",
			"-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=%d", v.Width, v.Height, v.FPS),
		)
	} else {
		if v.Loop {
			args = append(args, "-stream_loop", "-1")
		}
		args = append(args, "-i", source)
	}

	h264Filter := fmt.Sprintf(
		"scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1,fps=%d,format=yuv420p",
		v.Width, v.Height, v.Width, v.Height, v.FPS,
	)
	mjpegWidth, mjpegHeight, mjpegFPS, mjpegQuality := m.Width, m.Height, m.FPS, m.Quality
	if !m.Enabled {
		// A low-rate JPEG stream is still needed for snapshots and the preview.
		mjpegWidth, mjpegHeight, mjpegFPS, mjpegQuality = 640, 360, 1, 7
	}
	mjpegFilter := fmt.Sprintf(
		"scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1,fps=%d",
		mjpegWidth, mjpegHeight, mjpegWidth, mjpegHeight, mjpegFPS,
	)

	args = append(args,
		"-map", "0:v:0",
		"-an", "-sn", "-dn",
		"-vf", h264Filter,
	)
	args = append(args, encoderArgs(encoder, v)...)
	args = append(args,
		"-f", "mpegts",
		"-muxdelay", "0",
		"-muxpreload", "0",
		"-flush_packets", "1",
		"pipe:1",
	)

	args = append(args,
		"-map", "0:v:0",
		"-an", "-sn", "-dn",
		"-vf", mjpegFilter,
		"-c:v", "mjpeg",
		"-q:v", fmt.Sprintf("%d", mjpegQuality),
		// RFC 2435 (RTP/JPEG) only carries quantization tables, so the
		// receiver decodes with the standard Huffman tables. Optimized
		// tables would corrupt every frame on VLC/ffmpeg clients.
		"-huffman", "default",
		"-force_duplicated_matrix", "1",
		"-pix_fmt", "yuvj420p",
		"-f", "mjpeg",
		"pipe:3",
	)

	return args, nil
}

func encoderArgs(encoder string, v config.VideoConfig) []string {
	bitrate := fmt.Sprintf("%dk", v.BitrateKbps)
	maxrate := bitrate
	bufsize := fmt.Sprintf("%dk", v.BitrateKbps*2)

	switch encoder {
	case "h264_videotoolbox":
		return []string{
			"-c:v", "h264_videotoolbox",
			"-realtime", "1",
			"-allow_sw", "1",
			"-profile:v", "main",
			"-pix_fmt", "yuv420p",
			"-bf", "0",
			"-g", fmt.Sprintf("%d", v.GOP),
			"-b:v", bitrate,
			"-maxrate", maxrate,
			"-bufsize", bufsize,
		}
	default:
		return []string{
			"-c:v", "libx264",
			"-preset", "veryfast",
			"-tune", "zerolatency",
			"-profile:v", "baseline",
			"-pix_fmt", "yuv420p",
			"-bf", "0",
			"-g", fmt.Sprintf("%d", v.GOP),
			"-keyint_min", fmt.Sprintf("%d", v.GOP),
			"-sc_threshold", "0",
			"-b:v", bitrate,
			"-maxrate", maxrate,
			"-bufsize", bufsize,
		}
	}
}

func chooseEncoder(requested string) string {
	if requested == "libx264" || requested == "videotoolbox" {
		if requested == "videotoolbox" {
			return "h264_videotoolbox"
		}
		return "libx264"
	}
	if testEncoder("h264_videotoolbox") {
		return "h264_videotoolbox"
	}
	return "libx264"
}

func testEncoder(name string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:s=64x64:r=1",
		"-frames:v", "1",
		"-c:v", name,
		"-f", "null", "-",
	)
	return cmd.Run() == nil
}

// FFmpegPath returns the resolved ffmpeg binary path.
func FFmpegPath() string {
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return ""
	}
	return path
}
