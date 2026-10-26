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
   "status":"ok" }]
```
`status`: `ok | no_proxy | error`（前端仅做提示，不阻塞）

## 2. 艺人

```ts
type Performer = {
  id: number                 // 本地 id
  platform: "pornhub"
  performerId: string        // 平台侧 id 或 slug
  name: string
  url: string                // 平台主页
  avatarUrl: string | null   // 经过本地 /api/img 代理后的 URL
  bio: string | null
  videoCount: number | null      // 平台粉丝量信息（若可获取）
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

`GET /api/search/performers?q=<kw>&platform=pornhub&page=1` →
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
  platform: "pornhub"
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
- `variants` 按 (height, bandwidth) **降序**；`defaultVariantId` 即最高画质
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
| GET | `/api/settings` | `{syncIntervalHours, globalProxy, platformProxy:{pornhub},
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
| `rate_limited` | 429 | 上游限流 |
| `internal` | 500 | 服务端错误 |

---

## 附：M1 实现补充说明（与上方契约的差异）

以下为实现时确定、前端已按此对接的细节，均未改变既有字段语义：

1. **`PUT /api/settings`** 保存后返回完整的 `Settings` 对象（而非 204），前端以服务端返回为准；
1a. **`autoDownloadNew`**（默认 `false`）= 「同步到新片后自动下载」：打开后，**增量同步**到的新片会自动投进下载队列（进默认库）；首次全量回填不会自动下载。关闭时下载只能手动触发。
   `openlist.token` 为 write-only，响应里只回 `hasToken`。
2. **`GET /api/videos/new`** 与 `/api/videos` 同结构：接受 `cursor` / `limit`，返回 `{items, nextCursor, total}`；
   `since` 参数保留但当前忽略。
3. **`Settings.platformProxy`** 为 `{ "pornhub": "http://..." }`，空串表示回退到 `globalProxy`。
4. **`Performer.videoCount`** 是平台侧影片数量（不是粉丝数）；粉丝数用 `subscriberCount`。
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
