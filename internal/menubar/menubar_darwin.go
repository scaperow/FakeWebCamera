//go:build darwin

package menubar

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/systray"

	"fakewebcamera/internal/app"
)

// Run starts the macOS menu bar application and blocks until the user quits
// or ctx is cancelled (SIGINT/SIGTERM, logout, launchctl unload).
func Run(ctx context.Context, a *app.App) {
	systray.Run(func() {
		onReady(a)
		go func() {
			<-ctx.Done()
			systray.Quit()
		}()
	}, func() { a.Stop() })
}

func onReady(a *app.App) {
	icon := makeIcon()
	systray.SetTemplateIcon(icon, icon)

	title := systray.AddMenuItem("FakeWebCamera", "ONVIF / RTSP 虚拟摄像头")
	title.Disable()
	status := systray.AddMenuItem("启动中…", "")
	status.Disable()
	systray.AddSeparator()

	copyRTSP := systray.AddMenuItem("复制 RTSP 地址", "")
	copyONVIF := systray.AddMenuItem("复制 ONVIF 地址", "")
	openPanel := systray.AddMenuItem("打开控制面板", "")
	pickFile := systray.AddMenuItem("选择视频文件…", "")
	systray.AddSeparator()

	triggerMotion := systray.AddMenuItem("触发移动事件", "")
	motionSim := systray.AddMenuItemCheckbox("周期模拟移动侦测", "", a.Config().ONVIF.MotionSimulation)
	systray.AddSeparator()

	restart := systray.AddMenuItem("重启服务", "")
	autostart := systray.AddMenuItemCheckbox("开机自启", "", autostartEnabled())
	systray.AddSeparator()

	quit := systray.AddMenuItem("退出", "")

	statusText := func() {
		st, ok := a.Status().(app.Status)
		if !ok {
			return
		}
		if st.Running {
			status.SetTitle(fmt.Sprintf("运行中 · %s", st.IP))
			copyRTSP.Enable()
			copyONVIF.Enable()
			openPanel.Enable()
		} else {
			status.SetTitle("已停止")
			copyRTSP.Disable()
			copyONVIF.Disable()
			openPanel.Disable()
		}
		if st.Media.Source != "" && !st.Media.UsingTestSource {
			pickFile.SetTitle("选择视频文件…")
		}
	}
	statusText()

	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			statusText()
		}
	}()

	go func() {
		for range copyRTSP.ClickedCh {
			st := a.Status().(app.Status)
			copyToClipboard(st.RTSPURL)
		}
	}()
	go func() {
		for range copyONVIF.ClickedCh {
			st := a.Status().(app.Status)
			copyToClipboard(st.ONVIFURL)
		}
	}()
	go func() {
		for range openPanel.ClickedCh {
			st := a.Status().(app.Status)
			if st.WebURL != "" {
				_ = exec.Command("open", st.WebURL).Run()
			}
		}
	}()
	go func() {
		for range pickFile.ClickedCh {
			path, err := chooseVideoFile()
			if err != nil {
				log.Printf("menubar: choose file: %v", err)
				continue
			}
			if path == "" {
				continue
			}
			cfg := *a.Config()
			cfg.Video.Source = path
			if err := a.UpdateConfig(&cfg); err != nil {
				notify("FakeWebCamera", "切换视频源失败: "+err.Error())
				continue
			}
			notify("FakeWebCamera", "正在切换视频源…")
		}
	}()
	go func() {
		for range triggerMotion.ClickedCh {
			a.TriggerMotion(true)
		}
	}()
	go func() {
		for range motionSim.ClickedCh {
			on := !motionSim.Checked()
			if err := a.SetMotionSimulation(on); err != nil {
				log.Printf("menubar: motion simulation: %v", err)
				continue
			}
			if on {
				motionSim.Check()
			} else {
				motionSim.Uncheck()
			}
		}
	}()
	go func() {
		for range restart.ClickedCh {
			if err := a.Restart(); err != nil {
				notify("FakeWebCamera", "重启失败: "+err.Error())
			}
		}
	}()
	go func() {
		for range autostart.ClickedCh {
			if err := toggleAutostart(); err != nil {
				notify("FakeWebCamera", "开机自启设置失败: "+err.Error())
				continue
			}
			if autostart.Checked() {
				autostart.Uncheck()
			} else {
				autostart.Check()
			}
		}
	}()
	go func() {
		<-quit.ClickedCh
		systray.Quit()
	}()
}

func copyToClipboard(text string) {
	if text == "" {
		return
	}
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		log.Printf("menubar: pbcopy: %v", err)
	}
}

func chooseVideoFile() (string, error) {
	script := `POSIX path of (choose file with prompt "选择循环播放的视频文件" of type {"public.movie", "public.video"})`
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		// User cancellation is reported as an error by osascript.
		return "", nil
	}
	return strings.TrimSpace(string(out)), nil
}

func notify(title, message string) {
	script := fmt.Sprintf(`display notification %q with title %q`, message, title)
	_ = exec.Command("osascript", "-e", script).Run()
}

func autostartPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "LaunchAgents", "com.scaperow.fakewebcamera.plist")
}

func autostartEnabled() bool {
	path := autostartPath()
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func toggleAutostart() error {
	path := autostartPath()
	if path == "" {
		return fmt.Errorf("home directory not found")
	}
	if autostartEnabled() {
		_ = exec.Command("launchctl", "unload", path).Run()
		return os.Remove(path)
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}

	var args []string
	appPath := ""
	if idx := strings.Index(exe, ".app/Contents/MacOS/"); idx >= 0 {
		appPath = exe[:idx+4]
	}

	var program string
	if appPath != "" {
		program = "/usr/bin/open"
		args = []string{"-a", appPath}
	} else {
		program = exe
		args = []string{"run", "--menubar"}
	}

	var argXML strings.Builder
	argXML.WriteString("<string>" + xmlEscape(program) + "</string>")
	for _, arg := range args {
		argXML.WriteString("<string>" + xmlEscape(arg) + "</string>")
	}

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.scaperow.fakewebcamera</string>
  <key>ProgramArguments</key><array>%s</array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><false/>
  <key>ProcessType</key><string>Interactive</string>
</dict>
</plist>
`, argXML.String())

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		return err
	}
	if out, err := exec.Command("launchctl", "load", path).CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func makeIcon() []byte {
	const size = 22
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	black := color.NRGBA{A: 255}
	clear := color.NRGBA{}

	fill := func(x0, y0, x1, y1 int) {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				if x >= 0 && y >= 0 && x < size && y < size {
					img.SetNRGBA(x, y, black)
				}
			}
		}
	}

	// Camera body, top bump and viewfinder.
	fill(2, 6, 18, 18)
	fill(6, 3, 12, 6)
	fill(18, 8, 21, 13)

	// Lens hole (template icons use alpha as mask).
	cx, cy, r := 10, 12, 4
	for y := cy - r; y <= cy+r; y++ {
		for x := cx - r; x <= cx+r; x++ {
			dx, dy := x-cx, y-cy
			if dx*dx+dy*dy <= r*r {
				img.SetNRGBA(x, y, clear)
			}
		}
	}

	// Rounded corners on the body.
	for _, p := range [][2]int{{2, 6}, {17, 6}, {2, 17}, {17, 17}} {
		img.SetNRGBA(p[0], p[1], clear)
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
