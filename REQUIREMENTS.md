# MyPorn — 多平台订阅 · 本地库 · Emby 直挂 需求文档

版本：v0.4（决策已确认，技术栈见 ARCHITECTURE.md，剩余细节见第 10 节）
形态：单用户自托管 Web 应用（Docker）
首期平台：Pornhub；架构预留 xHamster / beeg

---

## 1. 一句话定位

把自己订阅的艺人新片，从多个第三方平台收拢到一个站点里：**在线看（最高画质）→ 一键存进本地库 → 自动生成 Emby 规范的 nfo/图片 → 可选上传 OpenList 并生成 strm**，让 Emby / Jellyfin 直接刮削播放。

核心闭环：

```
订阅艺人 ──定时同步──> 影片流 ──┬──> 在线播放（最高画质）
                              └──> 下载到指定本地库 ──> nfo/poster/thumb/fanart
                                                        └──> 上传 OpenList ──> 生成 strm
```

## 2. 已确认的前提

| 维度 | 决策 |
|---|---|
| 用户模型 | 单用户自托管，局域网/个人使用，无注册体系 |
| 访问控制 | 单密码 + HttpOnly Session，无 TOTP |
| 平台对接 | 后端抓取公开页面 + 解析 HLS 清单，适配器插件化 |
| 网络出口 | 支持全局代理，**且**可按平台单独指定（socks5 / http） |
| 播放链路 | 后端解析出直链交给浏览器播放（Video.js 10 播放器，HLS 经 hls.js 适配器），代理播放作为降级开关 |
| 画质 | **支持 4K，默认最高画质，不设上限**；可手动切档，卡顿时自动降档 |
| 首页形态 | 订阅流 + 艺人搜索页（只搜艺人，不做影片/分类浏览） |
| OpenList | 作为**上传目标**（视频 + 元数据）+ 生成 strm |
| 上传后本地 | 默认 **strm 模式**：上传成功后删本地视频，保留 nfo + 图片 + strm |
| strm 链接 | OpenList `/d/` 直链（带 token 或公开分享） |
| 本地库 | 创建时指定绝对目录，可建多个；**默认根目录 = 当前工作区 `./media`**（容器内 `/media`）；显示可用空间 |
| 首次同步 | **首次订阅全量回填该艺人全部影片**，之后仅增量 |
| 命名模板 | 可配置，**默认 `{标题}`**；支持 `{标题}{发布日期}{艺人}{平台}{id}{分辨率}` |
| Emby 联动 | 本期**不做** Emby API 自动扫库，仅产出规范元数据（预留接口） |
| 移动端 | 需要 PWA，可添加到主屏 |
| OpenList | `http://192.168.0.108:5244`，根路径 `/123云盘/资源目录/MyPorn` |
| 首期范围 | 只做 Pornhub 打通全链路，其它站后续接入 |

## 3. 术语

- **Platform**：内容来源站（pornhub / xhamster / beeg）
- **Performer（艺人）**：平台上的 model 主页，如 `pornhub.com/model/mini-diva`
- **Video（影片）**：单条视频，唯一键 = `platform + platformVideoId`
- **Library（本地库）**：一个本地磁盘根目录，可建多个
- **Metadata**：Emby 规范元数据 = `.nfo` + `poster` + `thumb` + `fanart`
- **strm**：内容为远端直链的 `.strm` 文本文件，Emby 据此播放远端文件

---

## 4. 功能需求

### 4.1 平台接入层（Adapter）

| 编号 | 需求 |
|---|---|
| FR-01 | 统一适配器接口，屏蔽各站差异：`searchPerformers` / `getPerformer` / `listPerformerVideos(page)` / `getVideo` / `resolveStreams` |
| FR-02 | 输出统一数据模型：`platform, platformVideoId, title, duration, publishDate, thumbUrl, previewUrl, tags[], performers[], viewCount, rating, sourceUrl` |
| FR-03 | HLS 解析：拉取 master playlist，解析出全部变体（分辨率 / 码率 / 编码）并按质量排序备用 |
| FR-04 | 网络层：全局代理（默认出口）+ 按平台覆盖代理，支持 `http` / `socks5` / 直连三态；自定义 UA；可选 Cookie（用于登录态/地区受限内容）；令牌桶限速 + 指数退避重试 |
| FR-05 | 解析规则与业务逻辑分离：每站一个 adapter 目录 + HTML fixture 单测，站点改版只改 adapter |
| FR-06 | 抓取结果落库可追溯：记录抓取时间、HTTP 状态、解析失败原因，失败项可手动重试 |
| FR-07 | 合规边界：只处理公开可访问内容，不绕过付费墙/DRM，每条影片保留并展示原始 `sourceUrl` |

### 4.2 订阅（艺人）

| 编号 | 需求 |
|---|---|
| FR-08 | 添加订阅：粘贴艺人主页 URL（主路径）；可选关键词搜索辅助（抓取站内搜索） |
| FR-09 | 艺人信息从平台抓取并展示：头像、名称、简介、订阅数、视频数、国家等可获取字段；首次入库即快照，之后按周期刷新 |
| FR-10 | 添加订阅后**首次同步为全量回填**：翻页遍历该艺人全部影片直到末页。按「数百部影片」量级设计：<br>① 先抓第 1 页立即入库，UI 秒出内容，剩余页后台续抓；<br>② 每页 20–30 条，翻页游标持久化，中断可从断点续跑；<br>③ 抓取限速 1 请求/1.5s + 随机抖动（500 部 ≈ 20–25 页 ≈ 40s）；<br>④ 缩略图不随回填下载，列表按需懒加载；<br>⑤ `raw` JSON 只保留必要字段，控制库体积；<br>⑥ 可选「仅回填最近 N 部」加速首次体验 |
| FR-11 | 后续为增量同步：默认 6 小时一次（可配置），只抓第 1 页、遇到已知 `platformVideoId` 即停止翻页；去重插入新片；记录 `lastSyncAt / nextSyncAt / lastError / backfillDone` |
| FR-12 | 订阅列表：网格卡片（头像 + 新片数角标），支持按最近更新/名称/添加时间排序，可启用/暂停/取消订阅（软删除，影片保留） |
| FR-13 | 影片流：聚合所有订阅的最新影片，发布时间倒序，无限滚动；筛选平台 / 艺人 / 时长 / 是否已有本地副本；已看标记 |
| FR-14 | 手动"立即同步"按钮，单艺人或全部 |
| FR-14b | **艺人搜索页**：关键词调平台艺人搜索接口，结果卡片（头像/名称/视频数/订阅数），点击进艺人详情，一键订阅；不做影片搜索与分类浏览 |

### 4.3 在线播放

| 编号 | 需求 |
|---|---|
| FR-15 | 详情页：封面/预览图、标题、艺人（可点进艺人页）、标签、时长、发布日期、来源链接、清晰度选择（播放器底部控制条里的菜单，默认最高档）；解析与缓冲期间显示模糊封面 + 加载动画，不出现黑屏文字占位 |
| FR-15b | 播放器氛围模式：把当前画面取帧放大模糊，在播放器四周铺一层同色环境光；可开关、默认开（系统「减少动效」时默认关）、选择记在本地 |
| FR-16 | **默认最高画质（含 4K）**：按 (height, bandwidth) 降序取第一个可用变体，不设分辨率上限；用户可手动切档并记住偏好；开启自动降档时，连续卡顿则临时降档、恢复后回到最高 |
| FR-16b | 下载同样取最高变体；下载确认框显示预估体积（码率 × 时长），4K 单片可达 GB 级，需提示磁盘与 OpenList 上传耗时 |
| FR-17 | 后端解析选定变体的 m3u8 地址返回前端，前端用 Video.js 10 播放（HLS 走 `@videojs/hlsjs-video`，mp4 走原生 `<video>`）；切档保留播放进度；音量与静音记在 `localStorage`，下次打开原样恢复；直链做 5 分钟内存缓存，避免重复解析 |
| FR-18 | 播放失败兜底：若直链因 Referer/UA/CORS 不可用，提供"代理播放"降级开关（`/api/stream/proxy`），并在 UI 上提示 |
| FR-19 | 播放地址失效（403/过期）时自动重新解析并恢复播放一次 |
| FR-20 | 续播：记录播放进度，卡片上显示已看进度条 |

### 4.4 下载与本地库

| 编号 | 需求 |
|---|---|
| FR-21 | 本地库管理：可创建多个库，字段 `name / rootPath / enabled / isDefault`；显示可用空间；路径不可写时阻止保存 |
| FR-22 | 下载任务：ffmpeg 直拉最高质量变体，合并为 mp4（**不转码**）；任务状态机 `queued → downloading → muxing → uploading → done / failed / canceled`，含进度、速度、ETA、错误信息 |
| FR-23 | 并发控制：可配置最大并发下载数（默认 2）、单任务限速；任务可暂停/取消/重试 |
| FR-24 | 幂等：同一 `videoId + library` 已存在则默认跳过并提示，可选覆盖 |
| FR-25 | 目录与命名（Emby 电影规范）：**默认影片名 = `{标题}`**，模板可配置（变量 `{标题}{发布日期}{艺人}{平台}{id}{分辨率}`）。每部影片独立文件夹，保证多文件元数据被 Emby 正确归属： |

```
{LibraryRoot}/{平台}/{艺人}/{影片名}/
├── {影片名}.mp4
├── {影片名}.nfo
├── poster.jpg      # 竖版海报
├── thumb.jpg       # 16:9 缩略
└── fanart.jpg      # 16:9 背景
```

示例（默认模板）：`/media/Pornhub/Mini Diva/Some Title/Some Title.mp4`
模板改成 `{发布日期} - {标题} [{id}]` 时文件夹与文件名同步变化。

| 编号 | 需求 |
|---|---|
| FR-26 | 文件名清洗：非法字符替换、去重空格、长度上限 120 字符、保留原始 ID 便于回溯 |
| FR-27 | 元数据生成（Emby `movie` nfo）：`title / originaltitle / plot / premiered / runtime / genre·tag / studio=平台名 / actor(name, role) / uniqueid(type=平台, default) / art(poster, fanart) / website=来源链接 / rating` |
| FR-28 | 图片：poster 优先竖图（艺人/影片竖版），缺则从 16:9 主图裁切；thumb 用视频缩略图；fanart 用 16:9 大图；任一图片失败只记警告不阻塞任务 |
| FR-29 | 下载完成后写库索引（影片 ↔ 文件 ↔ 本地库）；**本期不集成 Emby API**（不做 `RefreshLibrary` 自动触发），保留配置位与调用封装以便后续开启 |
| FR-30 | 下载队列页：实时进度、历史记录、失败原因、磁盘占用统计 |
| FR-30a | 下载触发方式：**默认纯手动**（影片卡片/详情页的下载按钮）；设置项「同步到新片后自动下载」默认**关闭**，打开后只对**增量同步**发现的新片生效，进入默认本地库；订阅后的**首次全量回填永不自动下载**（避免一订阅就下几百部）。已存在本地文件或已在队列中的影片会跳过 |

### 4.5 OpenList 上传 + strm

| 编号 | 需求 |
|---|---|
| FR-31 | OpenList 连接配置：`baseURL`（默认 `http://192.168.0.108:5244`）、`token`、目标根路径（默认 `/123云盘/资源目录/MyPorn`），带「测试连接」；token 读环境变量或 AES-GCM 加密存库，不明文落盘/入库 |
| FR-32 | 上传策略（**默认**）：`strm 模式` = 上传成功后删除本地视频，仅保留 nfo + 图片 + `.strm`；另提供 `仅本地` / `本地保留 + 上传` 两个可选策略，可按库配置 |
| FR-33 | 上传内容：视频（必需）+ nfo/图片（默认同步，便于远端刮削）；远端目录结构与本地完全一致 |
| FR-34 | strm 生成：内容为 OpenList **`/d/` 直链**（带 token 或公开分享链接），写入与 nfo 同目录的 `.strm`；支持单条/批量「刷新 strm」应对签名过期。**前置 spike**：123 云盘驱动的 `/d/` 是否支持 Emby 直接拉流（Range、限速、防盗链）需先实测（ARCHITECTURE.md §9） |
| FR-35 | 顺序与一致性：先传视频 → 校验成功 → 再写 strm / 删本地；任一步失败则保留本地文件并标记失败，可重试 |
| FR-36 | 大文件上传：分片/断点续传（按 OpenList API 能力实现），带进度与重试 |

### 4.6 设置与运维

| 编号 | 需求 |
|---|---|
| FR-37 | 设置页：网络与代理、平台 Cookie、同步间隔、默认下载库、命名模板、并发与限速、OpenList 配置 |
| FR-38 | 访问控制：单密码 + HttpOnly Session（无 TOTP），登录失败限流与锁定 |
| FR-39 | 日志与诊断：抓取日志、任务日志、结构化错误；健康检查端点 `/healthz` |
| FR-40 | 备份：SQLite 定时快照，可一键导出/恢复 |
| FR-41 | PWA：manifest + Service Worker，可添加到主屏、全屏启动；仅缓存 app shell 与静态资源，**不缓存视频**；离线时给出明确提示 |

---

## 5. 数据模型（核心表）

| 表 | 关键字段 |
|---|---|
| `platform` | id, key(pornhub), name, enabled, proxyUrl, cookie(加密), config |
| `performer` | id, platformId, platformPerformerId, name, avatarUrl, bio, stats(json), videoCount, lastFetchedAt |
| `subscription` | id, performerId, enabled, backfillDone, backfillCursor(page), fullBackfill(bool), lastSyncAt, nextSyncAt, lastError, createdAt |
| `video` | id, platformId, platformVideoId(唯一约束 with platformId), performerIds, title, duration, publishDate, thumbUrl, previewUrl, tags, sourceUrl, raw(json) |
| `video_stream` | id, videoId, height, bandwidth, codec, url, cachedAt |
| `library` | id, name, rootPath(绝对路径), enabled, isDefault, uploadPolicy(local/local_remote/strm), openlistPath, namingTemplate |
| `library_file` | id, videoId, libraryId, relDir, videoPath, nfoPath, posterPath, thumbPath, fanartPath, strmPath, size, createdAt |
| `job` | id, type(download/upload/strm/sync), videoId, libraryId, status, progress, speed, attempts, error, startedAt, finishedAt |
| `setting` | key, value(加密标记) |
| `log` | id, level, scope, message, meta, createdAt |

---

## 6. 非功能需求

| 维度 | 要求 |
|---|---|
| 性能 | 影片流首屏 <1s（读本地缓存，不实时抓取）；单次抓取单页 <5s；视频列表滚动流畅 |
| 可靠性 | 抓取/下载/上传均可重试且幂等；某平台解析失败不影响其它功能与页面渲染 |
| 安全 | 单密码 + Session；敏感配置（Cookie、OpenList token）AES 加密；强制反代 HTTPS；路径拼接防目录穿越；所有外链白名单校验 |
| 可维护 | Adapter 可插拔（新增站点 = 新增目录 + 注册）；关键解析逻辑有 fixture 测试 |
| 兼容 | Chrome / Safari / Edge 桌面 + 移动端浏览器；HLS 走 hls.js，播放器用 Video.js 10 皮肤，自适应布局 |
| 可观测 | 结构化日志、任务失败可见、健康检查 |

---

## 7. 技术栈（v0.3 已重定：低内存优先）

> 完整对比与论证见 **ARCHITECTURE.md（ADR-001）**，此处只放结论。前端选型细节见 ARCHITECTURE.md §7。

**结论：Go 单二进制（web + worker + scheduler 同进程）+ 内嵌 Svelte 前端 + SQLite(WAL) + ffmpeg 仅做 remux**

| 层 | 选型 |
|---|---|
| 后端 | Go 1.23，`net/http` + `chi`，单进程 goroutine 池 |
| 前端 | Svelte 5 + Vite + Tailwind，构建产物 `go:embed` 进二进制；Video.js 10 播放器（HLS 按需加载 hls.js）；SSE 推任务进度 |
| 数据库 | SQLite WAL，`modernc.org/sqlite`（纯 Go、无 cgo） |
| 下载 | **自研 HLS 分段下载器**（并发 8 连接、断点续传、精确进度）→ `ffmpeg -c copy` 封装 mp4 |
| 打包 | 多阶段 Dockerfile，alpine + ffmpeg，镜像 ≤100MB |

为什么换掉 Next.js：Next.js 单体 + worker 空载 250–400MB，Go 单进程 35–60MB，**差 5–8 倍**；且 Node 方案要么长任务与请求同进程（互相拖累），要么拆两进程（内存再翻倍）。Go 单进程用 goroutine 分池即可隔离，任务队列直接用 SQLite 表，免去 Redis/RabbitMQ。

为什么不用 Rust：内存只再省约 20MB，但适配器（本项目 80% 的后续维护量）用 Rust 迭代和编译都慢，性价不划算。

关键内存手段：全链路 `io.Copy` 流式、绝不把视频读进内存；HLS 自研下载器把每路下载内存从 60–200MB 压到约 10MB；上传走 `io.SectionReader` 分片。

---

## 8. 里程碑

| 阶段 | 内容 | 验收 |
|---|---|---|
| M1 ✅ | Pornhub adapter + 艺人订阅（首次全量回填）+ 艺人搜索 + 影片流 + 在线播放（最高画质） | 搜到艺人 → 订阅 → 全量回填完成 → 点开即播 |
| M2 ✅ | 本地库 + 下载 + Emby 元数据 + 队列 UI + PWA | 下载后目录被 Emby 正确刮削出海报/标题/艺人；手机加到主屏可用 |
| M3 | OpenList 上传 + strm + 刷新（含 `/d/` 直链 spike） | 上传后 Emby 用 strm 直接播放远端文件并可拖动 |
| M4 | xHamster / beeg adapter、新片通知、Emby API 自动扫库 | 三站统一体验 |

M2 实测结论（2026-10）：真机下载 7 部 Pornhub 影片全部成功，落盘 `{库}/Pornhub/{艺人}/{片名}/{片名}.mp4`
+ `.nfo`（标题/简介/标签/演职人员/工作室/唯一 ID）+ poster 1000×1500 / thumb 1280×720 / fanart 1920×1080；
进度条、速度（含 ffmpeg 直连链路的体积差推算）、暂停/续传、跨库再下载均正常。

## 9. 本期明确不做

- 不做转码 / 多码率转封装（只做原画直拉）
- 不做用户注册与多租户、不做 TOTP
- 不做影片搜索、分类/热门浏览（只搜艺人）
- 不做付费墙 / DRM 内容绕过
- 不做 Emby 插件（用 nfo + strm 原生对接）
- 不做弹幕、评论、推荐算法

## 10. 决策记录与剩余细节

已确认并并入正文：单用户单密码 · 全局+按平台代理 · 订阅流+艺人搜索 · 直链播放 · OpenList 作上传目标 ·
默认 strm 模式 · `/d/` 直链 · 首发 Pornhub · 本地库创建时指定目录 · 首次全量+后续增量 ·
命名模板默认 `{标题}` · 本期不做 Emby API · 需要 PWA · Go 单二进制技术栈。

仍待你补充（不阻塞开工）：

1. **123 云盘 `/d/` 直链是否可被 Emby 直接拉流**：M3 前先做 1 天 spike 验证（Range/限速/防盗链），结果决定 strm 用直链还是走本站反代。
2. **平台最高档实际是几 K**：`cn.pornhub.com` 未登录状态下 `get_media` 只回 240/480/720/1080 直链，
2160p 条目需要登录 Cookie；画质策略因此按「取最高可用」自动退化，无需改代码。
3. **OpenList token**：已在聊天中明文出现，建议在 OpenList 后台重置后填入 `.env.local`（已生成，权限 600、已 gitignore）。

已确认：本地库默认 `./media` · 4K/最高画质默认不设上限 · 单艺人可达数百部（回填按 §FR-10 设计） · PWA 需要 · Go 单二进制 + Svelte 5 前端。
