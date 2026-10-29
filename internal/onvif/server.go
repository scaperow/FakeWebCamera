package onvif

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"fakewebcamera/internal/config"
	"fakewebcamera/internal/media"
)

// Server implements the ONVIF SOAP services.
type Server struct {
	cfg       *config.Config
	frames    *media.FrameStore
	events    *EventHub
	iface     *net.Interface
	primaryIP net.IP
	started   time.Time
}

func New(cfg *config.Config, frames *media.FrameStore, iface *net.Interface, primaryIP net.IP) *Server {
	return &Server{
		cfg:       cfg,
		frames:    frames,
		events:    NewEventHub(),
		iface:     iface,
		primaryIP: primaryIP,
		started:   time.Now(),
	}
}

// ServeHTTP dispatches ONVIF SOAP requests.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	req, err := parseSOAPRequest(r)
	if err != nil {
		log.Printf("onvif: invalid request from %s: %v", r.RemoteAddr, err)
		writeFault(w, "Sender", "InvalidArg", "Invalid SOAP request")
		return
	}
	if req.Action == "" {
		writeFault(w, "Sender", "InvalidArg", "Missing SOAP action")
		return
	}

	if !s.authorize(r, req) {
		log.Printf("onvif: unauthorized %s from %s", req.Action, r.RemoteAddr)
		writeFault(w, "Sender", "NotAuthorized", "Not Authorized")
		return
	}

	body, err := s.dispatch(req)
	if err != nil {
		if err == errActionNotSupported {
			writeFault(w, "Sender", "ActionNotSupported", "Action "+req.Action+" is not supported")
		} else {
			log.Printf("onvif: %s failed: %v", req.Action, err)
			writeFault(w, "Receiver", "ActionFailed", err.Error())
		}
		return
	}

	log.Printf("onvif: %s from %s", req.Action, r.RemoteAddr)
	writeSOAP(w, body)
}

var errActionNotSupported = fmt.Errorf("action not supported")

func (s *Server) dispatch(req *parsedRequest) (string, error) {
	if strings.Contains(req.Raw.URL.Path, "media2") {
		switch req.Action {
		case "GetServiceCapabilities":
			return s.getServiceCapabilities(req.Raw), nil
		case "GetProfiles":
			return s.getProfiles2(req.Raw), nil
		case "GetVideoSources":
			return s.getVideoSources2(req.Raw), nil
		case "GetVideoSourceConfigurations":
			return s.getVideoSourceConfigurations2(req.Raw), nil
		case "GetVideoEncoderConfigurations":
			return s.getVideoEncoderConfigurations2(req.Raw), nil
		case "GetVideoEncoderConfigurationOptions":
			return s.getVideoEncoderConfigurationOptions2(req.Inner), nil
		case "GetStreamUri":
			return s.getStreamURI2(req)
		case "GetSnapshotUri":
			return s.getSnapshotURI2(req)
		}
	}

	switch req.Action {
	case "GetSystemDateAndTime":
		return s.getSystemDateAndTime(), nil
	case "GetDeviceInformation":
		return s.getDeviceInformation(), nil
	case "GetCapabilities":
		return s.getCapabilities(req.Raw), nil
	case "GetServices":
		return s.getServices(req.Raw), nil
	case "GetServiceCapabilities":
		return s.getServiceCapabilities(req.Raw), nil
	case "GetNetworkInterfaces":
		return s.getNetworkInterfaces(), nil
	case "GetHostname":
		return s.getHostname(), nil
	case "GetScopes":
		return s.getScopes(), nil
	case "SetScopes", "SetHostname", "SetNetworkInterfaces", "SetDiscoveryMode", "SystemReboot":
		return s.emptyResponse(req.Action), nil
	case "GetDiscoveryMode":
		return `<tds:GetDiscoveryModeResponse><tds:DiscoveryMode>Discoverable</tds:DiscoveryMode></tds:GetDiscoveryModeResponse>`, nil
	case "GetUsers":
		return s.getUsers(), nil
	case "GetDNS":
		return `<tds:GetDNSResponse><tds:DNSInformation/></tds:GetDNSResponse>`, nil
	case "GetNTP":
		return `<tds:GetNTPResponse><tds:NTPInformation/></tds:GetNTPResponse>`, nil
	case "GetNetworkProtocols":
		return `<tds:GetNetworkProtocolsResponse/>`, nil
	case "GetNetworkDefaultGateway":
		return `<tds:GetNetworkDefaultGatewayResponse><tds:NetworkGateway><tt:IPv4Address></tt:IPv4Address></tds:NetworkGateway></tds:GetNetworkDefaultGatewayResponse>`, nil
	case "GetWsdlUrl":
		return fmt.Sprintf(`<tds:GetWsdlUrlResponse><tds:WsdlUrl>%s/onvif/device_service?wsdl</tds:WsdlUrl></tds:GetWsdlUrlResponse>`, xmlEscape(s.baseURL(req.Raw))), nil
	case "GetEndpointReference":
		return fmt.Sprintf(`<tds:GetEndpointReferenceResponse><tds:GUID>%s</tds:GUID></tds:GetEndpointReferenceResponse>`, xmlEscape(s.cfg.Device.UUID)), nil
	case "GetRelayOutputs":
		return `<tds:GetRelayOutputsResponse/>`, nil

	case "GetProfiles":
		return s.getProfiles(req.Raw), nil
	case "GetVideoSources":
		return s.getVideoSources(req.Raw), nil
	case "GetVideoSourceConfigurations":
		return s.getVideoSourceConfigurations(req.Raw), nil
	case "GetVideoEncoderConfigurations":
		return s.getVideoEncoderConfigurations(req.Raw), nil
	case "GetVideoEncoderConfiguration":
		return s.getVideoEncoderConfiguration(req.Inner), nil
	case "GetVideoEncoderConfigurationOptions":
		return s.getVideoEncoderConfigurationOptions(req.Inner), nil
	case "GetCompatibleVideoEncoderConfigurations":
		return s.getVideoEncoderConfigurations(req.Raw), nil
	case "SetVideoEncoderConfiguration":
		return `<trt:SetVideoEncoderConfigurationResponse/>`, nil
	case "GetStreamUri":
		return s.getStreamURI(req)
	case "GetSnapshotUri":
		return s.getSnapshotURI(req)

	case "CreatePullPointSubscription":
		return s.createPullPointSubscription(req.Inner, req.Raw), nil
	case "PullMessages":
		return s.pullMessages(req)
	case "Renew":
		return s.renewSubscription(req)
	case "Unsubscribe":
		return `<tev:UnsubscribeResponse/>`, nil
	case "GetEventProperties":
		return s.getEventProperties(req.Raw), nil
	case "SetSynchronizationPoint":
		return `<tev:SetSynchronizationPointResponse/>`, nil
	}

	return "", errActionNotSupported
}

func (s *Server) emptyResponse(action string) string {
	prefix := "tds"
	switch action {
	case "SetScopes", "SetHostname", "SetNetworkInterfaces", "SetDiscoveryMode", "SystemReboot":
		prefix = "tds"
	}
	return fmt.Sprintf(`<%s:%sResponse/>`, prefix, action)
}

func (s *Server) getSystemDateAndTime() string {
	now := time.Now().UTC()
	return fmt.Sprintf(`<tds:GetSystemDateAndTimeResponse><tds:SystemDateAndTime><tt:DateTimeType>Manual</tt:DateTimeType><tt:DaylightSavings>false</tt:DaylightSavings><tt:UTCDateTime><tt:Time><tt:Hour>%d</tt:Hour><tt:Minute>%d</tt:Minute><tt:Second>%d</tt:Second></tt:Time><tt:Date><tt:Year>%d</tt:Year><tt:Month>%d</tt:Month><tt:Day>%d</tt:Day></tt:Date></tt:UTCDateTime></tds:SystemDateAndTime></tds:GetSystemDateAndTimeResponse>`,
		now.Hour(), now.Minute(), now.Second(), now.Year(), int(now.Month()), now.Day())
}

func (s *Server) getDeviceInformation() string {
	d := s.cfg.Device
	return fmt.Sprintf(`<tds:GetDeviceInformationResponse><tds:Manufacturer>%s</tds:Manufacturer><tds:Model>%s</tds:Model><tds:FirmwareVersion>%s</tds:FirmwareVersion><tds:SerialNumber>%s</tds:SerialNumber><tds:HardwareId>%s</tds:HardwareId></tds:GetDeviceInformationResponse>`,
		xmlEscape(d.Manufacturer), xmlEscape(d.Model), xmlEscape(d.FirmwareVersion),
		xmlEscape(d.SerialNumber), xmlEscape(d.HardwareID))
}

func (s *Server) getCapabilities(r *http.Request) string {
	mediaXAddr := xmlEscape(s.baseURL(r) + "/onvif/media_service")
	media2XAddr := xmlEscape(s.baseURL(r) + "/onvif/media2_service")
	eventsXAddr := xmlEscape(s.baseURL(r) + "/onvif/events_service")
	deviceXAddr := xmlEscape(s.baseURL(r) + "/onvif/device_service")

	return fmt.Sprintf(`<tds:GetCapabilitiesResponse><tds:Capabilities>`+
		`<tt:Device><tt:XAddr>%s</tt:XAddr>`+
		`<tt:Network><tt:IPFilter>false</tt:IPFilter><tt:IPVersion6>false</tt:IPVersion6><tt:DynDNS>false</tt:DynDNS><tt:Extension><tt:Dot11>false</tt:Dot11><tt:Extension/></tt:Extension></tt:Network>`+
		`<tt:System><tt:DiscoveryResolve>false</tt:DiscoveryResolve><tt:DiscoveryBye>true</tt:DiscoveryBye><tt:RemoteDiscovery>false</tt:RemoteDiscovery><tt:SystemBackup>false</tt:SystemBackup><tt:SystemLogging>false</tt:SystemLogging><tt:FirmwareUpgrade>false</tt:FirmwareUpgrade><tt:SupportedVersions><tt:Major>2</tt:Major><tt:Minor>60</tt:Minor></tt:SupportedVersions><tt:Extension/></tt:System>`+
		`<tt:IO><tt:InputConnectors>0</tt:InputConnectors><tt:RelayOutputs>0</tt:RelayOutputs><tt:Extension/></tt:IO>`+
		`<tt:Security><tt:TLS1.1>false</tt:TLS1.1><tt:TLS1.2>false</tt:TLS1.2><tt:OnboardKeyGeneration>false</tt:OnboardKeyGeneration><tt:AccessPolicyConfig>false</tt:AccessPolicyConfig><tt:X.509Token>false</tt:X.509Token><tt:SAMLToken>false</tt:SAMLToken><tt:KerberosToken>false</tt:KerberosToken><tt:RELToken>false</tt:RELToken><tt:Extension/></tt:Security>`+
		`<tt:Extension/></tt:Device>`+
		`<tt:Events><tt:XAddr>%s</tt:XAddr><tt:WSSubscriptionPolicySupport>true</tt:WSSubscriptionPolicySupport><tt:WSPullPointSupport>true</tt:WSPullPointSupport><tt:WSPausableSubscriptionManagerInterfaceSupport>false</tt:WSPausableSubscriptionManagerInterfaceSupport><tt:MaxNotificationProducers>8</tt:MaxNotificationProducers><tt:MaxPullPoints>8</tt:MaxPullPoints><tt:PersistentNotificationStorage>false</tt:PersistentNotificationStorage><tt:Extension/></tt:Events>`+
		`<tt:Media><tt:XAddr>%s</tt:XAddr><tt:StreamingCapabilities><tt:RTPMulticast>false</tt:RTPMulticast><tt:RTP_TCP>true</tt:RTP_TCP><tt:RTP_RTSP_TCP>true</tt:RTP_RTSP_TCP><tt:NonAggregateControl>false</tt:NonAggregateControl><tt:NoRTSPStreaming>false</tt:NoRTSPStreaming><tt:Extension/></tt:StreamingCapabilities><tt:Extension/></tt:Media>`+
		`<tt:Extension><tt:Media2><tt:XAddr>%s</tt:XAddr><tt:StreamingCapabilities><tt:RTPMulticast>false</tt:RTPMulticast><tt:RTP_TCP>true</tt:RTP_TCP><tt:RTP_RTSP_TCP>true</tt:RTP_RTSP_TCP><tt:NonAggregateControl>false</tt:NonAggregateControl><tt:NoRTSPStreaming>false</tt:NoRTSPStreaming></tt:StreamingCapabilities><tt:SnapshotUri>true</tt:SnapshotUri><tt:Rotation>false</tt:Rotation><tt:VideoSourceMode>false</tt:VideoSourceMode><tt:OSD>false</tt:OSD><tt:TemporaryOSD>false</tt:TemporaryOSD><tt:EXICompression>false</tt:EXICompression></tt:Media2></tt:Extension>`+
		`</tds:Capabilities></tds:GetCapabilitiesResponse>`,
		deviceXAddr, eventsXAddr, mediaXAddr, media2XAddr)
}

func (s *Server) getServices(r *http.Request) string {
	base := s.baseURL(r)
	services := []struct {
		ns   string
		path string
	}{
		{nsDevice, "/onvif/device_service"},
		{nsMedia, "/onvif/media_service"},
		{nsMedia2, "/onvif/media2_service"},
		{nsEvents, "/onvif/events_service"},
	}

	var b strings.Builder
	b.WriteString(`<tds:GetServicesResponse>`)
	for _, svc := range services {
		fmt.Fprintf(&b, `<tds:Service><tds:Namespace>%s</tds:Namespace><tds:XAddr>%s</tds:XAddr><tds:Version><tt:Major>2</tt:Major><tt:Minor>60</tt:Minor></tds:Version></tds:Service>`,
			svc.ns, xmlEscape(base+svc.path))
	}
	b.WriteString(`</tds:GetServicesResponse>`)
	return b.String()
}

func (s *Server) getServiceCapabilities(r *http.Request) string {
	switch {
	case strings.Contains(r.URL.Path, "media2"):
		return `<tr2:GetServiceCapabilitiesResponse><tr2:Capabilities SnapshotUri="true" Rotation="false" VideoSourceMode="false" OSD="false" TemporaryOSD="false" EXICompression="false"/></tr2:GetServiceCapabilitiesResponse>`
	case strings.Contains(r.URL.Path, "media"):
		return `<trt:GetServiceCapabilitiesResponse><trt:Capabilities SnapshotUri="true" Rotation="false" VideoSourceMode="false" OSD="false" TemporaryOSD="false" EXICompression="false"/></trt:GetServiceCapabilitiesResponse>`
	case strings.Contains(r.URL.Path, "events"):
		return `<tev:GetServiceCapabilitiesResponse><tev:Capabilities WSSubscriptionPolicySupport="true" WSPullPointSupport="true" WSPausableSubscriptionManagerInterfaceSupport="false" MaxNotificationProducers="8" MaxPullPoints="8" PersistentNotificationStorage="false"/></tev:GetServiceCapabilitiesResponse>`
	default:
		return `<tds:GetServiceCapabilitiesResponse><tds:Capabilities><tds:Network IPFilter="false" IPVersion6="false" DynDNS="false"/><tds:Security TLS1.1="false" TLS1.2="false" OnboardKeyGeneration="false" AccessPolicyConfig="false" X.509Token="false" SAMLToken="false" KerberosToken="false" RELToken="false"/><tds:System DiscoveryResolve="false" DiscoveryBye="true" RemoteDiscovery="false" SystemBackup="false" SystemLogging="false" FirmwareUpgrade="false"/></tds:Capabilities></tds:GetServiceCapabilitiesResponse>`
	}
}

func (s *Server) getNetworkInterfaces() string {
	name := "en0"
	var mac, ip string
	mtu := 1500
	if s.iface != nil {
		name = s.iface.Name
		mac = s.iface.HardwareAddr.String()
		mtu = s.iface.MTU
	}
	if s.primaryIP != nil {
		ip = s.primaryIP.String()
	}
	return fmt.Sprintf(`<tds:GetNetworkInterfacesResponse><tds:NetworkInterfaces token="eth0"><tt:Enabled>true</tt:Enabled><tt:Info><tt:Name>%s</tt:Name><tt:HwAddress>%s</tt:HwAddress><tt:MTU>%d</tt:MTU></tt:Info><tt:Link><tt:AdminSettings><tt:AutoNegotiation>true</tt:AutoNegotiation><tt:Speed>1000</tt:Speed><tt:Duplex>Full</tt:Duplex></tt:AdminSettings><tt:OperSettings><tt:AutoNegotiation>true</tt:AutoNegotiation><tt:Speed>1000</tt:Speed><tt:Duplex>Full</tt:Duplex></tt:OperSettings><tt:InterfaceType>6</tt:InterfaceType></tt:Link><tt:IPv4><tt:Enabled>true</tt:Enabled><tt:Config><tt:Manual><tt:Address>%s</tt:Address><tt:PrefixLength>24</tt:PrefixLength></tt:Manual><tt:DHCP>false</tt:DHCP></tt:Config></tt:IPv4></tds:NetworkInterfaces></tds:GetNetworkInterfacesResponse>`,
		xmlEscape(name), xmlEscape(mac), mtu, xmlEscape(ip))
}

func (s *Server) getHostname() string {
	name := sanitizeScope(s.cfg.Device.Name)
	if len(name) > 32 {
		name = name[:32]
	}
	return fmt.Sprintf(`<tds:GetHostnameResponse><tds:HostnameInformation><tt:FromDHCP>false</tt:FromDHCP><tt:Name>%s</tt:Name></tds:HostnameInformation></tds:GetHostnameResponse>`, xmlEscape(name))
}

func (s *Server) getScopes() string {
	var b strings.Builder
	b.WriteString(`<tds:GetScopesResponse>`)
	for _, scope := range strings.Fields(scopeList(s.cfg)) {
		fmt.Fprintf(&b, `<tds:Scopes><tt:ScopeDef>Fixed</tt:ScopeDef><tt:ScopeItem>%s</tt:ScopeItem></tds:Scopes>`, xmlEscape(scope))
	}
	b.WriteString(`</tds:GetScopesResponse>`)
	return b.String()
}

func (s *Server) getUsers() string {
	return fmt.Sprintf(`<tds:GetUsersResponse><tds:User><tt:Username>%s</tt:Username><tt:UserLevel>Administrator</tt:UserLevel></tds:User></tds:GetUsersResponse>`, xmlEscape(s.cfg.Auth.Username))
}

func writeSOAP(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, soapEnvelope(body))
}

func writeFault(w http.ResponseWriter, code, subcode, reason string) {
	w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = io.WriteString(w, soapFault(code, subcode, reason))
}
