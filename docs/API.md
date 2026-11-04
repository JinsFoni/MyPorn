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
`status`: `ok | no_proxy | error`（只是数据，前端不阻塞）。
`no_proxy` 只对「直连一般不通」的平台报（目前仅 `pornhub`）；xHamster / beeg 实测可直连，没配代理也是 `ok`。
发现页**不再**把 `no_proxy` 渲染成提示：它只是按「国内直连一般不通」的推断，实测没配平台代理也照样搜得到
（出口可能在系统代理/路由器上，应用无从判断），只有 `error` 才提示。

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
  hasLocalFile: boolean        // 是否已落盘到某个本地库
  localLibraryIds: number[]    // 落在哪几个库
  rotateDeg: number            // 播放时画面顺时针转多少度（0/90/180/270），见 E3
}
```

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/videos?performerId=&cursor=&limit=30&q=&hideDownloaded=1` | 影片流，播放时间倒序，**keyset 游标**分页 → `{"items":Video[],"nextCursor":"..."\|null}`。**固定排掉 `bilibili` 和 `youtube`**（`store.FeedExcludedPlatforms`，2026-11-03 补后者：两者各有自己的页面，投稿不是「追艺人的片」；YouTube 的 `videos` 行还只在下过载时才建，删掉下载后行按设计留着，于是 1/2/3/15/60 五条从关注流冒了回来）。`hideDownloaded=1` = 只出还没落到本地库的片（关注流用，判据是 `library_files` 里有没有这一行，删掉库文件后这条片会自己回来） |
| GET | `/api/videos/new?performerId=&since=` | 新片（`isNew`）列表，同结构，首页角标用；`performerId` 只数这一位 |
| GET | `/api/videos/{id}` | `Video` + `{"description":"...","related":Video[]}` |
| GET | `/api/videos/{id}/adjacent` | 同一份列表里的上下家 → `{"prev":Video\|null,"next":Video\|null}`（见 A4·补·2）。参数与 `/api/videos` 同口径（`performerId` / `platform` / `excludePlatform`（逗号分隔可排多个，关注流是 `bilibili,youtube`）/ `onlyNew` / `q` / `subscribedOnly` / `hideDownloaded`），**调用方要传跟原列表一样的值**，否则「下一个」会跳到原列表里根本没有的片上 |
| POST | `/api/videos/{id}/seen` | `204`，清除 isNew |
| POST | `/api/videos/{id}/rotate` | `{"deg":90}` → `{"ok":true,"rotateDeg":90}`，角度记在 `videos.rotate_deg`（见 E3） |

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
- **分轨档（B 站 DASH）的 `url` 不是 `/api/stream`，而是 `/api/videos/{id}/remux?v=<variantID>`**：
  音视频两条独立流浏览器播不了，服务端合成一份 mp4 再当普通文件播（见 §A4 与 ARCHITECTURE §13.3）。
  这种变体库里存了 `audio_url`；`url` 里出现的 `remux` 是前端判断「要等合流」的唯一依据
- 解析失败 `502 {"error":{"code":"resolve_failed","message":"..."}}`

**本地库里有文件时，第一路永远是它**（`id="local"`，`local:true`，`isDefault:true`）：

```json
{ "id":"local", "url":"/api/videos/12/file?libraryId=1", "local":true, "isDefault":true,
  "libraryId":1, "fileName":"xxx.mp4", "sizeBytes":360233190, "height":0, "codec":"", "hls":false }
```

- 线上那几路照旧排在后面（有 adapter 时才有）。下都下过了，没必要再连一次平台 CDN；
  而且 YouTube 这类**没有 adapter** 的平台，本地文件是唯一的播放方式——
  这时接口不会报 `unsupported_platform`，直接给本地这一路。
- 既没本地文件、平台又没 adapter 才 `400 unsupported_platform`（文案指到下载）。
- `GET /api/videos/{id}/file?libraryId=<id>` 直接下发文件，支持 `Range`（返回 `206`），
  播放器拖动进度条靠它。路径必须落在库根之内，否则 `403`；记录有、磁盘没了 → `404`。

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
| GET | `/api/settings` | `{syncIntervalHours, globalProxy, platformProxy:{pornhub,xhamster,beeg,bilibili},
  namingTemplate, downloadConcurrency, downloadRateKb, autoDownloadNew, libraryRoot,
  openlist:{baseUrl,root,hasToken}, bilibili:{hasCookie}}`（**不含任何密钥明文**） |
| PUT | `/api/settings` | 同结构，部分字段可选；密钥字段用 `{"openlist":{"token":"..."}}` 单独写，响应永不回显。
  B 站登录态写 `{"bilibili":{"cookie":"SESSDATA=…"}}`（裸的 SESSDATA 值也认），只回 `bilibili.hasCookie` |

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
- 平台没有 adapter（YouTube）时**自动落到 `yt_download`**：服务端用
  `videos.platform_video_id` 反查 `yt_videos`，所以详情页那个按钮对 YouTube 影片同样有效

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
`speedBps` 是**后端算的最近 3 秒平均**（`internal/speed`），不是各下载器自己报的数：开始下载后的头 1.5 秒
一律是 `0`（数据还不够，界面上就不显示速度，别拿半截窗口的数字骗人）；`0` 也可能表示刚好卡住。

### A4. 播放代理（M2 修正）

`GET /api/stream?u=<urlencoded 上游地址>&p=<platform>` → 媒体字节流
- 白名单域名：`pornhub.com / phncdn.com / xhcdn.com / xhamster.com / beeg.com / bilivideo.com / bilivideo.cn ...`
  （B 站的媒体 CDN 不带 `Referer: https://www.bilibili.com/` 一律 403）
- 服务端补 `Referer`（平台根地址）与 UA；透传 `Range` → `206`，支持拖动
- m3u8 会改写内部 URI 后返回 `application/vnd.apple.mpegurl`
- 该路由**不套 60s 整体超时**（长片要连续传输），靠请求上下文取消收尾

因此 `GET /api/videos/{id}/playback` 返回的 `variants[].url` 已经是 `/api/stream?...` 同源地址，
`proxyPlaybackUrl` 为首条变体的代理地址；每个变体多一个 `"hls": true|false` 字段，
前端据此选择 hls.js 或原生 `<video>`。

### A4·补. 分轨合流 `GET /api/videos/{id}/remux?v=<variantID>`

给 B 站那种「音视频分开」的档位用：服务端用 ffmpeg `-c copy` 合成 mp4，落盘缓存后按普通文件提供。

- 首次请求会等合流跑完，**等多久完全取决于档位**（下面都是本机实测）：

  | 档位 | 成品体积 | 冷启动耗时 |
  |---|---|---|
  | 480P / 25 分钟 | 68 MB | 约 15 秒 |
  | 1080P | 88 MB | 十几秒 |
  | 4K（AV1） | 554 MB | 49 秒（走官方 CDN）／7 分 31 秒（走 PCDN） |
  | 4K（HEVC） | 657 MB | 约 75 秒 |
  | 4K（H.264） | 1.77 GB | 约 3 分钟 |

- 缓存 key = `<videoID>-<variantID>`，目录 `DATA_DIR/mux/`，TTL 7 天；
- 同一个 key 并发（浏览器起播会打好几个 Range 请求）只跑一次 ffmpeg；
- 上游签名地址过期时会在合流前重新解析一次；调用方断开不会打断已经开始的合流；
- 响应带 `Range`/`206`、`Cache-Control: private, max-age=3600`；
- 找不到该变体 / 它没有音轨 → `404 not_found`；合流失败 → `502 mux_failed`；
  超过 10 分钟没合完 → `504 mux_timeout`（4K 慢到这一步基本都是 CDN 卡住了）；
- 失败后会顺手清掉这部片子的画质缓存，下一次 `/playback` 重新解析、通常拿到另一条线路。

#### A4·补·1 进度：`GET /api/videos/{id}/remux?v=<variantID>&status=1`

4K 一档要等 49 秒到几分钟，而上面那条路「合流没完就一个字节都不回」——前端只能干等，
用户看到的就是「点了 4K 没反应」。所以另开一条**立刻返回**的进度接口：

```json
{"state": "merging", "percent": 62.5, "elapsedSec": 48}
{"state": "ready",   "percent": 100,  "elapsedSec": 0}
{"state": "failed",  "percent": 0,    "elapsedSec": 12, "error": "...合流超时..."}
```

- 第一次问就顺手把合流**在后台启动**，不会阻塞；重复问只是取快照，不会重开 ffmpeg；
- `state` 取 `idle` / `merging` / `ready` / `failed`；`percent` 只在 `merging` 时有意义（0~99，
  100 只表示「盘上已经有成品」）；
- 失败记录保留 5 分钟，前端刷新一眼就能看到上次为什么没好，而不是无限「正在准备」；
- 前端拿到 `ready` 之后才把 `/api/videos/{id}/remux?v=…` 交给 `<video>`，那时是一个已经成型的
  文件，Range 秒开、能拖动。

### A4·补·2. 播放器「上一个/下一个」`GET /api/videos/{id}/adjacent`

播放器控制条那一对切歌按钮问的就是这个。查询参数和 `GET /api/videos` **同一套**（`performerId` /
`platform` / `excludePlatform`（逗号分隔可排多个平台）/ `onlyNew=1` / `q` / `subscribedOnly=1`），返回的是「在这份列表里，
按列表顺序排在本片前后紧挨着的那两条」：

```json
{ "prev": { "id": 1025, "title": "...", ... }, "next": { "id": 981, "title": "...", ... } }
```

- `prev` = 列表里更靠上（更新）的一条，`next` = 更靠下（更旧）的一条；到头的那一侧是 `null`，**不是 404**。
  影片本身不存在才 `404 not_found`。
- 排序键完全照抄列表：`COALESCE(published_at,'') DESC, id DESC`，所以同一时间戳的重复条目也不会漏/重
  （`(ts < ? OR (ts = ? AND id < ?))` 这种 keyset 条件，和翻页游标同一个写法）。
- `Video` 结构和列表项一致（带 `performers`、本地档标记）；共用 `store.feedFilter` / `store.scanVideo`，
  列表加过滤条件时这里不会漂。
- 前端按卡片链接上的 `?back=` 决定口径（关注流＝`excludePlatform=bilibili,youtube`、关注页按艺人筛＝
  `performerId` + `excludePlatform=bilibili,youtube`、艺人页＝`performerId`、
  B 站墙＝`platform=bilibili` + `subscribedOnly=1`），切过去时把 `?back=` 原样带下去。
- **当前这条自己不在那份列表里时，两边都是 `null`**（`store.videoInFeed` 先问一句再找邻居，2026-11-03 补）。
  典型场景：YouTube 的片子被关注流排掉，用户从 `/youtube` 或历史记录点进来，按时间戳算出来的「下一个」
  会直接落进关注流第一条（实测 `/video/2913?back=/` 的 `next` 是 1025）——那不是他正在看的列表。
  按钮置灰比跳到别的列表上诚实。

### A5. SSE 新增事件

| event | data |
|---|---|
| `job.updated` | 追加 `targetId` / `bytesDone` / `bytesTotal` / `speedBps`，`status` 可能是 `paused` |
| `download.done` | `{videoId, libraryId, path, sizeBytes}`（下载完成，前端刷新「已存」标记） |

`job.updated` 的 `type` 可能是 **`download`（本地库下载）或 `yt_download`（YouTube 存进本地库）**——
两者在「下载」页看到的是同一件事（`GET /api/downloads` 也是两种都返回），前端别只认前者
（2026-11-02 前端就是这么写的，YouTube 那条的进度条从 1% 直接冻到完成，见 `web/src/lib/live.svelte.ts`
里的 `DOWNLOAD_TYPES`）。另外这条连接**在标签页隐藏时是主动关掉的**（`lib/sse.ts` 的 `onVisibility`），
隐藏期间的进度不会补发，所以前端在重连时要自己拉一次 `/api/downloads` 对齐。

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
    "id": 12, "platform": "x",
    "url": "/api/img?p=x&u=…", "thumbUrl": "/api/img?p=x&u=…",
    "width": 1200, "height": 800, "seq": 1,
    "postedAt": "2026-10-23T23:05:00Z",
    "saved": false,
    "accountId": 3, "handle": "kiho_kanematsu", "displayName": "金松季歩(kanematsu kiho)",
    "postUrl": "https://x.com/kiho_kanematsu/status/2113776971603689874",
    "caption": "…"
  }],
  "nextCursor": "MjAyNi0xMS0wM1QwODo0MToyNFp8MjExNzUzNzYxNTI2ODQyMjEwNnwx"
}
```

排序固定为 `posted_at DESC, post_id DESC, seq ASC`：同一条帖子的几张图永远相邻且按原始顺序，
灯箱「下一张」因此天然是「先把这个帖子看完再进下一条」。

`displayName` 是博主自己写的名字（空串＝没给）。照片格子的悬停浮层、灯箱标题行都显示它，
空着才回退 `@handle`——2026-11-04 用户要求「悬浮时左下角显示名字，不要显示 X 的 ID」，
和头像条同一口径；想知道 ID 看 `title` / `handle` 字段。（这一版之前 `displayName` 只在
这份文档的示例里出现过，DTO 里其实没有，是 2026-11-04 补上的。）

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
    { "key": "x", "name": "X", "needsCookie": false, "hasCookie": true, "hint": "免登录可用：新帖从主页发现，历史每次 20 帖（越同步越全）" },
    { "key": "instagram", "name": "Instagram", "needsCookie": false, "hasCookie": false, "hint": "免登录可用：资料 + 最新 100 条（含原图）；想翻更早的历史再贴 Cookie" }
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

### B9. `POST /api/moments/photos/delete`

`{ "photoIds": [12, 13] }`（上限 500）→ 200 `{ "deleted": 2 }`。

照片墙的「多选 → 删除」。2026-11-04 之前的同名接口是**保存**（`/photos/save`，带 `libraryId`）：
照片改成抓取后**自动**下载入库之后，「存档」不再需要人动手，多选只剩清理这一个用途。

- 服务端批量**软删**（同 B9b），顺手重算涉及博主的 `photoCount`；
- 未知 id 直接跳过（不报错、也不计数），所以「墙已经被别的标签页删过了」不会让整批失败；
- 已经下载到图片库的文件**不动**。

### B9b. `DELETE /api/moments/photos/{id}` → 204

把一张照片从照片墙上删掉（照片格子右下角那个垃圾桶、灯箱里的「删除这张」）。
服务端是**软删**：行留着、`hidden = 1`。

- 为什么留行：`social_photos` 上有 `UNIQUE (platform, url)`，写入是 `INSERT OR IGNORE`。
  行真删掉，下一轮同步（默认 6 小时，手动同步也算）会把这同一张图原样插回来，
  用户看到的就是「删了又回来」。墓碑撞唯一键就挡住了。
- 已经存进本地库的文件**不动**（那是媒体库里的资产）。
- 副作用：顺手重算该博主的 `photoCount` / `postCount`（头像条上的「N 张」）。
- 幂等：已经删过的 id 再删一次仍是 204（前端连点两下不会弹错），只有 id 根本不存在才 404 `not_found`。

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
| `photo.saved` | `{ids, count}` | 一批照片已下载进图片库（自动落盘，§9）。**前端目前不消费**：照片墙不画任何「已存/待存」角标（2026-11-04 用户要求），留这条事件是给外部订阅方/以后的提示用 |

### B14. 任务类型

| `type` | 载荷 | 并发 |
|---|---|---|
| `social_sync` | `{accountId, mode}`（`accountId=0` 表示全部启用的博主） | 1（限流按 IP 算，并行只会更快撞 429） |
| `social_save` | `{photoIds?, accountId?}`：`photoIds` = 刚抓到的这几张；`accountId > 0` = 该博主存量补漏、`-1` = 所有博主（用来把「还没落盘的」一次扫干净） | 跟下载并发数一致 |

照片是**自动**下载入库的（落在设置里的 `photo_root/{名字}/{年}/{月}/`），前端不再有「保存」这个动作，
所以 `social_save` 的载荷里没有 `libraryId`——写入时 `social_photo_files.library_id` 固定是 `0`
（`0` = 设置里的图片库根目录，该列自 `0014_photo_root.sql` 起不再外键到 `libraries`）。

定时器：每 10 分钟醒一次，距上次抓取超过 `social.syncHours`（默认 6）就投递一轮 `social_sync`。

### B15. 手动清除冷却

```
POST /api/moments/cooldown
{ "platform": "instagram" }        // 省略或空串 = 清所有平台
→ 200 { "cleared": ["instagram"] }
```

冷却时间是「上一次撞墙时按 429 猜的」，用户换了 Cookie / 换了出口 IP 之后那个时间已经没有意义。
这个接口只清本地计时器，不碰上游任何状态；界面上对应「换过 Cookie 了？清除冷却并重试」。

## 附：M6 新增接口（YouTube 订阅）

约定同前：都在鉴权保护组内，错误体 `{"error":{"code","message"}}`，时间一律 RFC3339（无值给 `null`）。
所有图片字段都是 `/api/img?p=youtube&u=…` 形式的代理地址，前端不用关心防盗链和代理出口。

### C1. `GET /api/yt/channels`

```json
{
  "items": [
    {
      "id": 1, "channelId": "UCq-Fj5jknLsUf-MWSy4_brA", "handle": "",
      "title": "T-Series", "avatarUrl": "/api/img?…", "profileUrl": "https://www.youtube.com/channel/UC…",
      "enabled": true, "videoCount": 203, "unseenCount": 201,
      "backfilledAt": "2026-10-27T20:30:33Z", "lastSyncedAt": "2026-10-27T20:33:02Z", "lastError": ""
    }
  ],
  "stats": { "channels": 2, "videos": 326, "unseen": 322, "new24h": 322, "errors": 0 }
}
```

`stats.errors` = `lastError` 非空的频道数；`new24h` 按 `created_at` 算（不是发布时间——深度回填不给时间）。

### C2. `POST /api/yt/resolve`

```json
{ "url": "https://www.youtube.com/@lofigirl" }
→ 200 {
  "channelId": "UCSJ4gkVC6NrvII8umztf0Ow", "handle": "lofigirl", "title": "Lofi Girl",
  "avatarUrl": "/api/img?…", "profileUrl": "https://www.youtube.com/channel/UC…",
  "subscribed": false, "ytdlpOk": true
}
```

只解析不落库（给「添加前预览」）。收的形态：`UC…` 频道 id、`@handle`、裸 handle、
`/channel/UC…`、`/@h`、`/c/…`、`/user/…`、`watch?v=…`、`youtu.be/…`。
视频链接会先读它的 `channelId`，再抓一次频道页——否则拿到的标题是那条视频的。

`ytdlpOk=false` 表示只能抓 RSS（最近 15 条、没有时长），也下载不了。

### C3. `POST /api/yt/channels`

同一份入参。语义是「订阅」：

```json
{ "url": "@lofigirl" } → 200 { "channel": { …C1 的单项… }, "created": true }
```

`created=true` 时服务端会立刻投一次 `yt_sync`（`mode=deep`，默认回填 200 条）；
已存在时只刷新标题/头像，不动 `enabled` 与 `performer_id`（用户的开关不能被一次重复添加推翻）。

### C4. `PATCH /api/yt/channels/{id}` / C5. `DELETE /api/yt/channels/{id}`

`{"enabled": false}` → 200 单项；删除返回 204。
删除只清 `yt_channels`（`yt_videos` 随外键级联），**已下载进本地库的文件和影片记录不动**。

### C6. `POST /api/yt/channels/{id}/sync`

```json
{ "deep": false } → 200 { "jobId": 12 }
```

同一频道已有排队/在跑的任务时 `jobId=0`（不重复投）。`deep=true` 走 yt-dlp 列历史，慢（200 条约 6 秒）但有时长；
`deep=false` 走 RSS，1 次请求拿最近 15 条，带准确发布时间。

### C7. `POST /api/yt/channels/{id}/download-unseen`

```json
{ "libraryId": 1, "limit": 10 } → 200 { "queued": 10, "jobIds": [21,22,…] }
```

把该频道所有 `seen=0` 的视频逐条排进 `yt_download`。已排过的会自动跳过（同影片有在跑的任务）。

### C8. `GET /api/yt/videos`

查询参数：`channelId`（频道行 id）、`unseen=1`、`saved=1`、`cursor`、`limit`（默认 36，上限 100）。

```json
{
  "items": [{
    "id": 200, "videoId": "BRHJ7xD-bSU", "title": "WAITING 4 YOU (Official Video)",
    "url": "https://www.youtube.com/watch?v=BRHJ7xD-bSU",
    "thumbUrl": "/api/img?…hq720&f=…mqdefault…", "posterUrl": "/api/img?…maxresdefault&f=…hq720…",
    "embedUrl": "https://www.youtube-nocookie.com/embed/BRHJ7xD-bSU",
    "durationSec": 191, "views": 37000, "publishedAt": null, "dateKnown": false,
    "isLive": false, "seen": false, "saved": false, "downloaded": false,
    "description": "",
    "channel": { "id": 1, "channelId": "UC…", "handle": "", "title": "T-Series", "avatarUrl": "/api/img?…" }
  }],
  "nextCursor": "MTc2…"
}
```

排序 `COALESCE(published_at,'') DESC, id DESC`：**没有发布时间的排最后**（深度回填的条目就落在这里），
所以首屏一定是最新的。`dateKnown=false` 时 `publishedAt` 为 `null`，前端显示「时间未知」。

`thumbUrl` / `posterUrl` 里的 `f=` 是**代理层的兜底地址**：`i.ytimg` 的 `hq720` / `maxresdefault` 在老视频上会 404，
而 `/api/img` 抓不到时只发 1×1 空白（HTTP 200，前端的 `onerror` 发现不了），所以主地址抓不到就再抓 `f=`。

`downloaded` 表示这个库里已经有它的文件（`library_files` 存在），前端据此把按钮改成「再下一次」。

### C9. `POST /api/yt/videos/seen`

```json
{ "ids": [12, 13] }        // 或 { "channelId": 1 } 表示整个频道
→ 200 { "unseen": 320 }
```

返回全库剩余未看数，前端直接拿来更新侧边栏角标。

### C10. `POST /api/yt/videos/{id}/save` → `{ "saved": true }`

### C11. `POST /api/yt/videos/{id}/refresh`

按需补全单条信息（发布时间 / 时长 / 观看数），返回刷新后的单项（同 C8）。
没有 yt-dlp 时 409 `no_ytdlp`。前端在「点开一条 `dateKnown=false` 的视频」时调它，**不批量拉**：
单条 4 秒，为了一张卡片的时间去刷 200 条不值得。

### C12. `POST /api/yt/videos/{id}/download`

```json
{ "libraryId": 1, "overwrite": false } → 200 { "jobId": 21, "libraryId": 1 }
```

没有本地库时 409 `no_library`。`libraryId` 省略 = 默认库。
同一条视频已在队列里时 `jobId=0`。

### C13. `GET /api/yt/health`

```json
{ "path": "/opt/ytvenv/bin/yt-dlp", "found": true, "version": "2026.10.26",
  "ageDays": 1, "stale": false, "ffmpegOk": true, "error": "",
  "missingDates": 27, "missingDurations": 3, "missingStuck": 1 }
```

配置的路径用不了时（文件被删 / 是目录 / 没有可执行位 / `--version` 跑不起来）：

```json
{ "path": "/tmp/ytvenv/bin/yt-dlp", "found": false, "version": "",
  "ageDays": 0, "stale": false, "ffmpegOk": false,
  "error": "这个路径上没有可执行的 yt-dlp：/tmp/ytvenv/bin/yt-dlp",
  "missingDates": 27, "missingDurations": 0, "missingStuck": 0 }
```

`found` 的语义是「**能跑通 `--version`**」，不是「设置里填了路径」或「文件存在」。
2026-11-02 就是这条不一致坑的：`/tmp/ytvenv/bin/yt-dlp` 被 macOS 定时 tmp 清理删掉，
`found` 仍报 true、`version` 为空，界面显示「版本新鲜」，而点开任何一条视频都是 502
`fork/exec … no such file or directory`。现在 `path` 照旧回显用户填过的那一条，
`error` 说明为什么用不了，界面两处（管理 → YouTube、设置 → YouTube）都直接显示它。

`missingDates` / `missingDurations` = 还没有发布时间 / 还没有时长的视频条数（墙上据此决定显不显示「有 N 条没有时长 · 补齐」那一条）。两样分开数是因为两条抓取通道各缺一半：RSS（增量同步）不给时长，yt-dlp 的 flat 列表（深度回填）不给时间戳。直播不算「缺时长」。

`missingStuck` = **补过 3 次仍然缺**的条数（`yt_videos.meta_tries >= 3`），它们**不计入**上面两个数。
不加这个区分的话，那几条结构上补不到的行（短视频：频道 `/videos` 列表里没有、观看页又要登录）
会让墙上的提示条永远挂着——2026-11-04 用户问的就是这个。它们只在设置 → YouTube 里说明，
那里给一个「再试一次」（`force`，会把记账清零重排）。

`stale` 的判据是版本日期超过 90 天。这个接口给的是「为什么下载会失败」的答案：
实测 2026.07.04 版全线 403，2026.10.26 正常——所以版本号必须能在界面上看到。

### C13b. `POST /api/yt/fill-meta`

```json
{} → 200 { "jobId": 85, "total": 54, "missingDates": 0, "missingDurations": 54 }
```

排一个 `yt_meta` 任务补**发布时间 / 时长 / 播放量**。任务内部两遍：
① 按频道拉一次 yt-dlp 的 flat 列表批量补时长（实测 7 个频道各 1.0~2.4 秒、60/60 条都带 `duration`，
**不依赖登录态**）；② flat 里没有的那些（短视频、超列表深度的老条目、缺发布时间的行）才逐条看观看页
（一次普通 GET、1.29MB、1.5 秒）。
`total` 是「要补的行数」（缺时间或缺时长，一行只算一次），`missingDates` / `missingDurations` 分开给两个口径。
一条不缺时返回 `{ "jobId": 0, "total": 0, … }`，同类型任务已在队列里时 `jobId=0`。
点这个按钮等于 `force`：先清掉「补过几次」的记账，之前判「补不到」的行也重试一遍。
跑完 SSE 会推 `job.updated`（`type=yt_meta, status=done`），墙上重拉并重新数那两个数。

**补不到就记账**（2026-11-04 加）：每失败一次 `yt_videos.meta_tries + 1`，到 3 次就不再算「缺」，
自动补（同步完顺手跑的那次）也不再拿它去撞；换了一份 Cookie（`yt_cookies` 存回时）会全部清零。
文案也分清了：「N 条这次没拿到（可能只是这次请求失败）」
vs「剩下 N 条拿不到：YouTube 要求登录：观看页只剩登录墙」——后者不是「视频没了」。

### C14. `GET /api/manage/youtube`

```json
{ "stats": { …C1 的 stats… }, "channels": [ …C1 的 items… ] }
```

### C15. 设置扩展

```json
{
  "youtube": { "syncHours": 6, "backfillLimit": 200, "format": "", "ytdlpPath": "", "hasCookies": false,
               "cookiesBroken": false, "cookieLogins": 0, "cookieAlive": false },
  "platformProxy": { "…": "", "youtube": "" }
}
```

- `syncHours` 夹在 1–48；调度器每 10 分钟醒一次，够点就投一轮 `yt_sync(auto)`。
- `backfillLimit` 夹在 10–2000，是 `deep` 模式一次列多少条历史。
- `format` 是 yt-dlp `-f` 的覆盖值，留空 = 默认 `bv*[vcodec^=av01]+ba/bv*+ba/b`
  （**AV1 最高画质 + 最高音质**，再用 ffmpeg `-c copy` 合成 mp4，不转码）。
- `ytdlpPath` 留空 = 从 `PATH` 找；填绝对路径可以固定用一个独立安装的版本。
- `cookies` 只写：不传 = 不动，传空串 = 清除；`hasCookies` 只读。
- `cookieLogins` / `cookieAlive` 只读：库里那份 cookies.txt 还剩几条 YouTube 登录态 cookie
  （`SID` / `LOGIN_INFO` / `__Secure-*PSID` 等 8 个名字、未过期的），以及 `SID` + `LOGIN_INFO`
  是否都还在。`cookieAlive: false` = 这份 cookie 已经登录不上了，界面据此提示重导。
  这两个数会随 yt-dlp 每轮的回写而变（见 docs/M6-YOUTUBE.md §14.23）。
- `platformProxy.youtube`：抓 RSS 和 yt-dlp 下载都走这个出口，保存即生效（不用重启）。

### C16. SSE 新增事件

| 事件 | 载荷 | 用途 |
|---|---|---|
| `yt.new` | `{channelId, title, count}` | 视频墙增量刷新 + 侧边栏未读 +N |

### C17. 任务类型

| `type` | 载荷 | 并发 |
|---|---|---|
| `yt_sync` | `{channelId, mode}`（`channelId=0` 表示全部启用的频道；`mode`=`auto`/`deep`） | 1 |
| `yt_download` | `{ytVideoId, videoId, libraryId, overwrite}` | 跟 `download_concurrency` 一致 |

`yt_download` 的 `target_id` 是 **`videos` 表的行 id**（不是 `yt_videos`），这样它和普通 `download`
共用同一个下载页。`GET /api/downloads` 现在同时返回这两种类型。

错误码新增：`no_library`（没建库）、`no_ytdlp`（没有 yt-dlp / 版本不可用）。
抓取侧的语义沿用 M5：`needs_auth` / `rate_limited` / `blocked` / `stale_parser` / `not_found` / `upstream`。

### C18. 本地播放（M6 顺带补上的一条线）

```
GET /api/videos/{id}/file?libraryId=1
→ 200 / 206（Range）   Content-Type: video/mp4   Accept-Ranges: bytes
→ 403 forbidden        路径不在库目录内
→ 404 not_found        库里没记录 / 文件已不在磁盘上
```

`{id}` 是 `videos.id`（不是 `yt_videos.id`）。这条接口不经过 `/api/stream`（那是给平台 CDN
补 Referer 用的），也不需要登录态之外的任何东西；它把 `http.ServeFile` 直接接到库文件上，
所以 `If-Range` / `Last-Modified` / 206 分段都是标准行为。

它同时被算作「长连接」接口（`isLongLived`）：一部 8K 看两小时也一直连着，
套上 60 秒超时中间件会在播放中途被掐断。

### C19. YouTube 站内在线播放（服务端 HLS 代理）

三个口子凑成一次播放。前端只认第一个和第三个数出来的地址，**永远不会拿到 googlevideo 的签名链接**。

```
GET /api/yt/videos/{id}/stream
→ 200 {
    "videoId": "s8OtOH_Vfa0",
    "title": "Ek Onkar (Full Video)…",
    "durationSec": 184,
    "src": "/api/yt/videos/477/stream.m3u8",
    "default": 2160,
    "qualities": [
      { "height": 2160, "label": "2160p", "bandwidth": 18679803, "codecs": "vp09.00.50.08" },
      { "height": 1080, "label": "1080p", "bandwidth": 2399383,  "codecs": "avc1.640028" },
      …
    ],
    "expiresAt": "2026-10-28T03:56:04Z"
  }
→ 409 no_ytdlp        没有 yt-dlp（站内播放靠它解析清单）
→ 502 upstream        yt-dlp 解析失败 / 这条没有 HLS 清单
→ 404 not_found       库里没有这条视频
```

解析一次 4~5 秒（跑一次 `yt-dlp -j`），结果在服务端缓存 **30 分钟**，或被上游签名寿命
（`expire=`，实测约 6 小时）截断 —— 切画质、重开播放页都不会重新解析。

```
GET /api/yt/videos/{id}/stream.m3u8[?q=高度]
→ 200 application/vnd.apple.mpegurl   Cache-Control: private, max-age=10
```

返回的是**改写过的单变体 master**：只留一档画质（默认最高），把音频 rendition
（`#EXT-X-MEDIA:TYPE=AUDIO`）一起搬进来，所有 URI 换成下面的会话地址。
`?q=720` 这种手动降档也走这里——每次请求重新拉一次上游 master（20KB，几十毫秒）。

```
GET /api/yt/seg/{sid}/{n}
→ 200 application/vnd.apple.mpegurl    这里放的是子清单（还会再改写一层）
→ 200 video/mp4 | audio/mp4            真正的分片字节（Range 透传）
→ 410 session_expired                  会话没了/过期了：前端重新解析一次即可
```

`{sid}` 是后端随机生成的会话号（16 字节 hex），`{n}` 是会话内自增序号。会话里存的是
「序号 → 上游签名地址」，所以浏览器看到的分片地址不带签名、也不带 googlevideo 域名。
同一个上游地址登记两次拿到同一个序号（清单被重拉时，播放器手里的旧地址还能用）。

会话寿命跟着上游的 `expire` 走，另加 10 分钟宽限（播放器可能攥着上一版清单）；
另有上限 64 个会话、满了按 FIFO 淘汰。`handleYtStreamSeg` 被算作长连接接口。

```
GET /api/yt/videos/{id}/dash.m3u8[?q=高度]
→ 200 application/vnd.apple.mpegurl   「单文件 HLS」的 master（只有一档画质 + 音轨）
→ 302 /api/yt/videos/{id}/stream.m3u8?q=  建不起来时（无 AV1 直链 / 不是分片 mp4 / sidx 读不出）
```

4K（2160p）不走 VP9 的 HLS：HLS 里这一档只有 VP9 `vp09.00.51.08` 28.2 Mbps，而 DASH 的 AV1
（itag 401、`av01.0.13M.08`）只要 13.46 Mbps。AV1 那两条轨**本身就是分片 mp4**（`ftyp`+`moov`+`sidx`
+一串 `moof`+`mdat`），`sidx` 里逐段写着偏移/长度/时长，所以服务端直接把它当 HLS 发：
`#EXT-X-MAP` 指头、57 条 `#EXT-X-BYTERANGE` 指段。起播只要第一段（8.84 MB），不是整份 540 MB。
`/stream` 返回的画质列表里这一档带 `"dash": true`，`bandwidth`/`codecs` 报的是 AV1 那条。

```
GET /api/yt/dash/{sid}/{v|a}.m3u8        → 200 application/vnd.apple.mpegurl（子清单，全是 BYTERANGE）
GET /api/yt/dash/{sid}/{v|a}-init.mp4    → 200 video/mp4（ftyp+moov）
GET /api/yt/dash/{sid}/{v|a}.mp4?i=<n>   → 206 video/mp4（Range：偏移是**整份文件**的偏移）
→ 410 session_expired                    会话没了/过期了：重新解析一次即可
```

`{sid}` 是随机会话号（16 字节 hex）。会话里存的是「上游签名地址 + sidx 索引」，所以浏览器
看到的分片地址不带签名、也不带 googlevideo 域名。分片请求进来时服务端按偏移切 3 块并发向上游取
（`Range` 透传），边到边发；每发完一段，后台再把后面 4 段合成一次大请求预取进内存。
会话寿命跟着上游 `expire`，上限 64 个、满了 FIFO 淘汰。细节与实测数字见
`docs/M6-YOUTUBE.md` §14.11。

**为什么不用 iframe**：一批版权音乐视频在 `youtube-nocookie.com/embed/…` 里直接显示
「此视频不能观看」（用一张裸 HTML 页面复现过，跟本应用无关），而服务端代理出来能正常播。
详情见 `docs/M6-YOUTUBE.md` §14。

### C20. `GET /api/yt/videos/lookup?videoId=<视频 id>`

```
→ 200 单条视频（结构与 C8 的 items[] 完全一致）
→ 400 bad_request    缺 videoId
→ 404 not_found      库里没有这条（可能没订阅它的频道）
```

站内播放页 `/youtube/<videoId>` 用（2026-11-02 起播放是独立页，之前是墙上的弹窗 `?v=`）：
刷新、从别处贴链接进来时，墙上那一页（36 条 + 可能带频道筛选）未必包含这条，所以需要单取。
`{videoId}` 是 YouTube 的字符串 id，不是 `yt_videos.id`。

## 附：M7 补 —— B 站独立页面（`/bilibili` + 管理 → B 站）

B 站没有自己的表：UP 主是 `performers` 的一行（`platform='bilibili'`，`performer_id` = mid），
投稿是 `videos` 的一行。所以这一节只有「按平台取数」的读接口，播放/下载/合流仍然走
[A4·补](#a4补-分轨合流-get-apivideosidremuxvvariantid) 与 §3 那套。

### D1. `GET /api/bilibili/channels`

```
→ 200 {
    "items": [{
      "id": 10,                  // performers.id（不是 mid）；下载未看等接口用它
      "mid": "946974",
      "name": "影视飓风",
      "avatarUrl": "/api/img?p=bilibili&u=…",   // 已代理
      "url": "https://www.bilibili.com/space/946974",
      "subscriberCount": 18839524,
      "videoCount": 19,          // 本地已收录，不是 UP 主的总投稿数
      "unseenCount": 18,         // 其中 is_new = 1
      "downloaded": 1,           // 已落盘到任一本地库的条数
      "subscriptionId": 2,       // /api/subscriptions/{id} 那套接口用这个
      "enabled": true,
      "backfillDone": false,
      "backfillPage": 1,         // 已经翻到第几页（每页 20 条）
      "totalPages": 47,
      "lastSyncAt": "2026-10-28T15:27:09Z",
      "nextSyncAt": "2026-10-28T18:46:48Z",     // 被限流时会被推到 3 小时后
      "lastError": "抓取第 2 页失败: 上游限流：B 站限流了，等一会儿再同步"
    }],
    "stats": { "unseen": 18 }    // 侧边栏徽标用
  }
```

只列「还订阅着」的 UP 主（取消订阅是软删，删掉就不出现）。

### D2. `GET /api/bilibili/videos?cursor=&channelId=&unseen=1&limit=36`

```
→ 200 { "items": [<与 §3 影片结构一致，含 performers[].avatarUrl>], "nextCursor": "…" | null }
```

`platform='bilibili'` 的影片，按发布时间倒序做 keyset 分页；`channelId` 是 **D1 的 `id`**（performers.id）。

和 D1 同一把尺子：**只出「所属 UP 主还在订阅里」的投稿**。取消订阅是软删 `subscriptions.deleted_at`，
投稿行不会跟着删（重新订阅要能原样回来），所以墙上、未看角标（D1 的 `stats.unseen`）、
管理页读数、「下载未看」片单都用同一条 `liveSubFilter` 过滤（`internal/store/videos.go`）。
「停用」（`enabled=false`）不影响可见性，只影响要不要再同步。

### D3. `POST /api/bilibili/seen`

```
请求：{ "ids": [61], "channelId": 10 }     // 两者都可省
→ 200 { "unseen": 0 }
```

`ids` 只标这几条，`channelId` > 0 只标这位 UP 主的，都不给 = 全站 B 站投稿。
返回标完之后剩下的未看数（前端直接拿去更新徽标）。
注意：点开详情页时本来就有一条 `POST /api/videos/{id}/seen`（§3），两者效果一致。

### D4. `POST /api/bilibili/channels/{id}/download-unseen`

```
请求：{ "limit": 0 }        // 0 = 默认 50，上限 200
→ 200 { "queued": 12, "libraryId": 1 }
→ 400 no_library          还没有可用的本地库
```

把这位 UP 主未看的投稿批量排进 `download` 队列（落到默认库）。{id} 是 D1 的 `id`。

### D5. `GET /api/bilibili/health`

```
→ 200 {
    "hasCookie": true,    // 库里存了 Cookie（**不等于能用**，见 loggedIn）
    "cookieBroken": false,// 存着但用当前 APP_ENCRYPTION_KEY 解不开（密钥换过）——重填前先修密钥
    "loggedIn": true,     // 真拿这条 Cookie 打了一次 nav：能登录上吗
    "uname": "月白喵丶",    // nav 回的昵称（未登录是空串）
    "lastLoginOk": "2026-10-30T10:32:34Z", // 上一次 nav 明确确认「已登录」的时间（从没确认过就没有这个字段）
    "navError": "…",      // 仅在 nav 请求本身失败时出现（网络/风控）：有它说明「没问出来」，不代表 Cookie 失效
    "cookieSetAt": "2026-10-29T07:49:13Z", // 库里这条 Cookie 的写入时间
    "canRefresh": false,  // 这条凭据带 refresh_token（扫码登录才拿得到）→ 能自动续期
    "ffmpegOk": true,     // 分轨档位（480P 及以上）要靠它合流
    "hasProxy": false
  }
```

`canRefresh=false` 是手填 Cookie 的固有短板：那条串里没有 `refresh_token`，B 站一作废
就只能重填。扫码登录（D5b）会把 `refresh_token` 一起存下来，之后由服务端每 6 小时检查、
在到期前自动换新。

`lastLoginOk` / `cookieSetAt` 是为了把三种「没登录上」分开：**查不出来**（`navError` 非空）、
**曾经有效、后来被 B 站作废**（`lastLoginOk` 有值）、**从没成功过**（多半粘错了）。
Cookie 里自带的到期时间不算数——B 站可以在到期前作废登录态（2026-10-30 实测：内嵌到期
2027-04-25，第 5 天就回 `-101`，见 `docs/M7-BILIBILI.md` §7.11）。

这三项看起来像故障、其实是 B 站的规则，所以界面上要能直接看到（对应 M6 的 `GET /api/yt/health`）。

`hasCookie` 与 `loggedIn` 要分开看：Cookie 存进去了但格式不对（实例：浏览器导出的
`cookies.txt` 被整理成两百多个假 `SESSDATA`）时，`hasCookie=true` 而 `loggedIn=false`，
表现就是「配了 Cookie 还是 720P」。`loggedIn` 只在 `hasCookie=true` 时才会真去打 nav。

### D5b. `POST /api/bilibili/login/qrcode` —— 扫码登录

```
→ 200 { "url": "https://account.bilibili.com/h5/account-h5/auth/scan-web?…&qrcode_key=…",
        "key": "de88af434b0b1b1a154cf439579ba6ca" }
```

前端把 `url` 画成二维码（本地渲染，不把二维码地址发给第三方），`key` 用于轮询。
本机实测：打 B 站 passport 往返 **74 ms**。

### D5c. `GET /api/bilibili/login/qrcode?key=…` —— 轮询扫码结果

```
→ 200 { "status": "pending" | "scanned" | "success" | "expired", "message": "…", "uname": "…" }
```

`status=success` 时**服务端已经把凭据写进库**（`SESSDATA`/`bili_jct`/`DedeUserID` 取自
登录响应的 `Set-Cookie`，`refresh_token` 取自响应 JSON，`buvid3` 另打
`/x/web-frontend/getbuvid` 补齐），响应体里只有昵称，前端全程不接触凭据——
因为 `SESSDATA` 是 HttpOnly，任何「让用户复制」的路径都拿不到它（§7.11）。
建议 2 秒一次轮询，二维码 180 秒过期。

### D6. `GET /api/manage/bilibili`

```
→ 200 { "stats": { "channels": 1, "videos": 19, "unseen": 18, "downloaded": 1, "errors": 1 },
        "channels": [<D1 的 items[]>] }
```

管理 →「B 站」页签的整页数据，一次给全（与 `GET /api/manage/youtube` 同构）。

### D7. 通用接口的口径变化（M7 补）

| 接口 / 字段 | 变化 |
|---|---|
| `GET /api/videos`、`GET /api/videos/new` | 排除 `platform='bilibili'`（B 站有自己的墙） |
| `GET /api/performers`、`GET /api/subscriptions` | 排除 B 站（UP 主不是「艺人」） |
| `GET /api/manage` 的 `performers*` | 同样排除；`videos` / `files` / `bytes` / `downloads` 仍是全库口径 |
| 影片 DTO 的 `performers[]` | 多一个 `avatarUrl`（已代理）。B 站墙上每张卡的圆头像靠它：「订阅艺人头像表」不含 B 站 |
| SSE `video.new` | 多一个 `platform` 字段；前端据此只刷 B 站墙，不动「关注」流 |

## 附：媒体库（本地已下载的影片）

「下载」页看的是**任务队列**（排队中 / 下载中 / 失败 / 历史），这一组接口看的是
**磁盘上已经有的东西**（`library_files` 里那些文件真在的记录）。页面是 `/library`，
2026-11-03 加（用户要求「加一个媒体库页面，显示下载的视频」）。

### E1. `GET /api/library/videos` —— 已落盘的影片列表

查询参数（都可选，全部走服务端）：

| 参数 | 含义 |
|---|---|
| `libraryId` | 只看某个库；缺省 = 全部库 |
| `q` | 标题 **或** 文件名模糊匹配（`videos.title LIKE ? OR library_files.video_name LIKE ?`——下载会按命名模板改名，两边不一定一样） |
| `performer` | 只看这位**艺人**（平台影片的 `video_performers`）；顶部头像条点的就是它 |
| `channel` | 只看这个 **YouTube 频道**（`yt_videos → yt_channels`；YouTube 的片不落 `performers`，来源只有频道） |
| `sort` | `recent`（默认，按 `library_files.created_at` 倒序） / `size`（体积） / `duration`（时长） / `title`（文件名升序） |
| `cursor` / `limit` | keyset 分页，默认 24，上限 100 |

`performer` 和 `channel` 是**同一条「来源」轴**的两种写法（页面顶部那排头像，2026-11-03 加）：
平台影片按艺人筛、YouTube 影片按频道筛，一次只该传一个。两个都传时按艺人算。

```
→ 200 {
  "items": [{
    "videoId": 2893, "libraryId": 1, "libraryName": "主库",
    "title": "4K 美女外拍｜克罗拉[04]｜Bikini Lookbook｜비키니 룩북 (2026)",
    "fileName": "4K 美女外拍｜克罗拉[04]｜….mp4",
    "platform": "youtube", "performers": [],
    "channelName": "4kaamg",
    "avatarUrl": "/api/img?p=youtube&u=https%3A%2F%2Fyt3.googleusercontent.com%2F…",
    "durationSec": 201, "width": 3840, "height": 2160, "sizeBytes": 410375125,
    "downloadedAt": "2026-11-02T17:51:58Z",
    "thumbUrl": "/api/img?p=youtube&u=https%3A%2F%2Fi2.ytimg.com%2F…",
    "localThumbUrl": "/api/videos/2893/art?libraryId=1&k=thumb",
    "videoPath": "/…/媒体库/YouTube/4kaamg/…/4K 美女外拍….mp4",
    "relDir": "YouTube/4kaamg/4K 美女外拍…",
    "missing": false,
    "fileUrl": "/api/videos/2893/file?libraryId=1"
  }],
  "nextCursor": "…" | null,
  "total": 8, "totalSizeBytes": 1296960839,
  "byLibrary": [{ "libraryId": 1, "name": "主库", "fileCount": 8, "sizeBytes": 1296960839 }],
  "sources": [
    { "kind": "channel", "id": 12, "name": "4kaamg",
      "avatarUrl": "/api/img?p=youtube&u=https%3A%2F%2Fyt3.googleusercontent.com%2F…", "fileCount": 3 },
    { "kind": "performer", "id": 38, "name": "Hot Pearl",
      "avatarUrl": "/api/img?p=xhamster&u=https%3A%2F%2F…", "fileCount": 1 }
  ]
}
```

几个口径，改之前先看：

- **排序键是落盘时间，不是发布时间**：这一页回答的是「我什么时候下回来的」，不是「平台什么时候发的」。
- `total` / `totalSizeBytes` 跟随**全部筛选**。`byLibrary` / `sources` 是分面导航的计数，
  **各自那条轴不做过滤、其它条件照跟**：`byLibrary` 不跟 `libraryId`（画的是筛选胶囊上的数字，
  得先看得见「每个库各有几部」才点得下去），`sources` 不跟 `performer` / `channel`
  （否则点了头像之后头像条会塌成一颗，点回去就没路了）。数字口径都是「点下去会看到多少」。
- `sources[].kind` 只有 `performer` / `channel` 两种，两者 id 各自从 1 数起，**必须带 kind 才唯一**
  （媒体库页面前端把频道映射成负数塞进同一排头像的 id 空间，见 `Library.svelte` 的 `railIdOf`）。
  `avatarUrl` 走 `/api/img` 时用的平台参数是**头像自己的平台**（`performers.platform` / `youtube`）。
- `localThumbUrl` 只在**那张图真的还在磁盘上**时才给（服务端 `os.Stat` 过）；为 null 时前端退回
  `thumbUrl`（远端代理）。这样封面不会出现「404 破图」。
- `missing=true` = 记录在、文件没了（被手动删/挪走）。卡片要压暗并写「文件已不在磁盘上」，
  点击不再跳播放页（跳过去只会 404）。
- 同一部影片可以同时出现在两个库 → 返回**两行**，`keyOf` 用 `videoId-libraryId`，不要只用 `videoId`。
- `channelName` / `avatarUrl` 是卡片副行左边那一格（和关注流 / B 站 / YouTube 卡同一个位置）：
  平台影片取**第一位艺人**（B 站就是 UP 主），YouTube 影片的 `performers` 是空的，取它在
  `yt_channels` 里的频道（服务端 LEFT JOIN 出来的）。两样都没有时 `avatarUrl` 为 null、前端退化成
  名字首字母——别拿 `thumbUrl` 顶替，那是封面不是头像。
  代理用的平台参数是**头像所属平台**（艺人的 `platform`），不是影片的：图床认 Referer，
  拿影片的平台去拉一张跨站头像会被挡掉。
- 播放直接复用 `GET /api/videos/{id}/file`（`handlePlayback` 里本地那一路本来就排第一）。

### E3. `POST /api/videos/{id}/rotate` —— 记住画面上转了多少度

`{"deg": 90}` → `{"ok": true, "rotateDeg": 90}`。只收 0/90/180/270（别的值 400：45° 会让画面
转出一个黑三角），负数按模归到正角度；影片不存在 → 404。

**为什么要有它**：有些片子**像素本身就是横的**，容器里没有任何 rotation 元数据（ffprobe 读不出来），
任何播放器都只能照着横的放，只有用户知道该往哪边转。前端点一下转 90°（0→90→180→270→0），
先把本地状态改掉让画面立刻转，再把角度写回来，写失败就退回并报一句。

角度记在 **`videos.rotate_deg`**（不是 `library_files`）：转不转是**画面内容**的属性，同一部片下到
两个库里不该有两个角度。和 `/api/yt/videos/{id}/rotate`（`yt_videos.rotate_deg`）是同一套做法——
两条表是两条链路（YouTube 墙 vs 关注流/媒体库），谁也没法引用谁。

### E4. `DELETE /api/library/videos/{id}` —— 把这部片从本地库删掉（**整个文件夹 + 元数据**一起删）

```
→ 200 {"ok": true,
       "removed": ["/…/主库/xHamster/Hot Pearl/…/xxx.mp4", "…/xxx.nfo", "…/poster.jpg",
                   "…/xxx.zh.srt", "…/xxx.mp4.part", …],
       "kept": []}
```

- **磁盘文件真删**，不是只删索引（这一页叫「删除」，不是「移除记录」；下载页的「移除」才是只删任务）。
- **端掉的是这一部片的整个文件夹**（2026-11-03 用户要求「目录和元数据都要删掉」），不是只删
  `library_files` 记录里那 5 个路径：下载失败留下的 `*.part`、字幕、`thumb-sheet.jpg`、临时 `.thumb.src`
  都不在记录里，只按记录删就会剩一个装着垃圾的壳。`removed` 里如实列出删掉的文件。
- 三条前提，任一条不满足就退回「只删记录里那几个路径」：文件夹在库根之内、文件夹**不是库根本身**
  （有的库是根目录直接堆片，清空目录等于清空整个库）、文件夹里**没有别的 `library_files` 记录**
  （`CountLibraryFilesUnder`；`rel_dir` 被写脏成两条片共用一个目录时，端文件夹就是替别人删片）。
  后两条有测试：`TestDeleteLibraryVideoKeepsSharedFolder`、`TestDeleteLibraryVideoKeepsRoot`。
  > 这个守卫的第一版是 `substr(video_path, 1, len(prefix))`，`substr` 数**字符**、`len()` 数**字节**，
  > 路径里一带中文（「测试频道」）就一行也匹配不上、守卫等于没有——`TestDeleteLibraryVideoKeepsSharedFolder`
  > 当场把别人的片删掉了才暴露，改成分字符的 `instr(video_path, prefix) = 1`。
- 这条片派生的大文件一起清（`clearVideoDerived`）：合流成品 `data/mux/<videoId>-<variant>.mp4`
  （实测 4K 一档 500 MB 起步，本机 mux 目录一度 512 MB / 13 个文件）和下载工作目录
  `data/tmp/dl-<videoId>-<lib>`（下载中途失败会留下它）。
- 删完从影片文件夹往上收空目录，一路收到库根（`pruneEmptyDirs`）。**「这一层已经不存在」不算停**：
  整片文件夹是被 `RemoveAll` 端掉的，第一层就 `ReadDir` 失败，早先那种 `err != nil` 就 `return`
  的写法会停在原地——2026-11-03 用户删完 1/2/3/15/60 之后，`YouTube/T-Series`、`YouTube/Lofi Girl`、
  `bilibili/影视飓风` 三个空壳一直留在盘上。
- **先删文件、后删记录**：文件删不掉（播放器占着、权限不够）就留着记录并写进 `kept[].reason`
  （`ok` 变 `false`），否则媒体库里会剩一条指着空气的记录。
- 路径必须落在库根之内才删（`insideRoot`，和 `/file`、`/art` 同一道闸门）：`kept[].reason` 会是
  「文件不在库目录内」。库里的字符串**永远**不能直接拿去 `os.Remove` / `RemoveAll`。
- 影片本身（`videos` 行）留着：播放页删完要能退回平台在线播放；已经没本地文件时再删 → 404。
- 前端入口是播放页标题行那颗垃圾桶（本地已有的片子才有，见 ARCHITECTURE §7.6 媒体库行），
  点之前弹一个确认框问「本地库里它的整个文件夹会一起删掉（视频、nfo、海报、剧照）」。
  2026-11-04 起确认框是全站统一的 `ConfirmDialog`（`confirmDialog.ask()`），
  不再是浏览器原生的 `window.confirm`——深色界面里蹦出白框太出戏，见 ARCHITECTURE「确认框」一行。

实测（2026-11-03，本机 :18081，用一条现下的 xHamster 片 448 做「装了垃圾再删」）：
文件夹里先塞进 `*.zh.srt` / `*.mp4.part` / `thumb-sheet.jpg`，另造 `data/mux/448-1080p.av1.mp4` 与
`data/tmp/dl-448-1/`，再 `DELETE` → `removed` 列全 8 个文件，文件夹当场没了、
`data/mux/448-*` 与 `data/tmp/dl-448-1` 一起没了、`library_files` 行归零、媒体库从 9 条回到 8 条。
UI 一遍（真机 1440×900，当时的 `window.confirm` 打桩记下文案再点掉；现在换成统一确认框，
文案没变）：确认框文案即上面那句，
删完标题行那颗垃圾桶换成「下载到本地库」、播放器退回平台在线流。

### E2. `GET /api/videos/{id}/art?libraryId=1&k=thumb` —— 本地海报 / 剧照

`k` = `thumb`（默认） / `poster` / `fanart`，对应下载时一起落盘的 `thumb.jpg` / `poster.jpg` /
`fanart.jpg`。`→ 200 image/jpeg`，带 `Cache-Control: private, max-age=86400`；
没落盘（字段为空）或文件不在磁盘上 → 404。

**它和 `/api/img` 是两条线**：`/api/img` 是远端代理（平台封面地址会过期），这个是本地文件。
路径校验和 `/api/videos/{id}/file` 共用同一个 `serveLibraryPath`：库根之外一律 403
（`library_files.thumb_path` 被写脏时也不会变成任意文件读取，已有测试 `TestVideoArtRejectsOutsideRoot`）。

实测（2026-11-03，本机 4K 那条 2893）：`k=thumb` → 200 `image/jpeg` 121 664 B；
`k=poster` → 200 53 746 B；`k=fanart` → 200（这条片 fanart 与 thumb 是同一张）。
