# 更新记录

## v1.1.0

- 不需要服务器也能用：下载一个程序，`xueer-speaker login` 登录小米，就能让小爱用原声说话；`xueer-speaker mcp` 直接给 Claude Code、Codex 用。Linux / macOS / Windows 各一个单文件，`install.sh` 一行装好。
- AI 工具合成两个：`speaker_say`（念一句）和 `speaker`（`status` / `volume` / `dnd` / `do`），原来的 `speaker_status` 改成 `speaker` 的 `status`。
- 勿扰：交代完任务去睡觉时打开，说的话只记下不出声，到点自动关，关的时候念一句攒了几句。面板顶上也能开关。
- 让小爱做事：「明天八点叫我」这类，拒绝买东西、付款、打电话、发消息。
- 调音量：读或设音箱音量。
- 自检：`xueer-speaker test` 和面板「测试一下」，一步一步说卡在哪。

## v1.0.1

- 说明和面板以「AI 干完活让音箱叫你」为主：没接 Husic 时，面板只留音箱状态、音量、定时和说过的话（不再显示放歌、听口令）；README 把托盘、脚本接口、Husic 放歌挪到「进阶（可选）」。

## v1.0.0

第一个公开版本。

- AI 客户端通过 MCP 让小爱音箱说话（`speaker_say`、`speaker_status`），用 Edge 晓晓音色，失败时换成小爱原声。
- 面板：自带密码登录；在网页里登录小米（支持短信验证码）、选音箱；一键复制 Claude Code、Codex 等客户端的配置。
- 电脑开着时播报从电脑出声（Windows 托盘），关着时音箱自己念。
- 接 Husic 后可以喊「播放我的歌单」，放歌时能说「下一首」「暂停」「继续」「停止」。
- 定时：每周几或只一次，放歌、停、说一句。
- `POST /v1/say` 给脚本和机器人用。
- 一条 `docker compose up -d` 启动；镜像支持 amd64 和 arm64。
