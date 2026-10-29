package onvif

import (
	"encoding/xml"
	"fmt"
	"net"
	"net/http"
	"strings"

	"fakewebcamera/internal/config"
)

const (
	nsSOAP   = "http://www.w3.org/2003/05/soap-envelope"
	nsDevice = "http://www.onvif.org/ver10/device/wsdl"
	nsMedia  = "http://www.onvif.org/ver10/media/wsdl"
	nsMedia2 = "http://www.onvif.org/ver20/media/wsdl"
	nsEvents = "http://www.onvif.org/ver10/events/wsdl"
	nsSchema = "http://www.onvif.org/ver10/schema"
	nsTopics = "http://www.onvif.org/ver10/topics"
	nsAddr   = "http://www.w3.org/2005/08/addressing"
	nsWSNT   = "http://docs.oasis-open.org/wsn/b-2"
	nsWSTOP  = "http://docs.oasis-open.org/wsn/t-1"
	nsError  = "http://www.onvif.org/ver10/error"
	nsTDS    = "http://www.onvif.org/ver10/device/wsdl"
)

const envelopePrefix = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="` + nsSOAP + `" xmlns:tds="` + nsDevice + `" xmlns:tt="` + nsSchema + `" xmlns:trt="` + nsMedia + `" xmlns:tr2="` + nsMedia2 + `" xmlns:tev="` + nsEvents + `" xmlns:tns1="` + nsTopics + `" xmlns:wsa="` + nsAddr + `" xmlns:wsnt="` + nsWSNT + `" xmlns:wstop="` + nsWSTOP + `" xmlns:ter="` + nsError + `">
<s:Header/>
<s:Body>`

const envelopeSuffix = `</s:Body>
</s:Envelope>`

func soapEnvelope(body string) string {
	return envelopePrefix + body + envelopeSuffix
}

func soapFault(code, subcode, reason string) string {
	body := fmt.Sprintf(`<s:Fault><s:Code><s:Value>s:%s</s:Value><s:Subcode><s:Value>ter:%s</s:Value></s:Subcode></s:Code><s:Reason><s:Text xml:lang="en">%s</s:Text></s:Reason></s:Fault>`,
		code, subcode, xmlEscape(reason))
	return soapEnvelope(body)
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// Profile describes one ONVIF media profile.
type Profile struct {
	Token       string
	Name        string
	Width       int
	Height      int
	FPS         int
	Bitrate     int
	Encoding    string
	Path        string
	SourceToken string
	VSC         string
	VEC         string
}

func (s *Server) profiles() []Profile {
	v := s.cfg.Video
	out := []Profile{{
		Token:       "profile_h264",
		Name:        fmt.Sprintf("H264_%dP", v.Height),
		Width:       v.Width,
		Height:      v.Height,
		FPS:         v.FPS,
		Bitrate:     v.BitrateKbps,
		Encoding:    "H264",
		Path:        "/stream1",
		SourceToken: "vs_main",
		VSC:         "vsc_main",
		VEC:         "vec_h264",
	}}
	if s.cfg.MJPEG.Enabled {
		m := s.cfg.MJPEG
		out = append(out, Profile{
			Token:       "profile_mjpeg",
			Name:        fmt.Sprintf("MJPEG_%dP", m.Height),
			Width:       m.Width,
			Height:      m.Height,
			FPS:         m.FPS,
			Bitrate:     0,
			Encoding:    "JPEG",
			Path:        "/stream2",
			SourceToken: "vs_main",
			VSC:         "vsc_main",
			VEC:         "vec_mjpeg",
		})
	}
	return out
}

func (s *Server) profileByToken(token string) (Profile, bool) {
	profiles := s.profiles()
	if token == "" {
		return profiles[0], true
	}
	for _, p := range profiles {
		if p.Token == token {
			return p, true
		}
	}
	return Profile{}, false
}

// host returns the host the client used, so advertised URLs stay reachable.
func (s *Server) host(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "" || host == "localhost" || host == "127.0.0.1" || host == "::1" {
		if s.primaryIP != nil {
			return s.primaryIP.String()
		}
	}
	return host
}

func (s *Server) baseURL(r *http.Request) string {
	return fmt.Sprintf("http://%s:%d", s.host(r), s.cfg.Server.HTTPPort)
}

func (s *Server) rtspURL(r *http.Request, path string) string {
	return fmt.Sprintf("rtsp://%s:%d%s", s.host(r), s.cfg.Server.RTSPPort, path)
}

func (s *Server) snapshotURL(r *http.Request) string {
	return fmt.Sprintf("http://%s:%d/snapshot.jpg", s.host(r), s.cfg.Server.HTTPPort)
}

func scopeList(cfg *config.Config) string {
	return strings.Join([]string{
		"onvif://www.onvif.org/type/video_encoder",
		"onvif://www.onvif.org/type/NetworkVideoTransmitter",
		"onvif://www.onvif.org/name/" + sanitizeScope(cfg.Device.Name),
		"onvif://www.onvif.org/hardware/" + sanitizeScope(cfg.Device.HardwareID),
		"onvif://www.onvif.org/location/" + sanitizeScope(cfg.Device.Location),
		"onvif://www.onvif.org/Profile/Streaming",
	}, " ")
}

func sanitizeScope(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "fakecam"
	}
	replacer := strings.NewReplacer(" ", "_", "/", "_", "\\", "_", "\n", "_", "\r", "_")
	return replacer.Replace(s)
}
