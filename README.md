# 雪儿 speaker

**让家里的小爱音箱替 AI 开口。** Claude Code、Codex 这类 AI 助手干完活，用「晓晓」的声音念一句结果：做好了、出错了、要你回来定个事。脚本、机器人、定时提醒也能让它说话；装了 [Husic](https://github.com/hardware661/Husic) 的话，对音箱说一句「播放我的歌单」就放你的歌。

不刷机，原生小爱的功能都还在。

![面板：正在放的歌、定时、说过的话](docs/screenshots/panel.webp)

## 能做什么

- **AI 叫你。** 内置 MCP 服务，AI 客户端加上就有 `speaker_say`（念一句）和 `speaker_status`（音箱状态）两个工具。面板「设置 → 接入」里有 Claude Code、Codex 等客户端的配置，复制就能用。
- **好听，不吵。** 用 Edge 的晓晓音色，合成失败时换成小爱原声。同一句话 10 秒内不重复念，每个来源一分钟最多 6 句。
- **电脑开着就从电脑出声。** 装上 Windows 托盘后，电脑开着时播报从电脑放（电脑平时用蓝牙连着音箱，声音照样从音箱出来，也不会打断你正在听的歌）。电脑关着，音箱自己念。
- **喊一句放歌**（要接 Husic）。「播放我的歌单」「放一下我喜欢的歌」「播放我的通勤歌单」；放歌时还能说「下一首」「暂停」「继续」「停止」。
- **面板。** 手机电脑都能打开：随手念一句、调音量、设定时（每周几或只一次，放歌、停、说一句），还能看到说过的话和对小爱说过的话。
- **给脚本用的接口。** `POST /v1/say` 一行 curl 就能让音箱说话，适合告警、天气、家里的自动化。

## 三步用起来

需要一台和音箱在**同一个局域网**的机器（NAS、旧电脑、树莓派都行），装好 Docker：

```bash
git clone https://github.com/hardware661/xueer-speaker.git && cd xueer-speaker
docker compose up -d
docker compose logs speaker | grep setup_code   # 记下 6 位初始化码
```

1. 用手机或电脑打开 `http://<这台机器的局域网 IP>:8100`，填初始化码，设一个面板密码。
2. 在「设置 → 小米」登录音箱绑定的小米账号，收到短信验证码就填进去，然后选一台音箱。
3. 在「设置 → 接入」复制配置，交给你的 AI 客户端。

试一下：面板右下角输入「你好」，音箱就会说话。

> 第一次打开面板时请用局域网 IP 访问，不要用 `localhost`：音箱要按这个地址来取语音。地址不对的话，在「设置 → 接入」最下面改。

![设置 → 接入：复制给 AI 客户端的配置](docs/screenshots/connect.webp)

## 接入 AI

面板「设置 → 接入」里的配置已经带好地址和钥匙，下面只是说明长什么样。

**Claude Code**

```bash
claude mcp add --transport http speaker http://192.168.1.10:8100/mcp --header "Authorization: Bearer <钥匙>"
```

**Codex**（`~/.codex/config.toml`）

```toml
[mcp_servers.speaker]
url = "http://192.168.1.10:8100/mcp"
http_headers = { Authorization = "Bearer <钥匙>" }
```

然后在给 AI 的规则里（如 `CLAUDE.md`、`AGENTS.md`）加一句：

```
每次回复我之前调用一次 speaker_say，说清是哪个项目、结果怎样。
```

工具说明里已经写好了怎么说话：先说项目、再说结果，坏消息直说，80 字以内。

只支持 stdio 的 MCP 客户端，可以用程序自带的转发：`xueer-speaker mcp-local -url http://…/mcp -key-file ~/.speaker.key`（钥匙文件权限要是 600）。

## 让脚本说话

`/v1/say` 在 8101 端口上，默认只有这台机器能访问。钥匙在面板「设置 → 接入」的配置里，也可以在数据卷的 `keys.json` 里找到 `bot` 那一项：

```bash
curl -H "Authorization: Bearer <bot 钥匙>" -H "Content-Type: application/json" \
  -d '{"text":"主人，备份做完了","source":"backup"}' http://127.0.0.1:8101/v1/say
```

返回 `202` 表示已经排上队。太长（超过 80 字）返回 `400`，说得太频繁返回 `429`。

## Windows 托盘（可选）

电脑开着的时候，让播报从电脑出声：

1. 在 [Releases](https://github.com/hardware661/xueer-speaker/releases) 下载 `xueer-speaker-windows-x64-*.zip`，解压到 `%LOCALAPPDATA%\xueer-speaker`。
2. 运行 `XueerSpeaker.exe`，填服务器地址和电脑钥匙（面板「设置 → 接入」最后一块）。
3. 托盘图标变黑就是连上了。右键菜单里可以开机自启、暂停电脑播报、改连接。

需要 Windows 10/11 和 Microsoft Edge WebView2（Windows 11 自带）。

## 接 Husic 放歌（可选）

1. 给 Husic 开一个服务钥匙：启动 Husic 时加 `HUSIC_SERVICE_KEY_FILE=/path/to/service.key`（见 Husic 的部署文档）。
2. 把同一个钥匙文件放进 speaker 能读到的地方，在 `.env` 里写：

   ```
   XS_HUSIC=http://husic:18788
   XS_HUSIC_KEY=/data/husic.key
   ```

3. `docker compose up -d` 重建。面板顶上出现 Husic 灯，就能放歌了。

音箱只能放网易云的 mp3，所以只会放 Husic 里有网易云来源的歌。

## 设置

所有设置都可以写在 `.env` 里（参考 `.env.example`），也可以作为 `serve` 的参数。`xueer-speaker serve -h` 能列出全部。

| 环境变量 | 默认 | 作用 |
| --- | --- | --- |
| `XS_BASE` | 第一次打开面板的地址 | 音箱取语音的地址，必须是音箱访问得到的 |
| `XS_SPEAKER` | 面板里选 | 账号下有好几台音箱时用哪台（名字、did 或 MAC） |
| `XS_PERSONA` / `XS_OWNER` | `雪儿` / `主人` | AI 说话时的名字和对你的称呼 |
| `TZ` | `Asia/Shanghai` | 定时用的时区 |
| `XS_HUSIC` / `XS_HUSIC_KEY` | 不接 | Husic 地址和服务钥匙文件 |
| `XS_REMINDERS` | 不接 | 面板要列出的提醒来源（GET 返回 `[{"at":"…","text":"…"}]`） |
| `XS_AUTH` | `password` | 改成 `header`：前面的反向代理负责登录，并设置 `X-Owner-User` |

数据全部在 `speaker-data` 卷里：`speaker.db`（说过的话、定时，保留 30 天）、`keys.json`（各种钥匙）、`xiaomi.json`（小米登录）。备份就备份这个卷。

## 放到服务器上

音箱要能访问到 speaker 的地址，所以最简单的部署就是放在家里。如果放在公网服务器上：

- 前面加一层 HTTPS 反向代理（Caddy、nginx），`XS_BASE` 写 `https://你的域名`。
- `/a/` 是音箱取语音的路径，音箱不会带登录信息，**不要给它加登录**。链接本身是 32 字节随机串，几分钟后就失效。
- 面板自带密码登录。如果反向代理已经有统一登录，可以设 `XS_AUTH=header`，由代理设置 `X-Owner-User`。**代理一定要清掉客户端自己带来的这个头**，否则任何人都能进面板。

## 支持的音箱

实测过的只有 **Xiaomi 智能音箱 Pro（OH2P，固件 1.60.3）**。其他能放网络音乐的小爱音箱可能也能用，但播报和口令用到的接口各型号不一样，没有验证过。用别的型号试过的话，欢迎在 Issue 里说一声能不能用。

## 常见问题

**收不到验证码 / 提示「小米暂时限流」？** 短时间内登录太多次会被小米限流，等两小时再试。建议专门开一个小米小号，把音箱共享给它。

**面板显示音箱在线，但没有声音？** 多半是音箱取不到语音：看「设置 → 接入」最下面的地址，用手机连上同一个 Wi-Fi，打开这个地址能看到面板才算对。

**念到一半被打断？** 电脑在通过蓝牙往音箱放声音时，音箱念不了。装上 Windows 托盘，让播报从电脑出。

**AI 每次都要调用，会很吵吗？** 同一句话 10 秒内只念一次，每个来源一分钟最多 6 句。面板里也可以随时停。

## 开发

```bash
go test -race ./...                       # 全部测试，用假的小米云，不碰真机
go run ./cmd/xueer-speaker serve -data ./data -cache ./data/cache -edge-tts edge-tts
```

需要 Go 1.25，本机要装 `edge-tts`（`pip install edge-tts`）。Windows 托盘的构建见 [`pc/README.md`](pc/README.md)。代码结构和约定见 [CONTRIBUTING.md](CONTRIBUTING.md)。

## 参考

小米接口的用法参考了这些项目（没有复制代码）：

- [MiService](https://github.com/yihong0618/MiService)：MIoT 签名、MiNA 接口
- [xiaomusic](https://github.com/hanxi/xiaomusic)：`player_play_music` 的参数和各型号的差别
- [migpt-next](https://github.com/idootop/migpt-next)：对话记录接口
- [hass-xiaomi-miot](https://github.com/al-one/hass-xiaomi-miot) PR #2970：推送后把循环模式设为 2，音箱只念一遍，念完把声音还给蓝牙

## 许可

[MIT](LICENSE)。第三方组件的许可见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。

本项目和小米公司没有关系。「小爱同学」「Xiaomi」是小米的商标。用到的小米接口没有公开文档，小米随时可能修改。
