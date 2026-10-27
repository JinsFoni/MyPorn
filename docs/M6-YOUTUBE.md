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
| 图片 | 封面走 `/api/img`（缓存 + 隐私模式糊图）；统一用 `mqdefault.jpg`（320×180 无黑边）铺卡片，`maxresdefault` 做大图 | §11-6 |
| 错误语义 | 复用 `internal/social` 的 `rate_limited / blocked / stale_parser / not_found / upstream` | 三套脸的 UI 在 M5 已经做好了 |

### 2.2 已拍板（Q1–Q8）

| 编号 | 问题 | 结论 |
|---|---|---|
| Q1 / Q1b | 视频要不要落进本地库；「不要合轨」指什么 | **要下载**；AV1 优先，**接受 `-c copy` 封装**（不转码），产物 AV1+AAC 单文件 mp4 |
| Q2 | 历史深度 | **yt-dlp 一次列全**（配合 Q8 的 200 条上限） |
| Q3 | 移动端底栏 | **底栏不动**（保持 6 格）；动态页顶加「动态 / YouTube」切换 |
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
| 动态页（`/moments`）顶部 | 一条 `动态 / YouTube` 切换（Q3 A：手机用户从底栏只能进「动态」，靠这条切换过去） |
| 管理页 → 新 tab「YouTube」 | 贴链接添加、启用开关、单独同步、深度回填、删除、看视频数/未看数/上次同步/错误；顶部四格总览（频道数 / 视频数 / 24h 新增 / 出错频道） |
| 设置 → 「网络与代理」 | 多一行 **YouTube 代理**（与 Instagram / X 同款，保存即生效） |
| 设置 → 新分类「YouTube」 | 抓取总开关、增量间隔（默认 6h）、深度回填上限（默认 200）、最高分辨率（默认 1080）、格式串（高级）、yt-dlp 路径 + **版本健康度**、Cookie（可选） |
| 卡片点开 | 嵌播弹层（`youtube-nocookie.com/embed/<id>`）+ 标题 / 频道 / 时间 / 播放量 +「在 YouTube 打开」+「下载进本地库」 |
| 移动端底栏 | **不动**（关注 / 动态 / 艺人 / 下载 / 管理 / 设置，`grid-cols-6`） |

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
2. `published_at` 留空，`date_source=''`，卡片上时间位置显示「较早」；
3. RSS 覆盖到的那 15 条会被补上准确时间（`date_source='rss'`）；
4. 用户点开某条历史 / 收藏某条时，**按需单条补一次**（`yt-dlp -J <watch url>`，4.1s，落进后台任务，不阻塞 UI）。

收益是「一次回填 200 条只要 6 秒」，代价是「历史条目没日期」。这个取舍写在墙上就是：默认视图看新的，往回翻靠频道筛选 + 无限滚。

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
8. `writeArtwork` + `writeNFOFile`：封面优先取 `maxresdefault`（Emby 的 fanart 是 1920×1080，用卡片那张 320×180 会糊），
   取不到（老的/低清视频没有 maxres）自动退回库里存的那张
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

调度沿用现有 scheduler；`enabled=0` 跳过。SSE 新增 `yt.new`（`{channelId, title, count}`），侧边栏未看角标复用 `live`。

## 8. 前端

| 文件 | 内容 |
|---|---|
| `web/src/routes/Youtube.svelte` | 频道胶囊 + 16:9 网格 + `InfiniteList` + 嵌播弹层 |
| `web/src/components/YtCard.svelte` | 封面（`mqdefault` 铺满 / `maxresdefault` 大图）、时长角标、两行标题、副行「频道 · 3 小时前 · 12.3 万次观看」、未看小圆点、未看状态下右上角下载按钮、直播标记 |
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
GET    /api/yt/health                         yt-dlp 路径 / 版本 / 是否过期 / ffmpeg 可用
PUT    /api/settings                          + platformProxy.youtube / yt_* 五项
```

错误码沿用 `docs/API.md` 那张表：`needs_auth` / `rate_limited`（带 `resetAt`）/ `blocked`（不带）/ `stale_parser` / `not_found`。

## 10. 验收标准

1. 贴 `@handle`、`/channel/UC…`、`watch?v=…` 三种链接都能加成频道，10 秒内出频道信息与最近 15 条
2. 深度回填跑完（200 条 / 一个频道）**≤ 15 秒**，墙上出现带**时长**角标的卡片
3. 6h 后自动抓到新视频，侧边栏未看角标 +1，打开页面即清
4. 点卡片能站内播；断网时给「在 YouTube 打开」，不是白屏
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

### 11.6 封面

```
https://i.ytimg.com/vi/<id>/hqdefault.jpg     → 200 image/jpeg  25KB (480×360，4:3 带黑边)
https://i.ytimg.com/vi/<id>/maxresdefault.jpg → 200 image/jpeg 271KB
```

建议卡片用 `mqdefault.jpg`（320×180，16:9 无黑边），大图/海报用 `maxresdefault.jpg`。普通 JPEG，`/api/img` 链路（含隐私模式）零改造。

## 12. 风险与取舍

| 风险 | 说明 | 应对 |
|---|---|---|
| yt-dlp 版本 | 两周不升级就可能全线 403 | 界面显示版本 + 过期警告；`stale_parser` 文案直接指向升级 |
| 深挖条目没日期 | flat 模式不给时间，逐条补太慢 | 接受；标题 + 时长 + 播放量够辨认，按需单条补 |
| 首次回填 200 条上限 | 老频道历史远多于此 | 可配；管理页提供「再拉一批」（改 `--playlist-start` 续接） |
| AV1 只有视频轨 | 必须封装一次 | 已是既定方案；`-c copy` 不损画质，产物单 mp4 兼容 Chrome / Firefox / Safari 17+ / Emby |
| 高分辨率体积 | 8K AV1 90 秒就有 358 MB（实测），整频道批量下载很容易把盘吃满 | 不设高度上限（用户要「最高画质」）；`CheckSpace` 兜底 + 只做手动/整频道批量，不做全自动 |
| 嵌播依赖出站 | 断网就是黑框 | 卡片始终保留「在 YouTube 打开」 |
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
| 隐私模式 | 弹窗里不嵌 iframe，只放封面 + 提示 | 一嵌就等于让浏览器直连 YouTube 拉整段视频 |
| 详情页播放 | **新增本地播放**：`GET /api/videos/{id}/file`（ServeFile，支持 Range），`/playback` 把本地档排第一并作默认档 | 下完点开详情页原来是 `unsupported_platform`——没有 adapter 就没有播放地址，这是 §6.3 预警过的那条路，实测确实踩到了 |
| 详情页下载 | `POST /api/videos/{id}/download` 在没有 adapter 时用 `platform_video_id` 反查 `yt_videos`，落到 `yt_download` | 否则详情页那个「下载到本地库」按钮点了就报错 |
| 取消 / 暂停 | 下载期间每秒查一次取消标志；`Download` 先 `cmd.Wait()` 再收管道（`WaitDelay=3s`） | 原来点了取消要等整段下完：yt-dlp 调起来的 ffmpeg 也攥着管道，只等 EOF 会一直挂着（实测 60 秒） |
| 已看语义 | 点开一条视频才标已看（另有「全部标为已看」）；**不会因为打开页面就全清** | 照片墙那种「打开即清」对视频墙不成立：200 条历史没人会一口气看完 |
