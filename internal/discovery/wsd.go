package discovery

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/ipv4"

	"fakewebcamera/internal/config"
	"fakewebcamera/internal/netutil"
)

const (
	multicastAddress = "239.255.255.250:3702"
	discoveryNS      = "http://schemas.xmlsoap.org/ws/2005/04/discovery"
	addressingNS     = "http://schemas.xmlsoap.org/ws/2004/08/addressing"
	networkNS        = "http://www.onvif.org/ver10/network/wsdl"
)

var multicastAddr = &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 3702}

// Server answers WS-Discovery probes and announces the camera on the LAN.
type Server struct {
	cfg   *config.Config
	ifi   *net.Interface
	ip    net.IP
	conn  net.PacketConn
	pconn *ipv4.PacketConn

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	errMu   sync.Mutex
	lastErr string
}

func New(cfg *config.Config, ifi *net.Interface, ip net.IP) *Server {
	return &Server{cfg: cfg, ifi: ifi, ip: ip}
}

func (s *Server) Start(parent context.Context) error {
	conn, err := net.ListenPacket("udp4", ":3702")
	if err != nil {
		return fmt.Errorf("listen WS-Discovery on :3702: %w", err)
	}

	pconn := ipv4.NewPacketConn(conn)
	_ = pconn.SetControlMessage(ipv4.FlagInterface|ipv4.FlagDst, true)
	_ = pconn.SetMulticastTTL(1)
	_ = pconn.SetMulticastLoopback(true)

	if s.ifi != nil {
		if err := pconn.JoinGroup(s.ifi, multicastAddr); err != nil {
			log.Printf("discovery: join multicast on %s: %v", s.ifi.Name, err)
		}
	} else {
		ifaces, _ := net.Interfaces()
		for i := range ifaces {
			if ifaces[i].Flags&net.FlagUp == 0 {
				continue
			}
			_ = pconn.JoinGroup(&ifaces[i], multicastAddr)
		}
	}

	s.conn = conn
	s.pconn = pconn
	s.ctx, s.cancel = context.WithCancel(parent)
	s.done = make(chan struct{})

	go s.readLoop()
	go s.announce()

	log.Printf("discovery: WS-Discovery listening on :3702 (interface %s)", interfaceName(s.ifi))
	return nil
}

func (s *Server) Close() {
	if s.cancel != nil {
		s.sendBye()
		s.cancel()
	}
	if s.conn != nil {
		_ = s.conn.Close()
	}
	if s.done != nil {
		select {
		case <-s.done:
		case <-time.After(time.Second):
		}
	}
}

func (s *Server) readLoop() {
	defer close(s.done)
	buf := make([]byte, 64<<10)
	for {
		n, cm, src, err := s.pconn.ReadFrom(buf)
		if err != nil {
			if s.ctx != nil && s.ctx.Err() != nil {
				return
			}
			log.Printf("discovery: read: %v", err)
			continue
		}
		data := make([]byte, n)
		copy(data, buf[:n])
		go s.handle(data, cm, src)
	}
}

type probeEnvelope struct {
	Header struct {
		MessageID string `xml:"MessageID"`
		Action    string `xml:"Action"`
	} `xml:"Header"`
	Body struct {
		Probe struct {
			Types  string `xml:"Types"`
			Scopes string `xml:"Scopes"`
		} `xml:"Probe"`
		Resolve struct {
			EndpointReference struct {
				Address string `xml:"Address"`
			} `xml:"EndpointReference"`
		} `xml:"Resolve"`
	} `xml:"Body"`
}

func (s *Server) handle(data []byte, cm *ipv4.ControlMessage, src net.Addr) {
	var env probeEnvelope
	if err := xml.Unmarshal(data, &env); err != nil {
		return
	}

	ip := s.ip
	if cm != nil && cm.IfIndex > 0 {
		if candidate := netutil.IPv4ByIndex(cm.IfIndex); candidate != nil {
			ip = candidate
		}
	}
	if ip == nil {
		return
	}

	switch {
	case env.Body.Probe.Types != "" || env.Body.Probe.Scopes != "" || bytes.Contains(data, []byte("Probe")):
		if !s.probeMatches(env.Body.Probe.Types, env.Body.Probe.Scopes) {
			return
		}
		s.sendProbeMatches(ip, src, env.Header.MessageID, cm)
	case env.Body.Resolve.EndpointReference.Address != "":
		if !strings.Contains(strings.ToLower(env.Body.Resolve.EndpointReference.Address), strings.ToLower(s.cfg.Device.UUID)) {
			return
		}
		s.sendResolveMatches(ip, src, env.Header.MessageID, cm)
	}
}

func (s *Server) probeMatches(types, scopes string) bool {
	types = strings.ToLower(strings.TrimSpace(types))
	scopes = strings.ToLower(strings.TrimSpace(scopes))

	if types == "" && scopes == "" {
		return true
	}

	if types != "" {
		matched := false
		for _, token := range strings.Fields(types) {
			token = strings.TrimSuffix(strings.TrimSpace(token), ":")
			switch {
			case strings.Contains(token, "networkvideotransmitter"),
				strings.Contains(token, "videoencoder"),
				strings.HasPrefix(token, "tds:device"),
				strings.HasPrefix(token, "dn:device"),
				strings.Contains(token, "onvif"):
				matched = true
			}
			if matched {
				break
			}
		}
		if !matched {
			return false
		}
	}

	if scopes != "" && !strings.Contains(scopes, "onvif") {
		return false
	}
	return true
}

func (s *Server) sendProbeMatches(ip net.IP, dst net.Addr, relatesTo string, cm *ipv4.ControlMessage) {
	body := fmt.Sprintf(`<d:ProbeMatches><d:ProbeMatch>%s<d:Types>dn:NetworkVideoTransmitter</d:Types><d:Scopes>%s</d:Scopes><d:XAddrs>%s</d:XAddrs><d:MetadataVersion>1</d:MetadataVersion></d:ProbeMatch></d:ProbeMatches>`,
		s.endpointReference(), xmlEscape(scopeList(s.cfg)), xmlEscape(s.xaddr(ip)))
	msg := s.envelope("http://schemas.xmlsoap.org/ws/2005/04/discovery/ProbeMatches", body, relatesTo)
	if err := s.send(msg, dst, cm); err != nil {
		s.setError(err)
		log.Printf("discovery: probe match: %v", err)
	}
}

func (s *Server) sendResolveMatches(ip net.IP, dst net.Addr, relatesTo string, cm *ipv4.ControlMessage) {
	body := fmt.Sprintf(`<d:ResolveMatches><d:ResolveMatch>%s<d:Types>dn:NetworkVideoTransmitter</d:Types><d:Scopes>%s</d:Scopes><d:XAddrs>%s</d:XAddrs><d:MetadataVersion>1</d:MetadataVersion></d:ResolveMatch></d:ResolveMatches>`,
		s.endpointReference(), xmlEscape(scopeList(s.cfg)), xmlEscape(s.xaddr(ip)))
	msg := s.envelope("http://schemas.xmlsoap.org/ws/2005/04/discovery/ResolveMatches", body, relatesTo)
	if err := s.send(msg, dst, cm); err != nil {
		s.setError(err)
		log.Printf("discovery: resolve match: %v", err)
	}
}

// announce sends Hello a few times so a first-run Local Network permission
// prompt (which makes the first multicast send fail) does not hide the camera.
func (s *Server) announce() {
	for _, delay := range []time.Duration{500 * time.Millisecond, 3 * time.Second, 10 * time.Second} {
		select {
		case <-time.After(delay):
		case <-s.ctx.Done():
			return
		}
		if err := s.sendHello(); err == nil {
			s.setError(nil)
			return
		} else {
			s.setError(err)
			log.Printf("discovery: hello: %v (if a VPN is active, exclude 239.255.255.250 from its tunnel)", err)
		}
	}
}

func (s *Server) sendHello() error {
	body := fmt.Sprintf(`<d:Hello>%s<d:Types>dn:NetworkVideoTransmitter</d:Types><d:Scopes>%s</d:Scopes><d:XAddrs>%s</d:XAddrs><d:MetadataVersion>1</d:MetadataVersion></d:Hello>`,
		s.endpointReference(), xmlEscape(scopeList(s.cfg)), xmlEscape(s.xaddr(s.ip)))
	msg := s.envelope("http://schemas.xmlsoap.org/ws/2005/04/discovery/Hello", body, "")
	return s.send(msg, multicastAddr, nil)
}

func (s *Server) sendBye() {
	body := fmt.Sprintf(`<d:Bye>%s</d:Bye>`, s.endpointReference())
	msg := s.envelope("http://schemas.xmlsoap.org/ws/2005/04/discovery/Bye", body, "")
	if err := s.send(msg, multicastAddr, nil); err != nil {
		s.setError(err)
		log.Printf("discovery: bye: %v", err)
	}
}

func (s *Server) envelope(action, body, relatesTo string) []byte {
	var header strings.Builder
	fmt.Fprintf(&header, `<a:MessageID>urn:uuid:%s</a:MessageID>`, uuid.NewString())
	if relatesTo != "" {
		fmt.Fprintf(&header, `<a:RelatesTo>%s</a:RelatesTo>`, xmlEscape(relatesTo))
	}
	fmt.Fprintf(&header, `<a:To>http://schemas.xmlsoap.org/ws/2004/08/addressing/role/anonymous</a:To><a:Action>%s</a:Action>`, action)

	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:d="%s" xmlns:a="%s" xmlns:dn="%s">
<s:Header>%s</s:Header>
<s:Body>%s</s:Body>
</s:Envelope>`, discoveryNS, addressingNS, networkNS, header.String(), body))
}

// LastError reports the most recent send failure, if any. Discovery sockets can
// bind successfully while a VPN-owned multicast route still blocks Hellos.
func (s *Server) LastError() string {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.lastErr
}

func (s *Server) setError(err error) {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	if err == nil {
		s.lastErr = ""
		return
	}
	s.lastErr = err.Error()
}

func (s *Server) send(msg []byte, dst net.Addr, cm *ipv4.ControlMessage) error {
	if s.pconn == nil {
		return fmt.Errorf("socket not open")
	}
	control := cm
	if control == nil && s.ifi != nil {
		control = &ipv4.ControlMessage{IfIndex: s.ifi.Index}
	}
	_, err := s.pconn.WriteTo(msg, control, dst)
	return err
}

func (s *Server) endpointReference() string {
	return fmt.Sprintf(`<a:EndpointReference><a:Address>urn:uuid:%s</a:Address></a:EndpointReference>`, xmlEscape(s.cfg.Device.UUID))
}

func (s *Server) xaddr(ip net.IP) string {
	return fmt.Sprintf("http://%s:%d/onvif/device_service", ip.String(), s.cfg.Server.HTTPPort)
}

func scopeList(cfg *config.Config) string {
	return strings.Join([]string{
		"onvif://www.onvif.org/type/video_encoder",
		"onvif://www.onvif.org/type/NetworkVideoTransmitter",
		"onvif://www.onvif.org/name/" + scopeValue(cfg.Device.Name),
		"onvif://www.onvif.org/hardware/" + scopeValue(cfg.Device.HardwareID),
		"onvif://www.onvif.org/location/" + scopeValue(cfg.Device.Location),
		"onvif://www.onvif.org/Profile/Streaming",
	}, " ")
}

func scopeValue(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "fakecam"
	}
	return strings.NewReplacer(" ", "_", "/", "_", "\\", "_").Replace(s)
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func interfaceName(ifi *net.Interface) string {
	if ifi == nil {
		return "all"
	}
	return ifi.Name
}
