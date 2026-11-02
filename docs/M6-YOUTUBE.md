# M6 — YouTube 订阅（频道视频流 + yt-dlp 入库）

> **状态（2026-10-28）**：需求已确认（Q1–Q8 全部拍板，见 §2.2），**未开工**。抓取与下载两侧的 spike 都跑完了，
> 原始数据见 §11，其中两条结论直接改变了设计：
> ① 免登录 RSS 只有 15 条且**没有时长**；② **YouTube 不提供 AV1+音轨的现成文件**，必须封一次（`-c copy`）。
>
> 上位需求见 `REQUIREMENTS.md`，接口契约落地时并入 `docs/API.md`，解析自查清单并入 `docs/NEEDS-VERIFICATION.md`。
> 与 M5（`docs/M5-SOCIAL.md`）的关系：同一套「订阅 → 定时抓 → 一面墙」的骨架，但**这一期多了「下载入库」这条腿**，
> 横切设施（代理 / 图片缓存 / 错误语义 / SSE / 任务队列 / 命名与 nfo）全部复用，不新写。

一句话：**订阅 YouTube 频道 → 定时抓新视频 → 16:9 视频墙；点开站内播，看中的用 yt-dlp 下成 AV1 单文件进本地库。**

---

## 1. 本期范围

| 做 | 不做 |
|---|---|
| 频道增删改查（`@handle` / `/channel/UC…` / 任意视频链接）、启用/停用、单独同步、深度回填 | 搜索 YouTube、推荐流、热门榜、评论、字幕、社区帖、播放列表 |
| 定时增量（RSS，6h）+ 首次/手动深度同步（yt-dlp，默认 200 条） | 直播实时状态（直播条目当普通视频落库，墙上有「直播」标记即可） |
| YouTube 页：16:9 封面网格、频道筛选、未看/全部、无限滚 | 观看进度续播、投屏、画中画 |
| 卡片点开 → 站内嵌播（`youtube-nocookie`）+「在 YouTube 打开」 | 会员 / 付费 / 需要登录的受限视频 |
| **一键下载进本地库**：yt-dlp → AV1 视频 + AAC 音频 `-c copy` 封装成单文件 mp4 → 常规入库四件套（命名 / nfo / 海报 / 索引） | 自动下载（本期只手动） |
| 管理页新 tab「YouTube」；设置里新增 YouTube 分类（含代理、yt-dlp 路径与版本健康度） | 与「艺人」打通（表里留 `performer_id` 占位） |
| 侧边栏「YouTube」（`/youtube`，紧跟「动态」） | 移动端第 7 个 tab（底栏不动，见 §4） |

## 2. 决策

### 2.1 有实测依据的硬决策

| 维度 | 决策 | 依据 |
|---|---|---|
| 频道 id | `UC…` 为唯一键；`@handle` 解析时抓一次频道页取 `"externalId":"UC…"`（兜底 `<link rel="canonical">`） | §11-2。**同一份 HTML 里还有别的 `channelId`（推荐位），不能用** |
| 增量通道 | **RSS**：`feeds/videos.xml?channel_id=` | §11-1。官方 feed，免登录、1 次请求、15 条、含播放量与绝对时间。**和 IG 完全不是一回事** |
| 深度通道 | **yt-dlp** `-J --flat-playlist --playlist-end N` | §11-3。200 条实测 **5.8 秒**，每条都带 `duration` 与 `view_count`（RSS 没有时长） |
| 深度通道的代价 | flat 模式**没有任何时间戳**（200 条里 0 条有 `timestamp`） | 单条完整解析要 4.1 秒 ⇒ 200 条约 14 分钟且极易撞风控，所以**不逐条补时间** |
| 下载 | **yt-dlp 生成 AV1 + AAC 单文件**，封装用 `-c copy` | §11-4：所有 av01 档位都是 video-only，唯一的单文件只剩 `itag 18`（AVC 360p）⇒ 想同时要 AV1 和音轨，必须封装一次；`-c copy` 实测 0.05 秒、码流不变 |
| 失败主因 | **yt-dlp 版本过期**，不是解析难度 | §11-5：本机 2026.07.04（自称过期 90+ 天）对这个视频直接 403；换成 2026.10.26 一次成功 |
| 入库 | 走现有 M2 四件套：`Probe` → `RenderName`/`BuildMoviePath` → `writeNFOFile` + `writeArtwork` → `SaveLibraryFile` | 现成代码，YouTube 只是「另一个 platform」 |
| 图片 | 封面走 `/api/img`（缓存 + 隐私模式糊图）；卡片用 `hq720.jpg`（1280×720）、海报用 `maxresdefault`，都带 `f=` 兜底（老视频没有这两档 → 退回 `mqdefault`） | §11-6 |
| 错误语义 | 复用 `internal/social` 的 `rate_limited / blocked / stale_parser / not_found / upstream` | 三套脸的 UI 在 M5 已经做好了 |

### 2.2 已拍板（Q1–Q8）

| 编号 | 问题 | 结论 |
|---|---|---|
| Q1 / Q1b | 视频要不要落进本地库；「不要合轨」指什么 | **要下载**；AV1 优先，**接受 `-c copy` 封装**（不转码），产物 AV1+AAC 单文件 mp4 |
| Q2 | 历史深度 | **yt-dlp 一次列全**（配合 Q8 的 200 条上限） |
| Q3 | 移动端底栏 | 原先「底栏不动、动态页顶加切换」；2026-10-29 改成 **YouTube 直接进底栏**（`grid-cols-8`），动态页顶部那排切换拆掉 |
| Q4 | 页面形态 | **16:9 封面网格** |
| Q5 | 与艺人打通 | **不做**，只留 `performer_id` 占位 |
| Q6 | 下载落到哪 | **当普通本地库影片入库**：能进「关注」、能在详情页播、能生 nfo 让 Emby 刮到 |
| Q7 | 自动下载 | **只在墙上手动点**（单个 / 整频道批量） |
| Q8 | 首次列全上限 | **默认 200 条，可配**（`yt_backfill_limit`） |

## 3. 数据模型

新迁移 `0008_m6_youtube.sql`。**不复用 `social_*`**：那边一等公民是「帖子 + 照片」，这边是「频道 + 视频 + 下载目标」。

```sql
CREATE TABLE IF NOT EXISTS yt_channels (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    channel_id     TEXT NOT NULL UNIQUE,          -- UC + 22 位
    handle         TEXT NOT NULL DEFAULT '',      -- @xxx，可能为空
    title          TEXT NOT NULL DEFAULT '',
    avatar_url     TEXT NOT NULL DEFAULT '',
    profile_url    TEXT NOT NULL DEFAULT '',
    enabled        INTEGER NOT NULL DEFAULT 1,
    performer_id   INTEGER REFERENCES performers(id) ON DELETE SET NULL,  -- Q5 占位
    video_count    INTEGER NOT NULL DEFAULT 0,
    unseen_count   INTEGER NOT NULL DEFAULT 0,
    backfilled_at  TEXT NOT NULL DEFAULT '',      -- 深度回填最近一次时间，空 = 没跑过
    last_error     TEXT NOT NULL DEFAULT '',
    last_synced_at TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS yt_videos (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    channel_id    INTEGER NOT NULL REFERENCES yt_channels(id) ON DELETE CASCADE,
    video_id      TEXT NOT NULL,                  -- 11 位
    title         TEXT NOT NULL DEFAULT '',
    description   TEXT NOT NULL DEFAULT '',
    thumbnail_url TEXT NOT NULL DEFAULT '',
    published_at  TEXT NOT NULL DEFAULT '',       -- 空 = 不知道（深挖的老条目，见 §5.3）
    date_source   TEXT NOT NULL DEFAULT '',       -- rss / ytdlp / ''（未知）
    views         INTEGER NOT NULL DEFAULT 0,
    duration_sec  INTEGER NOT NULL DEFAULT 0,     -- 0 = 未知
    is_live       INTEGER NOT NULL DEFAULT 0,
    seen          INTEGER NOT NULL DEFAULT 0,
    saved         INTEGER NOT NULL DEFAULT 0,
    hidden        INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (video_id)
);

CREATE INDEX IF NOT EXISTS idx_yt_videos_wall ON yt_videos(published_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_yt_videos_chan ON yt_videos(channel_id, published_at DESC);

-- 「关注」流与 Emby 需要一个 platform 行
INSERT OR IGNORE INTO platforms (key, name) VALUES ('youtube', 'YouTube');
```

要点：

- **`video_id` 全局唯一**，不按频道去重：搬运/转发很常见，按视频 id 去重才不会墙上出现两条一样的。
- `published_at` 允许为空，`date_source` 记录来源。墙上的排序规则：`ORDER BY published_at DESC, id DESC`，空时间的排最后——**深挖回来的历史是用来「往回找」的，不是用来刷的**，配频道筛选使用。
- 计数冗余（`video_count` / `unseen_count`）在写入 / 标记已看的同一事务里更新，与 `social_accounts` 一致。
- **下载出来的影片进 `videos` 表**（`platform='youtube'`, `platform_video_id=<11 位>`）+ `library_files`，所以「关注」「下载」「详情页」天然认得它，Emby 也扫得到。

## 4. 信息架构

| 位置 | 内容 |
|---|---|
| 侧边栏「YouTube」（`/youtube`，`icon: 'youtube'`，紧跟「动态」之后） | 未看新视频混流，发布时间倒序；顶部一行频道筛选胶囊（头像 + 名字 + 未看数）；卡片右上角「下载」 |
| 动态页（`/moments`）顶部 | **不放**那排切换（2026-10-29 拆掉「动态 / YouTube / B 站」分段控件）：三个入口都在导航里，页面顶部只留筛选工具栏 |
| YouTube 页（`/youtube`）顶部 | **不放**那条切换（2026-10-28 去掉）：进了某条流就只管这条流，换流走侧边栏 / 底栏 |
| 管理页 → 新 tab「YouTube」 | 贴链接添加、启用开关、单独同步、深度回填、删除、看视频数/未看数/上次同步/错误；顶部四格总览（频道数 / 视频数 / 24h 新增 / 出错频道） |
| 设置 → 「网络与代理」 | 多一行 **YouTube 代理**（与 Instagram / X 同款，保存即生效） |
| 设置 → 新分类「YouTube」 | 抓取总开关、增量间隔（默认 6h）、深度回填上限（默认 200）、最高分辨率（默认 1080）、格式串（高级）、yt-dlp 路径 + **版本健康度**、Cookie（可选） |
| 卡片点开 | 嵌播弹层（`youtube-nocookie.com/embed/<id>`）+ 标题 / 频道 / 时间 / 播放量 +「在 YouTube 打开」+「下载进本地库」 |
| 移动端底栏 | 关注 / 动态 / YouTube / B 站 / 艺人 / 下载 / 管理 / 设置，`grid-cols-8`（「发现」仍在艺人页里） |

## 5. 抓取

### 5.1 两条通道，各司其职

| | 增量（RSS） | 深度（yt-dlp） |
|---|---|---|
| 触发 | 每 `yt_sync_hours`（默认 6h）自动 | 添加频道时自动跑一次；管理页可手动重跑 |
| 覆盖 | 最近 15 条 | `--playlist-end {yt_backfill_limit}`，默认 200 |
| 耗时 | 1 次请求，< 1s | 实测 200 条 5.8s |
| 给到 | 标题、绝对时间、播放量、封面、简介 | 标题、**时长**、播放量、封面（**没有时间**） |
| 依赖 | 无（只用标准库 XML） | 外部二进制 yt-dlp |

两条通道写同一张表，按 `video_id` upsert，**空值不覆盖已有值**（RSS 补时间，flat 补时长，两边都在补同一行）。

### 5.2 RSS 解析

`GET https://www.youtube.com/feeds/videos.xml?channel_id=<UC…>`（普通浏览器 UA）→ Atom：

`yt:videoId` · `<title>` · `<published>` · `media:thumbnail@url` · `media:statistics@views` · `media:description`（推广链接要塞进去展示前洗掉）

### 5.3 深度回填的时间问题（重要取舍）

flat 模式 200 条里 **0 条**带时间戳（§11-3）。逐条完整解析要 4.1s/条（200 条≈14 分钟）且极易撞风控，**不做**。所以：

1. 深度回填只补 `duration_sec` / `views` / `title`；
2. `published_at` 留空，`date_source=''`，卡片上时间位置显示「时间未知」；
3. RSS 覆盖到的那 15 条会被补上准确时间（`date_source='rss'`）；
4. 用户点开某条时按需单条补一次（`yt-dlp -J <watch url>`，4.1s，`date_source='watch'`）；
5. **整批补**：`yt_meta` 任务 + 墙上的「有 N 条视频没有发布时间 · 补齐」那一条（见下）。

**为什么整批补不走 yt-dlp**：单条要起进程、实测 4.1 秒，200 条就是 14 分钟。观看页是一次普通 GET——
实测 1.29MB（`Accept-Encoding: gzip` 之后约 1/5）、1.5 秒，`"publishDate"` / `"viewCount"` / `"lengthSeconds"`
全在页面 JSON 里（`internal/ytsub/watch.go`）。实测 27 条一轮 **45 秒**（≈1.7s/条，含限速），
落库 `date_source='watch'`；其中 3 条首次偶发失败、再点一次就补上了，所以按钮留着不隐藏，
失败文案也不写死成「已删除」。收益是「一次回填 200 条只要 6 秒」+「想起来补一次就全有日期了」。

### 5.4 失败语义

| 现象 | 归类 | 界面 |
|---|---|---|
| HTTP 429 / `Retry-After` | `rate_limited`（默认冷却 15 min） | 「限流，HH:MM 自动恢复」+ 清除冷却 |
| 「Sign in to confirm you're not a bot」/ 人机校验页 / 403 | `blocked`（**不设冷却**） | 「换出口 IP / 换代理」+ 设置直达 |
| yt-dlp 报 `Unable to extract` / `player response` 解析失败 | `stale_parser` **+ 明确写「yt-dlp 可能过期」** | 「去升级 yt-dlp」，附当前版本号 |
| 频道 404 / 0 条 | `not_found` | 「这个频道没有内容或已注销」 |

### 5.5 代理

`proxy_youtube` → `global_proxy` → 环境变量 `PLATFORM_PROXY_YOUTUBE` → `GLOBAL_PROXY`，**保存即生效**（`internal/proxying` 已经写好）。
yt-dlp 侧用 `--proxy <url>`，与站内客户端同一个取值点——否则「界面上配了代理但下载还是直连」这种割裂一定会在某天咬人。

## 6. 下载（yt-dlp → 入库）

### 6.1 命令

```
yt-dlp --no-playlist --newline --socket-timeout 20 --retries 3 --concurrent-fragments 4 \
       --proxy <若配置> --cookies <若配置，临时 0600 文件> \
       -f <格式串，默认 "bv*[vcodec^=av01]+ba/bv*+ba/b"> \
       --merge-output-format mp4 \
       -P "<dataDir>/tmp/yt-<videoID>-<libID>" -o "%(id)s.%(ext)s" \
       --progress-template "download:YTDLP %(progress.downloaded_bytes)s/%(progress.total_bytes_estimate)s %(progress.speed)s %(progress.eta)s" \
       https://www.youtube.com/watch?v=<id>
```

- 默认格式串 `bv*[vcodec^=av01]+ba/bv*+ba/b`：**AV1 最高画质视频轨 + 最高音质音轨**（要的就是画质和音质，不做高度上限）。
  回退链的含义是「拿得到」优先于「拿得漂亮」：AV1 + 任意音频 → 任意视频 + 任意音频 → 混流单文件。
  拿到什么编码就存什么，**不转码**；设置里的 `yt_format` 可以整串覆盖（比如嫌 mp4 里的 Opus 轨吵，改成 `bv*+ba[ext=m4a]/bv*+ba`）。
- `--merge-output-format mp4` 就是那次 `-c copy` 封装（yt-dlp 内部调 ffmpeg），实测 0.05 秒，码流字节不变。
- 进度用 `--progress-template` 输出机器可读行，映射到现有 `c.Progress(percent, text, done, total, speed)`，UI 与普通下载同一个进度条。
- 取消 / 暂停：`ctx.Done()` → 向子进程发 `SIGINT` 再 `SIGTERM`；yt-dlp 的 `.part` 留在工作目录，重跑带 `--continue` 续传（工作目录按 `videoID+libID` 固定，和现有 `dl-<videoID>-<libID>` 同一思路）。

### 6.2 入库流程（几乎全是现成的）

1. 确保 `videos` 行存在（`platform='youtube'`，标题 / 封面 / 时长 / 发布时间 / 原站 URL）→ 拿到 `videoID`
2. 幂等：`GetLibraryFile(videoID, libID)` 命中且文件在 → 跳过（`overwrite` 才重下）
3. yt-dlp 下到 `{dataDir}/tmp/yt-…/`（可续传）
4. `enc.Probe` 读真实时长/分辨率/体积
5. 补详情：条目是从 flat 列表来的，没有简介也没有发布时间，这时跑一次 `VideoInfo`（约 4 秒）
   ——既让 nfo 的 `<plot>` 有内容，也把发布时间写回 `yt_videos`（墙上「时间未知」就是这么消掉的）
6. 命名：`library.RenderName(tmpl, NameContext{Title, Performer=频道名, Platform="YouTube", ID=videoId, Date=published, Resolution})` → `library.BuildMoviePath`
7. 移动到目标位置（同盘 rename，不复制）
8. `writeArtwork` + `writeNFOFile`：底图按「站方封面（换成这条片子能给的最大尺寸）→ 视频自己的一帧 → 频道头像」
   挑，见 `internal/jobs/artwork.go` 的 `pickSource`；**目标尺寸是上限，不放大**
   （2026-11-02 之前这一步只用了库里存的 `hqdefault` 480×360 硬吹到 1000×1500，
   海报真像素只有 240×360——用户报的「poster/thumb/fanart 太糊」就是它）
9. `st.SaveLibraryFile`
10. 事件 `download.done`（复用现有事件，前端下载记录自动更新）

### 6.3 与现有下载的区别（要留心的点）

- 现有 `download` 任务绑定 `platform.Adapter`（要 `pickVariant` 拿 HLS 清单）；YouTube **没有 adapter**，所以这是**新任务类型 `yt_download`**，不走 `runDownloadJob`。
- 「关注」流里会出现 `platform='youtube'` 的影片，而 `reg.Get("youtube")` 查不到 adapter。所有走 registry 的路径（刷新、在线播放、重新下载）都要能容忍「平台未注册」而不是报错——**这是实现时最容易漏的一处**，要单测覆盖。
- 批量下载（整频道）就是给未看的视频逐个入队，队列并发沿用现有下载并发控制。
- **任务的 `target_id` 用 `videos` 的行 id，不是 `yt_videos` 的**：下载页是通用页面（按 `target_id` 查标题、缩略图、本地文件），
  两个表的 id 空间不同，用错会把别的影片的信息贴到这条任务上。所以入队时先把 `videos` 行建出来（`EnsureVideoForYt` 幂等）。
- 下载页（`GET /api/downloads`）现在同时返回 `download` 与 `yt_download`；统计里的「下载完成」也把两者算在一起。

## 7. 任务与调度

| 任务 | kind | 并发 | 说明 |
|---|---|---|---|
| 增量抓取 | `yt_sync` | 1（与 `social_sync` 同理：按 IP 限流，串行最稳） | 一轮 = 每个启用频道 1 次 RSS |
| 深度回填 | `yt_sync`（`deep=true` 参数） | 1 | 添加频道时自动跑；管理页可手动重跑 |
| 下载 | `yt_download` | 由现有下载并发控制 | §6 |
| 单条补时间 | 不是任务：`POST /api/yt/videos/{id}/refresh` | — | 点开一条没有时间的视频时同步调一次（4 秒） |
| 整批补元信息 | `yt_meta` | 1 | 走观看页（`internal/ytsub/watch.go`），不依赖 yt-dlp；补**发布时间 / 时长 / 播放量**三样（两条抓取通道各缺一半：RSS 不给时长、yt-dlp flat 不给时间戳）。墙上「补齐」按钮 = `POST /api/yt/fill-meta`；每轮 `yt_sync` 结束时缺的行 ≤ 40 条会自动排一次 |

调度沿用现有 scheduler；`enabled=0` 跳过。SSE 新增 `yt.new`（`{channelId, title, count}`），侧边栏未看角标复用 `live`。

## 8. 前端

| 文件 | 内容 |
|---|---|
| `web/src/routes/Youtube.svelte` | 频道头像条（`AvatarRail`，和关注页同款）+ 16:9 网格 + `InfiniteList` + 嵌播弹层 |
| `web/src/components/YtCard.svelte` | 封面（`hq720` 铺满 / `maxresdefault` 大图，均带 `f=` 兜底）、时长（右下角纯文字 + 三层 `text-shadow`，**不带黑色药丸底**）、**单行标题**（`truncate`，并把末尾那串 `#标签` 削掉——实测 `/youtube` 首屏 36 张卡里 21 张带，最长一条 54 字削完只剩 12 字的片名；原始标题仍在 `title` 提示和详情页上）、**36px 圆头像**（和 B 站墙的 `VideoCard` 同尺寸、同 `mt-2.5 flex items-start gap-2.5` 行）、副行「频道 · ▶ 观看数 · 日期」（观看数是 12px **空心**播放三角 + 数字——`stroke` 描边不填色，不再写「次观看」，省下的宽度留给频道名和日期；读屏走 `aria-label` 念「908 次观看」）、未看小圆点、未看状态下右上角下载按钮、直播标记 |
| `web/src/routes/Manage.svelte` | 新 tab `youtube`（结构照抄 moments 那块）+ 顶部四格总览 |
| `web/src/routes/Moments.svelte` | 页顶加「动态 / YouTube」切换 |
| `web/src/components/AppShell.svelte` | `NAV` 加一项 + `navIcon` 加 `youtube` + 未看角标 + `JOB_LABEL` 加 `yt_sync` / `yt_download` |
| `web/src/components/SettingsDialog.svelte` | YouTube 代理一行 + 新分类「YouTube」（含 yt-dlp 版本与健康度） |
| `web/src/lib/{types,api,platform}.ts` | `YtChannel` / `YtVideo` 类型、`yt.*` 方法、`PLATFORM_LABEL.youtube = 'YouTube'` |

## 9. 接口契约（草案）

```
GET    /api/yt/channels                       频道列表（含计数 / 错误 / 上次同步 / 回填时间）
POST   /api/yt/channels                       {url|channelId|handle} → 解析 + 落库 + 排队深度回填
POST   /api/yt/resolve                        {url} → 只解析不落库（添加前预览：标题/头像/视频数）
PATCH  /api/yt/channels/{id}                  {enabled?} / {performerId?}
DELETE /api/yt/channels/{id}
POST   /api/yt/channels/{id}/sync             {deep?:bool} → 入队
GET    /api/yt/videos?channel=&cursor=&unseen=  视频墙（keyset）
POST   /api/yt/videos/seen                    {ids?|channelId?}
POST   /api/yt/videos/{id}/save               {saved:true|false}
POST   /api/yt/videos/{id}/download           {libraryId} → 入队 yt_download
POST   /api/yt/channels/{id}/download-unseen  {libraryId} → 整频道批量入队
GET    /api/manage/youtube                    管理页总览
GET    /api/yt/health                         yt-dlp 路径 / 版本 / 是否过期 / ffmpeg 可用 / missingDates、missingDurations（还缺多少条时间 / 时长）
POST   /api/yt/fill-meta                      → 入队 yt_meta（整批补元信息），返回 {jobId, total, missingDates, missingDurations}
PUT    /api/settings                          + platformProxy.youtube / yt_* 五项
GET    /api/yt/videos/{id}/stream             站内播放：解析结果 + 画质清单（§14）
GET    /api/yt/videos/{id}/stream.m3u8[?q=]   改写后的单变体 master
GET    /api/yt/seg/{sid}/{n}                  会话取物口（子清单 / 分片）
```

上面三条是 §14 后补的「站内播放」口子，细节见 `docs/API.md` C19。

错误码沿用 `docs/API.md` 那张表：`needs_auth` / `rate_limited`（带 `resetAt`）/ `blocked`（不带）/ `stale_parser` / `not_found`。

## 10. 验收标准

1. 贴 `@handle`、`/channel/UC…`、`watch?v=…` 三种链接都能加成频道，10 秒内出频道信息与最近 15 条
2. 深度回填跑完（200 条 / 一个频道）**≤ 15 秒**，墙上出现带**时长**角标的卡片
3. 6h 后自动抓到新视频，侧边栏未看角标 +1，打开页面即清
4. 点卡片能站内播（服务端 HLS，最高画质到 4K）；断网时给「在 YouTube 打开」，不是白屏
5. 隐私模式下封面糊掉（与影片卡片一致）
6. 下载单个视频 → `ffprobe` 确认 **`av1` 视频流 + `aac` 音频流在同一个 mp4 里**、无重新编码痕迹（码流与源一致）、体积与源相近
7. 下载完成后：下载记录出现、影片详情页能播、`{库}/YouTube/{频道}/{片名}/` 下有 mp4 + nfo + 海报，Emby 能刮到标题与频道
8. 暂停后恢复：不重下已完成的片段（`.part` 续传）
9. 设置里换 YouTube 代理不重启即生效（站内抓取与 yt-dlp 下载都走新出口）
10. yt-dlp 换成过期版本时，界面给的是「yt-dlp 已过期，请升级」，不是一句 403
11. `go vet` / `go test` / `svelte-check` 全绿

## 11. spike 原始记录（2026-10-28，本机直连，无代理）

### 11.1 RSS（可用，免登录）

```bash
curl -s -A 'Mozilla/5.0' 'https://www.youtube.com/feeds/videos.xml?channel_id=UCq-Fj5jknLsUf-MWSy4_brA'
```

`200`，**恰好 15 个 `<entry>`**，无游标。字段：`yt:videoId` / `<title>` / `<published>`（RFC3339）/ `media:thumbnail@url`（480×360）/ `media:statistics@views` / `media:starRating@count` / `media:description`。**没有时长、没有 Shorts 标记。**

### 11.2 频道 id 解析

`https://www.youtube.com/@tseries`（2.8MB HTML）里同时有：

```
"externalId":"UCq-Fj5jknLsUf-MWSy4_brA"
<link rel="canonical" href="https://www.youtube.com/channel/UCq-Fj5jknLsUf-MWSy4_brA">
```

同一份 HTML 里另有大量别的 `"channelId":"UC…"`（推荐位），**不能取第一个 matching**。

### 11.3 yt-dlp flat 列频道

```bash
yt-dlp -J --flat-playlist --playlist-end 200 https://www.youtube.com/channel/<UC>/videos
```

- 200 条 **5.8 秒**，`duration` 200/200 有值，`view_count` 有值，`timestamp` **0/200**
- 单条完整解析（`yt-dlp -J <watch url>`）：**4.1 秒**，有 `upload_date` / `timestamp` / `view_count` / `channel` / `channel_id`

### 11.4 为什么必须封一次（AV1 没有音轨）

`ytInitialPlayerResponse.streamingData` 的实测结构：

| 项 | 结果 |
|---|---|
| `formats`（progressive） | **字段已不存在**（迁 SABR） |
| `adaptiveFormats` | 26 条，音视频全部分离 |
| av01 档位 | 144p / 240p / 360p / 480p / 720p / 1080p / 1440p / 2160p，**八档全部 video-only** |
| 唯一的单文件 | `itag 18`（AVC 360p + AAC）——`-f b` 拿到的就是它 |

`-c copy` 封装实测：**0.052 秒**，`ffprobe` 前后两条流参数完全一致（不重编码）。

### 11.5 yt-dlp 版本是命门

| 版本 | 结果 |
|---|---|
| 本机 homebrew `2026.07.04`（yt-dlp 自己警告「过期 90+ 天」） | 默认 client **HTTP 403**；`mweb` 只给 `itag 18`；`android` 不给 AV1 |
| 临时装的 `2026.10.26` | `av1 Main 1080p + aac LC 2ch` 单文件 mp4，184s / 20.0MB，**8.8 秒下完** |

⇒ 设置里必须显示 yt-dlp 版本 + 过期提示；抓取失败时优先归因到版本而不是「站方改版」。

### 11.6 封面（尺寸就写在文件名里，且有几档可能不存在）

同一支视频的各档实测（`i.ytimg.com/vi/<id>/<name>.jpg`，四个 id 取样）：

| 档 | 分辨率 | 体积 | 备注 |
|---|---|---|---|
| `mqdefault` | 320×180 | 6.4–17.3KB | 16:9 无黑边，**永远有**（老视频也有），当兜底 |
| `hqdefault` | 480×360 | 12.9–23.8KB | 4:3，两侧黑边，别铺 16:9 卡片 |
| `sddefault` | 640×480 | 36–70KB | 4:3，同上 |
| `hq720` | 1280×720 | 30–173KB | 16:9；**老视频 404**（`jNQXAC9IVRw` 上 `sddefault`/`hq720`/`maxres` 全 404，返回一张 120×90 灰图） |
| `maxresdefault` | 1280×720 | 同 `hq720` | 有 720 源时两者字节完全相同 |

结论：卡片用 `hq720`（墙上 2xl 断点 4 列、每张 360 CSS px，DPR2 要 720——1280 够且富余），
海报用 `maxresdefault`。**两档都可能 404，而 `/api/img` 抓不到时只发 1×1 空白（HTTP 200，前端 `onerror` 发现不了）**，
所以在代理层做兜底：DTO 拼地址时带 `f=<兜底地址>`，主地址抓不到就再抓 `f=`（卡片 → `mqdefault`，海报 → `hq720`）。
实测：`hq720` 存在 → 200 / 137,875 字节 / 1280×720；`jNQXAC9IVRw` 的 `hq720` → 走 `f=` 拿到 8,710 字节 / 320×180（原来是一张 1×1）。

顺带记一笔：`hq720.webp`（`i.ytimg.com/vi_webp/<id>/hq720.webp`）比 JPEG 小约 35%（6 个样本 67–88KB vs 109–140KB），
没采用——隐私模式的糊图流水线要 `image.Decode`，WebP 得每次都落到 ffmpeg 子进程，为 35% 带宽换一条更慢的路径不划算。

## 12. 风险与取舍

| 风险 | 说明 | 应对 |
|---|---|---|
| yt-dlp 版本 | 两周不升级就可能全线 403 | 界面显示版本 + 过期警告；`stale_parser` 文案直接指向升级 |
| 深挖条目没日期 | flat 模式不给时间；走 yt-dlp 逐条补太慢（4.1s/条） | 走观看页批量补（1.5s/条，`yt_meta`），墙上一条提示 + 一个按钮 |
| 首次回填 200 条上限 | 老频道历史远多于此 | 可配；管理页提供「再拉一批」（改 `--playlist-start` 续接） |
| AV1 只有视频轨 | 必须封装一次 | 已是既定方案；`-c copy` 不损画质，产物单 mp4 兼容 Chrome / Firefox / Safari 17+ / Emby |
| 高分辨率体积 | 8K AV1 90 秒就有 358 MB（实测），整频道批量下载很容易把盘吃满 | 不设高度上限（用户要「最高画质」）；`CheckSpace` 兜底 + 只做手动/整频道批量，不做全自动 |
| 站内播放依赖出口 | HLS 分片要服务端去 googlevideo 拉，断网/代理挂了就是黑框 | 播放页的浮层里留「重试」与「改用 YouTube 播放器」两条退路，标题行始终保留「在 YouTube 打开」 |
| 没有 adapter 的 platform 进「关注」 | 刷新 / 在线播放 / 重下三条路径都可能踩空 | `reg.Get` 失败按「只有本地文件」降级，补单测 |
| 法律 / 条款 | 下载 YouTube 内容与 ToS 冲突 | 只做用户在自己机器上的自选行为，不做分发；默认手动下载，不自动批量 |

## 13. 实现落定（与草案的差异）

写代码时定下来的、草案里没写或写得不一样的地方，一并记在这里：

| 点 | 落定方式 | 为什么 |
|---|---|---|
| 格式串 | `bv*[vcodec^=av01]+ba/bv*+ba/b`，无高度上限 | 用户明确要「最高画质 + 最高音质」；`yt_max_height` 的想法废弃 |
| yt-dlp 路径 | 新增设置 `ytdlpPath`（空 = 走 `PATH`），环境变量 `YTDLP_PATH` 兜底 | 想固定用一个独立安装的版本，不动系统里那个 |
| 健康度 | `GET /api/yt/health` + 设置页/管理页/视频墙三处展示 | 版本过期是头号故障，不能只在日志里 |
| 单条补时间 | `POST /api/yt/videos/{id}/refresh`（同步 HTTP，非任务） | 4 秒的事不值得排队 |
| 频道头像 | 解析 UC id 时多抓一次频道页取 `og:image` | RSS 里没有头像，只靠 RSS 会得到一堆首字母占位 |
| 频道名 | 视频链接反查频道时**再抓一次频道页** | 视频页的 `og:title` 是那条视频的标题 |
| `performer_id` | 写入时 0 → `NULL`，扫描时用 `sql.NullInt64` | 外键列不接受 0；直接扫进 `int64` 会在 NULL 上炸（这两个都实测踩到过） |
| 未读数 | `yt_videos.seen` + `stats.unseen`，侧边栏角标复用 `live.ytUnseen` | 与动态页同一套手感 |
| 下载页 | 混入 `yt_download`；`target_id` 用 `videos.id` | 用户在「下载」页应该看到同一件事 |
| 完成后进度 | `FinishJob` 落库时把进度补成 100 | 最后一次 `Progress` 常被 800ms 节流吞掉，否则页面出现「完成 · 92.0%」 |
| 隐私模式 | 播放页默认走**站内播放**（同源 HLS，浏览器不直连 YouTube）；只有解析失败时的「改用 YouTube 播放器」兜底按钮在隐私模式下隐藏 | §14 之后站内播放本身就是隐私友好的：一嵌 iframe 就等于让浏览器直连 YouTube 拉整段视频 |
| 详情页播放 | **新增本地播放**：`GET /api/videos/{id}/file`（ServeFile，支持 Range），`/playback` 把本地档排第一并作默认档 | 下完点开详情页原来是 `unsupported_platform`——没有 adapter 就没有播放地址，这是 §6.3 预警过的那条路，实测确实踩到了 |
| 详情页下载 | `POST /api/videos/{id}/download` 在没有 adapter 时用 `platform_video_id` 反查 `yt_videos`，落到 `yt_download` | 否则详情页那个「下载到本地库」按钮点了就报错 |
| 取消 / 暂停 | 下载期间每秒查一次取消标志；`Download` 先 `cmd.Wait()` 再收管道（`WaitDelay=3s`） | 原来点了取消要等整段下完：yt-dlp 调起来的 ffmpeg 也攥着管道，只等 EOF 会一直挂着（实测 60 秒） |
| 已看语义 | 点开一条视频才标已看（另有「全部标为已看」）；**不会因为打开页面就全清** | 照片墙那种「打开即清」对视频墙不成立：200 条历史没人会一口气看完 |

## 14. 站内在线播放（2026-10-28 后补的一条线）

### 14.1 起因：iframe 会拒播

M6 上线时播放页里放的是 `https://www.youtube-nocookie.com/embed/<id>`（当时还是个弹窗）。实测发现**一批版权音乐视频
在嵌入里直接被拒**，显示「此视频不能观看 / この動画は再生できません」：

| 视频 | iframe 嵌入 | 直接打开 watch 页 |
|---|---|---|
| `s8OtOH_Vfa0`（T-Series《Ek Onkar》） | ❌ 此视频不能观看 | ✅ 正常 |
| 多条 T-Series 官方 MV | ❌ 同上 | ✅ 正常 |
| `lTRiuFIWV54`（Lofi Girl 直播录像） | ✅ | ✅ |
| `jNQXAC9IVRw`（Me at the zoo） | ✅ | ✅ |

排查结论（写下来省得以后再查一遍）：

- **不是本应用的锅**：用一张裸 HTML 页面（不含任何本项目代码）嵌同一个 id，一样被拒。
- `oEmbed` 接口对所有样本都返回 200，说明**不是上传者关闭了嵌入**——是 YouTube 按内容/版权方策略拒绝嵌播。
- `file://` 下嵌会报 **error 153**（缺 Referer）；`http://127.0.0.1:…` 下就正常，说明 loopback Referer 是被接受的。
  所以「起个本地页面验证一下」这件事是可行的，别被 153 误导。

于是决定不再依赖 iframe：**由服务端解析 HLS 并代理，浏览器只跟本机说话**。

### 14.2 做法：不碰 `streamingData`，走 yt-dlp 给的 HLS

§11 已经记录过：观看页的 `streamingData` 里没有混流文件、只有 av01 纯视频轨，自己拼 DASH 太脆。
这次换一条路——`yt-dlp -j` 的输出里本来就有 HLS 清单（`formats[].protocol == "m3u8_native"`，
`manifest_url` 就是签名过的 `hls_variant` master，寿命约 6 小时、绑出口 IP）。于是：

1. `internal/ytsub.ResolveStream` 跑一次 `yt-dlp -j --no-playlist`（**故意不带 `-f`**：这里要的是清单，
   不是可下载格式），从 `formats[]` 里挑第一个有 `manifest_url` 的，顺带从地址里解析出 `expire=`。
2. `internal/ytstream.Manager` 做「地址簿 + 解析缓存」：按 `videoId` 缓存 30 分钟（或被签名寿命截断），
   每次播放开一个 **Session**，把上游签名地址登记成 `序号 → 地址`，对外只发 `/api/yt/seg/{sid}/{n}`。
3. `internal/hls.ParseMaster` 解析出变体与音轨，`PickVariant` 选最高一档、`AudioFor` 找到配套音轨。
4. `internal/httpapi` 把 master 改写成**单变体**清单（顺手把 `#EXT-X-MEDIA` 音轨搬进来），
   子清单再改写一层，分片走既有的 `proxyMedia`（Range 透传、按平台补 Referer / UA / 代理）。

浏览器全程只看到 `/api/yt/…`，**googlevideo 的签名链接一次都没出过服务端**。

### 14.3 为什么只留一档画质

用户要的是「最高画质」，那就别让 ABR 自己挑：

- 清单里只放一个变体 ⇒ hls.js 没有第二档可选，不会在网络抖动时把画面从 4K 掉到 480p。
- 手动降档仍然支持（播放器里的「画质」菜单 → `/stream.m3u8?q=720`），菜单里的档位就是 master 里
  全部变体的高度去重（2160p / 1440p / 1080p / 720p / 480p / 360p / 240p / 144p），带 `VP9 · 18.7 Mbps`
  这种编码与码率提示。
- 带宽是免费的：本机 / 局域网播放，4K 也就 18 Mbps。

### 14.4 实测数据（2026-10-28，本机直连）

| 项 | 结果 |
|---|---|
| 解析（`yt-dlp -j`） | 4~5 秒，YouTube 侧缓存后 30 分钟内不再付这个成本 |
| master 清单 | 24 KB，`hls_variant`，含 8 档视频 + 2 组音轨（233=Opus / 234=AAC） |
| 子清单 | 877 KB / 1369 行，分片地址是 `rr1---sn-….googlevideo.com/videoplayback/…` 的绝对地址 |
| 分片 | 200，`Content-Type` 有时是 `application/octet-stream`；**Range 被忽略**，永远整段返回 |
| 浏览器实播 | Chrome 起播 `readyState=4`、`videoWidth=3840`、`currentTime` 秒级推进，全程只有 `/api/yt/seg/…` 请求 |
| 切画质 | 点 720p → 新请求 `?q=720` → `videoWidth=1280`，播放不中断 |
| 隐私模式 | 同样能播（同源，不触发任何 YouTube 域名请求） |

### 14.5 失败语义

| 情况 | 表现 | 文案 / 处理 |
|---|---|---|
| 没有 yt-dlp | `409 no_ytdlp` | 指向「设置 → YouTube 路径」 |
| 这条没有 HLS 清单（直播 / 会员专享 / 分区限制） | `502 upstream` | 「这条视频没有可用的 HLS 清单（直播/会员专属/分区限制时会出现）」 |
| 会话过期（关掉页面放了几小时再回来） | 分片 `410 session_expired` | 前端「重试」会重新解析一次，不用刷新页面 |
| 上游分片 403（签名过期） | 分片非 2xx → hls.js 报错 | 同上，走「重试」重新开一个会话 |
| 任何播放失败 | 播放器覆盖层 | 「重试」+「改用 YouTube 播放器」（隐私模式下只留「重试」） |

`embedUrl` 仍然留在 C8 的响应里，就是给这个兜底用的。

### 14.6 已知限制

- **直播**：`is_live` 的条目理论上也能播（HLS），但清单会滚动，会话里的序号会一直增长；
  实测只验了点播，直播没验。
- 4K 是 28.2 Mbps 的 VP9 60fps，链路只有 1.5–2 倍余量（实测 3.5–8.4 MB/s）；DASH 里的同一部 4K AV1 只要
  13.5 Mbps，但我们走 HLS 拿不到（见 §14.9）。软件解码的老机器上 4K 会卡——那是本地解码能力，手动降到 1080p 即可。
- 后台标签页播放会被浏览器的节能策略降频（与本应用无关）。

### 14.7 播放是一条独立页（`/youtube/:videoId`）

2026-11-02 之前这里盖的是一层弹窗（`/youtube?v=<videoId>`），现在整页跳转：

```
/youtube/<videoId>         例如 /youtube/s8OtOH_Vfa0
```

- **和关注流、B 站点开影片同一种形态**（`/video/:id`）：整页播放器、标题行右端一排图标键、
  右侧 14rem 资料列、底部一排同频道的其它视频（见 `ARCHITECTURE.md`「YouTube 单条播放页」那一行）。
  改的理由不是审美：弹窗最宽 1024px、标题小一号、资料挤在标题下面一行，同一部片在两个入口下长得不一样。
- **URL 是唯一真相**：点卡片 → `pushState` 到 `/youtube/<id>`；浏览器后退回墙（墙的 `?channel=` / `?mode=`
  因为写在地址栏上，回来还是同一个视角，滚动位置由 `popstate` 里的 `state.scrollY` 还原）。
  标题换成视频名（`document.title`），浏览器标签页一眼能认出是哪条。
- 参数是 **YouTube 自己的 `videoId` 字符串**（`dQw4w9WgXcQ` 那种），不是 `yt_videos` 的行 id；
  取数走 `GET /api/yt/videos/lookup?videoId=…`（见 `docs/API.md` C20），播放地址走
  `GET /api/yt/videos/{id}/stream`。
- 标题行的「复制链接」复制的是上面这个站内地址（不是 YouTube 的 watch 链接）。
- **旧链接照样能用**：`/youtube?v=<id>` 一进墙就 `replace` 成 `/youtube/<id>`，
  频道 / 筛选参数原样带过去，历史里不会留一条「一进来就跳走」的记录。
- 「上一个 / 下一个」按**视频所属频道**的列表算（`GET /api/yt/videos?channelId=&limit=60`），
  不跟墙上的筛选：`?mode=unseen` 会和「进页面就标已看」打架（标完再按未看查，这条根本不在结果里，
  前后两格全灰）。同一份列表顺带就是底部那排「同频道的其它视频」。

### 14.8 起播为什么慢，后来做了什么

慢在两段，量过之后分开治：

| 阶段 | 本机实测 | 性质 |
|---|---|---|
| yt-dlp 解析（拿签名 master） | ~4.0–4.8 s | 网络：到 YouTube 要连续问 5 次（网页 / 两个 player API / player JS / m3u8），单个来回就 ~1 s |
| 解析之后到出画面 | ~1.6 s | 子清单 0.35 s + 第一个分片 0.9 s，都是「浏览器问一句、我们再替它跑一趟」 |

实际做的调整（按贡献排序）：

1. **预热（最大的一项）**：`GET /api/yt/videos/{id}/stream` 拿到解析结果后，立刻在后台把
   两条子清单和头两个分片取回来；点开一条进播放页、播放器开口时这些已经在本机内存里。
   内存里这份缓存按视频存（`ytstream.Warm`，15 分钟、最多 8 条 / 32 MB），
   与播放会话无关——会话负责发号，预热负责字节。
2. **预热要比播放器早**：前端在视频墙渲染出来时就预热头三张（错开 1.5 秒发，
   三个一起发会跟「刚进页面就点第一条」抢 CPU，实测把那个场景从 5.1 s 拖到 6.7 s），
   悬停任意一张也预热（`YtCard` 的 `pointerenter` / `focus`），这样点下去时那 4 秒往往已经跑完了。
3. **同一份 master**：`manifest.googlevideo.com` 每被请求一次就重新签一遍子清单地址
   （sig/lsig 每次都不同）。原来 info 和 master 各拉一次 master，预热存下的地址和播放器
   手里的地址是两个字符串，缓存永远打不中。现在解析结果按视频缓存（`cachedYtSource`，
   跟着签名到期），全流程共用同一份 URL。
4. **解析单飞**：悬停、进播放页、拉清单会在同一两秒里发出，没去重就并发跑三四个 yt-dlp，
   互相抢 CPU 和连接。`ytstream.Manager` 现在对同一 videoID 只跑一次，其余等结果。
5. **预热字节也单飞**：`ytstream.Warm` 原来只有「取回来的字节」这一层缓存，在途的那份看不见——
   起播时预热正在下 init 段和第一个分片，播放器自己也去要同一段，两条请求平分同一根出口带宽，
   第一帧被硬生生拖慢。现在预热先 `Claim` 一条地址（同一地址只有一个 goroutine 真去取），
   分片请求 `Wait` 在途的那次取数、取完直接从内存发（`Claim`/`Wait`/`Settle`，见 `Warm` 的注释）。
   冷启动「点第一张卡到出第一帧」实测 4.4 s → **2.0 s**（两条 2160p 子清单 + init 段不再各下两遍）。
6. **解析缓存从 30 分钟放到签名到期**（≈6 小时）：签名本来就有这么久，
   期间再问一遍 YouTube 没有任何意义。

结果（同一台机器、Chrome、点第一张卡到出第一帧）：

| 场景 | 之前 | 现在 |
|---|---|---|
| 这条视频解析过、预热已跑完 | 1600 ms | **200–300 ms** |
| 列表看了几秒再点（解析已跑完，但预热还在下分片） | 4400 ms | **2000 ms** |
| 列表刚出来就点（解析还在跑） | 5100 ms | ~4000–5500 ms（等 yt-dlp，绕不过去） |

剩下的几秒只能靠「早问」和「别重复问」化解，没法消除：master 地址是签名的，
不解析就拿不到，缓存又只能活到签名过期。解析这一步本身也随视频浮动：本机量最常见的一批
（含两三小时的长直播回放）单跑 `yt-dlp -j` 就要 5.8–7.7 s，其中它自己的 CPU 只占 0.4 s，
其余全在等 YouTube 的网页 / 两个 player API / player JS / m3u8。**这段时间的画面交给封面态**
（见 `ARCHITECTURE.md` 的「加载态」：轻糊的封面 + 一行「正在解析最高画质…」，不是黑屏）。

### 14.9 4K 卡顿的真凶：预热把第一个分片切成了 16 MiB

2026-10-29，`YFsMnJEbV1U`（321 s、2160p VP9 60fps、**28 220 650 bps**）在线播放卡顿复现与定位。
YouTube 原站点同一台机器流畅，说明链路够、也说明问题在我们这一段。

| 观测量 | 数值 |
|---|---|
| 4K 分片体积 / 时长 | 10.4–23.2 MB / 5.1–7.0 s（单连接 3.5–8.4 MB/s，4 路并发合计 6.4–7.6 MB/s） |
| 卡顿当时的症状 | `currentTime` 死钉在 0，`buffered` 空，40 秒里 28 次 `waiting`，hls.js 反复 `bufferAppendError` 打在同一个分片上 |
| 预热返回的第一个分片 | **恰好 16777216 字节**，重复请求 0.005 s 就回来（命中预热缓存）→ 浏览器每次拿到的都是同一段残缺字节 |
| 上游同一地址 | 17 488 904 字节，md5 与修好之后的代理响应一致 |

根因：`internal/httpapi/stream.go` 的 `maxWarmObject` 是 16 MiB，预热取数走 `io.LimitReader`
**静默截断**，4K 一个分片就是 17–23 MB，于是「预热过的那个分片」永远是半个文件；
截断后的字节被当成缓存命中反复喂给 MSE，hls.js 只能一直 append 失败。1080p（单分片 ~5 MB）碰不到这条线，
所以只有 4K 卡——这也解释了「B 站 4K 修好之后 YouTube 4K 还是卡」。

改了三处（都在同一条链路上，缺一条仍会卡或跳帧）：

1. **上限抬到 64 MiB，且超限不再截断**：`readCapped` 多读 1 字节，超限直接报错、不缓存
   （宁可退回直连，也不把半个文件当缓存）。`internal/ytstream` 的预热总预算同步 32 MB → 128 MB
   （4K 一段 21–23 MB，8 条视频的上界约 1 GB）。
2. **hls.js 缓冲 30 s / 60 MB → 60 s / 250 MB、回看缓冲 30 s**：默认值在 4K 下换算成只有 ~17 秒
   （3.5 MB/s × 60 MB），链路只要抖一下就 `waiting`。改完 61 秒采样里缓冲 12→30.6 s、`currentTime` 实时推进、
   0 次 stall、0 条错误。
3. **分片 403 重试**：googlevideo 会偶发对单个分片回 403（1 秒后重试就 200，curl 也能复现），
   以前直接 502 抛给 hls.js，前端要等 3–9 秒的 `fragLoadError` 重试链。现在
   `openUpstream` 对 403/408/429/5xx 在**还没往浏览器写任何字节之前**最多重试 3 次（150 ms / 400 ms 退避）。

排查时留了一个开关：`MYPORN_YT_WARM=0` 关掉整个起播预热（`warmYtVideo` 直接返回）。
「预热取回的字节」和「浏览器自己那条路」出问题不好分，关掉再跑一遍就能二分。

顺带量清楚的两件事，别下次再从零查：

- **无头 Chromium 不能用来判 4K 解码**：同一份分片（md5 与上游一致）在无头里会随机报
  `PipelineStatus::PIPELINE_ERROR_DECODE: video decode error!`，`navigator.mediaCapabilities` 却仍说
  `smooth/powerEfficient=true`。链路和缓存用 curl 计时、用分片体积判断，别用无头里的卡顿反推服务端。
- **YouTube 的 4K 在 HLS 里只有 VP9，没有 AV1**：master 的 2160p 变体是 `vp09.00.51.08` 28.2 Mbps；
  DASH（`protocol=https`）才有 `av01.0.13M.08` 2160p60 **13 461 975 bps**（刚好一半）。
  也就是说链路余量只有 1.5–2 倍，想再稳一点的正路是补 AV1/DASH，不是继续调缓冲。

### 14.10 4K 换成原生播放（AV1 + DASH），MSE 那条路不再碰

§14.9 的三处修完之后 4K 还是卡（用户 2026-10-29 第二次反馈）。复测确认卡顿有两个来源，
而两个都不是「再把缓冲调大」能解决的：

| 观测量（YFsMnJEbV1U，321 s，1440×900 窗口） | 数值 |
|---|---|
| HLS 里 2160p 唯一那一档 | `vp09.00.51.08`，**28 220 650 bps**（1.06 GB）→ 要 3.5 MB/s 才跟得上 |
| 本机单连接实测 | 3.5–8.4 MB/s（余量 1–2 倍，缓冲攒不起来） |
| 改前 40 秒采样 | **卡 13 次**，`currentTime` 钉在 32.39、`buffered` 到 32.4 就断 |
| DASH 的 AV1 2160p（itag 401） | `av01.0.13M.08`，**13 461 975 bps**（540 MB）→ 只要一半带宽 |
| 改后 50 秒采样（原生 `<video>`） | `currentTime` 0.97 → 50.18（每秒整 1 s）、**0 次卡顿**、0 条媒体错误、`readyState` 恒 4 |

另一个来源是 MSE 本身：这台机器的 VideoToolbox 在 MSE 上放 VP9 4K 每 5–30 秒崩一次
（`PIPELINE_STATUS_ERROR` / `PIPELINE_ERROR_DECODE`，见 §14.9 的自愈补丁），而同一份字节用原生
`<video>` 播放零错误。所以结论是**绕开 MSE**，而不是继续加自愈次数。

做法（复用 B 站 4K 那条现成的合流缓存 `internal/muxcache`）：

1. `ytsub.ResolveStream` 顺带从 `formats[]` 里挑出「只有画面」的 AV1 mp4 直链（+ 一条 AAC 音轨，
   实测 itag 140 / 129 kbps / 5.2 MB），存进 `DashTrack`。只认 `av01* + mp4 + https + aac`：
   VP9 那条是 webm 且 26.3 Mbps，码率翻倍没有意义。
2. `GET /api/yt/videos/{id}/file?q=2160` 用 ffmpeg `-c copy` 把两条流合一份 mp4 落盘缓存
   （`.tmp/run/data/mux/yt-<videoID>-2160p.mp4`），再按普通文件发——Range/206、拖进度条全都白拿。
   `?status=1` 立刻回 `{state,percent,elapsedSec}`，前端轮询到 ready 才把地址交给 `<video>`。
3. 画质菜单里这一档报的是**实际要播的那条**（AV1 13.5 Mbps），不是 HLS 的 VP9 28.2 Mbps，
   并且带 `progressive: true`；前端据此走 `engine="native"`。
4. 只有 2160p 走这条路（`h >= 2160`）；1440p 及以下的 VP9 码率在实测带宽下还跟得上，
   没必要多等一次合流。

实测合流耗时（同一台机器、同一时段）：**545 735 981 字节 / 4 分 0.5 秒**（单连接 ≈2.27 MB/s）。
第一次点开 4K 要等这么久，所以等待位上写了百分比 + 「不想等？先看 1440p」的按钮；
合好之后同一档秒开（缓存 TTL 7 天）。

#### 一个必须记住的坑：yt-dlp 要解 n 挑战，否则 4K 地址是「半残」的

不带 `--remote-components ejs:github` 时，AV1 画面与 m4a 音轨拿到的地址**头几 MB 能下、随后一律 403**
（yt-dlp 自己也在 1.9% 处 `HTTP Error 403`，ffmpeg 直接 403，curl 302 跟过去还是 403）。
本机有 deno，求解脚本从 yt-dlp-ejs 的 GitHub 拉一份（之后走缓存），同一个 itag 401 地址就能连续下完 540 MB。
所以 `ytsub.commonArgs` 一律带这个参数；老版本 yt-dlp 不认它时，`run`/`Download` 会认出
`unrecognized arguments` 并自动重跑一遍不带它的命令（`rcBad` 置位后永久不再带，免得把整个 YouTube 功能弄挂）。

同时记一笔**风控用量的边界**：连续解析十几次之后，同一批地址会在几分钟内全部 403
（冷却约 6 分钟后恢复；更换解析会换一批 CDN 边，但撞上 IP 级限流时换哪条都一样）。
排查时别把「刚 403、过一会儿又好了」当成代码 bug——判断依据是**同一条地址的 403 是否立刻出现**，
而不是「有没有 403」。因此合流失败时服务端会丢掉解析缓存重试一次（`ytstream.Manager.Forget`），
用户点「重试」也会清掉 5 分钟的失败备忘（`muxcache.Cache.Clear`）。

#### 上游解析不出来时，已经合好的 4K 照样能看

YouTube 的 IP 级风控（见上）会让 `yt-dlp -j` 连着几分钟拿不到任何地址，于是
`GET /api/yt/videos/{id}/stream` 直接 502、播放页里只剩「没有可用的 HLS 清单」——
可盘上那份 `yt-<videoID>-2160p.mp4` 跟上游一点关系都没有。所以解析失败时改成：
把 `muxcache` 里已经就绪的档位折成画质菜单返回（`cachedOnly: true`，`src` 为空，
只有 `progressive` 那几档），播放页照样原生播放，并在画面下方注明「先放已经合好的本地副本；
换档要等它能解析」。实测（限流中）：`currentTime` 50 秒内 0.98 → 50.17、0 次卡顿、
0 条媒体错误，`currentSrc` 就是 `/api/yt/videos/987/file?q=2160`。

### 14.11 4K 不再合流：把 AV1 的 moof 直接当 HLS 分片发（§14.10 那条路作废）

用户 2026-10-29 第三次反馈：「现在这个合流的方案不行，体验很差，我在 YouTube 原站点起播很快，
播放也不卡顿」。§14.10 的 `ffmpeg -c copy` 合流虽然能播、能拖，但**第一次点开要等 4 分钟**
（实测 545 735 981 字节 / 4 分 0.5 秒，第二次 4 分 16.9 秒），这不是「优化缓冲」能救的，
所以整条合流路径（`/api/yt/videos/{id}/file`、`muxcache` 那套）全部删掉，换成下面的做法。

#### 做法：读 sidx，把每段 moof+mdat 当 HLS 分片，用 Range 向 CDN 要

YouTube 的 DASH 画轨（itag 401、`av01.0.13M.08`、2160p60）是**分片 mp4**：

| 轨 | 头 | 第一段起点 | 段数 | 总长 |
|---|---|---|---|---|
| 画面（401） | `ftyp` 28 B + `moov` 673 B + `sidx` 716 B | 1417 | 57 | 540 420 903 B |
| 音轨（140） | `ftyp` 24 B + `moov` 699 B + `sidx` 428 B | 1151 | 33 | 5 199 521 B |

`sidx` 里逐条写着「这一段的 moof+mdat 偏移 / 长度 / 时长」，把它读出来就能写出一份规规矩矩的
HLS 清单：`#EXT-X-MAP` 指头，57 条 `#EXT-X-BYTERANGE:<len>@<offset>` 指段。
`internal/ytdash` 干的就是这件事（`Parse` / `Track.Playlist` / `Master`，纯函数，单测全覆盖）；
上游支持 Range（206），所以整条链路就是「浏览器按偏移要一段 → 服务端按偏移向上游要一段」。

于是起播只要**第一段（8.84 MB）**，而不是 540 MB：

| | §14.10 合流 | §14.11 单文件 HLS |
|---|---|---|
| 首帧（冷，含 12.9 s 解析） | 4 分 0.5 秒 | **16 秒** |
| 首帧（解析已缓存） | 4 分 0.5 秒 | **2~3 秒** |
| 拖动 | 支持（合完全文件后） | 支持（要哪段取哪段） |
| 带宽 | 13.46 Mbps | 13.46 Mbps（同一份字节，没有二次转码） |

#### 取数策略是量出来的：少开、开大、开两三条

googlevideo 的吞吐对**请求体积**极其敏感。同一份 401 地址、同一台机器、同一时段，用 curl 计时：

| 取法 | 吞吐 |
|---|---|
| 1 条 × 20 MB | 0.99 MB/s |
| 2 条 × 20 MB | 3.26 MB/s |
| **3 条 × 20 MB** | **9.31 MB/s**（60 MB / 6.4 秒） |
| 4 条 × 20 MB | 1.19 MB/s（第 4 条开始被限流，全部变慢） |
| 10 次 × 1 MB 串行 | 0.18 MB/s（每次新请求都要重新起步） |
| 6 条并发（游戏早期试的） | 直接不回应：连接建得起来、一个字节都不发 |

所以 `ytDashWorkers = 3`：每条请求切成 3 块并发取，边到边发（第 0 块到了就先写出去，
不用等整段拼完）。这个数字是甜点——再多就翻车，再少就喂不满（4K AV1 要 1.68 MB/s）。

#### 服务端往前预取 4 段，一次取 45 MB

播放器（hls.js）是「缓冲不够了才来要，一次只要一段」，只靠它的话服务端永远只有几秒余量。
所以每发完一段，后台就用**单独那 3 条连接**（`ytDashBulk`，不和播放抢）把后面 4 段合成
一次上游请求拉进内存：既把请求做大（见上表），又让播放器后面几次「零等待」。

实测（真机 harness 浏览器，静音，`YFsMnJEbV1U`，3840×2160）：

| 观测量 | 改前（每段一路、无预取） | 改后 |
|---|---|---|
| 60 秒里重缓冲 | 每 7 秒卡一次，卡在 73.32 秒 40 秒不动 | **0 次** |
| 缓冲余量 | 2~5 秒 | **0.18 → 26.31 秒**（越看越多） |
| `videoWidth×Height` | 3840×2160 | 3840×2160 |
| 丢帧（60 秒） | — | 183（4K60 用 MSE 解码，约占 5%） |

顺带量清楚一件事，避免以后再走弯路：**用原生 `<video>` 直接播 googlevideo 那条 AV1 直链
（不经过我们服务器）35 秒里 0 次卡顿、0 丢帧**——说明链路本身够 1.68 MB/s，卡顿出在
「分段取数的节奏」而不是「带宽不够」。但直链是 IP 绑定的、且 `fetch` 被 CORS 挡死，
所以只能拿它当对照实验，不能当实现（真这么干就得同步两个 media 元素，且换机器就播不了）。

#### 排查开关

`MYPORN_LOG_LEVEL=debug` 打开后，每次解析和每条分片都会留一行：

```
DEBUG msg="yt-dlp 解析完成" videoId=YFsMnJEbV1U elapsed=12.9s err=<nil>
DEBUG msg="4K 分片取回" seg=6 bytes=11687728 elapsed=3.572s
DEBUG msg="4K 分片命中预取" seg=3 bytes=11267195
```

「解析完成」那一行是起播的最大头（12.9 秒里绝大部分是 `yt-dlp -j`，其中含网页 + 两个播放器
API 请求 + n 挑战求解）；「分片取回」超过 10 秒就会开始 `waiting`，`命中预取`越多越顺。

降级策略没变，也不许静默降级：`/dash.m3u8` 建不起来（没有 AV1 直链、不是分片 mp4、sidx 读不出来）
就 302 回 `/stream.m3u8` 走原来的 VP9 HLS（慢但能看）；画质菜单里报的码率永远是**实际在播的那条**。

### 14.12 「插件能直连、音视频分开」——我们本来就是这么做的（2026-11-01 实测）

用户反馈：Quetta（自带播放器的视频浏览器）播 YouTube 很流畅，抓到两条地址「音视频是分开的」，
问能不能照搬。拆开看了一遍，结论是**这套形状我们早就在用**，缺的那一环（浏览器直连 googlevideo）
在当前架构里既做不到也不划算。证据如下，免得下次再从头查。

#### 插件抓到的那两条清单是什么

两条都是 YouTube 自己的 HLS 端点 `manifest.googlevideo.com/api/manifest/hls_playlist/…`：

| 轨道 | 清单 itag | 里面的 `sgovp/sgoap` | 实际编码 | 体积 / 时长 | 分片 |
|---|---|---|---|---|---|
| 画面 | 628 | `itag=315` | VP9 2160p50 | 315 612 961 B / 169.46 s = **14.9 Mbps** | 28 段 × 6.0 s，10.3–11.6 MB |
| 声音 | 234 | `itag=140` | AAC-LC 129.5 kbps | 2 744 371 B / 169.48 s | 28 段 × 6.0 s，324 KB 起 |

两条清单都没有「合成」这回事：画面清单只有 `#EXT-X-MAP`（`govp/slices=0-8027752`，即文件头
8.03 MB）+ 28 条 `govp/slices=0-220,<段区间>`（每段前面都拼了文件头前 220 字节），
声音清单同理（`goap/slices`）。也就是**一条只有画面的流 + 一条只有声音的流，由它自己的播放器
同时放、自己对齐**——不是 MSE 合流，也不是 ffmpeg 合成。

#### 我们这边的清单（同一条视频，`/api/yt/videos/987/dash.m3u8` 实测）

```
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="Default",DEFAULT=YES,AUTOSELECT=YES,URI="/api/yt/dash/80945…/a.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=13591476,CODECS="av01.0.13M.08,mp4a.40.2",RESOLUTION=3840x2160,FRAME-RATE=60.000,AUDIO="aud"
/api/yt/dash/80945…/v.m3u8          ← 57 段，8.8–11.8 MB/段，AV1 2160p60 13.59 Mbps
/a.m3u8                             ← 33 段，AAC 140，首段 161 788 B
```

形状完全一致（音轨走 `#EXT-X-MEDIA`、画面走 `#EXT-X-BYTERANGE` 分片）。差别只有两处：
**档位**（我们发 AV1 13.59 Mbps，插件那条是 VP9 14.9 Mbps，同一份画面的码率高一倍）和
**字节路径**（我们经本机代理，它浏览器直连 CDN）。

#### 为什么「浏览器直连」在网页里做不到：CORS 只回给 youtube.com

同一份 googlevideo 分片地址，只改 `Origin`：

| 请求头 | 响应里的 `Access-Control-Allow-Origin` |
|---|---|
| `Origin: https://www.youtube.com` | `https://www.youtube.com`（还有 `Allow-Credentials: true`） |
| `Origin: http://127.0.0.1:18081` | **没有这个头**（只有 `Vary: Origin`） |
| `Origin: null` | 没有 |

hls.js 是 `fetch`/XHR 取分片的，没有 ACAO 就一定被拦死。能跨域播的只有**原生 `<video src>`**
（媒体元素不走 CORS）——Quetta 之所以行，是因为分片是它自己的播放器取的，不在网页的 CORS 里。
顺带量了直连本身的吞吐（curl、同一时段）：单连接 2.2–8.6 MB/s 且偶发半路被掐（`transfer closed
with N bytes remaining`），比我们代理「3 路切块 + 往前预取 4 段」更不稳，所以直连也不划算。

#### 原生双元素（照插件的样子）与现有播放路径对照，同一条视频同一台机器

用 `<video muted src=itag401(AV1 2160p)直链>` + `<audio muted src=itag140>` 手工同步（真机浏览器）：

| 观测量 | 原生双元素 + 直连（插件式） | 现在的 hls.js + MSE + 本机代理 |
|---|---|---|
| 采样时长 | 78 s | 62 s |
| 卡顿（`waiting`） | 起播 1 次，之后 0 | **0** |
| 丢帧 | 1 / 3 919 | 1 / ~3 700 |
| 缓冲余量 | 10–17 s | **70–85 s**（预取一直往前拉） |
| `readyState` / 分辨率 | 恒 4 / 3840×2160 | 恒 4 / 3840×2160 |
| 出画面 | 3.73 s（页面加载起算） | 冷启动整页 5.86 s 到首帧（解析已缓存） |

结论：分轨不是缺的那块，我们早就分了；直连在网页里被 CORS 挡住、且吞吐不如代理。
**播放链路不动**。真要再快，方向是「解析那 4–13 秒」和「起播前两段」，不是改成直连。

### 14.13 插件为什么「解析」几乎不花时间，以及我们能砍掉的那 1.5 秒

用户追问：同一台机器，Quetta 点开就放，我们要等。差在哪，量了一遍。

#### 它根本没有「解析」这一步

Quetta 是自带播放器的浏览器：YouTube 页面自己的播放器已经把 `streamingData` 拿在手里了
（实测这个页面在 harness 浏览器里：导航后 **2.99 s** 发出第一条 googlevideo 分片请求、
**4.62 s** 收完第一段，`ytInitialPlayerResponse.streamingData` 早就有了），它只是接着把
清单和分片放出来。我们这边没有页面、没有播放器、没有 PO token，只能由 yt-dlp 从头问一遍。

#### 我们要付的账：一次解析 = 5 个来回 + 一次 JS 求解（实测 4.5 s）

`yt-dlp -v -j` 逐阶段打点（DgiefGG22EI，2026-11-01）：

| 阶段 | 耗时 |
|---|---|
| 下载网页（拿播放器 JS 的版本号） | 1.00 s |
| tv samsung 播放器 API | 0.71 s |
| visionos 播放器 API | 0.54 s |
| 下载播放器 JS（11a45ffb-main） | 0.93 s |
| deno 解 n 挑战 | 0.52 s |
| 下载 m3u8 信息 | 0.78 s |
| 合计 | **4.5 s** |

「便宜客户端」也试过了，走不通：直接 POST `youtubei/v1/player` 用 **ANDROID / IOS** 客户端
只要 **0.6~0.7 s**，`playabilityStatus=OK`，还给 AV1 2160p 直链（itag 401）且地址里没有 `n=`。
但那批地址是**半残**的——同一条 URL：`0-8 MB` 回 206、4.75 MB/s，`200 MB` 和 `280 MB`
立刻 403（重试三次都是 403）。原因就是挑战没解、也没有 PO Token（`yt-dlp -v` 里那句
`PO Token Providers: none`）。所以「播放器 JS 0.9 s + deno 0.5 s」这 1.4 s 省不掉，
省了就只能放开头几 MB。

#### 能省的：别再问两家客户端

yt-dlp 默认并发问 tv samsung + visionos 再把结果并起来。实测 **visionos 一家就把这里要的
全给了**（HLS 的 `manifest_url`、AV1 2160p 的 401 直链、AAC 140 音轨）：

| 客户端 | `yt-dlp -j` 耗时 | 有 HLS 清单 | 有 AV1(401) |
|---|---|---|---|
| 默认（tv samsung + visionos） | 4.5 s | 是 | 是 |
| tv_samsung 单独 | 3.58 s | **否**（0 条 m3u8） | 是 |
| tv_samsung,visionos | 3.97 s | 是 | 是 |
| **visionos 单独** | **2.42 / 2.48 / 3.18 s** | 是（17 条 m3u8） | 是 |

所以 `ytsub.ResolveStream` 改成两级：先 `--extractor-args youtube:player_client=visionos`，
**拿不到清单才退回默认客户端集合**再问一次（会员专属 / 私有 / 删除 / 地区限制这些
「本来就看不了」的判决直接放弃，不白等第二次——`worthDefaultClients`）。

应用内实测（`GET /api/yt/videos/{id}/stream`，冷解析，同一时段）：

| 视频 | 解析耗时（改前 → 改后） |
|---|---|
| YFsMnJEbV1U | 4.442 s → **2.561 s** |
| _naNThAiK4o | — → 2.862 s（整请求 2.99 s） |
| vHcb8AS4A2g | — → 2.604 s |
| dpta7lehP0M | — → 3.557 s |
| WFNq-9Ks02U（会员专属） | — → **1.76 s 直接判决**，没有第二次解析 |

改后回归：4K 的 AV1 那条（3840×2160、`readyState=4`、正常推进）与 1080p 的 HLS 那条
（1920×1080，master 带音轨 rendition）都实测能放；`go test ./internal/ytsub/ ./internal/ytstream/
./internal/httpapi/` 全绿。

剩下的两三秒是结构性的：网页 + 播放器 API + 播放器 JS + deno。真要再快，只能是「提前问」
（视频墙预热、悬停预热已经这么做了）或者「拿账号/PO token 换掉 JS 求解」，不是继续调参。

### 14.14 「卡住，然后进度条归 0」：自愈把管线重装从 0 开始拉了（2026-11-01 修）

用户报的现象：站内播放放到一半卡住，进度条掉回 0，再等也不动（有时候过一会儿从头开始放）。
复现靠 `SdVI6xIwuqk`（VP9 2100p + 1400p 音轨，290 s）：无头 Chromium 每次都能在 15–35 s 之间甩出一个
`PIPELINE_ERROR_DECODE` / `bufferAppendError`，正好是「自愈」这条路的入口。

原来自愈只有一招：清 `source`、等一会儿、重新装。量下来问题在三个地方，逐个说。

#### 一、重装后的管线从第 0 秒开始拉

`attach()` 重装片源时没告诉 hls.js 起点，`HlsJsOnlyAdapterCore` 建的 `Hls` 又是 `autoStartLoad: false`，
`startLoad()` 不带位置就按 0 起。日志实测那一轮：buffer 变成 `[[0,6],[30,36]]` 两段互不相连，
我们把 `currentTime` 设回 33.3，缓冲里没有 33.3 的东西，`readyState` 卡在 0、`paused` 一直是 true —— 55 秒没动静。

改：`applySource(el, src, startAt)` 一路把位置传到 `hlsEngineConfig()`，`startAt > 1` 时给 `hlsJs` 加
`startPosition`（和 `maxBufferLength:60 / maxBufferSize:250e6 / backBufferLength:30` 同一份配置）。
配置一变 hls.js 会重建实例（`sameEngine:false`，实测），也就是说起点是跟着实例一起生效的。

#### 二、续播点在管线装好之前就被消费掉了

`pendingSeek` 由 store 订阅里那段「片源一就绪就 seek」消费。自愈的等待窗口里 `canPlay`/`duration` 还是**上一条管线**
留下的值，于是这段代码抢先 seek 了一次并把 `pendingSeek` 清零，等新片源真装上时已经没有位置可去。日志对照：

| | 日志 |
|---|---|
| 改前 | `[dbg] recover at= 12.187499` → `[dbg] apply resumeFrom= 0` |
| 改后 | `[dbg] recover at= 12.187499` → `[dbg] apply resumeFrom= 12.187499` |

闸门是 `appliedSrc !== '' && Date.now() >= holdUntil`（自愈时 `appliedSrc` 清空、`holdUntil` 推到等待结束）。

#### 三、看门狗第一版拿 `engine.currentLevel` 当判据，反倒在掐好管线

「卡住但没报错」这一类（hls.js 的 `StreamController` 停在 STOPPED，`currentLevel = -1`、`readyState = 0`、
`paused = false`、时间不动）需要一个定时兜底。第一版判据是 `currentLevel === -1 && levels.length > 0`，结果实测
**自动选档时 `currentLevel` 一直是 -1**：一边正常推进 7.2 → 8.2 → 9.2 → 10.2 → 11.2 s、`readyState=4`，
一边 `noLevel= true` 一直亮着，于是每 4 秒冷却一到就 `startLoad()` 一次，把在飞的管线掐了自己重来 —— 进度条就这么跳的。
现在判据只看元素：`(!paused || wantPlay) && readyState < 2`（在播却没数据）或「`readyState >= 3` 但 6 秒没推进」，
连中 3 拍（1 秒一拍）、距上次推它 ≥4 秒才动手。

还有一条豁免是错的：`video.seeking` 时跳过看门狗。拖到没缓冲的地方卡住时 `seeking` 会一直是 true —— 实测
`pauseBuffering()` + `currentTime = 200`，`t=200 / readyState=1 / paused=false` 整整 16 秒纹丝不动，看门狗一声不响。
去掉这条豁免后同一场景 5 秒被推起来：`t+0..t+4` 停在 200.0，`t+5` 变 200.4、`readyState` 回 4，之后 201→208 一路正常。

#### 三之二、看门狗把「正在下一大块」当成了「卡死」：4K 首帧 35~45 秒的元凶（2026-11-01 修）

用户报「`5A3yPgCSVrg` 起播很慢、拖进度条也很慢」。量下来是**两件事叠在一起**：

1. 网络：这条片子是 4K60 AV1（itag 401）**26.99 Mbps**（615 630 690 B / 183 s），HLS 一段 5.038 s = **16 925 922 B**，
   要实时播得 3.37 MB/s；当天出境链路（Clash TUN，`utun4`）实测 1.3~2.3 MB/s/连接、并发也不加速。
   对照：同一台机器拉国内镜像 25.2 MB/s，GitHub 也掉到 1.4~1.9 MB/s —— 是这条路的天花板，不是 YouTube 针对本机。
2. 放大器（本仓库的 bug）：服务端 `streamYtRange` 把一段 3 等分、**第 0 块到齐才写第一个字节**，
   16.9 MB 一段的首块是 5.64 MB ⇒ 首字节 2.8~4.3 秒；而看门狗把「`readyState=1` 撑过 3 秒」判成卡死、
   每 4 秒 `startLoad()` 一次 —— `startLoad()` 会**掐掉在飞的分片请求**，掐断时服务端连第一个字节都还没发，
   于是无限重来。

实测证据（同一浏览器，页面里把 `engine.startLoad` 包了一层计时）：

| 观测 | 改前 |
|---|---|
| `startLoad(-1)` 调用时刻 | 3180 / 7180 / 12180 / 16180 / 20179 / 24179 / 28179 / 32179 / 36180 / 40180 ms（每 **4.0 s** 一次） |
| 同一个 `v.mp4?i=0` | 被取消 **9~10 次**，每次 `dur≈4000 ms`、`encodedBodySize = 0`，与 nudge 时刻一一对应 |
| 首帧 | 34.8 / 38.5 / 41.7 秒；有一次 45 秒仍 `readyState = 0` |
| 唯一起播成功的那次 | 预取碰巧把整段攒进内存、某次请求 **17 ms** 返回全量 16 925 922 B |

改法两条：服务端首块压到 1 MiB（`ytStairSpans`，其余两路均分 ⇒ 首字节变成「等 1 MiB」）；看门狗改成
「有分片在飞、且最近 30 秒内有动静就不 nudge」（hls.js 事件 `hlsFragLoading` / `hlsFragLoadProgress` / `hlsFragLoaded`，
失败走 `hlsError` 里 `details` 以 `fragLoad` 开头的那几条），nudge 冷却 4 s → 10 s。

| 实测（同一台机器、同一条链路） | 改前 | 改后 |
|---|---|---|
| 首帧（解析已缓存） | 34.8 / 38.5 / 41.7 s | **9.7 s** |
| 首帧（冷解析，yt-dlp 8.8 s） | —— | 13.4 s |
| nudge 次数 | 9~10 | **2**（起播前清单阶段那两次，属于预期的「引擎没在拉」救援） |
| 拖到 120 s | 7.3 / 11.4 s | **5.0 s** |
| 16.9 MB 范围的首字节 | ≈2.8~4.3 s（首块 5.64 MB） | 1.77 / 2.84 s（首块 1 048 576 B，`curl -w %{time_starttransfer}`） |
| 服务端「分片块取失败」 | 一次起播几十条 `context canceled` | 只剩换页/拖动本身该有的取消 |

剩下的是硬限制：这条链路仍然喂不满 26.99 Mbps，所以 4K 每段 16.9 MB 要 6~8 秒才到手（比它自己 5.038 s 的时长还长），
连播会周期性等缓冲。要顺滑只有两条路：换一条能跑 30 Mbps+ 的线路，或者在画质菜单里降到 720p/480p
（这部片 1080p 是 VP9 13.0 Mbps，今天这条路上仍然吃紧）。

#### 四、第一个错误先原地自愈，拆管线是兜底

hls.js 自带 `recoverMediaError()`（`detachMedia` → `attachMedia` → `startLoad(currentTime)`），元素不换、
`currentTime` 不丢——实测调用后 `video.error` 变 null、`currentTime` 5.94 → 5.03、buffer 重新长起来，
当时「没救回来」的真实原因只是没人再 `play()` 一次（元素被之前的失败留在 paused）。所以现在的顺序是：
第一个错误 → `recoverInPlace()` + `wantPlay` + 盯住原位；第二个错误起才拆片源重装（延迟 2500 / 8000 ms）。

#### 五、重装那几秒不许把 0 显示出来

拆了重装，元素的 `currentTime` 必然是 0，控制条照着显示就是「进度条归 0」。重装/原地自愈期间 `video-skin` 挂
`data-resuming="true"`，`style[data-myporn-skin]` 里加一条
`:host([data-resuming='true']) media-controls-content{visibility:hidden}` 把控制条收起来（加载态的糊底图 + 转环本来就在，
键盘快捷键挂在 player 上，不受影响）。同时 `onTime` 在目标位置没到之前不往外报，页面的 `lastPos` 也就不会被 0 覆盖
——否则下一次重试会从 0 起。

#### 实测回归（2026-11-01，`x87vdLgzq0I` / 1080p H.264）

| 场景 | 结果 |
|---|---|
| 起播 → 16 s 正常推进 | 0.9 → 14.0 s 连续，`readyState=4`，无 `resuming` 闪烁 |
| 清晰度 1080p → 720p（30.3 s 处） | `t=30.2 / rs=0 / resuming=true` → 1 秒后 `rs=4 / resuming=false`，30.4 → 36.4 连续，**位置没丢、没有归 0** |
| 人为冻住拉流（`pauseBuffering()` + 拖到 200 s） | 冻 5 秒后被看门狗推起来：200.0 → 200.4，之后 201 → 208 正常 |
| 拆管线重装（旧构建上的解码错误） | `recover at=12.187 → apply resumeFrom=12.187`、`recover at=18.477 → apply resumeFrom=18.477`，都在原位接上 |

顺带记两个排查坑：**换构建后必须先 unregister SW + 清 caches**，否则页面一直在跑旧 chunk
（这次测出的第一版看门狗「没生效」就是旧 chunk 干的：dist 里是 `VideoPlayer-BF1lATrS.js`，页面跑的是 `BJgWMylf`）；
`playwright-cli` 的 `run-code` 在**上一个客户端进程卡死**时会一起卡住（`eval` 还能用、`run-code` 永远不返回），
先 `ps aux | grep "playwright-cli -s"` 清掉再跑。

### 14.15 弹窗右上角的关闭键不跟控制条一起收（2026-11-01 修；2026-11-02 弹窗已不存在）

> 下面这段记录的是弹窗时代那颗关闭键。播放改成独立页之后关闭键没有了（退回＝浏览器后退），
> `.yt-close` / `.yt-media` 的选择器、以及给它用的 `watchControls()` / `watchFullscreen()` / `hideIdleCursor`
> 反射一起删掉了（`Youtube.svelte` 里不再有 `<style>` 块，`VideoPlayer.svelte` 里也不再写
> `video-skin[data-controls-visible]`）。原文保留只当结论与排查记录，**不要再照着它去读那两个属性**。

用户报：「鼠标悬停在播放器上时，过了一会，鼠标和菜单栏都自动隐藏了，但是右上角的关闭按钮没有自动隐藏」。

原因是这一颗键压根不知道控制条什么时候藏。它按 `.yt-media:hover` 显隐——而 hover 只看指针在不在画面里，
指针停着不动照样是 hover；控制条那边是**皮肤自己的空闲计时**（实测播放中静止约 6 秒开始淡出，1 秒内
`opacity` 到 0）。两套判据，自然对不上。实测同一时刻：`video-controls-content[data-visible]` 已经是 `false`、
`opacity: 0`，`.yt-close` 还是 `opacity: 1`。

皮肤其实已经在 shadow root 里记着这件事：`.media-container` 上的 `data-controls-visible`（它自己拿这个属性
隐藏光标：`@media (pointer:fine){.video-skin:not([data-controls-visible]){cursor:none}}`）。问题只是光 DOM 读不到
shadow 里的属性。所以 `VideoPlayer.svelte` 加了 `watchControls()`：一个只 filter 这一个属性的 `MutationObserver`，
把可见态**反射**到宿主元素上（`video-skin[data-controls-visible]`），页面这边一条 CSS 就能跟它同步。

两个容易踩的点：

- **那条选择器必须写 `:global(.yt-media:has(video-skin[data-controls-visible])) .yt-close`**。`video-skin` 是在
  `VideoPlayer` 组件里的，`Youtube.svelte` 的模板里看不见它，svelte 会把这条当成「没用到的选择器」**直接从产物里删掉**——
  构建后 `web/dist/assets/style-*.css` 里那条确实消失了（`grep yt-media` 只剩 `:not(:has(video-skin)):hover` 那条）。
  这不是警告级别的问题，是功能静默失效。
- **没有播放器时不能跟着控制条走**：解析中、解析失败、兜底 iframe 这三种状态都没有 `video-skin`，控制条不存在，
  关闭键必须退回 hover（`.yt-media:not(:has(video-skin)):hover .yt-close`），否则那几秒里弹窗关不掉
  （点背景也能关，但不能指望用户知道）。

实测（1440×900，`x87vdLgzq0I`，播放中，指针停在画面正中不动）：

| 时刻 | `video-skin[data-controls-visible]` | 控制条 `data-visible` | 关闭键 `opacity` |
|---|---|---|---|
| +1 s | 有 | true | 1 |
| +6 s | 无 | false | 0.02 → 0 |
| 鼠标再动一下 | 有 | true | 1 |

暂停态单独量过：暂停 8 秒后 `data-visible=true`、关闭键 `opacity=1`（皮肤暂停时不收控制条），此时点关闭键弹窗正常关闭。
