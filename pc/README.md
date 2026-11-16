# Windows 托盘 XueerSpeaker.exe

电脑上的雪儿 speaker：托盘图标、右键菜单、左键打开的面板窗口（WebView2）。真正连服务器、播报、调系统音量的是同目录的 `xueer-speaker-pc.exe`（Go，`cmd/xueer-speaker-pc`）。托盘用 `-engine` 把它当子进程跑，通过标准输入输出通信：

- 托盘 → 引擎：一行 `pause` 或 `resume`。暂停电脑播报就是断开连接，服务器会把播报全交给音箱。
- 引擎 → 托盘：状态每变一次，输出一行 JSON，如 `{"connected":true,"paused":false}`。引擎退出后，托盘 5 秒后重启它。

菜单里的歌名、播放键、音量用电脑钥匙直接请求服务器：`GET /pc/status`、`POST /pc/music/{pause,resume,next,stop}`、`POST /pc/play`、`POST /pc/volume`。请求走 Windows 系统代理。

## 文件（`%LOCALAPPDATA%\xueer-speaker\`）

| 文件 | 内容 |
| --- | --- |
| `XueerSpeaker.exe`、`xueer-speaker-pc.exe` | 托盘和引擎 |
| `server.txt`、`pc.key` | 服务器地址和电脑钥匙；第一次运行时填，托盘菜单「连接设置…」可以改 |
| `pc.log` | 引擎日志 |
| `panel-profile\` | 面板窗口的浏览器数据（面板登录） |
| `Runtime\` | WebView2 Loader |

开机自启写在 `HKCU\Software\Microsoft\Windows\CurrentVersion\Run\XueerSpeakerPC`。

## 构建

用 Windows 自带的 .NET Framework C# 编译器，不用另装 SDK。在 WSL 里：

```bash
python3 pc/fetch-sdk.py     # 下载 WebView2 SDK 到 pc/vendor/（版本和 SHA256 锁在 packages.lock.json）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$(wslpath -w pc/build.ps1)"   # → pc/dist/XueerSpeaker.exe
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -H windowsgui" -o pc/dist/xueer-speaker-pc.exe ./cmd/xueer-speaker-pc
```

在 Windows 上直接跑 `pc\build.ps1` 也可以。Release 里的压缩包由 GitHub Actions 用同样的步骤打包。

升级：先退出托盘（它会等引擎念完当前这句再退），覆盖两个 exe，再启动 `XueerSpeaker.exe`。

自测：`xueer-speaker-pc.exe -once 某个.mp3` 会播放一遍然后退出，输出 `played ok`。
