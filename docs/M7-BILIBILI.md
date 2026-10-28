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
| 回填列表 | **wbi 签名接口** `/x/space/wbi/arc/search`（`ps=20&pn=N&order=pubdate`，带 `wts`/`w_rid`），3 次 / 秒 | §7-2：签名版连打 15 页 3.3 秒零失败；未签名版一页就 -799（所以未签名只剩兜底） |
| 两个来源的页大小 | 都取 **20**（App 接口只肯给 20，传 `ps=30/50` 还是一页 20；web 接口迁就它） | 页大小不一致的话，「第 1 页 20 条 + 第 2 页从第 21 条起」会错位，中间那 30 条永远补不上 |
| 画质取舍 | durl（单文件）保底，DASH 里**只跳过与 durl 同分辨率的那一档**，更低/更高的照留 | §7-3：未登录时 durl 有 720P、DASH 只有 360P/480P。同档两条 `720p` 会在下拉框里重复，其余档留着手动可选 |
| 分轨播放 | 服务端 ffmpeg 合流成 mp4 缓存，浏览器只吃单文件 mp4 | B 站 DASH 的音视频是两条独立流，浏览器 `<video>` 播不了分轨 m4s |
| 合流的时机 | 点播到那一档时才合，合完落盘缓存 7 天；同一个 key 只跑一次 | §7-4：25 分钟 480P 一次 15.3 秒；44 分钟 PCDN 源 1 分 52 秒。默认档是 durl，起播不等合流 |
| 登录态 | 设置里填 Cookie（整段 Cookie 头 / 裸 SESSDATA / 浏览器导出的 `cookies.txt` 都认），**只影响画质**，不影响列表额度 | §7-7：带 Cookie 才有 1080P；限流是签名问题，与登录无关 |
| 媒体代理 | `/api/stream` 放行 `bilivideo.com` / `bilivideo.cn`，补 `Referer: https://www.bilibili.com/` | 不带 Referer 403 |
| 限流的语义 | `platform.ErrRateLimited`：已抓到的页照常入库、断点照留、退避 3 小时，**不当失败**（第 1 页就没抓到才失败） | §7-5：签名版下基本不会再触发，但兜底通道还要它 |

## 3. 抓取：两条通道（外加一条兜底）

```
增量（每 6h 一次，只抓第 1 页）
   └─ App  /x/v2/space/archive/cursor?appkey=…&sign=md5(q+appsec)&vmid=&ps=20&order=pubdate
        无风控；只给 20 条；offset/pn/page 传什么都忽略（访客拿不到第 2 页）

回填（首次 / 手动完整同步，按断点续翻）
   └─ wbi  /x/space/wbi/arc/search?mid=&ps=20&pn=N&order=pubdate&order_avoided=true
            &platform=web&web_location=1550101&wts=<秒>&w_rid=<md5(排序查询串 + mixin key)>
        签名材料取 /x/web-interface/nav 的 wbi_img.img_url / sub_url，缓存 30 分钟
        实测连打 15 页、3.3 秒、零失败（§7-2）；-352 时换 key 重试一次

兜底（nav 取不到图片名、或签名连换两把 key 都被拒）
   └─ 未签名 /x/space/arc/search  →  1 次 / 20 秒 + 退避重试（30s、60s）
```

**关键发现（2026-10-29）：卡人的不是 Cookie，是签名。** 同一个 IP、同一份 Cookie、
同一时刻，只是「查询串末尾多一个 `w_rid`」，结果就是「一页 -799」和「十五页零失败」的差别。
这也是浏览器为什么能随便翻：它每次都带签名。参考实现是
[bili-sync](https://github.com/amtoaer/bili-sync)（Rust）的
`crates/bili_sync/src/bilibili/{submission,credential}.rs`——它也没有 `dm_*` 指纹参数，
只做两件事：nav 取图片名拼 mixin key、`md5(排序后的查询串 + key)` 得 `w_rid`。

为什么还留着 App 通道：它**完全不设防**（web 列表被封时它照样返回），
是签名机制万一改版时的最后一条活路；代价是只给第 1 页，正好够增量用。

为什么不是「一律用 App 接口」：访客只能拿第 1 页（`next_cursor` 只对登录用户有意义，实测传了也没用）。

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

## 6. 界面：独立一页，管理里也独立一格

筛选状态写在地址栏（`/bilibili?up=<id>&mode=unseen`）：换 UP 主 / 全部·未看用 `replaceState`，
不堆历史；点投稿是整页跳转到 `/video/:id`，所以退回（浏览器后退，或详情页那个「返回 B 站」）
会自动回到同一个 UP 主、同一个筛选。卡片链接带 `?back=<原路径>`，详情页据此决定返回哪儿。

B 站不是「追一个艺人」，是「订阅一个内容源」。所以它和 YouTube（M6）一样**有自己的页面**，
不混进「艺人」「关注」流，也不在「关注管理」的列表里：

| 入口 | 位置 | 内容 |
|---|---|---|
| 视频墙 | 侧边栏「B 站」（`/bilibili`，徽标 = 未看条数） | UP 主胶囊 + 全部/未看 + 封面墙 + 「全部标为已看」「添加 UP 主」 |
| 管理 | 管理 → 「B 站」页签（`/manage?tab=bilibili`） | 读数、Cookie/ffmpeg 状态、加 UP 主、逐位 UP 主的同步/回填续翻/下载未看/停用/删除 |
| 设置 | 设置 → 「B 站」 | Cookie（SESSDATA）+ 画质说明；「网络与代理」里多一行 B 站专用代理 |
| 手机 | 「动态」顶部的分段控件（动态 / YouTube / B 站） | 底部导航仍是 6 格，塞不下第 7 格 |

墙上每张卡直接链到 `/video/{id}`：详情页就是那套现成的「播放 + 画质 + 下载」，
B 站没有专属播放器组件，只有「每档画质的地址从哪来」这一件事不同（§4）。

页面顶部固定摆两条「正常态说明」，因为它们看起来像故障、其实不是：

- **没填 Cookie** → 「最高只能看 720P」+ 一个去填 SESSDATA 的入口；
- **没有 ffmpeg** → 「480P 及以上是音画分轨，合流要它」；
- 有 UP 主上次同步失败时，直接写出原因和「N 小时后再试」，并给一条去管理的路。

「艺人」「关注」流这边做了三处收紧，保证同一件事只有一处入口：

- `/api/videos`、`/api/videos/new`（关注流 / 新片）排除 `platform='bilibili'`；
- `/api/performers`、`/api/subscriptions`（艺人页 / 关注管理）排除 B 站；
- 管理页「关注艺人」那个读数也不含 UP 主（影片 / 落盘 / 占用仍是全库口径——那是「库里有什么」）。

一个必须说明的取舍：**不再为 B 站另建表**。UP 主就是 `performers` 的一行、投稿就是 `videos` 的一行，
这一页只是按平台重新组织取数（`internal/store/bilibili.go` 顶部写了理由），所以播放、下载、
合流、限流退避全部复用已有实现，不存在「两套真相」。

## 7. 实测记录（2026-10-28 未登录 / 2026-10-29 配好登录 Cookie 后补测）

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
所以增量走 App，回填只能慢工出细活，且限流要退 3 小时而不是 30 分钟。

**2026-10-29 补测：上面这一整段只对「未签名」成立。** 同样的 IP、同样的 Cookie，带上
wbi 签名之后完全是另一个接口的脾气：

| 时刻 | 请求 | 结果 |
|---|---|---|
| 00:29 | `/x/space/wbi/arc/search?ps=20&pn=1..3`（签名） | 3 页全 `code 0`，各 30/30/30 条 |
| 00:30 | 同上，`pn=1..15` 连打（无间隔） | **15 页全过，共 3.3 秒**，`count 939` 每页都对 |
| 00:30 | 同参数不带 `w_rid` | 1 页后 `-799 请求过于频繁` |

也就是说：之前「1 次 / 20 秒 + 退避 30s/60s」是在给「没签名」还债。
现在回填 939 部（47 页）是**秒级**，不是小时级。

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

### 7.7 「配了 Cookie 还被限流」的两个真原因（2026-10-29 00:21–00:35 实测）

现象：设置里已经配好 Cookie（`/api/bilibili/health` 的 `hasCookie=true`），
空间投稿接口照样一页之后 `-799`：

| 时刻 | 请求 | 结果 |
|---|---|---|
| 00:21–00:23 | dprprince（新订阅）第 1 页（App cursor 通道） | 通过，20 条 |
| 00:23:27 | dprprince 第 2 页（web `arc/search`，**未签名**） | `-799 请求过于频繁` → 这一轮软停 |
| 00:22:5x | 影视飓风 第 2 页（同上，未签名） | 通过，20 条（片数 19 → 39） |
| 00:24:47 | 影视飓风 第 3 页（同上） | `-799` → 软停，断点留在第 2 页 |

拆开看，是两件独立的事叠在一起：

**原因一：没带 wbi 签名。** 名单额度跟登录态基本无关，跟「是不是浏览器那种带签名的请求」关系极大。
补上签名后同一 IP 连打 15 页零失败（§7-2）。→ 已修：`/x/space/wbi/arc/search` 为主，
未签名版只作兜底（`internal/platform/bilibili/wbi.go`）。

**原因二：存进去的那条 Cookie 根本不是合法 Cookie 头。** 用户当时粘的是浏览器导出的
`cookies.txt`（Netscape 格式，7 列制表符分隔），而 v1 的 `normalizeBiliCookie` 会把每一段
没有 `=` 的碎片都当成「裸 SESSDATA 值」补上 `SESSDATA=`。解密库里那条记录：

```
长度 4,639 字节 / 213 段 / 全部形如 SESSDATA=…
真实内容：SESSDATA=# Netscape HTTP Cookie File; SESSDATA=.bilibili.com;
          SESSDATA=TRUE; SESSDATA=1795708705; SESSDATA=buvid3; …
```

`hasCookie=true` 只是「库里非空」，B 站看到的是两百多个同名假字段，等于没登录。
（顺带解释了「配了 Cookie 还是 720P」：画质确实靠 Cookie，但这条 Cookie 从没生效过。）
→ 已修：`normalizeBiliCookie` 认三种输入（整段 Cookie 头 / 裸 SESSDATA / cookies.txt），
跳过 `#` 注释、认 `#HttpOnly_` 数据行、同名只留最后一个，并且**文件里逐行按域过滤**
（只收域里带 `bilibili` 的，整份浏览器导出时混进来的 `google.com` / `youtube.com` 不会被
转发给 B 站）；用例见 `internal/httpapi/cookies_test.go`。

补一条要紧的：那份 `cookies.txt` 本身是**好的**——把 213 段按 7 列一组的顺序还原回来，
正好是 30 条真 Cookie（`buvid3` / `bili_jct` / `DedeUserID` / `SESSDATA` 都在），
拿它打 nav 就是 `isLogin=true`。坏的只是「我们当时的整理逻辑」。

修好之后按同样的 Cookie 实测：

| 检查 | 结果 |
|---|---|
| `GET /x/web-interface/nav`（带 Cookie） | `isLogin=true`，`uname=月白喵丶`，6 级，`vipStatus=0` |
| 影视飓风「完整同步」（`backfill_limit=200`） | 39 → 239 部（10 页），**45 秒内跑完**，`last_error` 空 |
| dprprince「完整同步」（3 页） | 20 → 44 部，`backfill_total_pages=3`，`backfill_done=1`，无错误 |

两条收尾路径也因此不同：**翻到中途被限**按正常同步间隔（默认 6 小时）排队；
**第 1 页就被限**才算失败，退避 3 小时。签名版上线后这条路径基本只剩兜底意义。

### 7.8 画质实测：Cookie 到底买到了什么（2026-10-29，BV1F7nF6WE1a）

`/x/player/playurl?qn=127&fourk=1`，同一个 cid，只切换 Cookie：

| 通道 | 不带 Cookie | 带 Cookie（6 级、无大会员） |
|---|---|---|
| `fnval=0`（durl 单文件） | `quality=64` → **720P** | `quality=64` → 720P（没变） |
| `fnval=4048`（DASH） | `dash.video` 只有 **32（480P）/ 16（360P）** | `dash.video` 有 **80（1920×1080）**、64、32、16 |
| `accept_quality` | `[120,116,80,64,32,16]`（**骗人的**，4K 也列出来） | 同上 |

所以三件事要分开记：

1. **1080P 只能走 DASH**，durl 那一档永远是 720P——Cookie 不会让 durl 变高；
2. `accept_quality` 不能当依据，要看 `dash.video` 里实际出现的 `id`；
3. 4K（120）/ HDR（116）在 `accept_quality` 里，但 DASH 不返回——**要大会员**，本期不做绕过。

改完之后本机 `/api/videos/80/playback` 的实际档位（默认档自动变成了 1080p）：

```
1080p.av1 / 1080p.hevc / 1080p / 720p / 480p.av1 / 480p.hevc / 480p / 360p.av1 / 360p.hevc / 360p
default: 1080p.av1
```

### 7.9 4K 到底能不能放（2026-10-29 补测，BV1xrnz6gEVD）

用户报「4K 有选项但放不了」。实测结论分三段，每段都是不同的问题：

**一、4K 是真实存在的，不是假选项。** 这条片子（影视飓风，24:52）的 `accept_quality=[120 116 80 64 32 16]`，
`dash.video` 里真的有 `id=120 3840×2160`，而且三个编码都给：

| 编码 | 码率 | 成品体积 |
|---|---|---|
| `avc1.640034`（H.264） | 9.27 Mbps | 1.77 GB |
| `hvc1.1.6.L153.90`（HEVC） | 3.33 Mbps | 657 MB |
| `av01.0.13M`（AV1） | 2.78 Mbps | 554 MB |

也就是说 §7.8 那句「4K 要大会员、DASH 不返回」只对**未登录**成立；登录后（6 级、无大会员）
部分片子的 4K 是给的。别再把 4K 从候选里剔掉。

**二、浏览器放不动的真正原因是「合流要等」，不是格式不支持。** 4K 只有 DASH 分轨，
必须先整条流下完、ffmpeg 合成一份 mp4，浏览器才有第一个字节可拿。1080P 是 88 MB/十几秒，
4K 是 554 MB~1.77 GB，冷启动实测 **49 秒（AV1，官方 CDN）到 7 分 31 秒（AV1，PCDN）**。
这期间 `/remux` 一个字节都不回，页面只有一个转圈——用户口径就是「点了没反应/放不了」。
现在加了 `?status=1` 进度接口 + 前端遮罩百分比（见 `docs/API.md` A4·补·1），
并且 ready 之后才把地址交给 `<video>`。

**三、以前所有 stream 都指向 PCDN。** `pickMediaURL` 原来按「第一个 host 合法」取，
而 `*.mcdn.bilivideo.cn:8082` 恰好以 `.bilivideo.cn` 结尾，被当成官方 CDN 放行——
于是「优先官方、避开 PCDN」那个分支从来没生效过。实测这条片子 4K 每一条流的 primary 都是
`xy183x234x98x139xy.mcdn.bilivideo.cn:8082`，而 `backup_url` 里一直有官方镜像
（`upos-sz-mirrorcoso1` / `upos-sz-mirrorcos` / `cn-hncs-cm-03-08`）。改成按 rank 选：

| rank | 含义 | 例子 |
|---|---|---|
| 0 | 官方 CDN（`hostAllowed` 命中且不是 PCDN） | `upos-sz-mirrorcoso1.bilivideo.com` |
| 1 | 其它第三方 host | —— |
| 2 | PCDN | `*.mcdn.bilivideo.cn:8082`、`*.edge.mountaintoys.cn:4483` |

`primary + backup[]` 一起扫，同 rank 保持原顺序，命中 rank 0 立即停。改完后库里 `video_streams`
的 host 全变成 `upos-*`，同一档 4K AV1 从 7 分 31 秒降到 **49 秒**。

PCDN 不是永远慢（单条 30 MB range 实测 7.29 MB/s，跟官方镜像 7.52 MB/s 差不多），但它**会假死**：
一次实测写满 649 MB 后 3 分半零字节、连接没断、ffmpeg 无 CPU。所以除了排序，输入侧还挂了
`-rw_timeout 30000000 -reconnect 1 -reconnect_streamed 1 -reconnect_on_network_error 1`。

## 8. 已知限制 / 取舍

1. **多 P 投稿只取第 1 个 `cid`**。合集型投稿（一部视频挂几十个分 P）在墙上只出现一条，点开播的是第 1 P。
   要做的话是把 `pages[]` 展开成多条影片，键得改成 `bvid@cid`——那是另一个数据模型决定，本期不做。
2. **未登录上限 720P**。1080P 必须填 Cookie（实测见 §7.8：带 Cookie 后 DASH 出现 `id=80`=1920×1080，
   默认档自动变成 1080p）。4K 不一定需要大会员：部分片子登录后 `dash.video` 直接给 `id=120`
   （§7.9），给不给由 B 站定，所以**以实际返回为准**，能拿到就列出来。HDR（116）/ 杜比仍不处理。
3. **番剧 / 直播回放 / 课程不在范围**：App 列表里 `goto != "av"` 的条目直接丢（`pgc` 是番剧），
   否则会在墙上出现点开播不了的东西。
4. **充电专属 / 会员专享**：拿不到播放地址就报解析失败，不做任何绕过。
5. ~~首次回填很慢~~ → **已解决**（M7 补）：改用 wbi 签名后 939 部 / 47 页是秒级
   （实测 200 部 10 页 45 秒内跑完，§7-7）。未签名那条兜底路径仍按 1 次 / 20 秒，
   所以「签名彻底用不了」的极端情况下才会退回「半小时以上」。
6. **合流的第一下要等**（已大幅缓解，M7 补）：480P 首次约 15 秒、1080P 十几秒、4K 49 秒~3 分钟
   （取决于档位和源站线路）。现在会在这段时间显示百分比进度，合好之后同一档秒开；默认档是 durl，
   所以「点开就能看」不受影响。
7. **B 站不出现在「艺人」「关注」里**（M7 补）：它有独立页面与管理页签。想把某位 UP 主当艺人追，
   目前没有这条路——两边入口不同，混着摆只会让「关注」流的语义变模糊。
8. **墙上没有「收藏」**：YouTube 那页有（`yt_videos.saved`），B 站这边 `videos` 表没有对应字段。
   要加就得动通用表结构，本期不做。

## 9. 待验证

- ~~登录后 `arc/search` 的限额是否变宽~~ → **问错了**：额度取决于 wbi 签名，不取决于登录（§7-7）；
- ~~1080P 的 `dash.video` 是否给到 `id=80`~~ → **已实测给到**（§7.8）；
- ~~4K 是否真的要大会员~~ → 部分片子登录（非大会员）就给，见 §7.9；HDR（116）/ 杜比仍未验证；
- 签名版下 `-799` 的冷却窗口还有多长（现在基本撞不到，缺样本）；
- 登录后 App cursor 的 `next_cursor` 是否真能翻页（能的话 App 通道也能回填，多一层保险）；
- 大会员 4K + HDR：`dash.dolby` / `dash.flac` 是否需要单独处理（本期只认 `dash.video` / `dash.audio`）；
- wbi 图片名的轮换周期：现在按 30 分钟 TTL 缓存 + `-352` 换 key 重试一次兜着，但没刻意等过一次轮换。

## 10. 文件一览

| 文件 | 内容 |
|---|---|
| `internal/platform/bilibili/bilibili.go` | 适配器主体：两条列表通道 + 兜底、资料、详情、风控错误映射 |
| `internal/platform/bilibili/wbi.go` | wbi 签名：mixin key（nav 图片名 + 重排表）、`wts`/`w_rid`、key 缓存与 `-352` 换 key |
| `internal/platform/bilibili/playurl.go` | `playurl` 解析 + durl/DASH 合并规则 |
| `internal/platform/bilibili/bilibili_test.go` | 17 个用例（含 App 通道、限流映射、合并规则） |
| `internal/platform/bilibili/wbi_test.go` | 4 个用例：mixin key 官方向量、签名向量、`-352` 回落、签名正常时不碰兜底 |
| `internal/muxcache/` | 分轨合流的单飞缓存（含 8 个用例） |
| `internal/httpapi/handlers_remux.go` | `GET /api/videos/{id}/remux?v=` |
| `internal/library/media.go` | `RemuxFromURLs` / `RemuxTo`（两路输入 + 头信息） |
| `internal/jobs/download.go` | 有 `AudioURL` 时走「先合流再入库」 |
| `internal/store/migrations/0009_m7_bilibili.sql` | `video_streams.audio_url` |
| `web/src/assets/platform/bilibili.svg` | 平台图标 |
| `web/src/components/SettingsDialog.svelte` | 「B 站」分类（Cookie + 说明）+ 专用代理 |
| `internal/store/bilibili.go` | UP 主列表 / 未看计数 / 已看 / 未看片单（按平台取数，不另建表） |
| `internal/store/bilibili_test.go` | 4 个用例（UP 主计数含软删、已看作用域、平台过滤、统计与未看片单） |
| `internal/httpapi/handlers_bilibili.go` | `/api/bilibili/*` 与 `/api/manage/bilibili` |
| `internal/store/migrations/0010_m7b_bilibili_page.sql` | 投稿墙与未看角标的两个索引 |
| `web/src/routes/Bilibili.svelte` | 视频墙（UP 主胶囊 + 筛选 + 添加） |
| `web/src/components/SocialTabs.svelte` | 手机端「动态 / YouTube / B 站」分段控件 |
