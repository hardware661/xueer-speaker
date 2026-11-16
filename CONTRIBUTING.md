# 参与开发

欢迎提 Issue 和 PR。较大的改动请先开 Issue 聊聊想法。

## 目录

| 目录 | 内容 |
| --- | --- |
| `cmd/xueer-speaker` | 服务、排障命令行、给 stdio 客户端用的 `mcp-local` |
| `internal/xiaomi` | 小米云：密码与验证码登录、续期、MiNA、MIoT、蓝牙；小米的坑都关在这里 |
| `internal/conductor` | 唯一直接操作音箱的地方，播报、放歌、口令都发事件给它 |
| `internal/announcer` | 限流、去重、合成失败退回原声 |
| `internal/voice` | 调用 edge-tts、读 mp3 时长；`internal/audio`：给音箱的 `/a/` 临时链接 |
| `internal/listener`、`internal/command` | 读小爱对话记录、识别口令、决定做什么 |
| `internal/jukebox`、`internal/music` | 放什么、在哪放；读 Husic 曲库 |
| `internal/schedule`、`internal/store` | 定时；SQLite |
| `internal/server` | 三个端口的路由、面板登录、MCP、面板页面（`web/`） |
| `internal/pcmouth`、`internal/pcagent`、`cmd/xueer-speaker-pc`、`pc/` | 电脑端：服务器这头、Go 引擎、C# 托盘 |

## 本地开发

```bash
go run ./cmd/xueer-speaker serve -data ./data -cache ./data/cache -public 127.0.0.1:8100
```

本机要装 `edge-tts`（`pip install edge-tts`）。

## 提交前

```bash
gofmt -l . && go vet ./... && GOOS=windows go vet ./... && go test -race ./...
```

- 测试里的小米云是假的（`internal/xiaomi/fake_test.go`），照真实接口的行为写。改登录或接口时先在这里加上新行为。
- 改了托盘，请在 Windows 上按 `pc/README.md` 编一次。
- 修 bug 时请先写一个能复现问题的测试，确认它失败，再修。

## 真机

小米接口没有文档，同一个调用在不同型号上表现可能不同。真机上的怪现象先去同类项目（见 README「参考」）的 Issue 里搜一下。别连续重试登录：几次失败就会被小米限流两小时。
