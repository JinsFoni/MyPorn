# MyPorn API 契约 v1（M1）

后端监听 `:8080`（`PORT` 可配）。所有接口前缀 `/api`，JSON 请求/响应，UTF-8。
前端开发时用 Vite `server.proxy` 把 `/api` 与 `/healthz` 转发到 `http://localhost:8080`。

- 成功：`200/201/204`，body 为 JSON（204 无 body）
- 失败：`4xx/5xx` + `{"error":{"code":"...","message":"...","details":{...}}}`

## 0. 鉴权

- `mp_session` cookie：HttpOnly、SameSite=Lax、Path=/、Max-Age=30d
- 除 `POST /api/auth/login` 与 `GET /healthz` 外，所有 `/api/*` 未登录返回 `401 {"error":{"code":"unauthorized"}}`
- 前端收到 401 统一跳转登录页

| 方法 | 路径 | 请求 | 响应 |
|---|---|---|---|
| POST | `/api/auth/login` | `{"password":"..."}` | `204` + Set-Cookie；密码错 `401 {"error":{"code":"invalid_password"}}`；连续失败 5 次锁定 5 分钟 `429 {"error":{"code":"too_many_attempts"}}` |
| POST | `/api/auth/logout` | — | `204` |
| GET | `/api/auth/me` | — | `{"authenticated":true}` / `401` |

## 1. 平台

`GET /api/platforms` →
```json
[{ "key":"pornhub", "name":"Pornhub", "enabled":true,
   "baseUrl":"https://cn.pornhub.com",
   "hasProxy":true, "hasCookie":false,
   "status":"ok" },
 { "key":"xhamster", "name":"xHamster", "enabled":true,
   "baseUrl":"https://xhamster.com", "hasProxy":false, "hasCookie":false, "status":"ok" },
 { "key":"beeg", "name":"beeg", "enabled":true,
   "baseUrl":"https://beeg.com", "hasProxy":false, "hasCookie":false, "status":"ok" }]
```
`status`: `ok | no_proxy | error`（前端仅做提示，不阻塞）。
`no_proxy` 只对「直连一般不通」的平台报（目前仅 `pornhub`）；xHamster / beeg 实测可直连，没配代理也是 `ok`。

**平台 key**：`pornhub` / `xhamster` / `beeg`。`platform` 字段在艺人、影片、订阅、搜索接口里都取这三个值之一。

## 2. 艺人

```ts
type Performer = {
  id: number                 // 本地 id
  platform: "pornhub" | "xhamster" | "beeg"
  performerId: string        // 平台侧 id 或 slug
  name: string
  url: string                // 平台主页
  avatarUrl: string | null   // 经过本地 /api/img 代理后的 URL
  bio: string | null
  profile: { label: string, value: string }[]  // 资料区块（性别/身高/出身地…），顺序即平台页面顺序；可能是空数组
  social: { label: string, url: string }[]     // 平台主页挂的站外链接（官方网址/Twitter/Instagram…）；可能是空数组
  body: { heightCm: number, bust: number, waist: number, hips: number }  // 身材（已归一成厘米）；0 = 平台没给/没认出来
  rank: number                   // 平台给的模特排名（越小越靠前）；0 = 平台没给/没认出来，界面就不出「排名」那一行
  videoCount: number | null      // 平台资料页声明的影片总数；Pornhub 资料页**没有**这个数，通常为 null
  localVideoCount?: number       // 本地已收录的影片数（仅详情接口给；界面上的「N 部影片」用它）
  subscriberCount: number | null
  subscriptionId: number | null  // null = 未订阅
  enabled: boolean
  backfillDone: boolean
  backfillProgress: { donePages:number, totalPages:number|null } | null
  lastSyncAt: string | null   // RFC3339
  nextSyncAt: string | null
  newVideoCount: number       // 上次同步后新增、且未看过的数量
  lastError: string | null
}
```

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/performers?q=&subscribed=true\|false&page=1&limit=24` | 已入库艺人（M1 主要返回已订阅）→ `{"items":Performer[],"total":n,"page":1,"limit":24}` |
| GET | `/api/performers/{id}` | 单个艺人 `Performer` |
| POST | `/api/performers/resolve` | body `{"url":"https://cn.pornhub.com/model/mini-diva"}` → 远程解析但不入库，返回 `{platform,performerId,name,avatarUrl,url,videoCount}` |

### 订阅

| 方法 | 路径 | 请求 | 响应 |
|---|---|---|---|
| POST | `/api/subscriptions` | `{"url":"<艺人主页>","backfillLimit":0}`，`backfillLimit:0` 表示全量，`N>0` 只回填最近 N 部 | `201 {"subscriptionId":n,"performer":Performer,"jobId":n}` |
| GET | `/api/subscriptions` | — | `{"items":Performer[]}`（同 Performer 结构） |
| PATCH | `/api/subscriptions/{id}` | `{"enabled":false}` | `200 Performer` |
| DELETE | `/api/subscriptions/{id}` | — | `204`（软删，影片保留） |
| POST | `/api/subscriptions/{id}/sync` | `{"mode":"incremental"\|"full"}` | `202 {"jobId":n}` |

### 远程艺人搜索

`GET /api/search/performers?q=<kw>&platform=pornhub|xhamster|beeg&page=1`（`platform` 缺省为 `pornhub`）→
```json
{ "items":[{"platform":"pornhub","performerId":"mini-diva","name":"Mini Diva",
            "avatarUrl":"/api/img?u=...","url":"https://cn.pornhub.com/model/mini-diva",
            "videoCount":123,"subscriberCount":45600,"subscribedId":null}],
  "page":1, "hasMore":true }
```

## 3. 影片

```ts
type Video = {
  id: number
  platform: "pornhub" | "xhamster" | "beeg"
  platformVideoId: string
  title: string
  url: string                  // 平台观看页
  thumbUrl: string | null      // 已代理
  previewUrl: string | null    // 短预览 webm/mp4，已代理
  durationSec: number
  publishedAt: string | null   // RFC3339
  viewCount: number | null
  rating: number | null        // 0-100
  tags: string[]
  performers: { id:number|null, name:string, url:string|null }[]
  isNew: boolean               // 同步后新增且未看
  hasLocalFile: boolean        // M1 恒为 false
}
```

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/videos?performerId=&cursor=&limit=30&q=` | 影片流，播放时间倒序，**keyset 游标**分页 → `{"items":Video[],"nextCursor":"..."\|null}` |
| GET | `/api/videos/new?since=` | 新片（`isNew`）列表，同结构，首页角标用 |
| GET | `/api/videos/{id}` | `Video` + `{"description":"...","related":Video[]}` |
| POST | `/api/videos/{id}/seen` | `204`，清除 isNew |

### 播放

`GET /api/videos/{id}/playback` →
```json
{
  "videoId": 12,
  "variants": [
    { "id":"2160p", "height":2160, "width":3840, "bandwidth":12000000, "codec":"avc1.640033", "url":"https://.../index.m3u8", "isDefault":true },
    { "id":"1080p", "height":1080, "width":1920, "bandwidth":4000000,  "codec":"avc1.640028", "url":"https://.../index.m3u8", "isDefault":false }
  ],
  "defaultVariantId": "2160p",
  "proxyPlaybackUrl": "/api/videos/12/playback/proxy.m3u8",
  "expiresAt": "2026-10-26T04:10:00Z"
}
```
- `variants` 按 **(height, 编码偏好, bandwidth) 降序**；`defaultVariantId` 即最高画质。
  编码偏好为 AV1 > HEVC > VP9 > H.264（`model.SortVariantsDesc`），只在同分辨率内生效，
  详情见 ARCHITECTURE §7.4.1。前端还会按浏览器解码能力再排一次（解不了的编码靠后）。
- `id` 是稳定的选档标识：H.264 为 `1080p` / `2160p` / `sd` / `auto`；
  **非 H.264 带编码后缀**（`1080p.av1` / `1080p.hevc`），否则同分辨率两种编码会撞成同一个 id
- `proxyPlaybackUrl` 为直链失败时的降级入口（M1 可返回 `null`）
- 解析失败 `502 {"error":{"code":"resolve_failed","message":"..."}}`

## 4. 图片代理

`GET /api/img?u=<urlencoded 远端地址>&w=<可选宽度>` → 图片二进制
- 后端带正确 `Referer`/`UA` 拉取，磁盘缓存于 `data/imgcache/`
- 命中缓存 `200` + `Cache-Control: public, max-age=86400`；失败返回占位图 `200`（不 4xx，避免前端破图）

## 5. 实时事件（SSE）

`GET /api/events` → `text/event-stream`，`event:` 后跟 `data:` JSON：

| event | data |
|---|---|
| `job.updated` | `{id,type,status,progress,message,error}` |
| `sync.progress` | `{subscriptionId,performerId,page,totalPages,videosFound,phase:"backfill"\|"incremental"}` |
| `video.new` | `{videoId,performerId,title}` |
| `ping` | `{}`（每 25s，保活用） |

## 6. 任务

`GET /api/jobs?type=&status=&limit=50` → `{"items":[{id,type,status,progress,message,error,createdAt,startedAt,finishedAt,payload}]}`
`type`: `sync | download | upload | strm`；`status`: `queued | running | done | failed | canceled`
`POST /api/jobs/{id}/cancel` → `202`

## 7. 设置（M1 最小集）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/settings` | `{syncIntervalHours, globalProxy, platformProxy:{pornhub,xhamster,beeg},
  namingTemplate, downloadConcurrency, downloadRateKb, autoDownloadNew, libraryRoot,
  openlist:{baseUrl,root,hasToken}}`（**不含任何密钥明文**） |
| PUT | `/api/settings` | 同结构，部分字段可选；密钥字段用 `{"openlist":{"token":"..."}}` 单独写，响应永不回显 |

## 8. 健康检查

`GET /healthz` → `200 {"ok":true,"version":"0.1.0","uptimeSec":123}`（无需鉴权）

## 9. 错误码表

| code | HTTP | 含义 |
|---|---|---|
| `unauthorized` | 401 | 未登录/会话过期 |
| `invalid_password` | 401 | 密码错误 |
| `too_many_attempts` | 429 | 登录锁定 |
| `bad_request` | 400 | 参数校验失败 |
| `not_found` | 404 | 资源不存在 |
| `unsupported_platform` | 400 | URL 不属于已支持的平台 |
| `resolve_failed` | 502 | 上游解析/抓取失败 |
| `rate_limited` | 429 | 上游限流（带 `details.resetAt`，等到点就好） |
| `blocked` | 429 | 上游风控拦截（IG 的 429 + HTML 拦截页；**不带** `resetAt`，等没用，要换 IP / 换 Cookie） |
| `internal` | 500 | 服务端错误 |

---

## 附：M1 实现补充说明（与上方契约的差异）

以下为实现时确定、前端已按此对接的细节，均未改变既有字段语义：

1. **`PUT /api/settings`** 保存后返回完整的 `Settings` 对象（而非 204），前端以服务端返回为准；
1a. **`autoDownloadNew`**（默认 `false`）= 「同步到新片后自动下载」：打开后，**增量同步**到的新片会自动投进下载队列（进默认库）；首次全量回填不会自动下载。关闭时下载只能手动触发。
   `openlist.token` 为 write-only，响应里只回 `hasToken`。
2. **`GET /api/videos/new`** 与 `/api/videos` 同结构：接受 `cursor` / `limit`，返回 `{items, nextCursor, total}`；
   `since` 参数保留但当前忽略。
3. **`Settings.platformProxy`** 为 `{ "pornhub": "...", "xhamster": "...", "beeg": "..." }`，空串表示回退到 `globalProxy`；
   对应环境变量 `PLATFORM_PROXY_PORNHUB` / `PLATFORM_PROXY_XHAMSTER` / `PLATFORM_PROXY_BEEG`。
4. **`Performer.videoCount`** 是平台侧影片数量（不是粉丝数）；粉丝数用 `subscriberCount`。
   注意 **Pornhub 的资料页只有「视频播放量」和「订阅者」，没有影片总数**，所以 `videoCount` 经常是 `null`；
   艺人页显示的「N 部影片」用 `localVideoCount`（本地收录数，与下面影片列表一致），订阅数用 `subscriberCount`
   （从 `.infoBox[data-title="订阅者: 1,430,831"]` 里取完整数字）。
4a. **`Performer.profile` / `Performer.social`** 来自艺人主页的「资料区块」与右侧社交列表，同步时整体覆盖写入；
   上游页面没有这些内容时保留库里旧值，接口永远返回数组（空则 `[]`，不会是 `null`）。
   **「会勾起我性欲的是」「会浇灭我性欲的是」两项性癖偏好，以及「视频播放量 / 主页浏览量 / 已看视频」三行平台统计，
   在解析阶段就被丢掉**，不属于资料的一部分，也不会入库。
   另外 **「职业状态」「工作起始及结束时间」是全站口径不入库**（`model.DropProfileLabel`）：
   任何平台给了都丢，写库时再兜一次底，老数据在进程启动时被清掉。
4b. **`Performer.body`** 是资料行里「身高」「三围」的归一结果：身高统一成厘米（`5' 6" (167cm)` 优先取站点自己写的 `167`，
   只给英制才按 2.54 换算），三围拆成 `bust/waist/hips`（英制的 `35-25-36` 会换成 `89/64/91`）。
   **认出来的这两行会从 `profile` 里摘掉**（同一条数据不在页面上出现两次），认不出来的（如 `1.67 m`）原样留在 `profile` 里；
   四项全 0 时艺人页不出「身材」那一行。字段固定四项、缺的就是 `0`，不会是 `null`。
4a-2. **xHamster / beeg 的 `profile` / `social` / `body` / `rank` / `bio` 来自 Pornhub**：
   这两家的艺人页资料很薄，同步时会按艺人名到 Pornhub 上找同一个人（名字归一后必须完全一致），
   找到就把上述资料内容整份换过来；`platform` / `performerId` / `url` / `name` / `videoCount` /
   `subscriberCount` 与头像仍是本平台的（头像只在本地为空时才借用）。名字对不上就保持原样。
   资料来源的 `bio` 若只是一串网址（如 `Onlyfans.com/xyz`）不会覆盖本网站已有的简介。
   一次性返工老数据：`./myporn -backfill-profile`。
4c. **`Performer.rank`** 取艺人主页「排名信息」块里的**模特排名**（`.rankingInfo` 里第一个 `.infoBox`）——站点页头那块
   `模特排名 / 本周排名 / 本月排名 / 上月排名 / 年度排名` 里只取第一个，其余四个不解析。取不到就是 `0`，艺人页不渲染这一行。
   **单独一列**（`performers.model_rank`，整数）而不是塞进 JSON：它是一个会随同步更新的标量；同步时 `0` 不覆盖库里的旧值。
5. **`Performer.backfillProgress`** 为 `{donePages, totalPages}`，百分比由前端计算；`totalPages` 未知时为 0。
6. 没有批量同步端点：多艺人同步需逐个 `POST /api/subscriptions/{id}/sync`。
7. 没有 OpenList 连通性测试端点（设置页因此无"测试连接"按钮）。
8. `POST /api/videos/{id}/seen`、`DELETE /api/subscriptions/{id}`、`POST /api/jobs/{id}/cancel`
   成功返回 `204` / `202`，无响应体。
9. **同步失败退避**：任务失败后该订阅的 `nextSyncAt` 推迟 30 分钟，避免每分钟重试触发上游风控。

---

## 附：M2 新增接口（本地库 · 下载 · 队列）

### A1. 本地库

`Library`：
```jsonc
{
  "id": 1, "name": "主库", "rootPath": "./media/主库", "enabled": true, "isDefault": true,
  "uploadPolicy": "local",           // local | upload | strm（M2 只实现 local）
  "namingTemplate": "",              // 空 = 跟随全局 settings.namingTemplate
  "openlistPath": "",
  "exists": true, "writable": true, "spaceMessage": "可写",
  "freeBytes": 67954847744, "totalBytes": 494384795648,
  "fileCount": 7, "sizeBytes": 1370318222
}
```

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/libraries` | `{items:Library[], totalFileCount, totalSizeBytes, suggestedRootPath}`；一个默认库都没标时按「第一个可用库」兜底 |
| POST | `/api/libraries` | 建库，body `{name, rootPath, isDefault?, uploadPolicy?, namingTemplate?, openlistPath?}` → `201 Library`；**第一个库强制为默认库** |
| POST | `/api/libraries/validate` | body `{path}` → `{ok, exists, writable, freeBytes, totalBytes, message}`，用于表单里的「检查」 |
| PATCH | `/api/libraries/{id}` | 部分字段更新（含 `enabled` / `isDefault`）→ `Library` |
| DELETE | `/api/libraries/{id}` | `204`；**只删库记录，不动磁盘文件** |

### A2. 下载

`GET /api/videos/{id}/download-options` →
```json
{ "videoId": 12, "variantId": "1080p", "height": 1080, "bandwidth": 1353004,
  "estimatedBytes": 115512375, "durationSec": 683,
  "defaultLibraryId": 1, "libraries": [Library], "localCopies": [{"libraryId":1,"path":"...","sizeBytes":116410286}] }
```
- `variantId/estimatedBytes` 可能为 `null`（解析失败时前端显示「码率未知」并隐藏体积预估）

`POST /api/videos/{id}/download` → body `{libraryId?, overwrite?}`
- `202 {jobId, videoId, libraryId, title, skipped:false}` 已入队
- `200 {skipped:true, reason:"该影片在此库中已存在", localPath, sizeBytes}` 幂等命中
- `400 no_library`（没有任何库）、`409 already_queued`（同影片在队列里）

`GET /api/downloads?limit=&status=` →
```json
{ "activeCount": 2, "items": [
  { "id": 5, "videoId": 12, "libraryId": 1, "title": "...", "thumbUrl": "...",
    "status": "running", "progress": 54.1, "bytesDone": 146000000, "bytesTotal": 505500000,
    "speedBps": 15520000, "message": "下载中（ffmpeg）60%", "error": "",
    "libraryName": "主库", "localPath": "media/主库/Pornhub/Mini Diva/.../xxx.mp4",
    "sizeBytes": 0, "createdAt": "...", "startedAt": "...", "finishedAt": null } ] }
```

### A3. 任务控制（M2 扩展）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/jobs/{id}/pause` | 仅对 `queued/running` 有效 → `202`；状态变 `paused`，已下载分片保留 |
| POST | `/api/jobs/{id}/resume` | `paused` → 重新排队（续传） |
| POST | `/api/jobs/{id}/retry` | `failed/canceled` → 重新排队 |
| DELETE | `/api/jobs/{id}` | 只删记录；`queued/running` 时返回 `409` |

`Job` 增加字段：`targetId`（影片 id）、`bytesDone`、`bytesTotal`、`speedBps`；`status` 增加 `paused`。

### A4. 播放代理（M2 修正）

`GET /api/stream?u=<urlencoded 上游地址>&p=<platform>` → 媒体字节流
- 白名单域名：`pornhub.com / phncdn.com / xhcdn.com / xhamster.com / beeg.com ...`
- 服务端补 `Referer`（平台根地址）与 UA；透传 `Range` → `206`，支持拖动
- m3u8 会改写内部 URI 后返回 `application/vnd.apple.mpegurl`
- 该路由**不套 60s 整体超时**（长片要连续传输），靠请求上下文取消收尾

因此 `GET /api/videos/{id}/playback` 返回的 `variants[].url` 已经是 `/api/stream?...` 同源地址，
`proxyPlaybackUrl` 为首条变体的代理地址；每个变体多一个 `"hls": true|false` 字段，
前端据此选择 hls.js 或原生 `<video>`。

### A5. SSE 新增事件

| event | data |
|---|---|
| `job.updated` | 追加 `targetId` / `bytesDone` / `bytesTotal` / `speedBps`，`status` 可能是 `paused` |
| `download.done` | `{videoId, libraryId, path, sizeBytes}`（下载完成，前端刷新「已存」标记） |

### A6. 错误码补充

| code | HTTP | 含义 |
|---|---|---|
| `no_library` | 400 | 还没有可用本地库 |
| `already_queued` | 409 | 该影片已在下载队列 / 任务不可删除 |

## 附：管理页（统计 + 关注管理）

### A7. `GET /api/manage`

管理页（`/manage`）整页只打这一个请求：上面那排读数、下面的分项明细、关注列表都取自**同一份快照**。

```json
{
  "generatedAt": "2026-10-26T16:02:15Z",
  "stats": {
    "performers": 2, "performersEnabled": 2, "performersFailed": 0,
    "videos": 155, "videosNew": 134, "videosWithFile": 14,
    "files": 14, "bytes": 2971707187,
    "byPlatform": [{ "key": "pornhub", "n": 155, "bytes": 2971707187 }],
    "byLibrary":  [{ "id": 1, "name": "主库", "videos": 14, "bytes": 2971707187 }],
    "byDuration": [{ "key": "5to15", "n": 101, "bytes": 0 }],
    "byQuality":  [{ "key": "1080p", "n": 42, "bytes": 0 }],
    "byYear":     [{ "key": "2024", "n": 13, "bytes": 0 }],
    "ingest":     [{ "key": "2026-10", "n": 155, "bytes": 0 }],
    "tags":       [{ "key": "巨乳", "n": 44, "bytes": 0 }],
    "downloads":  { "done": 3, "failed": 0, "active": 0 },
    "recent": [{ "id": 201, "title": "...", "platform": "pornhub",
                 "thumbUrl": "/api/img?p=pornhub&u=...",
                 "publishedAt": "2020-10-27T12:25:45Z", "firstSeenAt": "2026-10-26T12:25:45Z" }]
  },
  "subscriptions": [ /* 同 GET /api/subscriptions 的条目，另加下面三个字段 */ ]
}
```

分档代号（前端负责翻成中文，改文案不用动后端）：

| 字段 | 代号 |
|---|---|
| `byDuration` | `lt5`（<5 分钟）/ `5to15` / `15to40` / `gt40` / `unknown`（未取得时长） |
| `byQuality` | `2160p` / `1440p` / `1080p` / `720p` / `480p` / `sd` / `none`（还没解析过清晰度）/ `unknown`——每部影片取 `video_streams` 里最高的 height |
| `byYear` | `YYYY`（`published_at` 的年份，最多 6 个，正序）/ `unknown`（没有发布时间） |
| `ingest` | `YYYY-MM`（`first_seen_at` 的月份，最多 12 个，正序） |
| `tags` | 标签原文，出现最多的 12 个 |

`subscriptions[]` 在 `Performer` 基础上追加：

| 字段 | 含义 |
|---|---|
| `localVideoCount` | 本地已收录的影片数（= 与该艺人关联的 `videos` 行数，不是平台的 `videoCount`） |
| `localFileCount` | 其中已落盘到任一本地库的影片数 |
| `localBytes` | 这些落盘文件占的体积 |

写操作沿用已有接口，不另开：`PATCH /api/subscriptions/{id}`（启用/停用）、
`POST /api/subscriptions/{id}/sync`（立即同步）、`DELETE /api/subscriptions/{id}`（取消关注）。

## 附：M5 新增接口（动态 / 照片墙）

全部挂在 `requireSession` 之后，与其它接口一样需要会话 Cookie。
错误码新增：`needs_auth`（409，上游要登录态）、`blocked`（429，上游返回风控拦截页而不是限流 JSON，
**不带** `resetAt`——等到点也不会好，提示换出口 IP / 重新导出 Cookie）、
`rate_limited`（429，`error.details.resetAt` 给出冷却结束时刻，
前端据此显示「HH:MM 之后自动恢复」）、
`stale_parser`（502，页面结构变了，需要更新解析）、`not_found`（404，账号不存在或已注销）。

### B1. `GET /api/moments`

照片流（照片墙），keyset 分页。

| 参数 | 说明 |
|---|---|
| `cursor` | 上一页返回的 `nextCursor`，首页不传 |
| `limit` | 默认 36，上限 60 |
| `accountId` | 只看某位博主 |
| `performerId` | 只看某个艺人名下的全部博主（艺人页「社交动态」用） |
| `platform` | `x` / `instagram` |

```json
{
  "items": [{
    "id": 12, "postId": "2113776971603689874", "platform": "x",
    "handle": "nasa", "displayName": "NASA", "avatarUrl": "https://pbs.twimg.com/…",
    "url": "https://pbs.twimg.com/media/…?name=orig",
    "thumbUrl": "/api/img?u=…&w=640",
    "width": 1200, "height": 800, "seq": 1,
    "caption": "…", "postUrl": "https://x.com/nasa/status/2113776971603689874",
    "postedAt": "2026-10-23T23:05:00Z",
    "saved": false, "performerId": 7
  }],
  "nextCursor": "MjAyNi0xMC0yM3wyMTEzNzc2OTcxNjAzNjg5ODc0fDE"
}
```

排序固定为 `posted_at DESC, post_id DESC, seq ASC`：同一条帖子的几张图永远相邻且按原始顺序，
灯箱「下一张」因此天然是「先把这个帖子看完再进下一条」。

### B2. `GET /api/moments/accounts`

```json
{
  "items": [{
    "id": 3, "platform": "x", "handle": "nasa", "displayName": "NASA",
    "avatarUrl": "…", "profileUrl": "https://x.com/nasa",
    "performerId": 0, "performerName": "",
    "enabled": true, "postCount": 20, "photoCount": 34,
    "backfillDone": false, "lastSyncedAt": "2026-10-27T18:00:00Z",
    "lastError": "", "createdAt": "2026-10-27T17:00:00Z"
  }],
  "platforms": [
    { "key": "x", "name": "X", "needsCookie": false, "hasCookie": true, "hint": "免登录可用，但一次只能取到最近 20 帖" },
    { "key": "instagram", "name": "Instagram", "needsCookie": true, "hasCookie": false, "hint": "必须先在设置 → 动态里粘贴 Cookie" }
  ],
  "unseen": 12
}
```

`unseen` 是「上次看动态之后新入库的照片数」，侧边栏红点用它。

### B3. `POST /api/moments/resolve`

`{ "url": "https://x.com/nasa" }` → 只探测、不落库（预览用）。
返回 `platform` / `platformName` / `handle` / `displayName` / `avatarUrl` / `postCount` /
`existingId`（已订阅过就是它的 id）/ `warning`（例如 X 免登录只能取 20 帖、IG 没配 Cookie）/
`performerId` + `performerName`（这个链接已经躺在某个艺人的社交链接里时给，用来在添加前
提示「会自动关联到艺人 X」）。

未配 Cookie 时探测 Instagram 直接 409 `needs_auth`，消息里带「去哪配」：

```json
{ "error": { "code": "needs_auth", "message": "Instagram 需要登录态：请在设置 → 动态里粘贴 sessionid" } }
```

前端拿到 `needs_auth` 会在同一处渲染「去设置 → 动态里粘贴 Instagram Cookie」的链接。

### B4. `POST /api/moments/accounts`

`{ "url": "…" }` → 认平台、抓一次资料、落库，并投递一次 `social_sync`（`mode=full`：从头回填）。
返回 201 + 账号对象。同一个账号重复提交不会新增行，只重投回填。

### B5. `PATCH /api/moments/accounts/{id}`

`{ "enabled": true }` / `{ "performerId": 7 }` / `{ "performerId": 0 }`（解除与艺人的关联）/
`{ "mode": "full" }`（清回填断点，下次从头再来）。

### B6. `DELETE /api/moments/accounts/{id}` → 204

级联删帖子与照片记录；**已经存进本地库的文件不删**。

### B7. `POST /api/moments/accounts/{id}/sync`

`{ "mode": "auto" | "full" }`，默认 `auto`（增量：遇到已入库的帖子就停）。
返回 `{ "queued": true }`；该账号已有在跑/排队中的同步时返回 `queued: false`。

### B8. `POST /api/moments/seen`

标记「动态已看到此刻」。照片墙打开时调一次。

### B9. `POST /api/moments/photos/save`

`{ "photoIds": [12, 13], "libraryId": 1 }`（`photoIds` 上限 200，省略 `libraryId` 用默认库）→
202 Accepted，入队 `social_save`。落盘路径：

```
{库根}/{平台显示名}/{博主名}/照片/{YYYY-MM-DD}_{post_id 后 8 位}/{序号两位}.jpg
```

同一个帖子目录里会写一份 `post.json`（正文、原帖地址、发布时间、图片清单），不写 `.nfo`——
动态是「看和存」，不进影片库的刮削口径。

### B10. 艺人详情里多出来的两段

`GET /api/performers/{id}` 在粉丝页/艺人页上用，M5 之后**只有该艺人名下挂着博主时**才多两个字段：

```json
{
  "socialAccounts": [ { "id": 3, "platform": "x", "handle": "nasa", "photoCount": 34, … } ],
  "socialPhotos":   [ { "id": 12, "thumbUrl": "…", "accountId": 3, "seq": 1, … } ]
}
```

`socialPhotos` 固定给最近 12 张（`hidden=0`），点任意一张跳到 `/moments?account={accountId}`。
没有关联时两个字段都不出现，老前端不受影响。

### B11. `GET /api/manage/moments`

管理页快照：`items`（与 B2 同构）+ `stats`{accounts, posts, photos, errors} + `hasInstagramCookie`。

### B12. 设置扩展

`GET/PUT /api/settings` 的 `social` 字段：

```json
{ "syncHours": 6, "hasInstagramCookie": true, "instagramCookie": "（只写，永不回显）" }
```

`syncHours` 夹在 1–48；`instagramCookie` 只写：不传 = 不动，传空串 = 清除。

`platformProxy` 里的 `instagram` / `x` 与影片平台那三项同一套机制：
**设置 > 环境变量**，平台专用 > 全局；每个请求发出去之前才取值，所以**保存即生效、不用重启**
（`internal/proxying`，`Transport.Proxy` 直接接到设置表上）。

`instagramCookie` 收三种写法（服务端统一归一化成 JSON 落库，加密保存）：

1. 浏览器扩展导出的 **Netscape cookies.txt**（`# Netscape HTTP Cookie File`，每行 7 个制表符字段，
   `#HttpOnly_` 前缀照收；非 `instagram.com` 的行直接丢掉）；
2. 整段 Cookie 头 `a=1; b=2`；
3. 归一化后的 JSON（内部存的形式，也认）。

只要里面没有 `sessionid` 就按「未配置」处理并返回 409 `needs_auth`。除三项关键 Cookie 外，
其余（`mid` / `ig_did` / `datr` 等）会原样带上——IG 的接口少了它们照样会 401 或回空列表。

### B13. SSE 新增事件

| 事件 | 载荷 | 用途 |
|---|---|---|
| `photo.new` | `{photoId, accountId, platform, handle}` | 照片墙增量刷新 + 侧边栏未读 +1 |
| `social.progress` | `{accountId, handle, page, newPhotos, backfilling}` | 抓取过程中的进度（一次同步可能连着好几页） |

### B14. 任务类型

| `type` | 载荷 | 并发 |
|---|---|---|
| `social_sync` | `{accountId, mode}`（`accountId=0` 表示全部启用的博主） | 1（限流按 IP 算，并行只会更快撞 429） |
| `social_save` | `{photoIds, libraryId}` | 跟下载并发数一致 |

定时器：每 10 分钟醒一次，距上次抓取超过 `social.syncHours`（默认 6）就投递一轮 `social_sync`。

### B15. 手动清除冷却

```
POST /api/moments/cooldown
{ "platform": "instagram" }        // 省略或空串 = 清所有平台
→ 200 { "cleared": ["instagram"] }
```

冷却时间是「上一次撞墙时按 429 猜的」，用户换了 Cookie / 换了出口 IP 之后那个时间已经没有意义。
这个接口只清本地计时器，不碰上游任何状态；界面上对应「换过 Cookie 了？清除冷却并重试」。
