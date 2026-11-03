# M5 — 社交动态（X / Instagram 照片墙）

> **状态（2026-10-28）**：需求已确认；**spike 已跑完**，原始数据见 §15。结论：X 无登录可用但只能拿最近 20 帖，
> Instagram 无登录 100% 不可用（必须 Cookie）。§2 与 §5 已按实测结果改写。
> 上位需求见 `REQUIREMENTS.md`，接口契约落地时并入 `docs/API.md`，解析自查清单并入 `docs/NEEDS-VERIFICATION.md`。

一句话：**订阅 X / Instagram 博主 → 定时抓帖子里的静态照片 → 汇总成一面照片墙；看中的一键存进本地库。**

---

## 1. 本期范围

| 做 | 不做 |
|---|---|
| 博主增删改查（粘贴主页链接）、启用/停用、单独同步 | 视频 / 动图 / Reels / Story（灯箱里给「原帖」外链兜底） |
| 定时抓取（默认 6h）、首次回填最近 100 帖、游标断点续跑 | 评论、点赞数以外的互动数据、私信、推荐算法 |
| 照片墙（混流 + 博主筛选）、灯箱看大图 | 动态页的播放能力（动态不接播放器） |
| 一键存进本地库（按博主/日期归档） | Emby 剧集式整合（照片不做 nfo，见 §9） |
| 与「艺人」打通（艺人页顺带显示社交照片） | 推送通知（只在页面内用 SSE 提示，复用现有事件中心） |
| 隐私模式覆盖（封面同款小糊图） | 按名字猜账号（只认 URL，见 §8） |

## 2. 已确认的决策

| 维度 | 决策 | 理由 |
|---|---|---|
| 抓取方式 | **X：无登录公开端点（默认）→ Cookie 升级；Instagram：只能 Cookie** | 实测（§15）：X 的 syndication 时间线免登录可用；IG 三个接口一律 `401 require_login`、主页直接 302 去登录页，免登录不存在可行路径。不引入 gallery-dl 这类外部依赖（守住单二进制） |
| 页面形态 | **纯照片墙** | 密度高、滚动爽；正文和原帖在灯箱里看 |
| 回填深度 | **每个博主最多 100 帖**，之后 **6 小时**增量（沿用 `syncIntervalHours` 默认，可单独覆盖） | 但 X 无登录**只给最近 20 帖且没有翻页游标**（§15-1），所以：X 未配 Cookie → 实际回填 20 帖，靠 6h 一轮慢慢累积；X 配了 Cookie 或 IG → 真的能翻到 100 帖 |
| 照片落地 | **支持一键存进本地库** | 复用 M2 的库结构，重要照片能进归档 |
| 与艺人关系 | **打通** | 艺人页已有 `social_json` 的社交链接，可精确匹配，不需要猜名字 |
| 隐私模式 | 覆盖动态页 | 和封面同一套 `/api/img?s=` 机制，保持一致 |

---

## 3. 信息架构

| 位置 | 内容 |
|---|---|
| 侧边栏「动态」（`/moments`，`icon: 'moments'`，位于「关注」之后） | 所有博主照片混流，发布时间倒序；页面顶部**不画标题**（2026-11-02 用户要求去掉左上角那个「动态」），直接是**博主头像条**（和关注页 / B 站页 / YouTube 页同一个 `AvatarRail`：56px 圆头像、点谁筛谁、再点一次取消，选中的写进地址栏 `?account=`，可深链）；下面一行工具栏只剩「全部」+ 平台筛选 +「添加博主」 |
| 管理页 → 新 tab「动态管理」 | 添加博主、启用开关、单独同步、删除、关联艺人（手动改）、看帖子数/照片数/上次同步/错误 |
| 设置弹窗 → 新分类「动态」 | 抓取总开关、同步间隔、回填上限、X / Instagram Cookie、Instagram 代理、图片缓存上限 |
| 艺人 page（`/performer/:id`） | 已关联的话多一块「社交动态」：最近 12 张缩略图，点开进灯箱 |
| 灯箱（`/moments?p=<photoId>` 可深链） | 大图 + 博主 + 时间 + 正文（超 3 行折叠）+「原帖」+「存进本地库」 |

**移动端底栏**：现有 6 项加「动态」变成 7 项，375px 宽度会挤到截断。方案：底栏固定 5 项
（关注 / 动态 / 艺人 / 下载 / 管理），设置走顶栏图标（已有），「发现」并入「艺人」页的搜索入口。
若不同意这个取舍，就得改成分组导航，属于额外工作量。

---

## 4. 数据模型

新迁移 `0007_m5_social.sql`：

```sql
-- 博主（社交账号）
CREATE TABLE IF NOT EXISTS social_accounts (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    platform      TEXT NOT NULL CHECK (platform IN ('x','instagram')),
    handle        TEXT NOT NULL,                      -- 不带 @，小写，规范化后
    display_name  TEXT NOT NULL DEFAULT '',
    avatar_url    TEXT NOT NULL DEFAULT '',
    profile_url   TEXT NOT NULL DEFAULT '',
    performer_id  INTEGER REFERENCES performers(id) ON DELETE SET NULL,  -- 打通用，可空
    enabled       INTEGER NOT NULL DEFAULT 1,
    backfill_done INTEGER NOT NULL DEFAULT 0,         -- 首次回填是否跑完
    cursor        TEXT NOT NULL DEFAULT '',           -- 回填断点（上游分页游标）
    post_count    INTEGER NOT NULL DEFAULT 0,         -- 已收录帖子数（缓存值）
    photo_count   INTEGER NOT NULL DEFAULT 0,
    last_synced_at TEXT,
    last_error    TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (platform, handle)
);

-- 帖子（只作为照片的容器与正文来源）
CREATE TABLE IF NOT EXISTS social_posts (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id   INTEGER NOT NULL REFERENCES social_accounts(id) ON DELETE CASCADE,
    platform     TEXT NOT NULL,
    post_id      TEXT NOT NULL,                       -- 平台侧 id（IG shortcode / X status id）
    url          TEXT NOT NULL DEFAULT '',
    caption      TEXT NOT NULL DEFAULT '',
    posted_at    TEXT NOT NULL DEFAULT '',            -- RFC3339，用于排序
    photo_count  INTEGER NOT NULL DEFAULT 0,
    first_seen_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (platform, post_id)
);

-- 单张照片 = 墙上一格
CREATE TABLE IF NOT EXISTS social_photos (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    post_id      INTEGER NOT NULL REFERENCES social_posts(id) ON DELETE CASCADE,
    account_id   INTEGER NOT NULL REFERENCES social_accounts(id) ON DELETE CASCADE,
    platform     TEXT NOT NULL,
    seq          INTEGER NOT NULL DEFAULT 0,          -- 帖内序号，灯箱里先走完同帖
    url          TEXT NOT NULL,                       -- 原图地址（不带签名的最好，带签名的靠每次重解析）
    width        INTEGER NOT NULL DEFAULT 0,
    height       INTEGER NOT NULL DEFAULT 0,
    posted_at    TEXT NOT NULL DEFAULT '',            -- 冗余：墙的 keyset 分页只查这一列
    hidden       INTEGER NOT NULL DEFAULT 0,          -- 原帖被删时软隐藏
    first_seen_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (platform, url)
);

-- 照片的落库记录
CREATE TABLE IF NOT EXISTS social_photo_files (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    photo_id   INTEGER NOT NULL REFERENCES social_photos(id) ON DELETE CASCADE,
    library_id INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
    rel_path   TEXT NOT NULL,
    size_bytes INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (photo_id, library_id)
);

CREATE INDEX IF NOT EXISTS idx_photos_wall    ON social_photos(posted_at DESC, id DESC) WHERE hidden = 0;
CREATE INDEX IF NOT EXISTS idx_photos_account ON social_photos(account_id, posted_at DESC);
CREATE INDEX IF NOT EXISTS idx_posts_account  ON social_posts(account_id, posted_at DESC);
```

量级估算：100 帖 × 平均 3 张 × 30 个博主 ≈ 9000 行照片、约 1.5 MB 索引与文本（不含图片本身）。
照片墙的 keyset 分页照抄 `internal/store/videos.go` 的 `encodeCursor/decodeCursor`。

**不做软删除的账号**：删博主 = 级联删帖与照片记录（已存进库的文件不动，那是用户的资产）。

---

## 5. 抓取

各站自己的适配器在 `internal/platform/xcom/`（已有头像来源，扩成完整包）与 `internal/platform/igram/`，
上层只认 `social.Adapter` 接口：

```go
type Adapter interface {
    Key() string                    // "x" / "instagram"
    Name() string                   // "X" / "Instagram"
    ParseProfileURL(raw string) (handle string, err error)
    GetProfile(ctx, handle) (*Account, error)              // 显示名 / 头像 / 主页
    ListPosts(ctx, handle, cursor string) (*PostPage, error) // 一页帖子 + nextCursor
    NeedsAuth() bool                 // 无登录端点刚失败时，UI 用它提示「需登录态」
}
```

### 5.1 X（已实测，见 §15-1）

- 唯一可用的免登录端点：`GET https://syndication.twitter.com/srv/timeline-profile/screen-name/{handle}`
  → HTML 里一段 `__NEXT_DATA__` JSON：
  - `props.pageProps.timeline.entries[]`（**固定 20 条**，全是 `type: "tweet"`，**没有 cursor 条目**）
  - 每条 `content.tweet`：`id_str` / `created_at`（`Fri Oct 23 23:37:56 +0000 2026`）/
    `full_text` / `permalink` / `user` / `extended_entities.media[]`
  - 每个 media：`type`（`photo` / `video` / `animated_gif`）、`media_url_https`（`pbs.twimg.com/media/…jpg`）、
    `original_info{width,height}`、`sizes`、`media_key`、`expanded_url`
  - 转推：存在 `retweeted_status`（要决定收不收，见下）
- **不能翻页**：加 `?cursor=` / `?max_id=` / `?page=` 等未知参数会直接返回非 JSON 的错误壳；
  `?showReplies=true` 会变成 0 条。构建产物里也挖不到任何翻页参数 → **免登录路径 20 帖封顶**。
- 错误语义（已实测）：账号不存在 / 无内容 → **HTTP 200 且 `hasResults: false`、`entries: 0`、`headerProps: null`**
  （不是 404），实现里必须靠 `hasResults` 判空，不能靠状态码。
- 已死的路子（别再试）：`cdn.syndication.twimg.com/timeline/profile`（200 但 0 字节）、
  `api.x.com/1.1/statuses/user_timeline.json`（guest token 下 404）、`x.com/{handle}/media`（307 去登录）。
- 另外两个可用的免登录端点（不入库、只做富化/校对）：
  `cdn.syndication.twimg.com/tweet-result?id={id}&token=x`（单条 tweet JSON，含 `mediaDetails`）；
  `POST api.x.com/1.1/guest/activate.json` 能拿到 guest_token（但没 queryId 就走不了 GraphQL）。
- 只收 `media[].type == "photo"`；`video` / `animated_gif` 跳过（**但它们的 `media_url_https` 就是封面，
  以后想加「视频只显示封面」时不用改结构**）。转推里的照片是否收：默认**收**（用原帖 `retweeted_status.permalink`
  做 post_id，避免同图重复入库）。
- 图片：`…jpg?name=orig` 拿原图实测可用（同一张 62 KB → 299 KB），`name=4096x4096` 结果相同，
  `name=large` ≈131 KB；**`pbs.twimg.com` 不校验 Referer**（带/不带都一样 200）。
- Cookie 升级路径：配 `auth_token` + `ct0` 后走 `i/api/graphql/{queryId}/UserMedia`，才有真游标。
  **queryId 随前端发版变化**，失效时明确报「需要更新 X 解析版本」，绝不静默返回空列表。

### 5.2 Instagram（**必须 Cookie**，见 §15-2）

免登录在这个站已经彻底没有路径，不要再尝试：

| 试过的路子 | 实测结果 |
|---|---|
| `i.instagram.com/api/v1/users/web_profile_info/?username=…`（带 `x-ig-app-id`、移动端 UA） | `401 {"require_login":true}` |
| `www.instagram.com/api/v1/users/web_profile_info/`（同一套头 + Referer） | `401 require_login` |
| `…/api/v1/users/{user}/usernameinfo/` | `429 {"spam":true}` |
| `www.instagram.com/{user}/?__a=1&__d=dis` | `201`，空响应体 |
| 主页 HTML（找首屏 JSON） | `302` 跳登录页，HTML 里 0 处 `cdninstagram` |

所以 IG 适配器**只有一条路**：用户在设置里贴 `sessionid` + `csrftoken` + `ds_user_id`（加密落库），
然后打 `web_profile_info`（或 `api/v1/feed/user/{user_id}/`）：

- 取 `data.user.edge_owner_to_timeline_media.edges[]`：`shortcode`、`display_url`（1080 长边）、
  `thumbnail_src`、`dimensions{width,height}`、`taken_at_timestamp`、`edge_media_to_caption`。
  carousel 走 `edge_sidecar_to_children`，每个 child 都收。
- 分页：`page_info.end_cursor` → 下次带 `max_id`，能真翻到 100 帖。
- 图片 CDN 是 `*.cdninstagram.com` / `*.fbcdn.net`，要补进 `platform.ImageHostPlatform` 与
  `imgcache.refererOverrides`（Referer 用 `https://www.instagram.com/`）。
  **是否校验 Referer 未验证**（拿不到任何真实图片地址），风险低：反正走服务端代理，带上 Referer 即可。
- IG 对 IP 的评分比 X 严（本次已吃到 `429 spam`），间隔按 3s 起，Cookie 失效要给出明确提示而不是空列表。

### 5.3 凭证

- 存 `settings` 表，用已有的 `encrypted` 列（`internal/secret` 的 AES-GCM），
  key 名 `social_cookie_x` / `social_cookie_instagram`，值是 JSON。
- UI 只显示「已配置 / 未配置 + 上次校验时间」，**不回显明文**；有「测试」按钮，
  抓一次 timeline 且拿到 media 才算通过。
- Cookie 失效的失败方式必须显式：账号行上标「登录态失效」，不要让用户以为这个博主没发图。

### 5.4 限速与失败（按实测改写）

| 项 | 取值 |
|---|---|
| 请求间隔 | X：1 req/1.5s；Instagram：1 req/3s（各 + ±500ms 抖动），**全局串行**，同一平台不并发 |
| 实测配额 | X syndication：**30 次 / 15 分钟，按 IP**（`x-rate-limit-limit: 30`，`x-rate-limit-reset` 给的是滚动窗口的绝对时间） |
| 冷却 | **读响应头**：`x-rate-limit-remaining: 0` → 冷却到 `x-rate-limit-reset`，不要写死 30 分钟；429 立刻停手，**绝不重试**（重试只会加深风控评分） |
| 单次同步请求上限 | X 无登录本来就是 1 个请求；有 Cookie / IG 时上限 20 |
| 首次批量添加 | 30 个博主一次性回填 = 30 个请求 = 正好打满一个 15 分钟窗口 → 队列必须**错峰**（同 X 平台串行 + 1.5s 间隔自然错峰，别开并发） |
| 错误三分类 | ①`rate_limited` 等窗口；②`needs_auth` 让用户贴 Cookie；③`stale_parser`（queryId / app-id 变了）提示更新适配器。三者 UI 上**不能长一个样** |
| 幂等 | 照片按 `(platform, url)` 去重；重复同步不产生新行 |

---

## 6. 任务与调度

复用现有 `jobs` 队列（SQLite 表 + 事务原子领取 + 崩溃恢复），新增两个任务类型：

| 任务 | 行为 |
|---|---|
| `social_sync` | 首次 `backfill_done=0`：从 `cursor` 续翻，直到凑满 100 帖或没有更多；之后：只抓第 1 页，遇到已入库 `post_id` 即停 |
| `photo_save` | 单张照片存进本地库（§9），独立 worker，默认并发 2，可暂停/重试 |

- 调度：`Scheduler` 里加一条与订阅同步并列的循环，间隔取设置项 `social_sync_hours`（默认 6）。
- 事件：新增 `photo.new {photoId, accountId, postId, postedAt}`、`social.progress {accountId, phase, posts, photos}`。
- 未读：设置项 `moments_seen_at`；`GET /api/moments/accounts` 返回 `unseenCount`
  （`count(social_photos where first_seen_at > moments_seen_at and hidden=0)`）。

---

## 7. 图片链路

- 一律走现有 `/api/img?u=&p=&w=`，`p` 传 `x` / `instagram` 以选对代理与 Referer。
- 墙上的格子用 `w=480`，灯箱用 `w=1600`（`imageUrl()` 统一加，隐私模式下由它换成小幅糊图 `s=3`）。
- **`imgcache` 原本没有淘汰机制**（照片量远大于封面，必须补）：`IMG_CACHE_MAX_MB`（默认 2048）
  + 超限时按 mtime 从最旧开始删到上限的 **90%**。触点是「启动扫一次 + 每次写缓存后最多十分钟一次」
  （`internal/imgcache/evict.go`，扫描异步、按 mtime 排序，不会卡住请求）。
  清理只删缓存目录里的文件，**不碰已存进本地库的照片**。
- 正文要清：`full_text` 里的 HTML 实体（`&amp;` 之类）解开、`t.co` 短链去掉（附件短链留着只是噪音），
  段落空行最多留一个——见 `xcom.cleanCaption`，`post.json` 与灯箱用的都是清理后的文本。

---

## 8. 与「艺人」打通

**只认 URL，不猜名字**（比头像/资料借用更严，因为这里能把陌生人挂到艺人页上）：

1. 规范化：去掉 query 与尾斜杠，host 归一到 `x.com` / `instagram.com`（`twitter.com` 视为 `x`），
   取路径首段为 handle，小写。
2. 匹配源是现成的 `performers.social_json`（里面有 Pornhub 页面上抓到的 Twitter / Instagram 链接），
   逐条规范化后与 `social_accounts(platform, handle)` 比对，命中就写 `performer_id`。
3. 添加博主时自动匹配并提示「已关联到艺人 X」；管理页可手动改或解除。
   实现：`store.FindPerformerBySocial(platform, handle)` 只做精确比对（不模糊、不看名字），
   在 `POST /api/moments/resolve` 里提前告诉用户会挂到谁名下，`POST /api/moments/accounts`
   落库后立刻 `SetSocialAccountPerformer`；管理页「解除」= `PATCH {performerId: 0}`。
4. 一个艺人可关联多个账号；一个账号最多挂一个艺人。
5. 艺人页「社交动态」区块：最近 12 张，只显示 `hidden=0`，点开进灯箱。
   落地方式：`GET /api/performers/{id}` 顺带返回 `socialAccounts` + `socialPhotos`（各 12 张上限），
   页面上是 6 列两行的小墙，点图跳 `/moments?account={id}`（照片墙带筛选，灯箱在那上面再点一次即开）。
   不在艺人页自己实现灯箱：两处灯箱迟早会不一致，跳过去只多一次点击。

---

## 9. 存进本地库

复用 M2 的库、路径清洗（`library.Sanitize`，单段 ≤120 字符）与磁盘空间校验：

```
{库根}/{平台显示名}/{博主名}/照片/{YYYY-MM-DD}_{postId 末 8 位}/
├── 01.jpg
├── 02.jpg
└── post.json     # 正文 / 原帖链接 / 发布时间 / 博主 / 原图尺寸，便于回溯
```

- **不写 `.nfo`**：Emby 对照片不刮削，写空的 nfo 只会制造垃圾；`post.json` 是给人看的。
- 单张写入用「先写 `.part` 再 rename」，中断不留半个文件；`(photo_id, library_id)` 唯一，重复存直接跳过。
- 没有可用库时前端直接禁用「保存」并提示去建库（复用现有 `no_library` 错误码语义）。
- 失败只影响这一张，任务里记录错误，灯箱该图角上标「保存失败 · 重试」。

---

## 10. 前端

| 项 | 要求 |
|---|---|
| 布局 | 照片墙 masonry，列数 2 / 3 / 4 / 5（<640 / ≥640 / ≥1024 / ≥1440）；每格保持原图宽高比，尺寸未知名用 1:1 |
| 滚动 | keyset 游标 + 无限滚动 + 离屏 DOM 回收（沿用 `InfiniteList` 的思路，不引入虚拟列表库） |
| 灯箱 | `←/→` 全局顺序翻（同帖多图先走完再进下一帖）、`Esc` 关闭、点图外关闭；`aria-modal`，焦点圈在灯箱内 |
| 新图 | 不自动插到墙顶（会让滚动跳位）；顶部浮条「N 张新照片 · 刷新」，点了才重置到顶部 |
| 空态 | 未加博主时给一句「粘贴 X 或 Instagram 主页链接开始」+ 跳管理页的按钮 |
| 隐私模式 | 与封面一致，全糊且失败时发空白，绝不退回原图 |
| 预算 | 动态页 chunk ≤ 30 KB gzip（不引第三方 masonry / 灯箱库），图片懒加载 + `loading="lazy"` |

---

## 11. 接口契约

> **实施后定稿见 `docs/API.md` 附录「M5 新增接口（动态 / 照片墙）」**（B1–B13）。
> 与下面这份草案的差异：存照片改成批量 `POST /api/moments/photos/save`（一次最多 200 张），
> 探测与新增拆成 `POST /api/moments/resolve` 与 `POST /api/moments/accounts`，
> 「已看」时刻存在 settings 的 `social_seen_at`（没有单独建表）。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/moments?cursor=&limit=&accountId=&platform=` | 照片流，keyset 分页，`limit ≤ 60` |
| GET | `/api/moments/accounts` | 博主列表 + 帖子/照片计数 + `unseenCount` |
| POST | `/api/moments/accounts` | `{url}` → 识别平台与 handle、抓一次资料，返回预览（与 `/performers/resolve` 同模式） |
| PATCH | `/api/moments/accounts/{id}` | `enabled` / `performerId` |
| DELETE | `/api/moments/accounts/{id}` | 移除（级联删记录，不动已存文件） |
| POST | `/api/moments/accounts/{id}/sync` | 立即同步（入队 `social_sync`） |
| POST | `/api/moments/seen` | 标记已读（写 `moments_seen_at`） |
| POST | `/api/moments/photos/{id}/save` | `{libraryId}` → 入队 `photo_save` |
| GET | `/api/manage/moments` | 管理页快照：博主 + 统计（帖子/照片/缓存占用/最近抓取/错误） |

错误码沿用现有表，新增：`needs_auth`（401 上游要登录态）、`already_saved`（409 已在库里）。

---

## 12. 验收标准

| 阶段 | 内容 | 验收 |
|---|---|---|
| **M5-a** | 表 + `social.Adapter` + X 无登录抓取 + 照片墙 + 动态管理 tab | 粘一个 X 主页链接 → 回填 100 帖 → 墙上出图（≥300 张）→ 灯箱能翻能存 → 管理页看得到同步状态 |
| **M5-b** | Instagram 抓取 + Cookie 配置 + IG 代理 | 同上；无 Cookie 失败时 UI 明确指出「需登录态」并给配置入口 |
| **M5-c** | 打通与打磨：艺人关联、一键存库、未读、隐私模式、缓存上限清理 | 艺人页出现社交区块；隐私模式全糊；清理任务跑过一轮；移动端底栏不截断 |

---

## 13. spike 结果（2026-10-28 已跑，原始数据见 §15）

| # | 待验 | 结论 |
|---|---|---|
| 1 | X syndication 时间线是否仍带 media | ✅ **可用**：`__NEXT_DATA__` 里 20 条 tweet，`extended_entities.media` 正常（NASA 一页 6 张照片 + 4 个视频，`?name=orig` 出原图） |
| 1b | X 能否翻页拿 100 帖 | ❌ **不能**：无 cursor 条目，任何分页参数都会让响应变成错误壳 → 免登录封顶 20 帖 |
| 2 | IG 免登录可用率 | ❌ **归零**：10/10 类请求全是 `401 require_login` / `429 spam`；主页 302 跳登录 → IG 必须 Cookie |
| 3 | IG 图 CDN 是否校验 Referer | ⏳ **仍未验证**（拿不到真实图片地址）。风险低：走服务端代理带 Referer 即可，实现时顺手验 |
| 4 | X `name=orig` 是否可用 | ✅ 可用（62 KB → 299 KB；`4096x4096` 同结果），且 **`pbs.twimg.com` 不校验 Referer** |
| 5 | 限流阈值 | ✅ 量到了：**30 次 / 15 分钟（按 IP）**，`reset` 时刻准时恢复（17:09:56 恢复，`remaining` 归 20） |
| 6 | 不存在账号的错误语义 | ✅ 200 + `hasResults: false`（不是 404）；实现**必须靠 `hasResults` 判空** |

---

## 14. 已知风险与取舍

- **条款与账号安全**：脚本化抓取这两个站都违反其用户条款，登录态账号可能被限流或限制。
  只建议配小号，Cookie 与库一起加密备份；被封只影响动态功能，不影响影片链路。
- **维护成本高于影片适配器**：X 的 `queryId`、IG 的 `app-id` 和风控策略都会变，
  这两家比 Pornhub / xHamster / beeg 更容易坏。对策：解析集中在小包内 + fixture 单测 +
  失败时 UI 明说是「需要更新解析」还是「没配 Cookie」，不静默返回空。
- **存储**：照片量大且长尾。缓存要有上限（§7），落库要用户主动触发（默认不自动存）。
- **边界**：动态只做「看和存」，不接播放器、不做 Emby 剧集整合；动态数据不进影片流与统计口径。

---

## 15. spike 原始记录（2026-10-28，本机直连，无代理）

环境：macOS 直连（`.env.local` 里的 `GLOBAL_PROXY` / `PLATFORM_PROXY_*` 全是空值，DNS 走 fake-IP 隧道）。
所有请求都用桌面 Chrome UA；IG 额外试了 iPhone UA + `x-ig-app-id: 936619743392459`。

### 15.1 X 可用性

```
GET https://syndication.twitter.com/srv/timeline-profile/screen-name/NASA
→ 200, 143792 B
props.pageProps.timeline.entries = 20 条，全部 type=tweet
props.pageProps.latest_tweet_id = 2113776971603689874
每页构成：photo 6 张、video 4 个；有 retweeted_status 的转推 1 条
media 项示例：{"type":"photo","media_url_https":"https://pbs.twimg.com/media/HVWk812WIAAg_hz.jpg",
              "original_info":{"width":3840,"height":2160},"media_key":"3_…","expanded_url":"…"}
created_at 格式："Fri Oct 23 23:37:56 +0000 2026"
```

### 15.2 X 翻页 / 参数探测（全部失败）

```
?showReplies=true   → 200，entries=0
?dnt=false ?lang=zh-cn ?cursor=DAAABC ?max_id=1 ?page=2 → 200 但无 __NEXT_DATA__（错误壳）
```

### 15.3 X 限流（关键）

```
首次探测后连打 8 次（无间隔）：
HTTP/2 429
x-rate-limit-limit: 30
x-rate-limit-remaining: 0
x-rate-limit-reset: 1793120996   # = 17:09:56 UTC
body: "Rate limit exceeded"
等 45 秒再试 → 仍 429（窗口没到）
17:10:37（reset 后）→ 200，remaining=20 → 19 → 18，每次 entries=20
```

### 15.4 X 图片

```
同一张图：无后缀 62271 B / ?name=orig 299338 B / ?name=4096x4096 299338 B / ?name=large 131538 B
带不带 Referer: https://x.com/ 都是 200 299338 B
```

### 15.5 X 已死的路子

```
cdn.syndication.twimg.com/timeline/profile?screen_name=…   → 200，0 B
api.x.com/1.1/statuses/user_timeline.json（带 guest_token）→ 404 code 34
POST api.x.com/1.1/guest/activate.json                     → 200 {"guest_token":"2115…"}（但没有 queryId，用不上）
x.com/{handle}/media                                       → 307（去登录）
cdn.syndication.twimg.com/tweet-result?id=…&token=x        → 200，3780 B，含 mediaDetails（单条富化可用）
```

### 15.6 X 错误语义

```
screen-name/qweqweqwe123zzz → 200, 2193 B, hasResults=false, entries=0, headerProps=null
```

### 15.7 Instagram（免登录全线 401）

```
i.instagram.com/api/v1/users/web_profile_info/?username=instagram      → 401 {"require_login":true,"status":"fail"}
www.instagram.com/api/v1/users/web_profile_info/（带 Referer）          → 401 同上
www.instagram.com/api/v1/users/{user}/usernameinfo/                    → 429 {"spam":true}
www.instagram.com/{user}/?__a=1&__d=dis                                → 201，空
www.instagram.com/nasa/（HTML）                                        → 302，0 B，0 处 cdninstagram
```

### 15.8 复跑方式

```bash
# X 时间线（一次就够，别连打：30 次/15 分钟，按 IP 计）
curl -s -H 'User-Agent: Mozilla/5.0 …Chrome/131…' \
  'https://syndication.twitter.com/srv/timeline-profile/screen-name/NASA' \
| python3 -c "import sys,re,json;h=sys.stdin.read();d=json.loads(re.search(r'__NEXT_DATA__\" type=\"application/json\">(.*?)</script>',h,re.S).group(1));print(len(d['props']['pageProps']['timeline']['entries']))"
# 看配额：加 -D - 打头，读 x-rate-limit-remaining / -reset
```

### 15.9 「动态一直不更新」怎么分清是抓取坏了还是上游没新帖（2026-11-03）

用户报「动态里好像没有抓取最新的推特照片」。量下来的结论是**抓取没坏，是这条公开路径上没有更新的帖子**，
顺手补了一条静默失败路径。可复现的判据：

```
GET …/screen-name/mysaaat        → 200, 122602 B, entries=32, 全部 type=photo
                                   最早 2024-09-24，最新 2025-07-04T16:30:07Z
换 UA(curl/8.7.1) + Cache-Control: no-cache + Accept-Language: ja-JP → 同一份 122602 B（不是 CDN 缓存差异）
GET …/screen-name/NASA (对照)     → 200, entries=20, 最新 2026-11-02T22:30:01Z（当天）→ 端点本身是好的
?showReplies=true                → 200 但无 __NEXT_DATA__（错误壳，和 §15.2 一致）
```

再拿现场这 32 条和库里对照：**32 条全在库里**（`social_posts` 共 33 条，多出来的一条
`1856304913774493929` 是上游已经删掉的旧帖）。也就是说 `internal/jobs/social.go` 那条
「第 1 页 → 全是已入库 → 当作没有新帖收工」的路径是对的：这一页确实没有新东西。

**顺带修掉的静默路径**：`ListPosts` 以前解析出 0 条也返回成功空页，于是「账号不存在/设了保护」
和「X 又改了页面结构」都会被记成「同步成功」，界面上就是「上次同步：刚刚，但墙上一年没动」。
现在 0 条会分流成 `not_found`（`hasResults:false`）或 `stale_parser`（有结果但解析不出帖子），
写进 `social_accounts.last_error`，见 `TestListPostsEmpty`。

**注意**：§15.9 说的「这条公开路径上确实没有更新的帖子」只对**时间线 widget** 成立。同一个号的主页 HTML
里其实有更新的帖子（2025-10、2026-07 都有），只是 widget 不给——发现这条路见 §15.10。

如果哪天主页 HTML 也开始落后于登录后看到的页面（敏感媒体/受保护帖不发给匿名访客），
唯一的解法是把 X 适配器改成带 Cookie 的登录抓取（现在 `xcom` 一行 cookie 都没用，设置里也没有
`x_cookie`），代价是要跟 X 的 GraphQL `queryId` 走，隔一阵就会失效一次。

### 15.10 widget 里没有的新帖，主页 HTML 里有（2026-11-03）

用户丢来 `https://x.com/mysaaat/status/2072599171006104014`（雪花 id 解出来是 2026-07-02T08:32:04Z），
问「免登录抓不到吗」。当天读数：

```
GET https://x.com/mysaaat                 → 200, 142173 B（桌面浏览器 UA）
                                            里面有 5 条帖子 id：2025-10-08 / 10-09 / 10-20 / 10-25 / 2026-07-02
同一 URL 换 Googlebot UA                  → 403, 0 B（所以必须用桌面 UA）
GET cdn.syndication.twimg.com/tweet-result?id=2072599171006104014&lang=en
                                          → 200 但只有 2 字节 `{}`（缺 token 就是空壳）
   同上 + &token=<算出来的>                → 200, 2022 B：created_at / text / mediaDetails（含 960×1280 原图）全在
   同上但 token=abc123                    → 200, 2022 B ← **服务端只校验「有没有 token 参数」，不校验值**
GET …/screen-name/mysaaat（时间线 widget） → 200, entries 还是停在 2025-07-04 的那 32 条，一条新的都没有
```

token 是官方嵌推 widget 那套：`((id / 1e15) * π)` 转 36 进制、去掉小数点和 0（照算，但不指望它被校验）。

主页 HTML 是 RSC 流（`$R[21].next($R[23]=…{"tweet_results":…})`，字符串里的 JSON 被转义成 `\"`），
所以只抠 id、不解整棵对象——两种写法都认：media 的 `…/status/{id}/photo/1` expanded_url，
以及 `edit_tweet_ids":[ "{id}" ]`。

效果（一次手动同步，job 412；job 413 是另一个号，验证没把正常的号弄坏）：

| 账号 | 之前 | 之后 | 补进来的 |
|---|---|---|---|
| @mysaaat | 33 帖 / 33 图，最新 2025-07-04 | 38 / 38，最新 2026-07-02 | 5 条：2025-10-08、10-09、10-20、10-25、2026-07-02（全带图） |
| @jun_kasui | 最新 2026-10-21 | 最新 2026-11-02 | 时间线本来就新鲜，主页那条路只是补充 |

局限：主页 HTML 一次只给 5 条 id，6 小时一轮的增量能把「最新」补上，中间若真漏掉若干条补不齐；
要全量还是得登录抓取（Cookie + GraphQL `UserMedia`）。


---

## 16. 实施记录（M5 已落地）

### 16.1 代码落点

| 位置 | 内容 |
|---|---|
| `internal/social/social.go` | `Adapter` 接口、`Error`（`needs_auth` / `rate_limited` / `stale_parser` / `not_found` / `upstream`）、`Throttle`（串行 + 间隔 + 冷却）、`NormalizeHandle`、`Registry` |
| `internal/social/xcom/` | X 免登录抓取：时间线 widget（历史）+ 主页 HTML 发现新帖（§15.10）+ 单帖 `tweet-result`（详情）、限流冷却、fixture 单测 |
| `internal/social/igram/` | Instagram Cookie 抓取（`web_profile_info` 首屏 + `feed/user` 翻页）、carousel 拆图、Cookie 归一化（**Netscape cookies.txt / Cookie 头 / JSON 三种写法**）、fixture 单测 |
| `internal/store/social.go` + `migrations/0007_m5_social.sql` | 四张表与全部读写、照片墙 keyset 游标、计数、URL 反查艺人 |
| `internal/jobs/social.go` | `social_sync`（每轮最多 5 页/账号，遇限流整轮停）/ `social_save`（下载入库 + `post.json`） |
| `internal/httpapi/handlers_moments.go` | B1–B11 全部端点 + `writeSocialErr`（错误码 → 状态码的唯一出口） |
| `web/src/routes/Moments.svelte` | 照片墙（`InfiniteList` 复用 + 灯箱 + 多选存档 + 添加博主） |
| `web/src/routes/Manage.svelte` | 「动态管理」页签：博主列表 / 同步 / 重填 / 启停 / 删除 |
| `web/src/components/SettingsDialog.svelte` | 「动态」分类：抓取间隔 + Instagram Cookie |
| `web/src/routes/Performer.svelte` | 艺人页「社交动态」区块（12 张，6 列两行，点图跳带 `account` 筛选的照片墙） |
| `internal/proxying/` | 代理解析器（设置优先、环境变量兜底）+ 动态 HTTP 客户端接线，改设置即时生效 |
| `internal/imgcache/evict.go` | 缓存上限与淘汰（`IMG_CACHE_MAX_MB`，默认 2048；按 mtime 淘汰到 90%；启动扫一次 + 写入后十分钟一次） |

测试（`go test ./...` 覆盖的部分）：

| 文件 | 覆盖 |
|---|---|
| `internal/store/social_test.go` | 账号 upsert 摘要（不覆盖空字段）、帖子去重、照片墙顺序与游标、URL 反查艺人 + 按艺人过滤、删号级联 |
| `internal/social/xcom/xcom_test.go` | 正常时间线、无 `__NEXT_DATA__`、`hasResults:false`、**0 条帖子必须报错（`not_found` / `stale_parser`）**、**主页 HTML 发现的新帖进结果且按时间倒序**、**主页被登录墙挡住时退回时间线**、token、真限流、**过期 `remaining:0` 头** |
| `internal/social/igram/igram_test.go` | 首屏/第二页解析、carousel 拆图、Cookie 解析、无 Cookie → `needs_auth`、401 / 429 |
| `internal/imgcache/evict_test.go` | 超限时从最旧删到 90%、未设上限/未超限时不动、空目录 |
| `internal/httpapi/social_err_test.go` | 五种 `social.Error` → 状态码/错误码映射、`rate_limited` 带 `details.resetAt`、超时不漏 Go 原始错误 |

### 16.2 与草案的取舍

- **照片墙顺序**：`posted_at DESC, post_id DESC, seq ASC`。同帖多图必须相邻且同序，
  否则灯箱「下一张」会在一堆帖子里乱跳。
- **X 转推**：转推的图也算（取 `retweeted_status` 的原帖信息），按原帖 id 去重——
  同一条被多个博主转推时墙上只出现一次。
- **同步并发固定为 1**：限流按 IP 算，并行抓只会更快撞 429。
- **免登录 X 的「20 帖」**：一次只有 20 条且无游标，所以「最近 100 帖」对 X 免登录实际是
  「每 6 小时累积 20 条新的」。IG（有 Cookie）和 X（将来接 Cookie）才真能一次翻到 100。
- **未做**：动态→Emby、动态内播放（动态只做「看和存」）；X Cookie/GraphQL 升级留到 M6。

### 16.3 实施中踩到的坑（值得记住）

1. **X 拒收 HTTP/1.1**。手工构造的 `http.Transport`（带自定义 `DialContext`）默认不开 HTTP/2，
   而 X 的 syndication 对 HTTP/1.1 一律回 `429`（还带一个假的 `reset`）——同一地址换 HTTP/2 立刻 200。
   这条坑把「先在 spike 里跑通」骗了整整一轮：spike 用 curl（HTTP/2）成功，服务端却全挂。
   修法：`platform.NewHTTPClient` 里显式 `ForceAttemptHTTP2: true`。
2. **`remaining=0` 但 `reset` 已过期**是缓存里的旧头，不该判死（见 16.4 第 2 条）。
3. **SSE 在登录过期后会每 20 秒刷一次 401**。`EventSource` 拿不到状态码，只能从 `error` 事件里猜；
   现在 `connectEvents` 在 `error` 时先 `GET /api/auth/me` 探一次，401 就彻底停手（把人交给普通 401 流程），
   断网才继续退避重连。否则照片墙开着过夜，控制台会被 401 刷满。
4. **IG 的 429 有两种意思，混成一个会误导人**。JSON `{"message":"Please wait a few minutes…"}`
   才是真限流；**HTML 拦截页**（`<title>Page Not Found</title>`，页面里也写着 wait a few minutes）
   是风控把整个会话/出口 IP 挡在门外。2026-10-28 实测：Cookie 有效（拦截页 HTML 里带着
   `class="no-js logged-in"`，文档页 200 正常渲染），但 `api/v1/users/web_profile_info` 一律
   429 + 拦截页；换成无 Cookie 请求则是正常的 JSON 401，说明**是会话/IP 被标记，不是我们的头不对**
   （Origin / Sec-Fetch / priority / 桌面 vs Safari UA 全试过，一模一样）。
   所以 `blocked` 单独一个错误种类：**不设冷却**（等下去不会好），文案直接指向「换出口 IP / 重新导出 Cookie」；
   界面上还给一个「清除冷却并重试」——冷却时间是上一次撞墙时猜的，用户换完 Cookie 立刻重试是合理的。
5. **限流冷却只在内存里**：进程重启即清零。够用（真撞限流时重启后第一次请求会自己再撞一次并重新计时），
   所以没有为它建表。

### 16.4 新增已知限制

- **Instagram 图片地址带签名，数小时内失效**。已缓存的图不受影响（缓存键是地址，命中就不再回源），
  但很久以前、当时没被看过的照片再打开会因签名过期抓不到，此时 `/api/img` 按既有契约给占位图，
  页面不会崩。想长期留住就点「存进本地库」。
- **限流头可能是过期值**：`remaining=0` 但 `reset` 已经过去时按正常响应处理（窗口已翻篇，
  否则会把可以用的响应误判成限流）。
- **限流是「按 IP + 按 HTTP 版本」的**：同一台机器上别的工具把配额用满，动态功能也会跟着停，
  报错会附一句「（HH:MM 之后自动恢复）」，到点自动恢复。
- **Instagram 对出口 IP 很挑**：同一个 Cookie 换了 IP 就可能整段被拦截（429 + HTML 拦截页）。
  抓 IG 要配代理走家宽/住宅出口，别让服务器直连机房 IP。
- **代理配置在界面里，且保存即生效**：设置 → 网络与代理 → 「Instagram 专用代理」
  （存 `proxy_instagram`，X 同理 `proxy_x`）。实现是 `internal/proxying.Resolver` +
  `platform.NewDynamicHTTPClient`：`Transport.Proxy` 是个函数，每次发请求才去读设置表，
  所以改完不用重启、不用重建客户端（有单测 `TestDynamicClientFollowsSettings` 守着）。
  优先级：设置里的平台代理 > 设置里的全局 > `PLATFORM_PROXY_*` 环境变量 > `GLOBAL_PROXY`。
  被拦时界面直接指到这块设置，不会让你干等 15 分钟。
- **Instagram Cookie 只要给得出就能用，但「给不出」的判断只看 sessionid**：缺 `sessionid` 一律当未配置；
  其余 Cookie（`mid` / `ig_did` / `datr` / `rur`）给了就原样带上，少这些时 IG 可能照样 401。
- **Instagram 全链路没有在真实账号上跑过**：手上没有可用 Cookie，首屏 `web_profile_info`、翻页
  `feed/user`、carousel 拆图、401/429 映射都只有 fixture 单测覆盖；第一次配好 Cookie 后建议先用
  小号试一条，确认能出图再挂大号。
