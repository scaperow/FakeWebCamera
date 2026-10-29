# FakeWebCamera

把一台 Mac 变成局域网里可被自动发现的虚拟 IP 摄像头：对外提供 **ONVIF** 与 **RTSP**，循环播放本地视频文件，附带 macOS 菜单栏应用和内嵌 Web 控制台。

- 手机 ONVIF App、ONVIF Device Manager 可以直接搜索到设备
- VLC / ffplay / IINA 可用 RTSP 播放
- 海康、大华等 NVR 可通过 ONVIF 添加（取决于固件兼容性，见下文）
- 单二进制、无外部服务依赖（只需系统已安装 `ffmpeg`）

```
RTSP (H.264)   rtsp://<IP>:8554/stream1
RTSP (MJPEG)   rtsp://<IP>:8554/stream2
ONVIF          http://<IP>:8080/onvif/device_service
快照           http://<IP>:8080/snapshot.jpg
控制台         http://127.0.0.1:8080
默认账号       admin / admin123（请在共享网络使用前修改）
```

## 功能

| 能力 | 说明 |
| --- | --- |
| ONVIF Device | `GetSystemDateAndTime`、`GetDeviceInformation`、`GetCapabilities`、`GetServices`、`GetScopes`、`GetNetworkInterfaces` 等 |
| ONVIF Media1 / Media2 | Profile、VideoSource、VideoEncoder 配置、`GetStreamUri`、`GetSnapshotUri` |
| ONVIF Events | `CreatePullPointSubscription` / `PullMessages` / `Renew`，支持移动侦测事件 |
| WS-Discovery | 响应 Probe / Resolve，启动时发送 Hello，退出时发送 Bye；按收包网卡返回对应 IP |
| RTSP | H.264 与 MJPEG 两路，TCP / UDP 传输，Basic + Digest 认证 |
| 视频源 | 任意 ffmpeg 可解码的本地文件，循环播放；文件缺失时自动使用测试图案 |
| 编码 | 优先 `h264_videotoolbox`（硬件），失败自动回退 `libx264` |
| 控制台 | 实时预览、连接地址、客户端列表、配置编辑、日志、移动事件、重启 |
| 菜单栏 | 状态、复制地址、打开控制台、选择视频文件、移动事件、开机自启、退出 |

## 系统要求

- macOS 12 或更新版本（Apple Silicon / Intel）
- `ffmpeg`（`brew install ffmpeg`）
- 从源码构建需要 Go 1.25+

## 快速开始

### 1. 安装 ffmpeg

```bash
brew install ffmpeg
```

### 2. 构建并运行

```bash
make build
./build/fakecam doctor          # 检查 ffmpeg、网卡、端口占用
./build/fakecam run             # 前台运行，Ctrl+C 退出
```

首次运行会在当前目录没有 `config.yaml` 时创建
`~/Library/Application Support/FakeWebCamera/config.yaml`。

指定视频文件：

```bash
cp config.example.yaml config.yaml
# 编辑 video.source 为你的视频路径
./build/fakecam run --config config.yaml
```

或者直接在控制台 / 菜单栏里选择文件。

### 3. 打开控制台

```bash
open http://127.0.0.1:8080
```

控制台默认只允许本机访问。要让局域网内其它设备打开，把 `web.allow_lan` 设为 `true`，此时需要输入 ONVIF 账号密码。

### 4. 打包成菜单栏 App

```bash
make app
open build/FakeWebCamera.app
```

`scripts/build-app.sh` 会生成图标、`Info.plist`（`LSUIElement=1`，不显示 Dock 图标）并做 ad-hoc 签名。首次打开如被 Gatekeeper 拦截，请在 **系统设置 → 隐私与安全性** 中点击“仍要打开”。

## 连接方式

### VLC / ffplay

```bash
ffplay -rtsp_transport tcp "rtsp://admin:admin123@192.168.1.10:8554/stream1"
ffplay -rtsp_transport tcp "rtsp://admin:admin123@192.168.1.10:8554/stream2"
```

VLC：`文件 → 打开网络串流`，填入上面的地址即可。UDP 也可用，但 TCP 在 Wi-Fi 下更稳定。

### 手机 ONVIF App

1. 手机与 Mac 连同一个 Wi-Fi / 局域网
2. 在 App 里选择“搜索设备”或“添加设备”
3. 应能看到 `Fake Web Camera`
4. 账号 `admin`，密码 `admin123`

部分 App 只支持手动输入，填写：

- 主机：Mac 的局域网 IP
- ONVIF 端口：`8080`
- RTSP 端口：`8554`
- 用户名 / 密码：`admin` / `admin123`

### ONVIF Device Manager / 命令行

```bash
# 设备信息（允许匿名）
curl -s http://192.168.1.10:8080/onvif/device_service \
  -H 'Content-Type: application/soap+xml; charset=utf-8' \
  -H 'SOAPAction: "http://www.onvif.org/ver10/device/wsdl/GetDeviceInformation"' \
  --data '<?xml version="1.0"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><tds:GetDeviceInformation xmlns:tds="http://www.onvif.org/ver10/device/wsdl"/></s:Body></s:Envelope>'
```

探测局域网内的 ONVIF 设备：

```bash
./build/fakecam probe --timeout 4s
```

### NVR

在 NVR 的“手动添加 / ONVIF”里填写：

| 字段 | 值 |
| --- | --- |
| 协议 | ONVIF |
| IP | Mac 的局域网 IP |
| 端口 | 8080 |
| 用户名 | admin |
| 密码 | admin123 |
| 通道 | 1（H.264）；部分 NVR 会读取第二个 MJPEG profile |

NVR 兼容性差异较大，建议先用 ONVIF Device Manager 确认能正常拿到 Profile 和 StreamUri。

## 配置

完整注释见 [`config.example.yaml`](config.example.yaml)。常用项：

| 配置 | 默认 | 说明 |
| --- | --- | --- |
| `server.rtsp_port` | 8554 | 改成 554 需要 root |
| `server.http_port` | 8080 | ONVIF + 控制台 |
| `server.interface` | auto | 发现与地址广播使用的网卡 |
| `server.udp_rtp_port` / `udp_rtcp_port` | 8000 / 8001 | 都设为 0 关闭 UDP |
| `auth.enabled` | true | 关闭后无需认证 |
| `auth.username` / `password` | admin / admin123 | RTSP 与 ONVIF 共用 |
| `video.source` | 空 | 空或不可读时使用测试图案 |
| `video.width` / `height` / `fps` | 1920×1080@25 | H.264 输出规格 |
| `video.encoder` | auto | auto / videotoolbox / libx264 |
| `mjpeg.width` / `height` | 1280×720 | 必须是 8 的倍数 |
| `mjpeg.enabled` | true | 是否提供 `/stream2` |
| `onvif.motion_simulation` | false | 周期性模拟移动事件 |
| `web.allow_lan` | false | 是否允许局域网访问控制台 |

配置文件权限为 `0600`，保存时原子写入。修改配置后服务会自动重启所有组件。

## 命令行

```
fakecam run [--config PATH] [--menubar]   启动服务（默认）
fakecam doctor [--config PATH]            检查 ffmpeg、网卡、端口
fakecam probe [--interface NAME]          主动探测局域网 ONVIF 设备
fakecam version                           打印版本
```

`--menubar` 或从 `.app` 启动时进入菜单栏模式。也可以设置环境变量 `FAKEWEBCAMERA_CONFIG` 指定配置文件。

## 工作原理

```
ffmpeg ──H.264/MPEG-TS──> MPEG-TS 解析 ──AU──> gortsplib ServerStream ──> RTSP /stream1
   └────MJPEG/JPEG──────> JPEG 切帧 ──┬──> gortsplib ServerStream ──> RTSP /stream2
                                      └──> 最新帧缓存 ──> /snapshot.jpg + 控制台预览
```

- 单个 ffmpeg 进程通过 `pipe:1` 输出 H.264（MPEG-TS），`pipe:3` 输出 MJPEG，避免重复解码。
- RTP 时间戳按墙钟生成（90 kHz），视频循环或源卡顿时不会出现时间戳回退。
- H.264 首个 SPS/PPS 到达后才创建 RTSP stream，保证 DESCRIBE 返回的 SDP 有效。
- 配置变更采用“保存 → 异步重启全部组件”，避免热更新竞态。
- WS-Discovery 按收包网卡的 `IfIndex` 选择返回的 IP，多网卡（Wi-Fi + 有线 + VPN）下地址正确。

## 参与贡献

欢迎提交问题和 Pull Request；开发及提交说明见 [CONTRIBUTING.md](CONTRIBUTING.md)。

## 开发

```bash
make test     # 单元测试
make vet      # go vet
make fmt      # gofmt
make tidy     # 整理依赖
```

目录结构：

```
cmd/fakecam/             CLI 入口（run / doctor / probe）
internal/app/            组件编排与状态
internal/config/         配置加载、校验、原子保存
internal/media/          ffmpeg 管线、MPEG-TS 解析、JPEG 切帧、帧缓存
internal/rtsp/           RTSP 服务与认证
internal/onvif/          SOAP 分发、Device / Media1 / Media2 / Events
internal/discovery/      WS-Discovery
internal/httpapi/        HTTP API、快照、预览、内嵌控制台
internal/menubar/        macOS 菜单栏（systray）
internal/netutil/        网卡选择
internal/logging/        日志环形缓冲
scripts/build-app.sh     打包 .app
```

依赖固定在 `gortsplib/v5 v5.6.0` 与 `mediacommon/v2 v2.9.0`：更高版本要求 Go 1.26。

## 常见问题

**手机/NVR 搜索不到设备**

- 确认处于同一网段，且 Mac 没有开启会隔离客户端的“访客网络”
- 首次运行时 macOS 会询问“本地网络”权限，必须允许
- 检查 **系统设置 → 网络 → 防火墙**，允许 FakeWebCamera 接收入站连接
- 用 `./build/fakecam probe` 确认组播 3702 正常
- 若同时运行了其它 ONVIF 服务，3702 端口会被占用（`doctor` 会提示）
- **VPN / 代理的 TUN 模式会接管 `224.0.0.0/4` 组播路由**，导致 WS-Discovery 发送失败
  （日志出现 `sendmsg: no route to host`）。请在 VPN 规则中排除 `239.255.255.250`，
  或临时关闭 TUN 模式；即使发现失败，仍可手动填写 IP 使用 ONVIF / RTSP

**能搜到但连不上**

- RTSP 默认 8554，不是 554；在 App 里手动改端口
- 确认账号密码；可先关闭 `auth.enabled` 排除认证问题
- 优先使用 TCP 传输

**画面卡顿或黑屏**

- `doctor` 检查 ffmpeg 是否存在
- 查看控制台日志里的 `[ffmpeg]` 行
- VideoToolbox 在某些视频源上会失败，可把 `video.encoder` 改成 `libx264`
- 4K 视频建议先降到 1080p 输出

**MJPEG 报 `width/height must be multiple of 8`**

RTP/JPEG 要求宽高为 8 的倍数，例如 960×544 而不是 960×540。

**端口 554 启动失败**

1024 以下端口需要 root：

```bash
sudo ./build/fakecam run --config config.yaml
```

**开机自启**

菜单栏里勾选“开机自启”，会写入
`~/Library/LaunchAgents/com.scaperow.fakewebcamera.plist`。取消勾选会移除该文件。

## 限制

- 只做视频，不含音频与双向对讲
- 不支持 PTZ、录像回放、OSD 叠加
- ONVIF 的配置类接口（`SetVideoEncoderConfiguration` 等）返回成功但不改变运行参数；请通过控制台修改
- 未做 App Sandbox（需要监听端口、组播和启动 ffmpeg）
- 未经真实海康/大华 NVR 验证，兼容性以 ONVIF Device Manager 与通用手机 App 为准

## License

本项目采用 [MIT 许可证](LICENSE)。
