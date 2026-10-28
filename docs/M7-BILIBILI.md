# M7 — bilibili 订阅（UP 主投稿流 + 在线播放 + 下载入库）

> **状态（2026-10-28）**：已实现并实测通过。真机验证记录见 §7（同一台机器、大陆家宽直连、无代理、**未登录**）。
> 上位需求见 `REQUIREMENTS.md`，接口契约并入 `docs/API.md`，解析自查清单并入 `docs/NEEDS-VERIFICATION.md`。
> 与 M5/M6 的关系：还是那套「订阅 → 定时抓 → 一面墙 → 看中的下载入库」的骨架，横切设施（代理 / 图片缓存 /
> 错误语义 / SSE / 任务队列 / 命名与 nfo / 本地库）全部复用。
>
> B 站这一期真正新加的东西只有两样：**一条不受风控的列表通道**，和**一个分轨合流的缓存**（见 §3、§4）。

一句话：**订阅 UP 主主页 → 定时抓新投稿 → 视频墙；点开站内播（720P 起，登录态可更高），看中的下成 mp4 进本地库。**

---

## 1. 本期范围

| 做 | 不做 |
|---|---|
| UP 主订阅（`space.bilibili.com/<mid>` / `m.bilibili.com/space/<mid>` / 纯数字 mid）、启用/停用、单独同步、深度回填 | 全站搜索、排行榜、推荐流、番剧/课程/直播、弹幕、字幕、评论 |
| 定时增量（6h，App 列表接口）+ 首次/手动深度回填（web 列表接口，断点续跑） | 多 P 投稿的分集（只取第 1 个 `cid`，见 §8） |
| B 站页：16:9 封面网格、UP 主筛选、未看/全部、无限滚 | 合集（season/series）视图 |
| 卡片点开 → 站内播放（720P 单文件直出；更高清走服务端合流） | 会员/付费/充电专属（拿不到地址就按「解析失败」报错，不猜） |
| 一键下载进本地库：单文件直下，或分轨（DASH）先合流再入库 | 自动下载（沿用全局「同步到新片后自动下载」开关） |
| 设置里新增「B 站」分类：SESSDATA Cookie（画质）+ B 站专用代理 | 扫码登录、账号体系、收藏夹、稍后再看 |

## 2. 决策（都有实测依据）

| 维度 | 决策 | 依据 |
|---|---|---|
| 唯一键 | `bvid`（`BV1…`）。av 号在列表里本来就不给，只用 bvid | 三处接口一致 |
| 增量列表 | **App 接口** `/x/v2/space/archive/cursor`（appkey 签名，固定 20 条） | §7-2：同一 IP 被 web 列表接口封着时，它照样正常返回 |
| 回填列表 | **web 接口** `/x/space/arc/search`（`ps=20&pn=N&order=pubdate`），1 次 / 20 秒 + 退避重试 | §7-2：能过，但节奏稍快就 -799 / HTTP 412 |
| 两个来源的页大小 | 都取 **20**（App 接口只肯给 20，传 `ps=30/50` 还是一页 20；web 接口迁就它） | 页大小不一致的话，「第 1 页 20 条 + 第 2 页从第 21 条起」会错位，中间那 30 条永远补不上 |
| 画质取舍 | durl（单文件）保底，DASH 里**只跳过与 durl 同分辨率的那一档**，更低/更高的照留 | §7-3：未登录时 durl 有 720P、DASH 只有 360P/480P。同档两条 `720p` 会在下拉框里重复，其余档留着手动可选 |
| 分轨播放 | 服务端 ffmpeg 合流成 mp4 缓存，浏览器只吃单文件 mp4 | B 站 DASH 的音视频是两条独立流，浏览器 `<video>` 播不了分轨 m4s |
| 合流的时机 | 点播到那一档时才合，合完落盘缓存 7 天；同一个 key 只跑一次 | §7-4：25 分钟 480P 一次 15.3 秒；44 分钟 PCDN 源 1 分 52 秒。默认档是 durl，起播不等合流 |
| 登录态 | 设置里填 `SESSDATA`（自动从整段 Cookie 头 / 裸值里抠），只影响画质与限额，不影响功能 | §7-6：未登录 1080P+ 拿不到 |
| 媒体代理 | `/api/stream` 放行 `bilivideo.com` / `bilivideo.cn`，补 `Referer: https://www.bilibili.com/` | 不带 Referer 403 |
| 限流的语义 | `platform.ErrRateLimited`：已抓到的页照常入库、断点照留、退避 3 小时，**不当失败**（第 1 页就没抓到才失败） | §7-5：这不是偶发错误，是 B 站列表接口的常态 |

## 3. 抓取：两条通道

```
增量（每 6h 一次，只抓第 1 页）
   └─ App  /x/v2/space/archive/cursor?appkey=…&sign=md5(q+appsec)&vmid=&ps=20&order=pubdate
        无风控；只给 20 条；offset/pn/page 传什么都忽略（访客拿不到第 2 页）

回填（首次 / 手动完整同步，按断点续翻）
   └─ web  /x/space/arc/search?mid=&ps=20&pn=N&order=pubdate&order_avoided=true
        1 次 / 20 秒；连打就 -799「请求过于频繁」→ HTTP 412；恢复要几十分钟
```

为什么不是「一律用 App 接口」：访客只能拿第 1 页（`next_cursor` 只对登录用户有意义，实测传了也没用）。
为什么不是「一律用 web 接口」：它被封的时候整个订阅就停摆，而增量才是天天要跑的那条。

失败语义（`internal/jobs/sync.go`）：

- 第 1 页就限流 → 任务失败（`抓取第 1 页失败: 上游限流…`），退避 **3 小时**（其它错误 30 分钟）；
- 翻页途中限流 → **不算失败**：进度入库、断点留在最后一页、任务文案写「同步被限流中断，新增 N 部（断点已存，稍后自动接着翻）」；
- 一页都没抓到的「软停」不能算完成——订阅会静悄悄地不动，所以那种情况仍然报失败。

## 4. 播放：durl 直出 + DASH 合流

```
GET /api/videos/{id}/playback
  └─ ensureVariants(5 分钟缓存)
       ├─ /x/web-interface/view      → title / cid / 分区 / owner
       └─ /x/player/playurl(qn=127&fnval=4048&fourk=1)
            ├─ durl[]  → 单文件 mp4（未登录 720P）
            └─ dash{}  → 分轨（video[] + audio[]），每档一个 Variant，带 AudioURL

播放列表（前端）：
  durl 档        → /api/stream?p=bilibili&u=<CDN>          直接播，Range 可用
  DASH 档        → /api/videos/{id}/remux?v=<variantID>    服务端合流后当一个普通文件播
```

合流缓存（`internal/muxcache`）：

- key = `<videoID>-<variantID>`，落在 `DATA_DIR/mux/`，TTL 7 天，10 分钟扫一次过期；
- 单飞：浏览器起播会同时打好几个 Range 请求，只有一个能触发 ffmpeg；
- 合流跑在 `context.Background()` 上（带 10 分钟超时），**调用方断开不会把已经跑了一半的合流打断**；
- 产物用 `http.ServeFile` 提供，Range 由标准库处理（实测 `206 Partial Content`）；
- 保存到库里的变体也带 `audio_url`（迁移 `0009`），所以下载走的是同一套判定：有 `AudioURL` 就先合流再入库。

## 5. 数据模型

只加了一列：

```sql
-- internal/store/migrations/0009_m7_bilibili.sql
ALTER TABLE video_streams ADD COLUMN audio_url TEXT NOT NULL DEFAULT '';
```

`Variant.AudioURL` 是「这一档音轨在哪儿」，空表示单文件（其它平台全部为空，行为不变）。
估算体积用「视频带宽 + 音轨带宽」（`download-options` 的 `estimatedBytes` 实测 150,153,388，实际文件 150,109,750，差 0.03%）。

## 6. 界面

- 平台图标：`web/src/assets/platform/bilibili.svg`（粉色 `#FB7299` 小电视）；
- 设置 → 侧边栏新增「B 站」（`bilibili` pane）：Cookie（SESSDATA）输入框 + 画质说明；「网络与代理」里多一行 B 站专用代理；
- 平台命名：`bilibili`（与 pornhub / xhamster / beeg / youtube / xcom 并列），艺人 = UP 主；
- 影片墙、详情页、播放器、下载对话框全部复用，没有 B 站专属组件。

## 7. 实测记录（2026-10-28，本机直连，未登录）

### 7.1 解析与订阅

```
POST /api/performers/resolve {"url":"https://space.bilibili.com/946974/video?spm_id_from=…"}
→ 影视飓风 / mid 946974 / 粉丝 18,839,277 / 投稿 951 / 头像 i0.hdslb.com/bfs/face/…
```

注意 URL 后面的 `?spm_id_from=` 这类跟踪参数不影响解析；`/video` 路径也认。

### 7.2 列表接口的脾气（同一个 IP，同一时段）

| 时刻 | 请求 | 结果 |
|---|---|---|
| 23:25 | `/x/space/arc/search?pn=1` | `-799 请求过于频繁` |
| 23:25 | `/x/v2/space/archive/cursor`（App，签名） | `code 0`，20 条，`count 939`，`has_next true` |
| 23:26–23:34 | 每隔 60 秒探一次 web 列表 | 一直 `-799` / HTTP 412 空响应 |
| 23:35–23:38 | 每隔 35 秒打 `pn=2` | `-799` → 412 空响应 → `-799` |
| 期间 | `/x/web-interface/view`、`/x/player/playurl`、`/x/web-interface/card` | 一直正常 |

结论：**web 列表接口的封禁是滚动的**（每探一次都像给它续期），而 App 接口不受影响。
所以增量必须走 App，回填只能慢工出细活，且限流要退 3 小时而不是 30 分钟。

### 7.3 播放地址

第 1 次同步（App 接口第 1 页）入库 19 部新片。`GET /api/videos/60/playback`：

| 档位 | 来源 | 带宽 | 说明 |
|---|---|---|---|
| `720p` H.264 1280×720 | durl | 805 kbps | 默认档，单文件，`/api/stream` 直出 |
| `480p.av1` | DASH | 364 kbps | 需要合流 |
| `480p.hevc` | DASH | 385 kbps | 需要合流 |
| `480p` H.264 | DASH | 527 kbps | 需要合流（合流后总带宽 720 kbps 左右） |
| `360p*` | DASH | — | 同上 |

未登录时接口照样把 `accept_quality` 里的 4K/1080P 列出来，但 `dash.video` 里只有 360/480 —— **以实际返回为准**。

### 7.4 合流与播放

| 场景 | 数据 |
|---|---|
| 25 分钟视频 480P H.264 合流 | 15.3 秒 → 99,155,226 字节，`h264 video + aac audio`，时长 1491.56 秒 |
| 同一档第二次请求 | 19 毫秒（磁盘命中），`206 Partial Content` |
| 44 分钟视频 480P（PCDN 源） | 1 分 52.8 秒 → 171,535,160 字节 |
| 浏览器（Chrome 155，本机） | 720P 直出：`readyState 4`，1280×720，起播即播；480P 三档（H.264 / AV1 / HEVC）合流后分别播 4 秒无错误，853×480 |
| 画质菜单 | `720p 0.8 Mbps · 最高 / 480p AV1 / 480p HEVC / 480p 0.5 Mbps / 360p×3`，切档即换 `src` 到 `/remux?v=…` |

### 7.5 同步被限流时的表现

```
job 45 sync failed  抓取第 2 页失败: 上游限流：B 站限流了，等一会儿再同步
→ subscriptions.last_error 记下原因，next_sync_at = 现在 + 3 小时，backfill_page 仍为 1
```

第 1 页那次（job 40）则是「回填第 1/47 页，新增 19 部」→ 翻页 2 被限流 → 保留 19 部、断点留在第 1 页。

### 7.6 下载

```
download-options → variantId 720p / height 720 / estimatedBytes 150,153,388
job 41 done      已完成 折1万次，会坏吗？iPhone Duo深度评测（143.2 MB）
→ .tmp/run/media/主库/bilibili/影视飓风/<标题>/<标题>.mp4
```

单文件档走 `ffmpeg -c copy` 直下；分轨档（登录后才有 1080P+）走 `RemuxFromURLs`：
两路输入、`-map 0:v:0 -map 1:a:0?`、`-c copy -movflags +faststart`，实测 286 秒 480P 素材 2.0 秒封完。

## 8. 已知限制 / 取舍

1. **多 P 投稿只取第 1 个 `cid`**。合集型投稿（一部视频挂几十个分 P）在墙上只出现一条，点开播的是第 1 P。
   要做的话是把 `pages[]` 展开成多条影片，键得改成 `bvid@cid`——那是另一个数据模型决定，本期不做。
2. **未登录上限 720P**。1080P/4K 必须填 SESSDATA；填了之后 `qn=127` 会拿到更高档位（本期**未实测**，见 §9）。
3. **番剧 / 直播回放 / 课程不在范围**：App 列表里 `goto != "av"` 的条目直接丢（`pgc` 是番剧），
   否则会在墙上出现点开播不了的东西。
4. **充电专属 / 会员专享**：拿不到播放地址就报解析失败，不做任何绕过。
5. **首次回填很慢**：951 部投稿 ≈ 48 页，按 1 次 / 20 秒 + 限流退避，正常也要半小时以上，被限流则以「天」计。
   断点续跑，不会白跑；增量不受影响。
6. **合流的第一下要等**：480P 起播首次 15 秒~2 分钟（取决于源站是 upos 还是 PCDN）。默认档是 durl，所以「点开就能看」不受影响。

## 9. 待验证（填了 Cookie 之后）

- 1080P / 4K 的 `dash.video` 实际给到哪些 `id`，以及 `qn=127` 是否一次到位；
- 登录后 `arc/search` 的限额是否变宽（如果变宽，回填可以把节奏调快）；
- 登录后 App cursor 接口的 `next_cursor` 是否真的能翻页（能翻的话，回填就完全不用碰风控接口了）；
- 大会员 4K + HDR：`dash.dolby` / `dash.flac` 是否需要单独处理（本期只认 `dash.video` / `dash.audio`）。

## 10. 文件一览

| 文件 | 内容 |
|---|---|
| `internal/platform/bilibili/bilibili.go` | 适配器主体：两条列表通道、资料、详情、风控错误映射 |
| `internal/platform/bilibili/playurl.go` | `playurl` 解析 + durl/DASH 合并规则 |
| `internal/platform/bilibili/bilibili_test.go` | 17 个用例（含 App 通道、限流映射、合并规则） |
| `internal/muxcache/` | 分轨合流的单飞缓存（含 8 个用例） |
| `internal/httpapi/handlers_remux.go` | `GET /api/videos/{id}/remux?v=` |
| `internal/library/media.go` | `RemuxFromURLs` / `RemuxTo`（两路输入 + 头信息） |
| `internal/jobs/download.go` | 有 `AudioURL` 时走「先合流再入库」 |
| `internal/store/migrations/0009_m7_bilibili.sql` | `video_streams.audio_url` |
| `web/src/assets/platform/bilibili.svg` | 平台图标 |
| `web/src/components/SettingsDialog.svelte` | 「B 站」分类（Cookie + 说明）+ 专用代理 |
