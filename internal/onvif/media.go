package onvif

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
)

func (s *Server) profileXML(p Profile) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<trt:Profiles token="%s" fixed="true">`, p.Token)
	fmt.Fprintf(&b, `<tt:Name>%s</tt:Name>`, xmlEscape(p.Name))
	fmt.Fprintf(&b, `<tt:VideoSourceConfiguration token="%s"><tt:Name>VideoSource</tt:Name><tt:UseCount>%d</tt:UseCount><tt:SourceToken>%s</tt:SourceToken><tt:Bounds x="0" y="0" width="%d" height="%d"/></tt:VideoSourceConfiguration>`,
		p.VSC, len(s.profiles()), p.SourceToken, s.cfg.Video.Width, s.cfg.Video.Height)
	b.WriteString(encoderConfigXML(p, s.cfg.Video.GOP))
	b.WriteString(`</trt:Profiles>`)
	return b.String()
}

func encoderConfigXML(p Profile, gop int) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<tt:VideoEncoderConfiguration token="%s"><tt:Name>%s</tt:Name><tt:UseCount>1</tt:UseCount><tt:Encoding>%s</tt:Encoding>`,
		p.VEC, xmlEscape(p.Encoding), p.Encoding)
	fmt.Fprintf(&b, `<tt:Resolution><tt:Width>%d</tt:Width><tt:Height>%d</tt:Height></tt:Resolution><tt:Quality>5</tt:Quality>`, p.Width, p.Height)
	fmt.Fprintf(&b, `<tt:RateControl><tt:FrameRateLimit>%d</tt:FrameRateLimit><tt:EncodingInterval>1</tt:EncodingInterval><tt:BitrateLimit>%d</tt:BitrateLimit></tt:RateControl>`,
		p.FPS, p.Bitrate)
	if p.Encoding == "H264" {
		fmt.Fprintf(&b, `<tt:H264><tt:GovLength>%d</tt:GovLength><tt:H264Profile>Baseline</tt:H264Profile></tt:H264>`, gop)
	}
	b.WriteString(`<tt:Multicast><tt:Address><tt:Type>IPv4</tt:Type><tt:IPv4Address>0.0.0.0</tt:IPv4Address></tt:Address><tt:Port>0</tt:Port><tt:TTL>1</tt:TTL><tt:AutoStart>false</tt:AutoStart></tt:Multicast><tt:SessionTimeout>PT0S</tt:SessionTimeout></tt:VideoEncoderConfiguration>`)
	return b.String()
}

func (s *Server) getProfiles(r *http.Request) string {
	var b strings.Builder
	b.WriteString(`<trt:GetProfilesResponse>`)
	for _, p := range s.profiles() {
		b.WriteString(s.profileXML(p))
	}
	b.WriteString(`</trt:GetProfilesResponse>`)
	return b.String()
}

func (s *Server) getVideoSources(r *http.Request) string {
	v := s.cfg.Video
	return fmt.Sprintf(`<trt:GetVideoSourcesResponse><trt:VideoSources token="vs_main"><tt:Framerate>%d</tt:Framerate><tt:Resolution><tt:Width>%d</tt:Width><tt:Height>%d</tt:Height></tt:Resolution></trt:VideoSources></trt:GetVideoSourcesResponse>`,
		v.FPS, v.Width, v.Height)
}

func (s *Server) getVideoSourceConfigurations(r *http.Request) string {
	v := s.cfg.Video
	return fmt.Sprintf(`<trt:GetVideoSourceConfigurationsResponse><trt:Configurations token="vsc_main"><tt:Name>VideoSource</tt:Name><tt:UseCount>%d</tt:UseCount><tt:SourceToken>vs_main</tt:SourceToken><tt:Bounds x="0" y="0" width="%d" height="%d"/></trt:Configurations></trt:GetVideoSourceConfigurationsResponse>`,
		len(s.profiles()), v.Width, v.Height)
}

func (s *Server) getVideoEncoderConfigurations(r *http.Request) string {
	var b strings.Builder
	b.WriteString(`<trt:GetVideoEncoderConfigurationsResponse>`)
	for _, p := range s.profiles() {
		b.WriteString(rewriteEncoderConfig(encoderConfigXML(p, s.cfg.Video.GOP), `<trt:Configurations`, `</trt:Configurations>`))
	}
	b.WriteString(`</trt:GetVideoEncoderConfigurationsResponse>`)
	return b.String()
}

func (s *Server) getVideoEncoderConfiguration(inner []byte) string {
	var op struct {
		ConfigurationToken string `xml:"ConfigurationToken"`
	}
	_ = xml.Unmarshal(inner, &op)

	p := s.profiles()[0]
	for _, candidate := range s.profiles() {
		if candidate.VEC == op.ConfigurationToken {
			p = candidate
			break
		}
	}

	config := rewriteEncoderConfig(encoderConfigXML(p, s.cfg.Video.GOP), `<trt:Configuration`, `</trt:Configuration>`)
	return `<trt:GetVideoEncoderConfigurationResponse>` + config + `</trt:GetVideoEncoderConfigurationResponse>`
}

func (s *Server) getVideoEncoderConfigurationOptions(inner []byte) string {
	var op struct {
		ConfigurationToken string `xml:"ConfigurationToken"`
		ProfileToken       string `xml:"ProfileToken"`
	}
	_ = xml.Unmarshal(inner, &op)

	p := s.profiles()[0]
	if op.ProfileToken != "" {
		if found, ok := s.profileByToken(op.ProfileToken); ok {
			p = found
		}
	}
	if op.ConfigurationToken == "vec_mjpeg" {
		for _, candidate := range s.profiles() {
			if candidate.VEC == "vec_mjpeg" {
				p = candidate
			}
		}
	}

	return `<trt:GetVideoEncoderConfigurationOptionsResponse><trt:Options>` + encoderOptionsXML(p) + `</trt:Options></trt:GetVideoEncoderConfigurationOptionsResponse>`
}

func encoderOptionsXML(p Profile) string {
	quality := `<tt:QualityRange><tt:Min>1</tt:Min><tt:Max>100</tt:Max></tt:QualityRange>`
	resolutions := fmt.Sprintf(`<tt:ResolutionsAvailable><tt:Width>%d</tt:Width><tt:Height>%d</tt:Height></tt:ResolutionsAvailable>`, p.Width, p.Height)

	if p.Encoding == "H264" {
		return quality + `<tt:H264>` + resolutions +
			`<tt:GovLengthRange><tt:Min>1</tt:Min><tt:Max>300</tt:Max></tt:GovLengthRange>` +
			fmt.Sprintf(`<tt:FrameRateRange><tt:Min>1</tt:Min><tt:Max>%d</tt:Max></tt:FrameRateRange>`, maxInt(p.FPS, 30)) +
			`<tt:EncodingIntervalRange><tt:Min>1</tt:Min><tt:Max>1</tt:Max></tt:EncodingIntervalRange>` +
			`<tt:H264ProfilesSupported>Baseline</tt:H264ProfilesSupported><tt:H264ProfilesSupported>Main</tt:H264ProfilesSupported><tt:H264ProfilesSupported>High</tt:H264ProfilesSupported>` +
			`</tt:H264>`
	}

	return quality + `<tt:JPEG>` + resolutions +
		fmt.Sprintf(`<tt:FrameRateRange><tt:Min>1</tt:Min><tt:Max>%d</tt:Max></tt:FrameRateRange>`, maxInt(p.FPS, 30)) +
		`<tt:EncodingIntervalRange><tt:Min>1</tt:Min><tt:Max>1</tt:Max></tt:EncodingIntervalRange>` +
		`</tt:JPEG>`
}

func (s *Server) getStreamURI(req *parsedRequest) (string, error) {
	p, err := s.profileFromInner(req.Inner)
	if err != nil {
		return "", err
	}
	return s.mediaURIXML("trt", "GetStreamUri", s.rtspURL(req.Raw, p.Path)), nil
}

func (s *Server) getSnapshotURI(req *parsedRequest) (string, error) {
	if _, err := s.profileFromInner(req.Inner); err != nil {
		return "", err
	}
	return s.mediaURIXML("trt", "GetSnapshotUri", s.snapshotURL(req.Raw)), nil
}

func (s *Server) mediaURIXML(prefix, action, uri string) string {
	return fmt.Sprintf(`<%s:%sResponse><%s:MediaUri><tt:Uri>%s</tt:Uri><tt:InvalidAfterConnect>false</tt:InvalidAfterConnect><tt:InvalidAfterReboot>false</tt:InvalidAfterReboot><tt:Timeout>PT0S</tt:Timeout></%s:MediaUri></%s:%sResponse>`,
		prefix, action, prefix, xmlEscape(uri), prefix, prefix, action)
}

func (s *Server) profileFromInner(inner []byte) (Profile, error) {
	var op struct {
		ProfileToken string `xml:"ProfileToken"`
	}
	_ = xml.Unmarshal(inner, &op)
	p, ok := s.profileByToken(op.ProfileToken)
	if !ok {
		return Profile{}, fmt.Errorf("unknown profile token %q", op.ProfileToken)
	}
	return p, nil
}

func rewriteEncoderConfig(config, openTag, closeTag string) string {
	config = strings.Replace(config, `<tt:VideoEncoderConfiguration`, openTag, 1)
	config = strings.Replace(config, `</tt:VideoEncoderConfiguration>`, closeTag, 1)
	return config
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
