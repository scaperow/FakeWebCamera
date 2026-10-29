package netutil

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

// Interface describes a network interface that can carry ONVIF/RTSP traffic.
type Interface struct {
	Name     string   `json:"name"`
	Index    int      `json:"index"`
	IPv4     []string `json:"ipv4"`
	Loopback bool     `json:"loopback"`
	Up       bool     `json:"up"`
}

// List returns all interfaces with their IPv4 addresses.
func List() ([]Interface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	out := make([]Interface, 0, len(ifaces))
	for _, ifi := range ifaces {
		item := Interface{
			Name:     ifi.Name,
			Index:    ifi.Index,
			Loopback: ifi.Flags&net.FlagLoopback != 0,
			Up:       ifi.Flags&net.FlagUp != 0,
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.To4() == nil {
				continue
			}
			item.IPv4 = append(item.IPv4, ip.String())
		}
		out = append(out, item)
	}
	return out, nil
}

// Select resolves "auto" or a concrete interface name.
func Select(name string) (*net.Interface, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "auto" {
		return autoSelect()
	}
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return nil, fmt.Errorf("interface %q: %w", name, err)
	}
	if ifi.Flags&net.FlagUp == 0 {
		return nil, fmt.Errorf("interface %q is down", name)
	}
	return ifi, nil
}

func autoSelect() (*net.Interface, error) {
	list, err := List()
	if err != nil {
		return nil, err
	}

	type candidate struct {
		name  string
		score int
	}
	var candidates []candidate
	for _, item := range list {
		if item.Loopback || !item.Up || len(item.IPv4) == 0 {
			continue
		}
		score := 100
		switch {
		case item.Name == "en0":
			score = 0
		case strings.HasPrefix(item.Name, "en"):
			score = 10
		case strings.HasPrefix(item.Name, "utun"), strings.HasPrefix(item.Name, "awdl"),
			strings.HasPrefix(item.Name, "llw"), strings.HasPrefix(item.Name, "bridge"),
			strings.HasPrefix(item.Name, "vmnet"), strings.HasPrefix(item.Name, "docker"),
			strings.HasPrefix(item.Name, "tailscale"):
			score = 90
		}
		candidates = append(candidates, candidate{name: item.Name, score: score})
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no active IPv4 interface found")
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score == candidates[j].score {
			return candidates[i].name < candidates[j].name
		}
		return candidates[i].score < candidates[j].score
	})
	return net.InterfaceByName(candidates[0].name)
}

// PrimaryIPv4 returns the first usable IPv4 address of an interface.
func PrimaryIPv4(ifi *net.Interface) (net.IP, error) {
	addrs, err := ifi.Addrs()
	if err != nil {
		return nil, err
	}
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.To4() == nil {
			continue
		}
		if ip.IsLinkLocalUnicast() {
			continue
		}
		return ip.To4(), nil
	}
	return nil, fmt.Errorf("interface %s has no usable IPv4 address", ifi.Name)
}

// IPv4ByIndex finds the primary IPv4 address for an interface index.
func IPv4ByIndex(index int) net.IP {
	ifi, err := net.InterfaceByIndex(index)
	if err != nil {
		return nil
	}
	ip, err := PrimaryIPv4(ifi)
	if err != nil {
		return nil
	}
	return ip
}

// OutboundIPv4 returns the address the OS would use for an external route.
// It is only used as a last-resort fallback for auto selection.
func OutboundIPv4() net.IP {
	conn, err := net.Dial("udp4", "8.8.8.8:80")
	if err != nil {
		return nil
	}
	defer conn.Close()
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return addr.IP.To4()
	}
	return nil
}
