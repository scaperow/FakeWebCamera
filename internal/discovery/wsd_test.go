package discovery

import (
	"strings"
	"testing"

	"fakewebcamera/internal/config"
)

func TestProbeMatches(t *testing.T) {
	s := &Server{cfg: config.Default()}

	cases := []struct {
		types  string
		scopes string
		want   bool
	}{
		{"", "", true},
		{"dn:NetworkVideoTransmitter", "", true},
		{"dn:Device", "", true},
		{"", "onvif://www.onvif.org/type/NetworkVideoTransmitter", true},
		{"dn:NetworkVideoTransmitter", "onvif://www.onvif.org/name/Fake", true},
		{"wsdp:Device pub:Computer", "", false},
		{"", "http://example.com/scope", false},
		{"tds:Device", "onvif://www.onvif.org/type/NetworkVideoTransmitter", true},
	}

	for _, tc := range cases {
		if got := s.probeMatches(tc.types, tc.scopes); got != tc.want {
			t.Fatalf("probeMatches(%q, %q) = %v, want %v", tc.types, tc.scopes, got, tc.want)
		}
	}
}

func TestScopeList(t *testing.T) {
	cfg := config.Default()
	cfg.Device.Name = "Living Room Cam"
	cfg.Device.HardwareID = "FC-9"
	cfg.Device.Location = "Desk"

	scopes := scopeList(cfg)
	if strings.Contains(scopes, " ") && !strings.Contains(scopes, "Living_Room_Cam") {
		t.Fatalf("name was not sanitized: %s", scopes)
	}
	for _, want := range []string{
		"onvif://www.onvif.org/type/NetworkVideoTransmitter",
		"onvif://www.onvif.org/name/Living_Room_Cam",
		"onvif://www.onvif.org/hardware/FC-9",
		"onvif://www.onvif.org/location/Desk",
	} {
		if !strings.Contains(scopes, want) {
			t.Fatalf("scope %q missing from %q", want, scopes)
		}
	}
}

func TestEnvelopeIsValidXML(t *testing.T) {
	s := &Server{cfg: config.Default()}
	msg := s.envelope("http://schemas.xmlsoap.org/ws/2005/04/discovery/Hello", "<d:Hello/>", "")
	if !strings.Contains(string(msg), "urn:uuid:") {
		t.Fatalf("missing message id: %s", msg)
	}
	if !strings.Contains(string(msg), "xmlns:d=\"http://schemas.xmlsoap.org/ws/2005/04/discovery\"") {
		t.Fatalf("missing discovery namespace: %s", msg)
	}
}
