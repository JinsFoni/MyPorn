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
不堆历史；点投稿是整页跳转到 `/video/:id`，所以退回（浏览器后退；播放页上不再有「返回 B 站」按钮，
2026-11-02 撤了）会自动回到同一个 UP 主、同一个筛选。卡片链接带 `?back=<原路径>`，
详情页据此决定「上一个 / 下一个」按哪份列表算（退回后的滚动位置由 `history.state` 还原，与它无关）。

B 站不是「追一个艺人」，是「订阅一个内容源」。所以它和 YouTube（M6）一样**有自己的页面**，
不混进「艺人」「关注」流，也不在「关注管理」的列表里：

| 入口 | 位置 | 内容 |
|---|---|---|
| 视频墙 | 侧边栏「B 站」（`/bilibili`，徽标 = 未看条数） | UP 主胶囊 + 全部/未看 + 封面墙 + 「全部标为已看」「添加 UP 主」 |
| 管理 | 管理 → 「B 站」页签（`/manage?tab=bilibili`） | 读数、Cookie/ffmpeg 状态、加 UP 主、逐位 UP 主的同步/回填续翻/下载未看/停用/删除 |
| 设置 | 设置 → 「B 站」 | Cookie（SESSDATA）+ 画质说明；「网络与代理」里多一行 B 站专用代理 |
| 手机 | 底部导航第 4 格「B 站」（`grid-cols-8`） | 原先是「动态」顶部的分段控件；2026-10-29 拆掉那排切换，B 站和 YouTube 直接进底栏 |

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

### 6.1 取消订阅 = 整条内容源摘掉（2026-10-29 修）

B 站投稿是**同步时就落进 `videos`** 的；YouTube 不是（它下载时才落库，而且 `yt_videos JOIN yt_channels`，
删频道自然消失）。于是取消订阅（软删 `subscriptions.deleted_at`）之后 `videos` 那几行还在，
墙上照样挂着——2026-10-29 实测：管理页删掉「影视飓风」之后，墙上仍有他 **219** 条投稿
（B 站全库 263 条），侧边栏未看角标也照着数 252 条。

现在墙、未看角标、管理页读数、「下载未看」片单统一加了同一条 `liveSubFilter`
（`internal/store/videos.go`，墙侧是 `FeedQuery.SubscribedOnly`）：一条片所属的艺人里至少还得有一位在订阅里。
实测删掉之后：墙 263 → **44** 条、未看 252 → **35**、管理页读数 `{channels:1, videos:44, unseen:35, downloaded:0}`，
库里影片行和已落盘文件一条没动；重新订阅同一位 UP 主 → 44 → 263 原样回来（这就是软删、不硬删的理由，
`TestUnsubscribedUpVideosLeaveWall` 盯着这一整串）。

「停用」（`enabled=0`）不走这条路：它的意思是「先不同步」，投稿继续留在墙上。

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

**为什么这条能拿到 4K？** 不是「免费 4K」，是**超清试看窗口正好覆盖全片**。playurl 的
`view_info.strategy_trial_info` 里写着：

```json
{"prompt_text": "本视频可超清试看前24分52秒", "time_length": 1492, "auto_switch": true}
```

`time_length`=1492 秒 = 全片时长 → 整条都在试看范围内，等于免费 4K。随手抽 12 条片子对照：

| bvid | dash 给到 4K？ | `strategy_trial_info` |
|---|---|---|
| BV1xrnz6gEVD | 是（120,116,80…） | 「试看前24分52秒」→ 1492s / 全片 1492s |
| BV1QEgZ6rEGj | 是（120,80…） | 「试看前16分9秒」→ 969s / 全片 969s |
| BV1d2N16KEh6 | 是（120,80…） | 「试看前11分49秒」→ 709s / 全片 709s |
| 另外 9 条（影视飓风等） | **否**，dash 最高只到 `id=80` | 整个字段都没有 |

也就是说：**`support_formats` 里永远列着 120（4K）、`accept_quality` 也永远列着，
但给不给是逐片决定的**——有试看窗口的才真给，没窗口的一律 1080P。所以 12 条里只有 3 条能放 4K。
别把 4K 从候选里剔掉（能拿到就该列），也别以为它普遍可用。

**未验证**：试看窗口短于全片时（比如只给前 5 分钟），DASH 照样返回 4K 流，但播到窗口外
B 站会不会在 CDN 侧掐断——没遇到样本，暂按「会掐」预期。真遇到的话表现是「前几分钟 4K、
后面卡住/403」，那时只能退到 1080P，没有绕过办法（那是会员权益，不绕过）。

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

## 7.10 「明明配过 Cookie，界面又让我重配」（2026-10-29 实测修掉）

症状：设置里显示「未配置 Cookie」，/bilibili 顶部横幅提示去填一个。但库里那条 `bili_cookie`
好好地待在 `settings` 表里（`encrypted=1`，2336 字节，当天 06:23 UTC 写的）。

量到的事实：

| 步骤 | 结果 |
|---|---|
| `SELECT length(value) FROM settings WHERE key='bili_cookie'` | 2336 字节，`encrypted=1` |
| 用仓库 `.env.local` 里的 `APP_ENCRYPTION_KEY` 解它 | `cipher: message authentication failed` |
| 让**当时正在跑的那个服务**新写一条密钥，再用同一把密钥解 | 同样解不开 |
| 启动日志（改完之后加的） | `ERROR 已存的 Cookie/Token … 解不开 fields=bili_cookie 当前密钥指纹=nfrgtL7X 写库时用的指纹=""` |

结论：**不是没配，是解不开**——库里的密文是另一把 `APP_ENCRYPTION_KEY` 写的。这条路径
只有在 AES-GCM 验签失败时才出现，最可能的两种来路：密钥换过；或者启动时的 shell 里残留
`APP_ENCRYPTION_KEY`（`config.LoadDotEnv` 不覆盖已存在的环境变量，于是 `.env.local` 被静默盖掉）。

三处改动（都为了「别再让人白填一遍」）：

1. `secret.Box.Decrypt` 把验签失败包成 `secret.ErrWrongKey`，并给出 `Fingerprint()`
   （密钥 sha256 前 6 字节，可写日志、不可还原）；
2. `store.SecretState` 变成三态（`Absent` / `OK` / `WrongKey`），`SetSecret` 顺手记
   `secret_key_fp`，启动时 `reportKeyState` 比对并打日志；
3. 接口加 `cookieBroken`（`/api/settings` 的 `bilibili`、`/api/bilibili/health`），
   前端三处（/bilibili 横幅、管理 → B 站 健康行、设置 → B 站）都按它显示「存了但解不开」。

注意：密文用 AES-GCM，密钥丢了**不可能**恢复。这条记录只能重填一次（重填时会用当前密钥加密）。

## 7.11 「Cookie 又失效了」但不是密钥的锅（2026-10-30 实测）

症状：/bilibili 顶部又亮红条。刚踩过 §7.10 的密钥坑，第一反应是「密钥又被盖掉了」。

量到的事实（这次**不是**密钥问题）：

| 检查 | 结果 |
|---|---|
| `/api/bilibili/health` | `hasCookie=true`、`cookieBroken=false`（解密成功） |
| `settings.secret_key_fp` 与当前密钥指纹 | 一致；`bili_cookie` 写于 2026-10-29T07:49:13Z |
| `nav` / `x/space/myinfo` / `nav/stat` 三个接口 | 一律 `{"code":-101,"message":"账号未登录"}` |
| 只发 `SESSDATA=`（原样 `%2C` 222 字节 / URL 解码 216 字节） | 两种写法都是 `-101` |
| SESSDATA 内嵌的到期时间 | **2027-04-25 01:08:10** |
| `bili_ticket_expires` | 2026-10-29 23:59:40（比发现时早约 19 小时） |
| Cookie 字段数 | 36 个，`buvid3` / `b_nut` / `buvid4` / `bili_jct` 都在（不是漏字段） |

结论：**Cookie 能解开、字段齐全、内嵌到期日还有半年，B 站却在到期前把这条登录态作废了。**
现实中就三种来路：在那台浏览器里退出登录、改密码、风控（本机出口 IP 和浏览器不是同一个，
风控嫌疑最大但没法证实）。所以「Cookie 里的到期时间」不能当「登录态还有效」用。

改了什么（下次一眼分清三种情况，不再一律写成「粘错了」）：

1. `/api/bilibili/health` 多三个字段：`navError`（nav 请求本身失败 = 网络/风控，**不代表** Cookie 失效）、
   `lastLoginOk`（上一次 nav 明确回「已登录」的时间，落在 `settings.bili_login_ok_at`，最多 30 分钟写一次）、
   `cookieSetAt`（这条 Cookie 的写入时间，取自 `settings.updated_at`）；
2. nav 回未登录时打一条 WARN：`B 站登录态被作废：nav 回未登录（Cookie 能解开，是 B 站那边不认了） 填于=… 上次确认有效=…`，
   日志从此有时间线，不用再靠猜；
3. /bilibili 横幅与管理页健康行按三态分别说话：查不出来（网络）/ 曾经有效、现在被作废（带上次有效时间）/
   从没成功过（带填写时间）。

**附带的坑（同一天踩到）**：让用户「控制台里敲 `document.cookie` 复制」拿到的那串里**没有**
`SESSDATA`——它是 `HttpOnly`，JS 读不到。那种串（实测 16 个字段、1629 字节，少了 SESSDATA）
打 nav 必定 `-101`，现象看起来又像「配了还是没登录」。现在 `PUT /api/settings` 遇到没有
SESSDATA 的串会直接 400 并在报错里写清三种取法（Network 请求头 / Application → Cookies /
cookies.txt），设置页的说明也补了同一段。

自查一条命令（`-101` 就是登录态没了，与密钥无关）：

```bash
curl -s 'https://api.bilibili.com/x/web-interface/nav' -H 'Cookie: SESSDATA=…' | head -c 200
```

## 7.12 扫码登录 + 自动续期（2026-10-30 实现）

§7.11 的两条结论指向同一件事：**手填 Cookie 这条路本身有上限**——`SESSDATA` 是 HttpOnly
（抄不到），串里也没有 `refresh_token`（作废后无法自救）。所以照 `amtoaer/bili-sync` 的做法
补了两条路：扫码登录（一次性拿全五项）和自动续期（长期不回退）。

### 7.12.1 扫码登录

| 步骤 | 接口 | 关键点 |
|---|---|---|
| 1 | `POST /api/bilibili/login/qrcode` | 转发 `passport …/qrcode/generate`，回 `{url, key}`；实测往返 **74 ms** |
| 2 | `GET …/login/qrcode?key=` | 转发 `…/qrcode/poll`，B 站的 `data.code` 映射成 `pending/scanned/success/expired`（0 / 86101 / 86090 / 86038） |

成功那一下的取法与手填完全不同：

- `SESSDATA` / `bili_jct` / `DedeUserID` 从**登录响应的 `Set-Cookie`** 里挑（`HttpOnly` 只在这里出现）；
- `refresh_token` 从响应 JSON 的 `data.refresh_token`（浏览器那边存成 `ac_time_value` cookie）；
- `buvid3` 另打 `GET api.bilibili.com/x/web-frontend/getbuvid` 补齐（这个接口不要登录）；
- 三项缺一（尤其缺 `refresh_token`）就报错让人重扫，**不**收下一条「用几天又失效」的凭据。

前端：`web/src/components/BiliLoginDialog.svelte` 用 `qrcode` 包在本地渲出 240×240 的图
（二维码地址不外发），2 秒轮询、180 秒倒计时，过期给「点一下重新生成」。入口在
`/bilibili` 的三处横幅和管理 → B 站 健康行。

真机验证（playwright-cli，mute 无关）：点「扫码登录」后弹窗出现、`img` 是
`data:image/png;base64,…`、`naturalWidth=240`、倒计时从 180 往下走；`poll` 返回
`{"status":"pending","message":"等待扫码"}`（50 ms）。**成功分支需要手机真扫，见 §9。**

### 7.12.2 自动续期（四步，顺序不能换）

| 步骤 | 请求 | 用途 |
|---|---|---|
| 1 | `GET passport …/x/passport-login/web/cookie/info` | `data.refresh` 决定要不要续 |
| 2 | `GET www.bilibili.com/correspond/1/<RSA密文>` | 页面里抠 `<div id="1-name">…</div>` → `refresh_csrf` |
| 3 | `POST passport …/web/cookie/refresh`（form） | `csrf=<旧 bili_jct>`、`refresh_csrf`、`refresh_token=<旧>`、`source=main_web` → Set-Cookie 出新的三件套 + JSON 出新的 `refresh_token` |
| 4 | `POST passport …/web/confirm/refresh`（form） | `csrf=<新 bili_jct>`、`refresh_token=<**旧**>`，确认后旧的才作废 |

第 2 步的 RSA 公钥是 B 站前端写死的，`RSA-OAEP(SHA-256)` 加密 `refresh_<毫秒-20000>`
（提前 20 秒防本机时钟比服务器快），转 hex 塞进 URL。`refresh.go` 顶部有完整注释。

调度：`runBiliCredentialScheduler`（`cmd/myporn/main.go`）每 10 分钟看一眼、满 **6 小时**
才真查一次；启动 3 分钟后第一次。检查失败只打 `WARN`（网络抖动 ≠ Cookie 失效），
续期成功打 `INFO B 站登录态已续期 mid=…`。

另有一条实测：**已作废的登录态**打 `cookie/info` 回 `{"code":-101,"message":"账号未登录"}`，
所以代码把 `-101` 单独翻译成「只能重新扫码」，而不是笼统的「刷新失败」。

### 7.12.3 存储

| secret key | 内容 |
|---|---|
| `bili_cookie` | 规范化的 Cookie 头，只有四段：`buvid3=…; SESSDATA=…; bili_jct=…; DedeUserID=…` |
| `bili_ac_time_value` | `refresh_token`；**手填 Cookie 时为空** → `health.canRefresh=false` |

两条都用现有 `secret.Box` 加密。手填路径保持不变（设置里粘 Cookie 仍然有效），
只是 `canRefresh` 会一直是 false，`/bilibili` 会提示「这条不能自动续期，扫一次码补上」。

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

- **「B 站自己置 `refresh=true` 那一刻，定时任务自动续上」这一条还没等到**：四步续期已手工
  真跑过（见 §7.12.4），但 `data.refresh` 由 B 站按自己的时间表置位，刚登录时是 `false`，
  要等它临近过期才会变 `true`。到时候看日志有没有 `INFO B 站登录态已续期 mid=…` 即可。

### 7.12.4 真机验证记录（2026-10-30 19:19–19:21）

手机扫完码之后逐项量了一遍：

| 项 | 结果 |
|---|---|
| 服务端日志 | `B 站扫码登录成功 mid=34899708 可自动续期=true` → 紧接着 `B 站登录态确认有效 uname=月白喵丶` |
| `GET /api/bilibili/health` | `loggedIn=true`、`uname="月白喵丶"`、`canRefresh=true`、`cookieBroken=false` |
| 落库 | `bili_cookie` 504 字节密文、`bili_ac_time_value` 80 字节密文，同一秒写入（`2026-10-30T11:19:58Z`） |
| 凭据解码 | `SESSDATA` 222 字节、`bili_jct` 32、`refresh_token` 32、`buvid3` 有、`Complete()=true` |
| 页面 | `/bilibili` 三处横幅（扫码 / 被作废 / 不能自动续期）全部消失 |
| 第 1 步 `cookie/info` | `need_refresh=false`（刚登录，还没进续期窗口），无错误 |
| 第 2~4 步 | 手工强制跑完：`correspond` 抠到 `refresh_csrf` → `cookie/refresh` 换新 → `confirm/refresh` 确认，**全程 0.97 s**；写库后再打 nav 仍回 `月白喵丶`，说明新凭据生效、旧的已作废 |

强制轮换用的是一次性工具（不入库，已删）：读库里的 `bili_cookie` + `bili_ac_time_value` →
`RefreshCredential` → 回写两个 secret。正是因为刚扫的码重扫成本只有几十秒，才敢在真机上新旧轮换一次；
平时这一动作由 `runBiliCredentialScheduler` 在 B 站置位后自动完成。

另一条副作用实测：扫码之后 `nav` 立刻可用，但**旧的手填 Cookie 已经彻底没用了**（那条会话本身
早被作废），库里只剩这一条新凭据。

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
| `web/src/components/AppShell.svelte` | 手机底栏 8 格：YouTube / B 站 直接进底栏（原先那排「动态 / YouTube / B 站」分段控件已拆掉） |

### 7.13 横版封面被当成竖屏裁掉（2026-11-01 修）

用户报：「B 站的横版视频起播画面被裁切成竖图加虚化背景了」，附的截图正是「正在准备 1080p AV1 · 75%」那一屏。

根因不在 B 站，也不在播放链路，在 `web/src/lib/coverCrop.ts` 的封面窄条探测器：它是为 **YouTube 竖屏封面**写的
（站方把竖画面居中烘进 1280×720 的画布，两侧是拉过去糊过又压暗的填充），判据只有一条——**锐度剖面在哪断**。
一张正常的 16:9 封面只要主体居中、两侧是虚焦/暗背景/纯色墙，锐度剖面跟烘出来的填充长得一模一样，于是被判成窄条，
`VideoLoading` 和 `CoverArt` 就按「竖屏」给它铺虚化底 + 中间竖条。

规模（本机全库量，不是抽样）：342 张 B 站封面全是 16:9 原画（任何非 null 都是误判），**36 张被裁 = 10.5%**——
卡片墙和播放器加载态都中招。

改法：候选边界加两道「这真是站方烘的填充吗」的验收（两道都是量的，不是拍的）：

| 判据 | 烘出来的填充 | 342 张 B 站封面（误判组） |
|---|---|---|
| 接缝那一列「整列都在跳」（灰度差 >12 的行占比，取左右较窄的一侧） | 0.31 – 1.00 | 0 – 0.24（中位 0.07） |
| 填充区亮度 / 中间那条亮度 | 0.24 – 0.33 | 35/36 在 0.52 – 2.14 |

阈值取「接缝 ≥ 0.3 且亮度比 ≤ 0.45」。同一批图一次性跑新旧两版探测器：

| 封面集 | 旧版裁掉 | 新版裁掉 |
|---|---|---|
| B 站 343 张（全横版） | 36 | **0** |
| YouTube 154 张 | 10 | 9（丢的 `XFJ8fCUOYRI` 放大看是正常 16:9 照片，本来就该丢） |

真机验证：`/video/2235` 的「正在准备 1080p AV1」加载态现在是整张封面铺满（`composed=false`、`img.video-veil-art`），
另外四部旧版被裁的片子（2227 / 2218 / 2204 / 2197 / 2196）同样铺满；YouTube 竖屏那几部仍是竖条合成
（`Ggbmi3YHa2c` 400×720、`LuaQDfY96fA` 380×720），没有反向误伤。

排查坑：判断「加载态是不是走了合成」别只看有没有 `.video-veil`——合成要等 `<img>` 的 `onload` 之后才发生，
加载态刚出现的那一帧看到 `composed=false` 是正常的，得连采 1~2 秒。

## 7.12 「清晰度切不了」：1080P 和 1080P 高码率撞成同一个 id（2026-11-02 实测）

用户报 `/video/2207?back=/bilibili`（`BV1NTp26eEkn`，4:27）「无法切换清晰度」。量下来是三件事叠在一起：

**一、playurl 有时把同一分辨率的「普通」和「高码率」两版一起返回。** 同一部片子连解析两次，
一次给 13 条档位、一次给 10 条——多出来的正是 `qn=80`（1080P）和 `qn=112`（1080P 高码率）各三份：

```
1080p.av1  1198322   ← 112 高码率
1080p.av1   702429   ← 80
1080p.hevc 1491267 / 832382
1080p      2010247 / 1361202
```

`Variant.ID()` 只按「分辨率 + 编码族」编号，于是**三对档位的 id 完全一样**。前端菜单是按 id 选中/打勾的，
点第二条「1080p AV1」等于点第一条——地址没变、画面没变，看起来就是「点了没反应」（实测：点第 2 行后
`video.currentSrc` 与点击前一致）。

改法（`model.DedupeVariants`，在 `ensureVariants` 读缓存后和重新解析后各跑一次）：同 id 只留**码率最高**
的那条。列表仍按 `SortVariantsDesc` 排，所以留下的就是这条片子的「最好那一版」。

**二、菜单把每个编码都列一行，10~13 行挤在 224px 的弹层里。** 皮肤给菜单的 `max-height` 是 224px，
一屏只看得到 4~5 行，剩下的要滚动；更糟的是皮肤那层底是「10% 白 + 背景模糊」，
菜单往上顶到亮画面上时白字直接糊进画面里（这条片子的封面是大片橙色）：

| | 改前 | 改后 |
|---|---|---|
| 行数（`BV1NTp26eEkn`） | 13 行，其中 3 对 id 相同 | **4 行**（1080p / 720p / 480p / 360p） |
| 弹层底 | `oklch(1 0 0 / 0.1)` 磨砂玻璃 | `rgb(12 14 18 / 0.94)` 实底 |
| 可见行 | 4~5 行，其余要滚 | 4 行全可见（197px），4K 片 5 行 230px 也不滚 |
| 点每一行 | 有 4 行点了没反应 | 4 行全部生效（640×360 / 852×480 / 1280×720 / 1920×1080） |

菜单改成**一个分辨率一行**：编码是同一清晰度内部的事（§7.4.1 已经定了「同分辨率内 AV1 优先」，
`codecRankOf` 还会把本机解不了的编码排到最后），副文案里照样写着用的是 AV1 还是 HEVC、多少 Mbps。
