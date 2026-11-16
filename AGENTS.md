# xueer-speaker · AGENTS

让小爱音箱替 AI 开口：Go 服务（MCP、面板、`/v1/say`）+ Windows 托盘，不刷机。本仓是公开开源版。

## 先读

1. `README.md`（功能与部署）
2. `CONTRIBUTING.md`（目录、本地开发、提交前检查）

## 常用命令

```bash
gofmt -l . && go vet ./... && GOOS=windows go vet ./... && go test -race ./...
go run ./cmd/xueer-speaker serve -data ./data -cache ./data/cache -public 127.0.0.1:8100
```

## 分层

- `internal/xiaomi` 是唯一和小米云打交道的地方；`internal/conductor` 是唯一直接操作音箱的地方，其他包只发事件给它。
- `internal/server` 只做 HTTP、登录和 MCP，调下层；面板静态文件在 `internal/server/web`。
- 电脑端：`internal/pcmouth`（服务器这头）↔ `internal/pcagent` + `cmd/xueer-speaker-pc`（Go 引擎）↔ `pc/`（C# 托盘）。

## 禁止

- 公开仓：不提交小米账号、钥匙、私人部署信息或交接记录（线上部署与交接放在作者自己的私有仓和运维仓）。
- 不刷机，不改原生小爱的行为。
- 别连续重试小米登录（会被限流两小时）。
- 修 bug 先写能复现的测试。
- 推送、打标签、发 Release、推镜像每一步都要主人当次同意。
