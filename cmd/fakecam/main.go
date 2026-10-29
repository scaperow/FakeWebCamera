package main

import (
	"context"
	"encoding/xml"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/ipv4"

	"fakewebcamera/internal/app"
	"fakewebcamera/internal/config"
	"fakewebcamera/internal/logging"
	"fakewebcamera/internal/media"
	"fakewebcamera/internal/menubar"
	"fakewebcamera/internal/netutil"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "1.0.0"

func main() {
	cmd := "run"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
		args = args[1:]
	}

	switch cmd {
	case "run", "menubar":
		run(args, cmd == "menubar")
	case "doctor":
		doctor(args)
	case "probe":
		probe(args)
	case "version", "--version", "-v":
		fmt.Println("FakeWebCamera", version)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Print(`FakeWebCamera - 局域网 ONVIF / RTSP 虚拟摄像头

用法:
  fakecam run [--config PATH] [--menubar]   启动服务（默认）
  fakecam doctor [--config PATH]            检查 ffmpeg、网卡、端口
  fakecam probe [--interface NAME]          主动探测局域网内的 ONVIF 设备
  fakecam version                           打印版本

常用地址:
  RTSP   rtsp://<IP>:8554/stream1
  MJPEG  rtsp://<IP>:8554/stream2
  ONVIF  http://<IP>:8080/onvif/device_service
  面板   http://127.0.0.1:8080
`)
}

func run(args []string, forceMenubar bool) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	configPath := fs.String("config", "", "配置文件路径")
	menubarFlag := fs.Bool("menubar", false, "以菜单栏应用方式运行")
	_ = fs.Parse(args)

	path := resolveConfigPath(*configPath)
	cfg, err := config.Load(path)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := cfg.Save(path); err != nil {
			log.Printf("save default config: %v", err)
		}
	}

	ring := logging.Setup(logPathFor(path))
	root, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	a := app.New(root, cfg, path, ring)
	if err := a.Start(root); err != nil {
		log.Fatalf("start: %v", err)
	}
	defer a.Stop()

	if forceMenubar || *menubarFlag || isAppBundle() {
		log.Printf("starting menu bar application")
		menubar.Run(root, a)
		return
	}

	log.Printf("FakeWebCamera %s running; press Ctrl+C to stop", version)
	<-root.Done()
}

func resolveConfigPath(flagPath string) string {
	if flagPath != "" {
		return config.ExpandPath(flagPath)
	}
	if env := os.Getenv("FAKEWEBCAMERA_CONFIG"); env != "" {
		return config.ExpandPath(env)
	}
	if _, err := os.Stat("config.yaml"); err == nil {
		if abs, err := filepath.Abs("config.yaml"); err == nil {
			return abs
		}
	}
	return config.DefaultPath()
}

func logPathFor(cfgPath string) string {
	if isAppBundle() {
		return config.LogPath()
	}
	return ""
}

func isAppBundle() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	return strings.Contains(exe, ".app/Contents/MacOS/")
}

func doctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	configPath := fs.String("config", "", "配置文件路径")
	_ = fs.Parse(args)

	path := resolveConfigPath(*configPath)
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Printf("✗ 配置: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("FakeWebCamera doctor")
	fmt.Println("--------------------")
	fmt.Printf("配置: %s\n", path)

	ok := true
	if ffmpeg := media.FFmpegPath(); ffmpeg == "" {
		fmt.Println("✗ ffmpeg: 未找到，请先 brew install ffmpeg")
		ok = false
	} else {
		fmt.Printf("✓ ffmpeg: %s\n", ffmpeg)
		if out, err := exec.Command(ffmpeg, "-version").Output(); err == nil {
			line := strings.SplitN(string(out), "\n", 2)[0]
			fmt.Printf("  %s\n", line)
		}
	}

	ifi, err := netutil.Select(cfg.Server.Interface)
	if err != nil {
		fmt.Printf("✗ 网卡: %v\n", err)
		ok = false
	} else {
		ip, err := netutil.PrimaryIPv4(ifi)
		if err != nil {
			fmt.Printf("✗ 网卡 %s: %v\n", ifi.Name, err)
			ok = false
		} else {
			fmt.Printf("✓ 网卡: %s (%s)\n", ifi.Name, ip)
			fmt.Printf("  RTSP:  rtsp://%s:%d/stream1\n", ip, cfg.Server.RTSPPort)
			if cfg.MJPEG.Enabled {
				fmt.Printf("  MJPEG: rtsp://%s:%d/stream2\n", ip, cfg.Server.RTSPPort)
			}
			fmt.Printf("  ONVIF: http://%s:%d/onvif/device_service\n", ip, cfg.Server.HTTPPort)
		}
	}

	checkTCP := func(name string, port int) {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			fmt.Printf("✗ %s 端口 %d: %v\n", name, port, err)
			ok = false
			return
		}
		_ = ln.Close()
		fmt.Printf("✓ %s 端口 %d 可用\n", name, port)
	}
	checkTCP("RTSP", cfg.Server.RTSPPort)
	checkTCP("HTTP", cfg.Server.HTTPPort)
	if cfg.Server.UDPRTPPort > 0 {
		checkUDP := func(name string, port int) {
			conn, err := net.ListenPacket("udp4", fmt.Sprintf(":%d", port))
			if err != nil {
				fmt.Printf("✗ %s UDP 端口 %d: %v\n", name, port, err)
				ok = false
				return
			}
			_ = conn.Close()
			fmt.Printf("✓ %s UDP 端口 %d 可用\n", name, port)
		}
		checkUDP("RTP", cfg.Server.UDPRTPPort)
		checkUDP("RTCP", cfg.Server.UDPRTCPPort)
	}

	conn, err := net.ListenPacket("udp4", ":3702")
	if err != nil {
		fmt.Printf("✗ WS-Discovery 3702: %v（可能已有 ONVIF 服务占用）\n", err)
		ok = false
	} else {
		_ = conn.Close()
		fmt.Println("✓ WS-Discovery 端口 3702 可用")
	}

	fmt.Println()
	fmt.Println("提示:")
	fmt.Println("  - 首次运行如弹出“本地网络”权限，请选择允许。")
	fmt.Println("  - 防火墙如拦截入站连接，请在 系统设置 > 网络 > 防火墙 中允许。")
	fmt.Println("  - 554 端口需要 root；默认 8554 无需 root。")

	if !ok {
		os.Exit(1)
	}
}

func probe(args []string) {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	interfaceName := fs.String("interface", "auto", "网卡名称")
	timeout := fs.Duration("timeout", 3*time.Second, "等待响应时间")
	_ = fs.Parse(args)

	ifi, err := netutil.Select(*interfaceName)
	if err != nil {
		log.Fatalf("interface: %v", err)
	}

	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	defer conn.Close()

	p := ipv4.NewPacketConn(conn)
	_ = p.SetMulticastInterface(ifi)
	_ = p.SetMulticastTTL(1)

	msg := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery" xmlns:dn="http://www.onvif.org/ver10/network/wsdl">
<s:Header><a:MessageID>urn:uuid:%s</a:MessageID><a:To>urn:schemas-xmlsoap-org:ws:2005:04:discovery</a:To><a:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/Probe</a:Action></s:Header>
<s:Body><d:Probe><d:Types>dn:NetworkVideoTransmitter</d:Types></d:Probe></s:Body></s:Envelope>`, uuid.NewString())

	dst := &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 3702}
	// Pin the outgoing interface. Without this a VPN default route can swallow
	// the multicast packet and the probe fails with "no route to host".
	control := &ipv4.ControlMessage{IfIndex: ifi.Index}
	if _, err := p.WriteTo([]byte(msg), control, dst); err != nil {
		log.Fatalf("send probe via %s: %v\n提示: 若开启了 VPN，请确认组播路由没有被 VPN 接管", ifi.Name, err)
	}
	fmt.Printf("已通过 %s 发送 WS-Discovery Probe，等待 %s...\n\n", ifi.Name, *timeout)

	_ = conn.SetReadDeadline(time.Now().Add(*timeout))
	buf := make([]byte, 64<<10)
	found := 0
	for {
		n, _, src, err := p.ReadFrom(buf)
		if err != nil {
			break
		}
		var resp struct {
			Body struct {
				ProbeMatches struct {
					ProbeMatch []struct {
						XAddrs string `xml:"XAddrs"`
						Scopes string `xml:"Scopes"`
						Types  string `xml:"Types"`
					} `xml:"ProbeMatch"`
				} `xml:"ProbeMatches"`
			} `xml:"Body"`
		}
		if xml.Unmarshal(buf[:n], &resp) != nil {
			continue
		}
		for _, match := range resp.Body.ProbeMatches.ProbeMatch {
			found++
			fmt.Printf("发现设备 #%d\n  来源: %s\n  XAddrs: %s\n  Types: %s\n", found, src, match.XAddrs, match.Types)
			if match.Scopes != "" {
				fmt.Printf("  Scopes: %s\n", match.Scopes)
			}
			fmt.Println()
		}
	}

	if found == 0 {
		fmt.Println("未发现设备。请确认：")
		fmt.Println("  - FakeWebCamera 已启动")
		fmt.Println("  - 两台设备在同一网段")
		fmt.Println("  - macOS 本地网络权限已允许")
		os.Exit(1)
	}
}
