# M8 — krx18 影片链接导入 + 在线播放

> **状态（2026-11-04）**：已实现并真机验证。用户给的那一部
> `https://krx18.com/movies/office-wifes-affair-2026` 现在贴进「发现」就能在站内播：
> **1920×1080 / h264 / 1:00:08 / 静音实播通过**，不用再下 1.4 GB。
>
> **2026-11-04 续**：加了独立的 `/krx18` 页面，订阅单位是 **tag 归档页**（`/tag/<女优名>/`）——
> 站上确实没有艺人主页，但每部片子都打了女优 tag，所以一位女优 = 一个 tag = 一条订阅。
> 实测订阅 `do-hee-%eb%8f%84%ed%9d%ac`：**3 页 / 92 部，6 秒回填完**，墙上点开静音实播通过
> （1920×1080，冷启动 9.4 s / 预热后 221 ms）。见 §11。
>
> 与 M6/M7 的关系：krx18 没有艺人主页，入口是**贴影片链接导入**（§9）+ **贴 tag 归档页订阅**（§11），
> 横切设施（代理转发 `/api/stream`、变体表 `video_streams`、播放页、画质选择、同步调度）全部复用。

一句话：**贴一个 krx18 影片页链接 → 解析出 HLS 清单 → 站内直接播（1080p），分片走本机代理。**

---

## 1. 范围

| 做 | 不做 |
|---|---|
| 贴影片页链接导入（`krx18.com/movies/<slug>`）→ 落库 → 跳详情页 → 在线播 | 批量导入、按分类浏览（`/genre`、`/release`） |
| 贴 tag 归档页订阅（`/tag/<女优名>/`）→ 分页回填 → `/krx18` 墙上点开就播 | 下载（上游只给清单正文、不下发地址，见 §11.4） |
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
- ~~平台 chip 用 `movieOnly` 过滤~~：**2026-11-04 已撤掉**。krx18 现在能按名字搜 tag、能订阅
  归档页，`GET /api/platforms` 不再回 `movieOnly`，`platform.MovieSite` 接口一起删了（见 §11.1）。
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

---

## 11. 续：tag 归档页订阅 + `/krx18` 独立页面（2026-11-04）

用户要的是「和 B 站 / YouTube 一样的单独一页，订阅单位是
`https://krx18.com/tag/do-hee-%eb%8f%84%ed%9d%ac/` 这种 tag 页」。

### 11.1 「艺人」在这里就是一个 tag

站上确实没有艺人主页（整站首页只有 `/movies`、`/genre`、`/release` 三种链接），
但**每部片子都打了 tag**，而 tag 归档页 `GET /tag/<slug>/` 就是「这位女优的全部片子」。
所以订阅这条路上「艺人」= 一个 tag，`performers.performer_id` 存的是 tag 的 slug。

| 数据 | 从哪来 | 实测 |
|---|---|---|
| 名字 / 片数 / 归档地址 | `GET /wp-json/wp/v2/tags?slug=<slug>` | 0.96 ~ 1.02 s，153 B（`_fields=id,count,name,slug,link`） |
| 按名字搜 | `GET /wp-json/wp/v2/tags?search=<q>&orderby=count&order=desc` | 0.80 ~ 0.99 s，774 B（`q=do hee` 命中 3 个 tag） |
| 影片列表 | 抓 `/tag/<slug>/`、`/tag/<slug>/page/<n>/` 的 HTML | 0.51 ~ 0.97 s / 150 ~ 181 KB / **每页 39 部** |

**影片的 `/wp-json/wp/v2/posts` 是空的**（2 字节 `[]`，站点把 posts 接口关了），
所以列表只能抓 HTML；tag 接口却开着，正好拿来解析订阅源的名字与总数。

两个坑（都踩过）：

1. **`slug` 字段本身是 percent-encoded 的**：标签接口回的是 `do-hee-%eb%8f%84%ed%9d%ac`
   （小写十六进制），而地址栏那条进 `url.Path` 是解码态 `do-hee-도희`。两种形态混用会出两个后果：
   比较永远不相等（「贴地址订阅」报「没有这个 tag」，实测就卡在这里），
   编码态再 `PathEscape` 会把 `%` 转成 `%25`、归档页 404。现在入库 / 比较 / 拼地址
   一律走解码后的这一份（`canonicalTagSlug`）。
2. **翻过末页是 404，不是空页**：`/page/4/` 实测 404 + 137 KB 的整站模板。
   适配器把上游 4xx 包成 `*statusError`，404 → `platform.ErrNotFound`，
   同步任务据此认「到头了」（`jobs.syncSubscription` 里那条 `page > 1 && errors.Is(...)`）。

### 11.2 回填与同步（复用现成调度，没有新表）

订阅源是 `performers` 的一行、影片是 `videos` 的一行（`platform='krx18'`），
所以调度、回填断点、限流退避、增量同步全是现成的那套（见 `internal/store/platform_channels.go` 顶部）。
唯一为它改的共用代码：全量回填遇到「库里已有的片」时补挂一次 `video_performers` 关联——
先贴影片页导入、后订阅 tag 的那部片子否则不会出现在 tag 的墙上（实测丢了一条：92 部只挂上 91 部）。

实测（本机家宽，无代理）：

| 动作 | 耗时 | 结果 |
|---|---|---|
| 订阅 `do-hee`（首次全量，3 页） | **6 s**（`14:06:08 → 14:06:14`） | 91 部入库（第 92 部此前已导入过） |
| 全量重扫（补挂关联） | 8 s | 92 部全部挂上 |
| 增量同步（第 1 页就撞到已知片即停） | 5 s | 新增 0 |
| 订阅 `do-hee-i`（单页 tag，10 部） | 4 s | 10 部入库，第 2 页 404 → 「翻到末页」收工 |

### 11.3 `/krx18` 页面（前端）

和 `/bilibili` 同构：顶部是按订阅源筛的头像条（点谁筛谁），下一行是「全部 / 未看」+
「全部标为已看」+「添加订阅」，主体是封面墙（`InfiniteList` + `VideoCard`），
筛选状态写在地址栏（`?tag=<performers.id>&mode=unseen`），点开是整页 `/video/<id>`。
侧边栏多一项 `krx18` 和未看角标（SSE 的 `video.new` 带 `platform`，只刷这一页的墙）。

- 添加订阅只认 tag 归档页地址（`/tag/…`）；贴影片页地址不会被当成 tag
  （`ParsePerformerURL` 与 `ParseVideoURL` 各认各的）。
- 「发现」页现在也能选 krx18 并按名字搜 tag：`movieOnly` / `platform.MovieSite` 已删除。
- 手机底栏跟着加到 **10 格**（关注 / 艺人 / 动态 / YouTube / B 站 / krx18 / 下载 / 媒体库 /
  管理 / 设置）。320px 实测每格 32px，带文字时「动态 / YouTube / B 站」会粘成一串、
  「媒体库」还会断成两行，所以标签在 ≤360px 改成 `sr-only`（只留图标，读屏照样念得出名字）、
  360~400px 字号降到 10px 且禁止折行；实测 390 / 430px 无溢出、320px 只留图标。
- **这一页没有下载键**（`VideoCard` 与 `/video/:id` 都对 krx18 隐藏）：原因见 11.4。

播放实测（从 `/krx18` 墙上点开 `office-wifes-affair-2026`，静音）：

| 指标 | 值 |
|---|---|
| 分辨率 / 时长 | 1920×1080 / 3608.14 s（h264） |
| 冷启动到 `readyState 4`（服务端清单缓存为空） | **9.39 s**（含四跳解析 4~6 s + 首段） |
| 预热后同一部再打开 | **221 ms** |
| 播放 25 s 时缓冲领先 | 59.3 s |

### 11.3.1 竖版封面（照原站的海报走）

2026-11-04 用户要求「改成和原站点一样的竖版封面」。原站是 DooPlay 主题，归档页的海报框
写死在 CSS 里：`.poster{padding-top:140%}`（即 **1 : 1.4** 的竖框，图片 `object-fit:cover`），
栅格是 `width:calc(100%/5)`（桌面 5 列，全宽布局 6 列），媒体查询里 ≤1130px 4 列、≤667px 3 列。
krx18 给的海报图也确实是竖的（实测 353×500）。

所以 `VideoCard` 多了一个 `portrait`：封面框从 `aspect-video` 换成 `aspect-[5/7]`（= 1.4，
和原站同一个比例）。默认值跟着平台走（`portrait = video.platform === 'krx18'`），
因为「封面的形状」属于上游数据长什么样，不属于某一条墙——同一部片在 `/krx18` 墙、
艺人页、播放页「相关影片」三处不该有三种裁法。整页 `/video/:id` 的播放器仍是 16:9，不动。

列数用 `poster-grid` 这个 @utility（`web/src/app.css`），实测（内容区 = 视口 − 侧边栏 240 − 页边 64）：

| 视口 | 列数 | 封面宽 | 封面比例 |
|---|---|---|---|
| 390 | 2 | 177 px | 1.40 |
| 820 | 3 | 255 px | 1.40 |
| 1024 | 3 | 224 px | 1.40 |
| 1280 | 4 | 229 px | 1.40 |
| 1440 | 5 | 213 px | 1.40 |
| 1536 | 5 | 232 px | 1.40 |
| 1700 | 6 | 219 px | 1.40 |
| 1920 | 6 | 255 px | 1.40 |

1440 下 5 列 213px，原站同宽是 5 列 232px —— 同一个密度。手机刻意比原站少一列
（原站 3 列，这里 2 列）：我们的卡片封面下面还挂着头像 + 标题 + 观看数 + 日期（原站只有标题和日期），
390px 上 3 列每格只剩 ~115px，那行字就没法看了。

**别用 Tailwind 的断点变体做这件事**：自定义断点（`@theme{--breakpoint-3xl:1400px}`）会被
Tailwind 4 排在 `sm`/`lg`/`xl` 之前，两条 `grid-template-columns` 打架时后出现的赢，
实测 1440 上仍然是 4 列（CSS 里 `min-width:1400px` 那条排在 `40rem/64rem/80rem` 前面）。
换成 `min-[1400px]:` 任意断点也一样。最后是手写媒体查询放进 `@utility poster-grid` 才可控。

### 11.3.2 关注流不收 krx18（同日按用户要求）

`store.FeedExcludedPlatforms` 从 `bilibili, youtube` 变成 `bilibili, youtube, krx18`：
订阅一个 tag 一次回填几十上百部，关注流那一页装的是「这两天谁发了新片」，
塞进来之后整页都是同一个人的片库。`CountNewVideos`（侧边栏「关注」角标）原来自己抄了一份
字面量，这次改成直接用 `FeedExcludedPlatforms`，免得下次加平台又漏一处。

排除只针对**没有指定艺人**的列表。带上 `performerId` 时一个平台都不排，否则艺人页会因为
「这个人的片子来自被排掉的站」而变空——2026-11-04 实测 `/performer/152`（Do Hee，库里 92 部）
改完之后直接 0 条、页面写「还没有同步到影片」。这条同时修好了 B 站 UP 主的艺人页
（`/performer/147` 之前也是 0 条，现在 100 条）。

### 11.4 不支持下载

上游 `resolve-token` 回的是 m3u8 **正文**、不是地址，档位的 `url` 因此是空的
（见 `variant()` 的注释）。原来的空地址会一路掉进 ffmpeg，报
`Error opening input file .`（2026-11-04 实测 job 744 失败原文）。
现在：① 下载任务在入口处直接回「这个站暂不支持下载…」；② 前端不显示下载键。

### 11.5 覆盖面还是那个老问题

§7 的结论没变：**只支持 `loadvid` 这一个播放器**（抽样 34 部里 1 部）。
`do-hee` 这个 tag 的 92 部里，点开会明确报「Server 2 用的是 mov18plus.cloud 播放器（暂不支持在线播放）；
Server 1 用的是 play.playkrx18.site 播放器（暂不支持在线播放）」——不是静默失败，但确实是少数能播。
要真正用起来，得先啃掉那两家播放器（§7 末）。

### 11.6 复现

```bash
# 订阅一个 tag（会立刻回填）
curl -s -b .tmp/cookie.txt -X POST localhost:18081/api/subscriptions \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://krx18.com/tag/do-hee-%EB%8F%84%ED%9D%AC/"}'
# 订阅源列表（含未看计数）
curl -s -b .tmp/cookie.txt localhost:18081/api/krx18/channels
# 墙（channelId = 上面 items[].id）
curl -s -b .tmp/cookie.txt 'localhost:18081/api/krx18/videos?channelId=152&limit=36'
# 全部标为已看
curl -s -b .tmp/cookie.txt -X POST localhost:18081/api/krx18/seen -d '{"channelId":152}'
```

单测：`go test ./internal/platform/krx18/` 里的 `TestParseTagPage`（真 HTML 夹具）、
`TestParsePerformerURL`、`TestAdapterSubscription`（httptest 顶替整站，把「地址 → slug →
标签接口 → 归档页 → 末页 404 变 ErrNotFound」走一遍）、`TestCanonicalTagSlug`。
