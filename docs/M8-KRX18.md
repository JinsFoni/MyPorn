# M8 — krx18 影片链接导入 + 在线播放

> **状态（2026-11-04）**：已实现并真机验证。用户给的那一部
> `https://krx18.com/movies/office-wifes-affair-2026` 现在贴进「发现」就能在站内播：
> **1920×1080 / h264 / 1:00:08 / 静音实播通过**，不用再下 1.4 GB。
>
> 与 M6/M7 的关系：这次没有「订阅」这一层——krx18 只有 `/movies`、`/genre`、`/release`，
> 没有艺人主页。所以入口换成**贴影片链接导入**，横切设施（代理转发 `/api/stream`、
> 变体表 `video_streams`、播放页、画质选择）全部复用。

一句话：**贴一个 krx18 影片页链接 → 解析出 HLS 清单 → 站内直接播（1080p），分片走本机代理。**

---

## 1. 范围

| 做 | 不做 |
|---|---|
| 贴影片页链接导入（`krx18.com/movies/<slug>`）→ 落库 → 跳详情页 → 在线播 | 影片/分类浏览、搜索、批量导入 |
| HLS 清单改写 + 分片代理（`/api/krx18/playlist.m3u8` → `/api/stream?p=krx18&u=…`） | 站内没有的播放器（见 §7） |
| 分辨率/码率实测探测（`ffprobe` 首段 1 MB） | 字幕（站点自己挂 Eng Sub，拿不到独立字幕轨） |
| Cloudflare 指纹绕过（uTLS + HTTP/2） | 登录态 / 会员内容（本站不需要登录） |

## 2. 四跳链路（实测 2026-11-04，本机家宽直连、无代理）

| # | 请求 | 实测耗时 | 备注 |
|---|---|---|---|
| 1 | `GET /movies/<slug>` | 1.4 ~ 1.9 s | 被 Cloudflare 挡的那一跳，必须 uTLS（§3） |
| 2 | `POST /wp-admin/admin-ajax.php`（`action=doo_player_ajax&post=<id>&nume=<n>&type=movie`） | 0.7 ~ 1.4 s | 要 `X-Requested-With: XMLHttpRequest` + `Referer`，回 `{"embed_url":…}` |
| 3 | `GET` embed 页（`cdn.loadvid.com/videos/play/<id>`） | 0.8 ~ 1.0 s | 从 `window.LoadVidConfig` 里抠 `videoHash:` / `videoToken:` |
| 4 | `POST https://cdn.loadvid.com/videos/resolve-token`（`{token,hash}`） | 0.8 ~ 1.2 s | 回**清单正文**（74,699 B） |

合计 4.4 ~ 5.9 s。`POST /api/videos/import` 整个来回（四跳 + 拿首段 1 MB 探分辨率）实测
**4.3 s / 6.8 s** 两次，返回 `{id:3253, durationSec:3608}`。

## 3. Cloudflare：只有 `krx18.com` 需要浏览器指纹

同一台机器、同一个时刻：

| 客户端 | 结果 |
|---|---|
| Go 默认 `net/http` | **403**，响应头 `cf-mitigated: challenge`，正文 161,478 B 的「Just a moment...」 |
| `curl`（默认 TLS） | 同上 403 |
| `utls` `HelloChrome_Auto` + `golang.org/x/net/http2.Transport` | **200**，161,478 B 的真页面 |

坑：`http.Transport` 上挂 `DialTLSContext` 换成 uTLS 握手**不行**——拿到的是 HTTP/2 连接却按
HTTP/1.1 解析，报 `malformed HTTP response`。必须用 `http2.Transport{DialTLSContext: …}`。
UA 也必须跟着指纹一起改（用 Chrome 的 UA 配 Chrome 的指纹）。

封装在 `internal/platform/fingerprint.go`：`NewBrowserHTTPClient(timeout)`，带 cookie jar。
分片那一段（`server7.stream-baby1.top`）不需要它——不需要 Referer、不需要 UA、CORS 全开——
但既然已经建好了客户端，就一起走，实测不比裸 `http.Client` 慢（见 §6 的对照）。

## 4. 清单与分片

- 单档**媒体清单**（不是主清单）：`#EXT-X-VERSION:3`、`#EXT-X-TARGETDURATION:8`、
  **没有 `#EXT-X-MAP`**（所以不需要 init 段，`hls.js` 当纯 TS 序列播）、557 条 `#EXTINF`，
  总时长 3608.1 s。
- 分片 URL 形如
  `https://server7.stream-baby1.top/user/708/hls/1/97ce22b2-…-1788084169/<8hex>.png`，
  平均 2.5 MB / 6.5 s。**后缀是 `.png`、`Content-Type` 也写 `image/png`，内容其实是 MPEG-TS**
  （首字节 `0x47`）。别按 mime 过滤，会一个分片都取不到。
- 分片地址里既没有签名也没有过期时间：同一条地址隔两天再取照样 200。所以清单缓存 30 分钟
  （`playlistTTL`）——换清单这一步要 4~11 s，而播放器每次起播都会来要一次清单。

## 5. 分辨率/码率是探出来的，不是读出来的

清单里没有 `RESOLUTION` / `BANDWIDTH`，直接按清单做变体会显示「0p」。所以导入时用
`Range: bytes=0-1048575` 拿首段前 1 MB 写临时文件、`ffprobe -f mpegts` 量出：

| 项 | 值 |
|---|---|
| 编码 / 分辨率 | `h264` / `1920×1080` |
| 视频码率（`stream=bit_rate`） | 2,803,782 |
| 整段实测（`ffprobe` 全片） | 1,356,121,692 B / 3608.234667 s → 3,006,726 |
| 界面显示的估算值（`总字节×8/段时长`） | 2,991,991（与真实差 **0.5%**） |

`ffprobe` 的路径沿用设置里的 `ffprobe_path`（`cfg.FFprobePath`）。

## 6. 播放通路与实测

```
播放页 → GET /api/videos/<id>/playback
         变体表里 krx18 这条的 URL 是**空**的（播放地址要现算），handlePlayback 用
         SitePlaylistProvider.PlaylistPath(slug) 合成 /api/krx18/playlist.m3u8?v=<slug>
       → 该端点取清单（缓存命中 22 ms / 冷 4~11 s），把 557 条分片改写成
         /api/stream?p=krx18&u=<urlencoded>，正文 96,979 B，Content-Type application/vnd.apple.mpegurl
       → hls.js 逐段拉 /api/stream，本机代理转发上游分片
```

浏览器实测（`playwright-cli`，元素 `muted` + `v.muted = true`）：

| 指标 | 值 |
|---|---|
| 画面 / `duration` | 1920×1080（`readyState 4`）/ 3608.137974 |
| 冷起播（重启服务端 + 全新浏览器会话） | 清单 11.4 s（四跳 + 探分辨率）+ 首段 7.6 s = **19.5 s**，那会儿 CDN 只跑 0.35 MB/s |
| 二次起播（清单命中适配器缓存） | **211 ms**（浏览器缓存里也还有分片） |
| 缓冲 60 s 后 ahead | 60.2 s |
| 60 s 墙钟推进 | 54.1 s（前 20 s 有两次短 buffer，之后 40 s 推进 38.9 s ≈ 实时） |
| 跳转到 1800 s / 3000 s / 3590 s 到可播 | 3.33 s / 1.84 s / 2.27 s |
| 首段 2,695,785 B 通过代理 | 1.50 ~ 3.09 s |

uTLS vs 裸 `http.Client` 取同一分片（交替各 6 次，两次实验）：

| 实验 | uTLS 中位数 | 裸客户端中位数 |
|---|---|---|
| A | 1.35 s | 4.26 s |
| B | 3.28 s | 4.30 s |

两次都是 HTTP/2.0，所以不是「uTLS 更慢」，而是链路本身在这个量级上抖。

## 7. 覆盖面：**只支持 loadvid 这一个播放器**（这条要看清）

站点给每部片配了 1~3 个「Server」，指向不同 embed 站。实测抽样 **34 部**
（`/movies` 首页 + `/movies/page/3` + `/movies/page/6` + `/genre/eng-sub` + `/genre/korea`）：

| 播放器 | 抽样命中 | 状态 |
|---|---|---|
| `cdn.loadvid.com` | **1**（用户点名那部，Server 2） | ✅ 已支持 |
| `play.playkrx18.site` | 33 | ❌ 暂不支持 |
| `mov18plus.cloud` | 33 | ❌ 暂不支持 |
| `openload.co` / `streamango.com`（老片） | 若干（2017 等老片） | ❌ 已停服 |

不支持的两家不是「少写了个正则」：

- `play.playkrx18.site`：播放逻辑在 `appv1.js`（javascript-obfuscator 混淆，字符串表 + WebCrypto），
  页面本身还挂 CF Turnstile——无头浏览器直接被挑战页挡在门口（`HTTP 403 / 请稍候…`）。
- `mov18plus.cloud`：把 `media` 字段加密了，解出来的 id 指向 `player.abyssplayer.com/<id>`，
  实测那页只回一个占位视频（标题 `bbb_sunflower_2160p_30fps_normal.mp4`）。

所以导入接口会**如实报是哪个播放器**，而不是一句「解析失败」：

```
{"error":{"code":"resolve_failed","message":"解析这部影片失败: 这部片没有能用的播放器
 （Server 2 用的是 mov18plus.cloud 播放器（暂不支持在线播放）；
   Server 1 用的是 play.playkrx18.site 播放器（暂不支持在线播放））"}}
```

老片（`living-with-my-cousin-2017`）：

```
（Server 2 用的是 openload（2019 年已关站）；Server 1 用的是 streamango（已关站））
```

要覆盖剩下 97% 的片子，得先啃上面两家之一（工作量估半天到一天，成功率不确定）。

## 8. 顺手修掉的一个播放器 bug（`VideoPlayer.svelte`）

看门狗（`stallTick`）靠 `fragInFlight` 判「引擎手上有没有分片在下载」。问题：
**监听是在 `stallTick` 第一秒才挂上的，而第一个分片 100~200 ms 就开始下了**——那次
`hlsFragLoading` 没人听见，`fragInFlight` 停在 0，第 3 秒的 nudge 就把这条**正在下**的首段
`startLoad()` 掐掉重来。

krx18 上量得很清楚（首段 2,695,785 B）：被掐的那次**只发出 300 B**、耗时 2.87 s，
重来的那条 4.39 s，起播从 4.9 s 变成 7.8 s。

修法：加 `fragFirstSeenAt` / `fragWatchSince` 和一个 `FIRST_FRAG_GRACE_MS = 15 s` 宽限——
「一个分片事件都还没见过」的头 15 秒不当卡死处理。15 s 盖得住实测最慢的首段（CDN 抖动那
次 7.6 s），真空转超过 15 s 才允许掐。修完冷起播只发**一次**首段请求（网络面板里 1 条
`/api/stream…` 200，不再有 300 B 的 abort）。

## 9. 前端改动

- 「发现」页的输入框现在同时认三种东西：艺人名字、艺人主页链接、**影片页链接**。
  贴影片页 → `POST /api/videos/import` → toast「已导入《…》，正在打开」→ 跳 `/video/<id>`。
- 平台 chip 用 `movieOnly` 过滤：krx18 没有艺人主页，不该出现在「搜艺人」的平台选择里
  （`GET /api/platforms` 新增 `movieOnly` 字段）。
- 新平台图标 `web/src/assets/platform/krx18.svg`（红底白「18」+ 虚线环，照站点 logo 的调子）。
- 已知小瑕疵：发现页对任何链接都先试 `/api/performers/resolve`，影片链接那一次会 400
  （浏览器控制台会留一条 `Failed to load resource: 400`），随后才走导入。功能无影响。

## 10. 复现

```bash
export GOMODCACHE=$PWD/.gocache/mod GOCACHE=$PWD/.gocache/build \
       GOPATH=$PWD/.gocache/gopath GOTMPDIR=$PWD/.gocache/tmp
make build
PORT=18081 YTDLP_PATH=$PWD/.tmp/ytvenv/bin/yt-dlp nohup ./bin/myporn >> .tmp/run/run.log 2>&1 &
# 登录（密码在 .env.local 的 APP_PASSWORD）
curl -s -c .tmp/cookie.txt -X POST localhost:18081/api/auth/login -H 'Content-Type: application/json' -d '{"password":"…"}'
# 导入
curl -s -b .tmp/cookie.txt -X POST localhost:18081/api/videos/import \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://krx18.com/movies/office-wifes-affair-2026"}'
# 播放信息
curl -s -b .tmp/cookie.txt localhost:18081/api/videos/<id>/playback
# 改写后的清单
curl -s -b .tmp/cookie.txt 'localhost:18081/api/krx18/playlist.m3u8?v=office-wifes-affair-2026' | head -8
```

单测：`go test ./internal/platform/krx18/`（解析影片页 / embed 配置 / 清单 / iframe 抠地址 /
播放器判别，夹具是抓下来的真 HTML 片段）。
