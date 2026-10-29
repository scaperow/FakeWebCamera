package onvif

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fakewebcamera/internal/config"
)

func testServer() *Server {
	cfg := config.Default()
	cfg.Auth.Enabled = true
	cfg.Auth.Username = "admin"
	cfg.Auth.Password = "admin123"
	cfg.Auth.ClockSkewMinutes = 1
	return &Server{cfg: cfg}
}

func digestToken(t *testing.T, user, password, created string) *usernameToken {
	t.Helper()
	nonce := []byte("0123456789abcdef")
	h := sha1.New()
	h.Write(nonce)
	h.Write([]byte(created))
	h.Write([]byte(password))
	return &usernameToken{
		Username:     user,
		Password:     base64.StdEncoding.EncodeToString(h.Sum(nil)),
		PasswordType: "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest",
		Nonce:        base64.StdEncoding.EncodeToString(nonce),
		Created:      created,
	}
}

func TestVerifyUsernameToken(t *testing.T) {
	s := testServer()
	created := time.Now().UTC().Format(time.RFC3339Nano)

	if !s.verifyUsernameToken(digestToken(t, "admin", "admin123", created)) {
		t.Fatal("valid digest rejected")
	}
	if s.verifyUsernameToken(digestToken(t, "admin", "wrong", created)) {
		t.Fatal("wrong password accepted")
	}
	if s.verifyUsernameToken(digestToken(t, "root", "admin123", created)) {
		t.Fatal("wrong username accepted")
	}
}

func TestVerifyUsernameTokenRejectsStaleCreated(t *testing.T) {
	s := testServer()
	created := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339Nano)
	if s.verifyUsernameToken(digestToken(t, "admin", "admin123", created)) {
		t.Fatal("stale Created accepted")
	}
}

func TestVerifyUsernameTokenPasswordText(t *testing.T) {
	s := testServer()
	ut := &usernameToken{
		Username:     "admin",
		Password:     "admin123",
		PasswordType: "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordText",
	}
	if !s.verifyUsernameToken(ut) {
		t.Fatal("PasswordText rejected")
	}
	ut.Password = "nope"
	if s.verifyUsernameToken(ut) {
		t.Fatal("wrong PasswordText accepted")
	}
}

func TestAuthorizeAnonymousInfo(t *testing.T) {
	s := testServer()
	req := httptest.NewRequest(http.MethodPost, "/onvif/device_service", nil)
	req.RemoteAddr = "192.168.1.50:1234"

	if !s.authorize(req, &parsedRequest{Action: "GetSystemDateAndTime"}) {
		t.Fatal("anonymous GetSystemDateAndTime should be allowed")
	}
	if s.authorize(req, &parsedRequest{Action: "GetProfiles"}) {
		t.Fatal("anonymous GetProfiles must be rejected")
	}

	req.SetBasicAuth("admin", "admin123")
	if !s.authorize(req, &parsedRequest{Action: "GetProfiles"}) {
		t.Fatal("basic auth rejected")
	}
	req.SetBasicAuth("admin", "bad")
	if s.authorize(req, &parsedRequest{Action: "GetProfiles"}) {
		t.Fatal("bad basic auth accepted")
	}
}

func TestParseSOAPRequestActionFromHeader(t *testing.T) {
	body := `<?xml version="1.0"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><trt:GetProfiles xmlns:trt="http://www.onvif.org/ver10/media/wsdl"/></s:Body></s:Envelope>`
	req := httptest.NewRequest(http.MethodPost, "/onvif/media_service", strings.NewReader(body))
	req.Header.Set("SOAPAction", `"http://www.onvif.org/ver10/media/wsdl/GetProfiles"`)

	parsed, err := parseSOAPRequest(req)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Action != "GetProfiles" {
		t.Fatalf("action = %q", parsed.Action)
	}
}

func TestParseSOAPRequestActionFromBody(t *testing.T) {
	body := `<?xml version="1.0"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><tds:GetDeviceInformation xmlns:tds="http://www.onvif.org/ver10/device/wsdl"/></s:Body></s:Envelope>`
	req := httptest.NewRequest(http.MethodPost, "/onvif/device_service", strings.NewReader(body))

	parsed, err := parseSOAPRequest(req)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Action != "GetDeviceInformation" {
		t.Fatalf("action = %q", parsed.Action)
	}
}

func TestParseSOAPRequestUsernameToken(t *testing.T) {
	body := `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
  xmlns:wsse="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd"
  xmlns:wsu="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd">
  <s:Header><wsse:Security><wsse:UsernameToken>
    <wsse:Username>admin</wsse:Username>
    <wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest">abc=</wsse:Password>
    <wsse:Nonce EncodingType="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary">bm9uY2U=</wsse:Nonce>
    <wsu:Created>2026-01-01T00:00:00Z</wsu:Created>
  </wsse:UsernameToken></wsse:Security></s:Header>
  <s:Body><tds:GetProfiles xmlns:tds="http://www.onvif.org/ver10/media/wsdl"/></s:Body>
</s:Envelope>`
	req := httptest.NewRequest(http.MethodPost, "/onvif/media_service", strings.NewReader(body))

	parsed, err := parseSOAPRequest(req)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.UsernameToken == nil {
		t.Fatal("UsernameToken not parsed")
	}
	if parsed.UsernameToken.Username != "admin" || parsed.UsernameToken.Nonce != "bm9uY2U=" {
		t.Fatalf("unexpected token: %+v", parsed.UsernameToken)
	}
}

func TestNormalizeAction(t *testing.T) {
	cases := map[string]string{
		`"http://www.onvif.org/ver10/media/wsdl/GetProfiles"`: "GetProfiles",
		"http://www.onvif.org/ver10/device/wsdl/GetServices":  "GetServices",
		"GetCapabilities":   "GetCapabilities",
		"  'GetProfiles'  ": "GetProfiles",
	}
	for in, want := range cases {
		if got := normalizeAction(in); got != want {
			t.Fatalf("normalizeAction(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestActionFromBody(t *testing.T) {
	inner := []byte(`<trt:GetStreamUri xmlns:trt="http://www.onvif.org/ver10/media/wsdl"><trt:ProfileToken>p</trt:ProfileToken></trt:GetStreamUri>`)
	if got := actionFromBody(inner); got != "GetStreamUri" {
		t.Fatalf("action = %q", got)
	}
	if got := actionFromBody([]byte("not xml")); got != "" {
		t.Fatalf("invalid xml should return empty, got %q", got)
	}
}

func TestParseISODuration(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"PT60S", 60 * time.Second},
		{"PT5M", 5 * time.Minute},
		{"PT1H30M", 90 * time.Minute},
		{"P1D", 24 * time.Hour},
		{"", time.Minute},
		{"bogus", time.Minute},
	}
	for _, tc := range cases {
		if got := parseISODuration(tc.in, time.Minute); got != tc.want {
			t.Fatalf("parseISODuration(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestSOAPEnvelopeEscapes(t *testing.T) {
	out := soapEnvelope(`<x>` + xmlEscape(`a<b&c`) + `</x>`)
	if !strings.Contains(out, "a&lt;b&amp;c") {
		t.Fatalf("escaping failed: %s", out)
	}
	if !strings.HasPrefix(out, "<?xml") {
		t.Fatalf("missing xml declaration")
	}
	var probe struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal([]byte(out), &probe); err != nil {
		t.Fatalf("generated envelope is not valid XML: %v", err)
	}
}

func TestProfiles(t *testing.T) {
	cfg := config.Default()
	cfg.Video.Width = 1280
	cfg.Video.Height = 720
	cfg.MJPEG.Enabled = true
	s := &Server{cfg: cfg}

	profiles := s.profiles()
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(profiles))
	}
	if profiles[0].Encoding != "H264" || profiles[0].Path != "/stream1" {
		t.Fatalf("unexpected H264 profile: %+v", profiles[0])
	}
	if profiles[1].Encoding != "JPEG" || profiles[1].Path != "/stream2" {
		t.Fatalf("unexpected MJPEG profile: %+v", profiles[1])
	}

	if _, ok := s.profileByToken("profile_h264"); !ok {
		t.Fatal("profile_h264 not found")
	}
	if _, ok := s.profileByToken("nope"); ok {
		t.Fatal("unknown profile accepted")
	}
	if p, ok := s.profileByToken(""); !ok || p.Token != "profile_h264" {
		t.Fatal("empty token should default to the first profile")
	}

	cfg.MJPEG.Enabled = false
	if len(s.profiles()) != 1 {
		t.Fatal("MJPEG profile should disappear when disabled")
	}
}
