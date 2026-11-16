# 0001 晓晓声音用上游 edge-tts 命令行，不自己用 Go 重写

- 日期：2026-11-14
- 状态：已采用

## 背景

Edge 朗读接口要带 `Sec-MS-GEC` 签名。2026 年起上游 rany2/edge-tts 用的是 v2 算法（改过初始状态的 SHA-256 + 自定义 CS64，见上游 `src/edge_tts/drm.py`），微软改过不止一次。Go 社区的移植（wujunwei928/edge-tts-go 等）跟进有快有慢。

## 决定

容器里装锁定版本的上游 `edge-tts`（Python），`voice` 包用 `edge-tts --voice zh-CN-XiaoxiaoNeural --text … --write-media …` 生成 mp3。`voice.Engine` 接口后面有两个实现：真 edge-tts 命令、测试用的假引擎。

## 后果

- 微软改签名时：升级镜像里的 edge-tts 版本即可，不改 Go 代码。
- 镜像多一个 Python（约 50MB），每句多起一个进程（约 1 秒、30MB 内存，一次只跑一个）。
- Edge 不可用时降级到小爱原声，所以这条依赖坏了也不会"叫不出来"。
