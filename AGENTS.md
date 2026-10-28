# AGENTS.md

给在这个仓库里干活的人和模型看的约定。只写「不看就会做错」的那些。

## 前端 / 真机验证用哪个浏览器

**用 `playwright-cli`（仓库里已经配好：`.playwright/cli.config.json`，日志落在 `.playwright-cli/`），
或者 harness 自带的浏览器。**

**不要**再起/连外部 Chrome 跑测试（比如某个固定在 `:9222` 上、靠 `browser-tools` 系列脚本驱动的那种）。
理由：那套东西不在本仓库的依赖里，启动方式、profile、缓存策略都不受控，
同一份构建在它上面和一个干净的浏览器里表现可能不一样——排查前端问题时这会变成假线索。

配套经验：本项目带 Service Worker（PWA），换构建后**必须**先
`navigator.serviceWorker.getRegistrations()` 全部 unregister + `caches.keys()` 全部 delete，
否则页面会一直跑旧 chunk（`index-XXXXXXXX.js` 的 hash 对不上就是它），
看起来像「改了没生效」。

**播放测试一律静音**（元素加 `muted`、再 `v.muted = true`）。测试视频的声音会从这台机器的
音箱里放出来，人还坐在前面。

**无头 Chromium 的「解码」结论不能当结论**：4K VP9 在无头里会随机报
`PipelineStatus::PIPELINE_ERROR_DECODE: video decode error!`（同一份字节 ffmpeg 能解、md5 与上游一致），
量链路/缓存用 curl 或片段计时，别用无头浏览器的卡顿反推服务端有问题。

## 构建与运行

```bash
export GOMODCACHE=$PWD/.gocache/mod GOCACHE=$PWD/.gocache/build \
       GOPATH=$PWD/.gocache/gopath GOTMPDIR=$PWD/.gocache/tmp
make build          # 前端 + 后端（web/dist 会被拷进 internal/webui/dist）
make backend        # 只重编二进制
make web            # 只编前端
```

```bash
PORT=18081 YTDLP_PATH=/tmp/ytvenv/bin/yt-dlp nohup ./bin/myporn >> .tmp/run/run.log 2>&1 &
# 登录拿 cookie：POST /api/auth/login {"password":"..."} → .tmp/cookie.txt
```

数据库在 `.tmp/run/data/myporn.db`（**不是** `.tmp/myporn.db`，后者是早期踩坑留下的幻觉）。

## 提交

- 标题一行中文，动词开头，说清「改了什么」。禁止英文标题、禁止 `fix: xxx` 这类前缀。
- 正文写**证据**：实测数字、量到的时间/体积/分辨率、对照表。别人（和下一次的自己）
  要能只读 commit 就判断这次改动为什么是这么改的。别写「优化了体验」这种。
- 代码注释同样写实测事实，不写空话。

## 画质

默认取最高画质（2160p 优先），**永不静默降级**。降级必须让用户看见并可选。
B 站 4K 能不能放取决于 `dash.video` 里实际返回的 `id`，`accept_quality` / `support_formats`
永远列着 120，不能当依据（见 `docs/M7-BILIBILI.md` §7.9）。

## 提问

- 不要为了确认而提问，能自己量的就自己量，量完直接做。
- 需要选择时给建议 + 一句话理由，不给开放式问题。
