package rtsp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph264"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtpmjpeg"
	"github.com/bluenviron/gortsplib/v5/pkg/liberrors"

	"fakewebcamera/internal/config"
)

// Client describes one connected RTSP reader.
type Client struct {
	Remote    string    `json:"remote"`
	Path      string    `json:"path"`
	Transport string    `json:"transport"`
	Since     time.Time `json:"since"`
}

// Server is the RTSP endpoint that serves /stream1 (H.264) and /stream2 (MJPEG).
type Server struct {
	cfg *config.Config

	server *gortsplib.Server

	mu          sync.RWMutex
	writeMu     sync.Mutex
	h264Stream  *gortsplib.ServerStream
	h264Media   *description.Media
	h264Format  *format.H264
	h264Enc     *rtph264.Encoder
	h264Ready   bool
	mjpegStream *gortsplib.ServerStream
	mjpegMedia  *description.Media
	mjpegFormat *format.MJPEG
	mjpegEnc    *rtpmjpeg.Encoder
	mjpegReady  bool

	clients map[*gortsplib.ServerSession]Client

	h264TS  uint32
	mjpegTS uint32

	started time.Time
	ctx     context.Context
	cancel  context.CancelFunc
}

func New(cfg *config.Config) *Server {
	return &Server{cfg: cfg, clients: make(map[*gortsplib.ServerSession]Client)}
}

// Start binds the RTSP/RTP listeners and serves in the background.
func (s *Server) Start(parent context.Context) error {
	s.ctx, s.cancel = context.WithCancel(parent)
	s.started = time.Now()

	s.server = &gortsplib.Server{
		Handler:      s,
		RTSPAddress:  fmt.Sprintf(":%d", s.cfg.Server.RTSPPort),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	if s.cfg.Server.UDPRTPPort > 0 && s.cfg.Server.UDPRTCPPort > 0 {
		s.server.UDPRTPAddress = fmt.Sprintf(":%d", s.cfg.Server.UDPRTPPort)
		s.server.UDPRTCPAddress = fmt.Sprintf(":%d", s.cfg.Server.UDPRTCPPort)
	}

	if err := s.server.Start(); err != nil {
		return fmt.Errorf("start RTSP server on :%d: %w", s.cfg.Server.RTSPPort, err)
	}

	if err := s.ensureMJPEG(); err != nil {
		s.server.Close()
		return err
	}

	log.Printf("rtsp: listening on :%d (udp rtp :%d, rtcp :%d)",
		s.cfg.Server.RTSPPort, s.cfg.Server.UDPRTPPort, s.cfg.Server.UDPRTCPPort)

	go func() {
		err := s.server.Wait()
		if err != nil && s.ctx.Err() == nil {
			log.Printf("rtsp: server stopped: %v", err)
		}
	}()

	return nil
}

func (s *Server) Close() {
	if s.cancel != nil {
		s.cancel()
	}

	s.mu.Lock()
	h264 := s.h264Stream
	mjpeg := s.mjpegStream
	s.h264Stream, s.mjpegStream = nil, nil
	s.h264Ready, s.mjpegReady = false, false
	s.mu.Unlock()

	if h264 != nil {
		h264.Close()
	}
	if mjpeg != nil {
		mjpeg.Close()
	}
	if s.server != nil {
		s.server.Close()
	}
}

func (s *Server) ensureMJPEG() error {
	if !s.cfg.MJPEG.Enabled {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mjpegStream != nil {
		return nil
	}

	f := &format.MJPEG{}
	media := &description.Media{
		Type:    description.MediaTypeVideo,
		Formats: []format.Format{f},
	}
	stream := &gortsplib.ServerStream{
		Server: s.server,
		Desc:   &description.Session{Medias: []*description.Media{media}},
	}
	if err := stream.Initialize(); err != nil {
		return fmt.Errorf("initialize MJPEG stream: %w", err)
	}
	enc, err := f.CreateEncoder()
	if err != nil {
		stream.Close()
		return fmt.Errorf("create MJPEG encoder: %w", err)
	}

	s.mjpegStream = stream
	s.mjpegMedia = media
	s.mjpegFormat = f
	s.mjpegEnc = enc
	s.mjpegReady = true
	return nil
}

func (s *Server) ensureH264(sps, pps []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.h264Stream != nil {
		return nil
	}
	if len(sps) == 0 || len(pps) == 0 {
		return fmt.Errorf("missing SPS/PPS")
	}

	f := &format.H264{
		PayloadTyp:        96,
		PacketizationMode: 1,
		SPS:               append([]byte(nil), sps...),
		PPS:               append([]byte(nil), pps...),
	}
	media := &description.Media{
		Type:    description.MediaTypeVideo,
		Formats: []format.Format{f},
	}
	stream := &gortsplib.ServerStream{
		Server: s.server,
		Desc:   &description.Session{Medias: []*description.Media{media}},
	}
	if err := stream.Initialize(); err != nil {
		return fmt.Errorf("initialize H.264 stream: %w", err)
	}
	enc, err := f.CreateEncoder()
	if err != nil {
		stream.Close()
		return fmt.Errorf("create H.264 encoder: %w", err)
	}

	s.h264Stream = stream
	s.h264Media = media
	s.h264Format = f
	s.h264Enc = enc
	s.h264Ready = true
	log.Printf("rtsp: H.264 stream ready (SPS %d bytes, PPS %d bytes)", len(sps), len(pps))
	return nil
}

// WriteH264 packetizes and broadcasts one access unit.
func (s *Server) WriteH264(au [][]byte, sps, pps []byte) {
	if len(au) == 0 {
		return
	}

	s.mu.RLock()
	ready := s.h264Ready
	s.mu.RUnlock()
	if !ready {
		if err := s.ensureH264(sps, pps); err != nil {
			return
		}
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	s.mu.RLock()
	stream, media, enc := s.h264Stream, s.h264Media, s.h264Enc
	s.mu.RUnlock()
	if stream == nil || media == nil || enc == nil {
		return
	}

	packets, err := enc.Encode(au)
	if err != nil {
		log.Printf("rtsp: H.264 encode: %v", err)
		return
	}
	ts := s.nextTimestamp(&s.h264TS, s.cfg.Video.FPS)
	for _, pkt := range packets {
		pkt.Timestamp = ts
		if err := stream.WritePacketRTP(media, pkt); err != nil && !isClosed(err) {
			log.Printf("rtsp: write H.264: %v", err)
			return
		}
	}
}

// WriteMJPEG packetizes and broadcasts one JPEG frame.
func (s *Server) WriteMJPEG(jpeg []byte) {
	if !s.cfg.MJPEG.Enabled || len(jpeg) == 0 {
		return
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	s.mu.RLock()
	stream, media, enc := s.mjpegStream, s.mjpegMedia, s.mjpegEnc
	s.mu.RUnlock()
	if stream == nil || media == nil || enc == nil {
		return
	}

	packets, err := enc.Encode(jpeg)
	if err != nil {
		log.Printf("rtsp: MJPEG encode: %v", err)
		return
	}
	ts := s.nextTimestamp(&s.mjpegTS, s.cfg.MJPEG.FPS)
	for _, pkt := range packets {
		pkt.Timestamp = ts
		if err := stream.WritePacketRTP(media, pkt); err != nil && !isClosed(err) {
			log.Printf("rtsp: write MJPEG: %v", err)
			return
		}
	}
}

// nextTimestamp returns a wall-clock 90 kHz RTP timestamp and guarantees that
// it strictly increases. Hardware encoders occasionally emit two frames in a
// burst, which would otherwise produce duplicate PTS and make some NVRs drop
// frames. Called with writeMu held.
func (s *Server) nextTimestamp(last *uint32, fps int) uint32 {
	if fps <= 0 {
		fps = 25
	}
	step := uint32(90000 / fps)
	wall := uint32(time.Since(s.started).Seconds() * 90000)

	// Quantize to whole frame intervals. Clients derive DTS by converting the
	// RTP timestamp into the declared frame rate; millisecond-level scheduling
	// jitter would otherwise make two frames land in the same tick and produce
	// duplicate DTS (ffmpeg logs "non monotonically increasing dts").
	ts := (wall / step) * step
	if ts <= *last {
		ts = *last + step
	}
	*last = ts
	return ts
}

func isClosed(err error) bool {
	return errors.Is(err, liberrors.ErrServerStreamClosed{})
}

// Clients returns a snapshot of connected readers.
func (s *Server) Clients() []Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Client, 0, len(s.clients))
	for _, c := range s.clients {
		out = append(out, c)
	}
	return out
}

func (s *Server) streamFor(path string) (*gortsplib.ServerStream, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	switch strings.TrimSuffix(path, "/") {
	case "", "/", "/stream1", "/h264", "/live":
		return s.h264Stream, s.h264Ready
	case "/stream2", "/mjpeg":
		return s.mjpegStream, s.mjpegReady
	default:
		return nil, false
	}
}

func (s *Server) authorize(conn *gortsplib.ServerConn, req *base.Request) bool {
	if !s.cfg.Auth.Enabled {
		return true
	}
	return conn.VerifyCredentials(req, s.cfg.Auth.Username, s.cfg.Auth.Password)
}

func (s *Server) unauthorized() (*base.Response, *gortsplib.ServerStream, error) {
	return &base.Response{StatusCode: base.StatusUnauthorized}, nil, liberrors.ErrServerAuth{}
}

// OnConnOpen implements gortsplib.ServerHandlerOnConnOpen.
func (s *Server) OnConnOpen(ctx *gortsplib.ServerHandlerOnConnOpenCtx) {
	log.Printf("rtsp: connection from %s", ctx.Conn.NetConn().RemoteAddr())
}

// OnConnClose implements gortsplib.ServerHandlerOnConnClose.
func (s *Server) OnConnClose(ctx *gortsplib.ServerHandlerOnConnCloseCtx) {
	if ctx.Error != nil && s.ctx != nil && s.ctx.Err() == nil {
		log.Printf("rtsp: connection closed: %v", ctx.Error)
	}
}

// OnSessionOpen implements gortsplib.ServerHandlerOnSessionOpen.
func (s *Server) OnSessionOpen(ctx *gortsplib.ServerHandlerOnSessionOpenCtx) {
	client := Client{
		Remote: ctx.Conn.NetConn().RemoteAddr().String(),
		Since:  time.Now(),
	}
	if tr := ctx.Session.Transport(); tr != nil {
		client.Transport = tr.Protocol.String()
	}
	s.mu.Lock()
	s.clients[ctx.Session] = client
	s.mu.Unlock()
	log.Printf("rtsp: session opened from %s", client.Remote)
}

// OnSessionClose implements gortsplib.ServerHandlerOnSessionClose.
func (s *Server) OnSessionClose(ctx *gortsplib.ServerHandlerOnSessionCloseCtx) {
	s.mu.Lock()
	delete(s.clients, ctx.Session)
	s.mu.Unlock()
}

// OnDescribe implements gortsplib.ServerHandlerOnDescribe.
func (s *Server) OnDescribe(ctx *gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	if !s.authorize(ctx.Conn, ctx.Request) {
		log.Printf("rtsp: DESCRIBE unauthorized from %s", ctx.Conn.NetConn().RemoteAddr())
		return s.unauthorized()
	}

	stream, ready := s.streamFor(ctx.Path)
	if !ready || stream == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}

	log.Printf("rtsp: DESCRIBE %s from %s", ctx.Path, ctx.Conn.NetConn().RemoteAddr())
	return &base.Response{StatusCode: base.StatusOK}, stream, nil
}

// OnSetup implements gortsplib.ServerHandlerOnSetup.
func (s *Server) OnSetup(ctx *gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	if !s.authorize(ctx.Conn, ctx.Request) {
		log.Printf("rtsp: SETUP unauthorized from %s", ctx.Conn.NetConn().RemoteAddr())
		return s.unauthorized()
	}

	stream, ready := s.streamFor(ctx.Path)
	if !ready || stream == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}
	return &base.Response{StatusCode: base.StatusOK}, stream, nil
}

// OnPlay implements gortsplib.ServerHandlerOnPlay.
func (s *Server) OnPlay(ctx *gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	s.mu.Lock()
	if c, ok := s.clients[ctx.Session]; ok {
		c.Path = ctx.Path
		if tr := ctx.Session.Transport(); tr != nil {
			c.Transport = tr.Protocol.String()
		}
		s.clients[ctx.Session] = c
	}
	s.mu.Unlock()
	log.Printf("rtsp: PLAY %s from %s", ctx.Path, ctx.Conn.NetConn().RemoteAddr())
	return &base.Response{StatusCode: base.StatusOK}, nil
}
