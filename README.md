# MyPorn

单用户自托管的视频聚合站：订阅艺人 → 在线看最高画质 → 下载进本地库 → 生成 Emby 元数据 → 上传 OpenList 生成 strm。

当前进度：**M4 的 xHamster / beeg 适配器**（新片通知与 Emby 自动扫库按要求暂缓）。

- 需求：`REQUIREMENTS.md`
- 架构与选型：`ARCHITECTURE.md`
- 接口契约：`docs/API.md`
- 上线前必读：`docs/NEEDS-VERIFICATION.md`（解析选择器需在能访问站点的机器上核对）

## 当前状态（三个站都能用）

M1（订阅 + 影片流 + 在线播放）已实现并实测通过：

- 单密码登录（HttpOnly 会话，5 次错误锁 5 分钟）
- 订阅艺人：粘贴主页链接或站内搜索 → 抓资料 → 入库 → 自动投递首次全量回填
- 全量回填：首页优先、分页游标断点续跑、1 req/1.5s 限速、进度落库 + SSE 实时推送
- 增量同步：按 `syncIntervalHours` 调度，遇已入库影片即停，失败退避 30 分钟
- 艺人页：一块面板从头读到尾——头像旁边是名字 + 平台图标（点它开平台主页）+ `N部影片 · N订阅` + **身材**（`167cm · B89 · W64 · H91`，站点给的英制抓取时换算成厘米）和模特排名（`#269`），右侧是资料行（性别/出身地/体重等）；一道发丝线以下，左边是简介、右边是站外链接药丸（官方网址 / Twitter / Instagram）；两项性癖偏好和平台统计行（播放量等）解析时就丢掉
- 影片流：keyset 游标分页 + 无限滚动（离屏页 DOM 回收），新片标记
- 在线播放：解析全部清晰度并按质量降序，**默认最高画质**（4K 不限），Video.js 10 播放器（HLS 才按需加载 hls.js），
  解析和缓冲时不是黑屏，而是把封面（播放中则是刚才那一帧）糊开当环境光 + 中间一个转环，出画面就淡出（这层只盖画面、在控制条下面，所以拖进度条时进度条还在）；清晰度菜单和**氛围模式**开关都在播放器底部控制条里（和音量 / 设置 / 画中画 / 全屏同一行）；音量 / 静音 / 切档偏好下次打开都记得，氛围模式取当前画面当环境光糊在播放器四周
- 编码偏好：分辨率优先，**同分辨率内 AV1 > HEVC > VP9 > H.264**（平台给 AV1 就用 AV1，体积约省一半）；
  浏览器解不了的编码自动让位，下载仍 `-c copy` 不转码
- 播放地址一律走 `/api/stream` 同源代理（平台 CDN 校验 Referer，浏览器直连会 404），支持 Range 拖动
- 图片代理 + 磁盘缓存（规避防盗链），SSE 事件 `video.new` / `sync.progress` / `job.updated`
- PWA：manifest + service worker，离线可打开外壳（HTTPS/反代由使用者负责）
- 管理页（`/manage`）：**统计**（平台 / 本地库 / 时长 / 清晰度 / 发布年份分布、入库趋势、热门标签、最近入库）+
  **关注管理**（搜索、排序、全部同步、启用开关、取消关注），整页只打一个 `GET /api/manage` 快照；
  订阅的写操作全在这里，艺人页是纯只读页（信息 + 同步进度 + 影片列表）

M2（本地库 + 下载队列 + Emby 元数据）已实现并实测通过：

- 多个本地库：目录可校验可写/剩余空间、命名模板、默认库、停用与删除（删库不动文件）
- 下载：影片卡片/影片页一键入队 → 并发分片下载 HLS 或直连 mp4 走 ffmpeg 封装，默认最高画质
- 队列：并发上限、暂停/继续/取消/重试、断点续传（分片复用到暂停后的恢复）、实时进度/速度/ETA（SSE）
- Emby 结构：`{库}/Pornhub/{艺人}/{片名}/{片名}.mp4` + `.nfo`（含标签/简介/评分）+ poster(1000x1500)/thumb(1280x720)/fanart(1920x1080)
- 下载前自动补全影片详情（简介/标签/评分），一键「再下载一份」到别的库
- **下载默认纯手动**：只有点卡片/详情页的下载按钮才会入队；设置里可选「同步到新片后自动下载」（默认关闭，首次全量回填永不自动下载）

M4 的 xHamster / beeg 适配器已实现并实测通过（详见下节）；新片通知、Emby API 自动扫库本轮不做。

## 多站适配器（xHamster / beeg）

各站自己维护自己的解析器，上层（订阅、同步、播放、下载、元数据）完全不区分平台。

- **xHamster**：页面上的数据全在 `window.initials` 这段 JSON 里（分类、艺人、影片列表、播放器设置），
  直接读 JSON 而不是爬 DOM——站点几乎每周都在改 class 名，JSON 结构反而稳。
  播放地址是密文：`standard.<编码>.auto` 里是十六进制串，头 5 字节给了 PRNG 算法与种子，
  剩下的是「明文 XOR 密钥流」；解出来是一个带 `_TPL_` 占位符的 master 清单模板，
  再按 `hostMap` 把域名换成本次可用的 CDN 节点（`video7.xhcdn.com` 之类）。
  站点同时提供 AV1 与 H.264 两套清单，两套都解析、合并后按「分辨率 → 编码 → 码率」排序，
  所以默认档常常直接是 **AV1**（体积约为 H.264 的一半）。
- **beeg**：站点自己的前端打的是 `store.externulls.com` 这组公开 JSON 接口，
  我们照着打同一组接口（艺人 `GET /tag?slug=`、影片列表 `GET /tag/videos/{slug}`、
  影片详情 `GET /facts/file/{id}`、播放地址 `GET /video/play_url/{id}`）。
  播放地址是「模板 + CDN 前缀」：拼成 master 清单地址后照常解析出各档清晰度。
  标签里的 `{...}` 是前端换图标的占位（`Big {Tits}`），入库时去掉括号。
- **两站的播放地址都带签名、几小时就过期**，所以每次播放都现解析一次（`ResolvePlayback`），
  不缓存、也不把地址写进库。
- **搜索只在名字写全时命中**：xHamster 的联想搜索接口被 Cloudflare 挡住（服务端过不去），
  beeg 也没有公开搜索，两家都退化成「按 slug 精确探测」——名字打全就能搜到，打半个查不到。
  粘贴主页链接永远可用。
- 代理：`PLATFORM_PROXY_XHAMSTER` / `PLATFORM_PROXY_BEEG` 或设置页里的专用代理，
  留空跟随全局代理；两家实测在国内可直连，所以「没配代理」不会在界面上报警。

未开始：M3（OpenList 上传 + strm + `/d/` 直链 spike）。

首屏预算：index 48.3 KB + CSS 9.4 KB ≈ 57.7 KB gzip；影片页再加载 84.0 KB 的播放器（Video.js 10 + 清晰度菜单 + 氛围模式 + 加载态），只有 HLS 片源才额外下载 173 KB 的 hls.js。

## 技术栈

Go 单二进制（web + worker + scheduler 同进程）+ SQLite(WAL) + 内嵌 Svelte 5 前端 + ffmpeg（remux/直链封装）。

## 快速开始

```bash
cp .env.example .env.local      # 已存在则跳过
openssl rand -base64 32         # 填入 APP_ENCRYPTION_KEY
make build                      # 构建前端 + 后端
./bin/myporn -root .            # 默认 http://localhost:8080
```

首次访问用一个密码登录（`APP_PASSWORD`，默认 `changeme`，请改掉）。

开发模式（前端热更新）：

```bash
./bin/myporn -root . &          # 后端 :8080
make dev-web                    # 前端 :5173，/api 已代理到 8080
```

## 目录

```
cmd/myporn/            入口：配置 → 存储 → 调度 → HTTP
internal/config/       环境变量与 .env.local
internal/store/        SQLite、迁移、各表查询
internal/platform/     适配器接口与注册表
internal/platform/pornhub/  Pornhub 解析（随站点改版维护的地方）
internal/platform/xhamster/  xHamster 解析（window.initials JSON + 播放地址解密）
internal/platform/beeg/      beeg 解析（store.externulls.com 公开接口）
internal/hls/                HLS master 清单解析（三个站共用，保证编码偏好一致）
internal/jobs/         任务队列：同步、下载、进度与取消
internal/events/       进程内事件中心（SSE）
internal/httpapi/      HTTP 接口、图片/媒体代理、静态资源
internal/webui/        前端构建产物（go:embed）
web/                   Svelte 5 前端源码
```

## 常用命令

```bash
make build    # 前端 + 后端
make backend  # 只编译后端
make web      # 只构建前端并同步到 embed 目录
make test     # Go 单元测试（解析器 fixture）
make vet
make run
```

补齐历史影片的详情元数据（标签、简介等，标签只有详情页才有）：

```bash
./bin/myporn -root . -backfill-meta
```

补艺人头像（本地没有头像的，按名字到 beeg 借一张）：

```bash
./bin/myporn -root . -backfill-avatar
```

## 真实网络冒烟测试（可选）

需要能访问 `cn.pornhub.com`，默认跳过，`LIVE=1` 才跑：

```bash
LIVE=1 go test ./internal/platform/pornhub/ -run TestLiveResolveStreams -v      # 清晰度解析
LIVE=1 go test ./internal/platform/pornhub/ -run TestLiveHLSSegmentDownload -v  # 分片下载 + ffmpeg 封装
LIVE=1 LIVE_VKEY=<viewkey> go test ./internal/platform/pornhub/ -run TestLive -v
```

## 调试解析器

```bash
./bin/myporn -root . -debug-parse "https://cn.pornhub.com/model/<slug>"
./bin/myporn -root . -debug-video "<viewkey>"
```

## 起播速度与缓存

播放地址的解析链路做了几处优化，实测（同一条影片）：

| 场景 | 优化前 | 现在 |
| --- | --- | --- |
| 首次解析（缓存为空） | 8–9 秒 | ~3 秒 |
| 5 分钟内的再次请求 | 8–9 秒 | ~10 毫秒 |
| 同步后打开最新几部 | 8–9 秒 | 基本 0（同步完后台预热） |

- 清晰度列表落库缓存 5 分钟（`video_streams.fetched_at`）；命中缓存直接返回，不再打上游。
- 播放走「快路径」：只解析能播的地址，不为了码率去逐个拉 `master.m3u8`（下载/体积预估才需要真实码率，那条路仍会补上）。
- 一个影片要打好几个上游地址，解析内部并发执行，并按 URL 去重。
- 每次同步结束后，后台预热该艺人最新 6 部影片的播放地址。
- 前端鼠标移到卡片上（或键盘聚焦）就先预取一次播放地址。

## 艺人头像从哪来

Pornhub 的头像只在艺人页面的 DOM 里，改版一次就抓不到（于是界面只能显示占位图）。
所以头像允许跨站借：本地没有头像时，按**艺人名**去 beeg 的公开接口查一次——

- `GET https://store.externulls.com/tag?slug=minidiva` 拿到 beeg 给这个人用的照片，
  拼成 `https://thumbs.externulls.com/photos/{id}/to.webp?crop_id={crop}`；
- **名字对不上就不用**（接口是按 slug 模糊查的，宁可留空也不放错人）；
- 命中一次就写进 `performers.avatar_url`，之后不再重查；查不到的会冷却 30 分钟再试。

触发时机：订阅时的预览与创建、每次同步开头、艺人详情/列表接口发现头像为空时（后台补，
不阻塞响应）。图片照旧走 `/api/img` 代理，前端要的宽度会被翻译成 beeg 图床的 `size_new`
参数，所以列表页拿的是 240/320 的小图，不是原图。

## 说明

- 本项目仅用于个人自用，只处理公开可访问内容，不绕过付费墙或 DRM。
- 播放统一由服务端代理（补 Referer/UA），因此浏览器不需要能直连平台 CDN。
- 数据库与 `APP_ENCRYPTION_KEY` 需一起备份，否则加密字段（Cookie / OpenList Token）无法解密。
