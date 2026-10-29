package onvif

import (
	"bytes"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"strings"
	"time"
)

type parsedRequest struct {
	Action        string
	Inner         []byte
	UsernameToken *usernameToken
	Raw           *http.Request
}

type usernameToken struct {
	Username     string
	Password     string
	PasswordType string
	Nonce        string
	Created      string
}

type soapEnvelopeIn struct {
	Header struct {
		Security struct {
			UsernameToken struct {
				Username string `xml:"Username"`
				Password struct {
					Type  string `xml:"Type,attr"`
					Value string `xml:",chardata"`
				} `xml:"Password"`
				Nonce struct {
					EncodingType string `xml:"EncodingType,attr"`
					Value        string `xml:",chardata"`
				} `xml:"Nonce"`
				Created string `xml:"Created"`
			} `xml:"UsernameToken"`
		} `xml:"Security"`
	} `xml:"Header"`
	Body struct {
		Inner []byte `xml:",innerxml"`
	} `xml:"Body"`
}

func parseSOAPRequest(r *http.Request) (*parsedRequest, error) {
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		return nil, err
	}

	var env soapEnvelopeIn
	if err := xml.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("parse SOAP envelope: %w", err)
	}

	action := actionFromHTTP(r)
	if action == "" {
		action = actionFromBody(env.Body.Inner)
	}

	req := &parsedRequest{
		Action: action,
		Inner:  env.Body.Inner,
		Raw:    r,
	}

	ut := env.Header.Security.UsernameToken
	if ut.Username != "" || ut.Password.Value != "" {
		req.UsernameToken = &usernameToken{
			Username:     strings.TrimSpace(ut.Username),
			Password:     strings.TrimSpace(ut.Password.Value),
			PasswordType: ut.Password.Type,
			Nonce:        strings.TrimSpace(ut.Nonce.Value),
			Created:      strings.TrimSpace(ut.Created),
		}
	}
	return req, nil
}

func actionFromHTTP(r *http.Request) string {
	if v := r.Header.Get("SOAPAction"); v != "" {
		return normalizeAction(v)
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		if _, params, err := mime.ParseMediaType(ct); err == nil {
			if v := params["action"]; v != "" {
				return normalizeAction(v)
			}
		}
	}
	return ""
}

func actionFromBody(inner []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(inner))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local
		}
	}
}

func normalizeAction(action string) string {
	action = strings.TrimSpace(action)
	action = strings.Trim(action, `"'`)
	action = strings.TrimSpace(action)
	if i := strings.LastIndex(action, "/"); i >= 0 {
		action = action[i+1:]
	}
	return strings.TrimSpace(action)
}

func (s *Server) authorize(r *http.Request, req *parsedRequest) bool {
	if !s.cfg.Auth.Enabled {
		return true
	}

	if s.cfg.Auth.AllowAnonymousInfo {
		switch req.Action {
		case "GetSystemDateAndTime", "GetCapabilities", "GetServices", "GetDeviceInformation", "GetNetworkInterfaces", "GetScopes", "GetHostname":
			return true
		}
	}

	if user, pass, ok := r.BasicAuth(); ok {
		if user == s.cfg.Auth.Username && pass == s.cfg.Auth.Password {
			return true
		}
		return false
	}

	if req.UsernameToken == nil {
		return false
	}
	return s.verifyUsernameToken(req.UsernameToken)
}

func (s *Server) verifyUsernameToken(ut *usernameToken) bool {
	if ut.Username != s.cfg.Auth.Username {
		log.Printf("onvif: auth failed for user %q", ut.Username)
		return false
	}

	passwordType := strings.ToLower(ut.PasswordType)
	if strings.Contains(passwordType, "passworddigest") {
		nonce, err := decodeBase64(ut.Nonce)
		if err != nil {
			log.Printf("onvif: auth failed: invalid nonce: %v", err)
			return false
		}
		if ut.Created == "" {
			log.Printf("onvif: auth failed: missing Created")
			return false
		}
		if !s.createdWithinSkew(ut.Created) {
			log.Printf("onvif: auth failed: Created outside allowed clock skew (%s)", ut.Created)
			return false
		}

		h := sha1.New()
		h.Write(nonce)
		h.Write([]byte(ut.Created))
		h.Write([]byte(s.cfg.Auth.Password))
		expected := base64.StdEncoding.EncodeToString(h.Sum(nil))
		if subtle.ConstantTimeCompare([]byte(expected), []byte(ut.Password)) != 1 {
			log.Printf("onvif: auth failed: wrong digest")
			return false
		}
		return true
	}

	// PasswordText or missing Type.
	if subtle.ConstantTimeCompare([]byte(ut.Password), []byte(s.cfg.Auth.Password)) != 1 {
		log.Printf("onvif: auth failed: wrong password")
		return false
	}
	return true
}

func (s *Server) createdWithinSkew(created string) bool {
	t, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		t, err = time.Parse(time.RFC3339, created)
	}
	if err != nil {
		// Be lenient with clients that use a non-standard timestamp.
		return true
	}
	skew := time.Duration(s.cfg.Auth.ClockSkewMinutes) * time.Minute
	diff := time.Since(t)
	if diff < 0 {
		diff = -diff
	}
	return diff <= skew
}

func decodeBase64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty value")
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}
