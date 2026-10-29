package onvif

import (
	"fmt"
	"net/http"
	"strings"
)

func (s *Server) media2ProfileXML(p Profile) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<tr2:Profiles token="%s" fixed="true">`, p.Token)
	fmt.Fprintf(&b, `<tt:Name>%s</tt:Name>`, xmlEscape(p.Name))
	b.WriteString(`<tt:Configurations>`)
	fmt.Fprintf(&b, `<tt:VideoSource><tt:SourceToken>%s</tt:SourceToken><tt:Name>VideoSource</tt:Name><tt:UseCount>%d</tt:UseCount></tt:VideoSource>`,
		p.SourceToken, len(s.profiles()))
	fmt.Fprintf(&b, `<tt:VideoEncoder><tt:Token>%s</tt:Token><tt:Name>%s</tt:Name><tt:UseCount>1</tt:UseCount></tt:VideoEncoder>`,
		p.VEC, xmlEscape(p.Encoding))
	b.WriteString(`</tt:Configurations>`)
	fmt.Fprintf(&b, `<tt:VideoSourceConfiguration token="%s"><tt:Name>VideoSource</tt:Name><tt:UseCount>%d</tt:UseCount><tt:SourceToken>%s</tt:SourceToken><tt:Bounds x="0" y="0" width="%d" height="%d"/></tt:VideoSourceConfiguration>`,
		p.VSC, len(s.profiles()), p.SourceToken, s.cfg.Video.Width, s.cfg.Video.Height)
	b.WriteString(encoderConfigXML(p, s.cfg.Video.GOP))
	b.WriteString(`</tr2:Profiles>`)
	return b.String()
}

func (s *Server) getProfiles2(r *http.Request) string {
	var b strings.Builder
	b.WriteString(`<tr2:GetProfilesResponse>`)
	for _, p := range s.profiles() {
		b.WriteString(s.media2ProfileXML(p))
	}
	b.WriteString(`</tr2:GetProfilesResponse>`)
	return b.String()
}

func (s *Server) getVideoSources2(r *http.Request) string {
	v := s.cfg.Video
	return fmt.Sprintf(`<tr2:GetVideoSourcesResponse><tr2:VideoSources token="vs_main"><tt:Framerate>%d</tt:Framerate><tt:Resolution><tt:Width>%d</tt:Width><tt:Height>%d</tt:Height></tt:Resolution></tr2:VideoSources></tr2:GetVideoSourcesResponse>`,
		v.FPS, v.Width, v.Height)
}

func (s *Server) getVideoSourceConfigurations2(r *http.Request) string {
	v := s.cfg.Video
	return fmt.Sprintf(`<tr2:GetVideoSourceConfigurationsResponse><tr2:Configurations token="vsc_main"><tt:Name>VideoSource</tt:Name><tt:UseCount>%d</tt:UseCount><tt:SourceToken>vs_main</tt:SourceToken><tt:Bounds x="0" y="0" width="%d" height="%d"/></tr2:Configurations></tr2:GetVideoSourceConfigurationsResponse>`,
		len(s.profiles()), v.Width, v.Height)
}

func (s *Server) getVideoEncoderConfigurations2(r *http.Request) string {
	var b strings.Builder
	b.WriteString(`<tr2:GetVideoEncoderConfigurationsResponse>`)
	for _, p := range s.profiles() {
		b.WriteString(rewriteEncoderConfig(encoderConfigXML(p, s.cfg.Video.GOP), `<tr2:Configurations`, `</tr2:Configurations>`))
	}
	b.WriteString(`</tr2:GetVideoEncoderConfigurationsResponse>`)
	return b.String()
}

func (s *Server) getVideoEncoderConfigurationOptions2(inner []byte) string {
	p, err := s.profileFromInner(inner)
	if err != nil {
		p = s.profiles()[0]
	}
	return `<tr2:GetVideoEncoderConfigurationOptionsResponse><tr2:Options>` + encoderOptionsXML(p) + `</tr2:Options></tr2:GetVideoEncoderConfigurationOptionsResponse>`
}

func (s *Server) getStreamURI2(req *parsedRequest) (string, error) {
	p, err := s.profileFromInner(req.Inner)
	if err != nil {
		return "", err
	}
	return s.mediaURIXML("tr2", "GetStreamUri", s.rtspURL(req.Raw, p.Path)), nil
}

func (s *Server) getSnapshotURI2(req *parsedRequest) (string, error) {
	if _, err := s.profileFromInner(req.Inner); err != nil {
		return "", err
	}
	return s.mediaURIXML("tr2", "GetSnapshotUri", s.snapshotURL(req.Raw)), nil
}
