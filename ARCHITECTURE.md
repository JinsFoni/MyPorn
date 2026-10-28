# ADR-001 技术栈与架构决策

状态：已采纳（v1）
约束来源：REQUIREMENTS.md v0.3

---

## 1. 驱动因素（按权重排序）

| 权重 | 因素 | 含义 |
|---|---|---|
| P0 | **常驻内存要低** | 7×24 跑在家用 NAS/小主机，目标：应用进程空载 ≤60MB，整机（含 ffmpeg 瞬时）≤200MB |
| P0 | 长任务与请求隔离 | 抓取、GB 级 HLS 下载、OpenList 上传都是小时级任务，不能拖垮 Web 进程 |
| P1 | 抓取适配器迭代效率 | 站点改版是**长期主要维护成本**，解析代码必须改得快、测得快 |
| P1 | 吞吐与并发 | 2–8 路并发下载 + 同时浏览列表，不能互相阻塞 |
| P1 | 部署/备份简单 | 单镜像、单文件数据库、冷启动秒级 |
| P2 | 镜像体积 | NAS 磁盘有限，理想 ≤100MB |

## 2. 候选方案对比（内存为**估算值**，含运行时与 SQLite 基线，不含 ffmpeg）

| 方案 | 进程模型 | 空载常驻 | 2 路下载时 | 镜像 | 适配器迭代 | 判断 |
|---|---|---|---|---|---|---|
| **Go 单二进制** | web+worker+scheduler 同进程 | 35–60 MB | 55–80 MB | ~80 MB | 快（goquery + fixture 测试） | ✅ **采纳** |
| Rust (axum) | 同进程 | 15–35 MB | 30–50 MB | ~60 MB | 慢（编译/借用检查拖累解析迭代） | 次选：只赢 20MB |
| Node (Fastify) + worker | 2 进程 | 90–160 MB | 130–220 MB | ~180 MB | 快 | 否 |
| Next.js 单体 + worker | 2 进程 | 250–400 MB | 300–450 MB | ~400 MB | 最快 | 否 |
| Python (FastAPI) + worker | 2 进程 | 110–180 MB | 150–250 MB | ~250 MB | 快 | 否 |

**结论**：内存差 3–8 倍是硬差距，不是调优能追回的。Rust 更省但省下的 20MB 换不回适配器开发效率——而这套系统 80% 的后续工作量都在适配器上。

## 3. 技术选型

| 层 | 选型 | 理由 |
|---|---|---|
| 语言/运行时 | **Go 1.23** | 静态二进制、goroutine 天然适配多路下载、内存可控、交叉编译简单 |
| 进程模型 | **单二进制单进程**（无 broker） | 免去 Redis/RabbitMQ，任务队列直接用 SQLite 表 + goroutine 池 |
| HTTP | 标准库 `net/http` + `chi` | 零框架负担；chi 只做路由 |
| 数据库 | **SQLite（WAL）** + `database/sql` | 单用户规模足够；备份=复制文件；**默认 `modernc.org/sqlite`（纯 Go，无 cgo，构建简单）**，若压测写入不足可切 `mattn/go-sqlite3`（接口兼容，仅换驱动） |
| HTML 解析 | `goquery` + 可选的 json 提取（页面内嵌 `flashvars`/`window.__DATA`） | 选择器写法直观，fixture 单测成本低 |
| HLS 下载 | **自研分段下载器（Go）** + `ffmpeg -c copy` 仅做 remux | 见 §5，内存与可控性都优于 ffmpeg 直拉 |
| 媒体探测 | `ffprobe`（子进程，一次性） | 校验实际分辨率/时长，写进 nfo |
| 定时任务 | 进程内 `time.Ticker` + `subscriptions.next_sync_at` | 无需 cron 容器 |
| 实时进度 | **SSE**（`text/event-stream`） | 单向推送足够，反代友好，比 WebSocket 省连接管理 |
| 前端 | **Svelte 5 + Vite + Tailwind**，产物 `go:embed` 进二进制 | 编译期无运行时框架、包体小（详见 §7）；播放器用 **Video.js 10**（`@videojs/html` 自定义元素），hls.js 只在 HLS 片源时才下载 |
| 前端路由 | `svelte-spa-router`（或手写 hash 路由） | 够用、无 SSR 负担 |
| PWA | manifest + Service Worker（仅缓存 app shell） | 可加主屏；**不缓存视频** |
| 打包 | 多阶段 Dockerfile，`alpine` + `ffmpeg` | 最终镜像 ~80–100MB |
| 密码/加密 | `bcrypt`（密码）+ AES-GCM（Cookie/OpenList token 落库加密） | 敏感配置不明文存 DB |

## 4. 进程与并发模型

```
┌─────────────────────── single process ────────────────────────┐
│  HTTP server (net/http)  ──► /api/*, SSE /api/events, static   │
│  Scheduler (1 goroutine) ──► 每 60s 扫 next_sync_at → 入 jobs  │
│  Job Runner (worker pools)                                    │
│     ├─ fetch pool   (4)  抓取页面/详情/图片                     │
│     ├─ download pool(2)  HLS 分段下载 → remux                   │
│     └─ upload pool  (2)  OpenList 流式上传 → strm → 删本地      │
│  SQLite (WAL) ── jobs 表作为唯一队列，claim 用单条 UPDATE 事务  │
└───────────────────────────────────────────────────────────────┘
```

要点：
- **任务表即队列**：`UPDATE jobs SET status='running', started_at=? WHERE id=(SELECT id FROM jobs WHERE status='queued' ORDER BY priority,id LIMIT 1) RETURNING *` —— 原子领取，进程崩溃重启后把遗留 `running` 重置为 `queued`，天然可恢复。
- **内存安全**：所有媒体数据走 `io.Copy` / `io.Pipe` 流式，任何路径都不把视频读进内存；上传用 `io.SectionReader` 做分片。
- **隔离**：每个 job 有 `context.WithTimeout`，`recover()` 捕获 panic 并记为 failed，不拖垮进程。
- **限速**：`golang.org/x/time/rate` 做全局带宽令牌桶，下载/上传共享或独立。

## 5. 关键决策：为什么自己写 HLS 下载器

| 维度 | ffmpeg 直拉 m3u8 | 自研分段下载 + ffmpeg remux |
|---|---|---|
| 内存 | 随码率/并发上涨，不稳定（60–200MB/路） | 恒定低（每段 1–2MB 缓冲），~8–15MB |
| 断点续传 | 差（-reconnect 半吊子） | 原生：按分片落盘，重启续传 |
| 进度精度 | 难解析 | 精确到分片/字节 |
| 限速/并发连接 | 不可控 | 完全可控（默认 8 连接/文件） |
| 抗单连接限速 | 弱 | 强（多连接分段） |
| 成本 | 零代码 | 约 300 行 |

**流程**：解析 master → 选最高变体 → 并发拉 `init` 分片 + media 分片 → 顺序落盘到 `.part` → 完成后 `ffmpeg -c copy` 封装 mp4（不重编码，数秒、低内存）→ `ffprobe` 校验 → 失败回退 ffmpeg 直拉一次。

## 6. 性能预算与验收

| 指标 | 目标 | 验证方式 |
|---|---|---|
| 空载常驻内存 | ≤60 MB | `docker stats` 24h |
| 2 路下载 + 2 路上传 | ≤100 MB（不含 remux 瞬时） | 压测脚本 |
| 影片流首屏（1 万条库） | P95 <200 ms | 造 10k 数据实测 |
| 并发 100 短请求 | P95 <100 ms | `hey`/`oha` |
| 冷启动 | <1 s | 计时 |
| 镜像体积 | ≤100 MB | `docker images` |

## 7. 前端技术栈明细

### 7.1 结论

**Svelte 5（runes）+ TypeScript + Vite 6 + Tailwind CSS 4 + Video.js 10**，构建产物 `go:embed` 进二进制，无 SSR、无 Node 运行时。

### 7.2 框架对比（框架运行时体积为 gzip **估算**）

| 方案 | 框架体积 | 路由 | 状态 | 本项目适配度 | 结论 |
|---|---|---|---|---|---|
| **Svelte 5** | ~12 KB | 自写 60 行 | runes 内置 | 编译期生成直改 DOM，长列表滚动稳，单文件组件开发快 | ✅ 采纳 |
| SolidJS | ~8 KB | 需配 | signals 内置 | 性能同级，生态/资料少，团队接手成本高 | 备选 |
| Vue 3 | ~34 KB | vue-router | + Pinia | 成熟，但运行时大 2×，还得多引 2 个库 | 否 |
| React 19 | ~45 KB | react-router | + Zustand | 生态最大，但本项目无 SSR/生态依赖需求，纯负担 | 否 |
| htmx + Go 模板 | ~14 KB | 服务端 | 无 | 列表+SSE 很省，但播放器交互、PWA 预缓存策略写起来别扭 | 极简备选 |

选 Svelte 的核心理由：**本项目不需要 SSR**（单用户、无 SEO），所以 Next/SvelteKit 的价值为零；而 Svelte 的编译期产物让「手机浏览器 + 弱网」下的首屏与滚动体验最好，包体只有 React 方案的 1/3。

### 7.3 依赖清单（刻意极简）

- 运行依赖：`svelte`、`@videojs/html`（播放器外壳与皮肤）、`@videojs/hlsjs-video`（HLS 适配器，自带 hls.js）
- 开发依赖：`vite`、`@sveltejs/vite-plugin-svelte`、`typescript`、`svelte-check`、`tailwindcss` + `@tailwindcss/vite`、`vite-plugin-pwa`
- **不引入**：UI 组件库（有 Tailwind 够）、状态库（runes 内置）、axios（fetch 够）、表单库、图表库、zod（TS 类型手写对齐）

### 7.4 关键实现约定

| 关注点 | 方案 |
|---|---|
| 路由 | 手写 ~60 行 history router；Go 侧 `chi` 对非 `/api` 路径 fallback 到 `index.html` |
| 数据 | 原生 `fetch` + 手写 typed client（与 Go struct 对齐；二期可从 OpenAPI 生成） |
| 状态 | Svelte 5 runes（`$state/$derived/$effect`），跨页共享放 `.svelte.ts` 模块 |
| 实时进度 | `EventSource('/api/events')` 单连接，页面卸载即关；不用 WebSocket |
| 长列表 | IntersectionObserver 分页 + keyset 游标；先用「页淘汰」（离屏页卸载 DOM），万条库足够；真卡再上虚拟滚动 |
| 播放器 | **Video.js 10**：`<video-player>`+`<video-skin>` 自定义元素（皮肤自带进度条/音量/倍速/画中画/全屏/快捷键/手势），皮肤只用 `--media-*` 变量调成站点的单色冷调；**清晰度菜单**与**氛围模式**两个按钮挂进皮肤底部控制条（详见 §7.4.2），氛围光靠画布取帧 → 放大模糊，音量与氛围开关都记在 `localStorage`；mp4 用原生 `<video>`，HLS 用 `<hlsjs-video>` 且 `await import()` 按需加载；**编码偏好见 §7.4.1** |
| 图片 | `loading=lazy` + 宽高占位防跳动；缩略图走 `/api/img?u=`（后端代理 + 磁盘缓存，绕开 Referer/403；抓取/缓存/缩图全在 `internal/imgcache`，HTTP 处理函数只解析参数、发字节）；**媒体图（封面 / 头像 / 缩略图）一律经 `lib/img.ts` 的 `imageUrl()` 出地址**——那是唯一入口，隐私模式要在那里把参数从 `w=`（显示宽）换成 `s=`（小糊图边长），漏了它就等于在隐私模式里露原图；图标与平台 logo 不走它，保持清晰。媒体图仍标 `data-media`，但只是标记（便于审计哪些 `<img>` 是封面/头像），不再承担样式 |
| 封面预热 / 修复 | pornhub 的封面走 `pix-*.phncdn.com/…/rs:fit:320:180`，**地址带签名、一天就过期**（过期后 472；去掉签名参数是 470，换图床是 401——实测只有带新鲜签名才给图）。列表页懒加载，隔两天再点开某部片时地址早就死了，封面就永久空白。所以封面必须**趁地址还新鲜时抓进本地缓存**，之后不再依赖签名：① 同步时新片一入库就预热（`warmCovers`，一轮最多 40 张，后台跑）；② 启动后和每轮同步收尾各扫一遍「封面没进缓存」的影片补齐（`RepairCovers`，一轮最多 60 张、扫 400 条，同一时刻只跑一轮）；③ 补的时候先把**带签名（`hdnea=` / `validto=` / `//pix-`）的排最前**——这类「错过就没了」，缓存丢了就只能趁详情页还能给出新链接时救回来：抓不到就用 `GetVideo` 重解析详情页，地址变了就写回 `videos.thumb_url` 再抓一次（地址没变则不重试，免得每轮都打上游）。实测：清空 imgcache 冷启动，85 秒补回 60 张，其中 24 条过期签名封面全部换成新签名并落盘 | `internal/jobs/covers.go`、`internal/imgcache/imgcache.go`、`internal/store/videos.go` |
| 主题 | Tailwind 原子类，**暗色为默认**，移动端优先布局（视觉基线见 §7.6） |
| PWA | `vite-plugin-pwa`，`registerType: 'prompt'`，precache 仅 app shell（html/js/css/icon），**不缓存视频与 API 响应** |

### 7.6 视觉基线（参考 beeg.com）

极简单色片库风：黑底、白字、无彩色强调、无描边、无阴影。所有灰阶都由白色叠加透明度得到，改主题只需要换 `web/src/app.css` 里的 6 个变量。唯一的彩色例外是顶部那层环境光（`.canvas-glow`，beeg 同款底图）。

| 项 | 取值 | 文件 |
|---|---|---|
| 画布 | `#020408`（`--color-ink`，`html` / `body` / `theme-color` 都对齐这个值） | `web/src/app.css`、`web/index.html` |
| 面板 / 抬升 | 白 5% `--color-panel`、白 9% `--color-raise`、细线 `--color-line` | 同上 |
| 文字 | 白 95% 正文 / 60% 次要 / 40% 弱化；**白色即强调色**（`--color-hi`，选中态、主按钮） | 同上 |
| 语义色 | 仅两个：`--color-brick`（错误/危险）、`--color-sage`（已完成） | 同上 |
| 字体 | Roboto 400/500（正文与 UI，~44KB）+ **700（只给艺人卡姓名，~22KB，按需下载）**，都是 latin 单个 woff2；中文回落到系统 PingFang / Noto Sans SC | `web/src/app.css` |
| 字标 | 站名 `MyPorn` 用**品牌字** `--font-brand`（`font-brand`）= **Saira Extra Condensed 600**（`@fontsource/saira-extra-condensed`，latin 600 一个 woff2 ~20KB）——超窄、字距紧，对标 beeg.com 左上角那个又高又窄的 `beeg` 字标；只用在三处：桌面侧边栏顶（24px）、移动端顶栏（20px）、登录页（32px），**正文一律 Roboto**（别拿它排正文，窄体排中文会很难看） | `web/src/app.css`、`web/src/components/AppShell.svelte`、`web/src/routes/Login.svelte` |
| 字号 | 12 元信息 / 14 正文 / 16 小标题 / 20 页面标题（移动）/ 28 页面标题（桌面，字重 400） | `web/src/components/TopBar.svelte` 等 |
| 圆角 | 媒体与面板 12px（`rounded-xl`）、按钮与标签 9999px（药丸）、头像 50% | 全局 |
| 布局 | **桌面端**：**悬浮式**侧边导航——`fixed left-3 top-3 bottom-3`、240px 宽的圆角玻璃块（`rounded-xl` + 白 5% 底 + `backdrop-blur`，四边留 12px 空隙、不贴视口边缘，自身可滚动），内容区让出 `16.5rem`（`lg:pl-[16.5rem]`）；网格按「扣掉侧边栏后的剩余宽度」定档——1440 宽 4 列、1536 以上 5 列。导航图标一律 22px（`h-[1.375rem]`）、`viewBox 0 0 24 24`、描边 1.5，**按光学校齐**：图形外框统一落在 13.5–14.5 之间（`getBBox` 实测，占 24 方框的 55–60%）——「管理」那把扳手是斜的，原样画会顶满整个方框（19×19，比兄弟图标大 30%），所以单独缩到 `scale(0.75)` 并用 `non-scaling-stroke` 保持 1.5 描边，外框变成 14.25×14.25，和「下载」的 15×13.5 齐平；换任何新图标先看它的外框，别只看 `viewBox`。导航项有**关注 / 艺人 / 发现 / 下载 / 管理**五项，**栏底一行只放两枚无底色的 36px 圆形图标按钮**：**左下角是隐私模式**（眼睛／划掉的眼睛，开着时是白的）、**右下角是设置齿轮**（`aria-label` + `title` 兜住可访问性；未选中白 60%、激活时白 95%），不再显示实时连接状态（连接状态只在移动端顶栏留一个小圆点）。移动端没有侧边栏，隐私开关挪到顶栏右侧、连接状态点左边。隐私模式本身见「隐私模式」一行。齿轮点开的是**弹窗**而不是页面，见 §7.7。**移动端**：隐藏侧边栏，保留 48px 顶栏（`backdrop-blur` + 上深下淡的渐变底，免得压出一条硬边）与底部 6 个图标 tab（`grid-cols-6`，设置仍占一格，带文字） | `web/src/components/AppShell.svelte` |
| 环境光 | `.canvas-glow`：照搬 beeg 的实现——`web/src/assets/glow.webp`（1000×563、8KB，取自 `beeg.com/dist/assets/glow.webp`）铺满顶部 **49vh** 高的色带（`background-size: cover`），上面压一层 `linear-gradient(to bottom, rgba(0,0,0,.6), 画布色)`。`absolute` 定位，**跟着内容一起滚走**（不是 `fixed`）；承载容器必须 `isolate`，否则 `-z-10` 会被 `body` 的画布底色盖掉 | `web/src/app.css`、`web/src/assets/glow.webp`、`web/src/components/AppShell.svelte`、`web/src/routes/Login.svelte` |
| 卡片 | 封面（16:9、r12）→ 左上**时长纯文字**（`text-meta` + 三层 `text-shadow`，**不带胶囊底**，亮封面上也读得清；**新片白药丸已去掉**，封面角上只留时长一项）→ 36px 圆形艺人头像 + 标题 + `艺人 · 时间`；**不显示「已存」标识**（`hasLocalFile` 只用于下载按钮文案）。栅格 2 / 3 / 4 / 5 列（`sm` / `xl` / `2xl` 递增），间距 `gap-x-4 gap-y-7`——**列间距 16px 与 beeg 影片栅格的 16px 一致**；行间距 28px 比 beeg 的 12px 松，因为我们的文字块只有 ~48px 高、beeg 是 ~90px，行间留 28px 才有同等呼吸感 | `web/src/components/VideoCard.svelte`、`web/src/components/InfiniteList.svelte` |
| 隐私模式 | 桌面侧边栏**左下角**的眼睛按钮（移动端在顶栏右侧、连接状态点左边）：打开后**所有封面、头像、缩略图一律糊成一片**，**图标与平台 logo 不糊**。实现刻意**不用 CSS 滤镜**：`filter: blur()` 会给每张图各开一个合成层，滚动时新进视口的卡片还得重糊一遍（Chrome trace 实测 raster 时间多出近一半——「开着隐私模式滚动掉帧」就是这么来的）。改成**后端出小糊图**：`imageUrl()` 在隐私模式下把 `/api/img?u=…&w=480` 换成 `?u=…&s=120&sv=3`，后端把原图缩到 `s` 宽、面积平均 → 三遍盒式模糊 → 加一点抖动噪声 → **编码成 PNG**，浏览器把它拉伸开就是一片平滑的糊影。`s` 跟着显示宽度走（显示宽的四分之一，夹在 16–160）：120px 铺到 480px 是 4 倍放大，同一个比例给 36px 的小头像只剩 1.1 倍，所以模糊半径也按比例放大（宽/18、每遍），**糊的程度跟分辨率无关**。

「平滑」踩过三个坑，都不是「糊得不够」而是**台阶**：① **编码**——Go 的 jpeg 编码器只输出 4:2:0，一百来像素宽的图色度再减半，拉开后平滑区域全是彩色方块（「48px + JPEG q82」那版就是这样，观感像劣质 GIF），所以必须 PNG（无损 4:4:4，120 宽约 9KB，仍远小于它替换掉的 480 宽封面）；② **核**——单遍盒式模糊的核是个方框，半径一大轮廓上就有方台阶，叠三遍（方差相加）才像高斯；③ **量化**——8 位通道装不下「跨几百像素才降一级」的缓慢渐变，四倍放大后是一圈圈色带，落位前给每个像素加 ±1.5 的三角分布零均值噪声，台阶就化成细颗粒了。

代价：PNG 解码比 JPEG 慢，一屏两百来张的**首次**滚动会多花约 100–150ms 的 raster（送进合成器后就没了；实测第二轮 raster 反而是隐私模式更低：82ms vs 124ms），帧时间 p95 两边都是 16.7ms，看不出掉帧。小图有独立磁盘缓存（键里带 `s`）；已有原图缓存时直接在本地缩，不额外打上游；pornhub 的 `pix-*.phncdn.com` 封面是 **AVIF**（Go 的 stdlib 与 `x/image` 都没有解码器），这条路上再让 ffmpeg 转一道（同时最多 3 个进程；它只当解码器，输出**无损 PNG** 到 4×`s` 宽，不能再输出 JPEG——那等于缩图前先损一次），解不出来就发 1×1 透明占位图——隐私模式的失败方式只能是「看不见」，绝不能漏原图。组件完全不知道隐私模式的存在：开关只把 `data-privacy="on"` 打到 `<html>` 上（留给样式与排查），真正换图的是 `imageUrl()`。状态存本机 `localStorage`（`myporn.privacy`，不写后端——这是「这台设备此刻怎么显示」），模块一加载就落到 `<html>` 上，否则图片是异步来的、首屏会用原图闪一下。三处例外：① **影片页播放器的 `poster` 直接不给**（那是 `<video>` 自身的属性，没法只糊它不糊画面，宁可在解析片源那一两秒黑底 + 转环）；② **卡片悬停预览片不发**（鼠标一扫就播整段画面，比糊封面更露）；③ 首字母/占位兜底本来就是文字，不处理。按钮画的是**当前状态**：关着是眼睛、开着是划过一道斜线的眼睛，`aria-pressed` 同步，未选中白 60%、开着白 95%。 | `web/src/lib/privacy.svelte.ts`、`web/src/lib/img.ts`、`internal/imgcache/blur.go`、`web/src/components/AppShell.svelte`。改了生成方式记得同步 bump 小图缓存键（后端 `smallPipeline`）与 URL 上的 `sv=`，否则浏览器 HTTP 缓存里那份旧图还会被命中 |
| 平台标识 | 艺人主页标题旁边**直接放平台自己的 app icon**（`PlatformIcon.svelte`）：16px 方形（`sm` 档，见下）、`rounded-[22%]`、**保留原色**（这是全站唯一允许出现品牌色的地方之一，跟顶部环境光同级别）——pornhub = 橙底黑 `PH`（从站方 apple-touch-icon 抠出的橙色方块，64px PNG 2.2KB）、xhamster = 深色方砖 + 白色仓鼠头（站方 180px touch icon，64px PNG 5.8KB）、beeg = 官方 `favicon.svg`（渐变底 + 白色字标）；图标坐在一块 **毛玻璃圆角底座**上（`sm` 档 `h-6 w-6` + `rounded-[0.625rem]`＝10px、`md` 档 `h-8 w-8` + `rounded-xl`＝12px、`xs` 档 `h-5 w-5` + `rounded-[0.375rem]`＝6px，圆角比图标留出的那圈再舍一点，看着更软：半透明白色上下渐变 13%→5%、顶部一道内高光 `inset 0 1px 0 rgba(255,255,255,.14)`、`backdrop-blur-xl`、**四周不描边**——原先那圈 `ring-1 ring-inset ring-white/15` 已去掉：底座本来就比画布亮一档，边由明暗差给出来，画上白线反而像个按钮）——底座比图标每边大 4px，留出来那圈就是玻璃边；深色底上 blur 没有内容可糊，毛玻璃质感实际由那三层叠加给出，blur 保留是为了图标将来压在封面/图片上时也成立；**三档尺寸按并排文字的大小选**：`md` 32px 底座 + 24px 图标（预留给更大的标题，目前没人用）、`sm` 24px + 16px（艺人主页的 28px 艺名旁、管理页关注行的 14px 名字旁）、`xs` 20px + 12px（12px 元信息行，最近入库列表）；**艺人页那个 28px 艺名用的是 `sm` 而不是 `md`**：名字那一行只有 20px 高（对齐资料表的行距，见「艺人页资料」），32/24px 的底座会把行撑高、下面三行就整体错开，所以那里传 `-my-0.5` 把底座上下各 2px 的溢出抵掉，行高仍是 20px、底座中线仍落在名字的中线上（比名字的大写带上下各多 2px）；`alt` / `title` 仍是平台名，读屏与悬停都不丢信息；**不认识的平台回退成文字药丸**（带 `href` 时就变成可点的文字药丸）。**艺人主页上这个图标本身就是外链**（传 `href={performer.url}`，`target=_blank` + `rel=noreferrer noopener`，悬停时底座从 13%→5% 提到 22%→12% 提示可点，`aria-label` =「打开{平台}主页」），所以原来动作行里那个**「平台主页」文字按钮已去掉**：平台图标就贴在艺人名字后面，想去平台主页点它最自然，不用在一排同步按钮里再认出第四个按钮。影片页的「平台」是键值行，仍显示文字。（第一版做的是单色剪影，已按需求改成原色 app icon。） | `web/src/components/PlatformIcon.svelte`、`web/src/assets/platform/`、`web/src/routes/Performer.svelte` |
| 头像条 | 关注页顶部（`TopBar` 之上，因为关注页外壳标题为空）横向滚动的**已订阅艺人头像条**：72px 圆头像（`h-18 w-18`）+ 96px 宽的姓名（`text-meta`，单行截断），**不显示任何计数**；点某个头像进 `/performer/<id>`。容器 `flex gap-3 overflow-x-auto no-scrollbar`，用 `-mx-3 px-3 sm:-mx-4 sm:px-4 lg:mx-0 lg:px-0` 做贴边滚动，**首列头像仍与内容左边缘（296 / 12px）对齐**；按 `lastSyncAt` 倒序，数据来自 `GET /api/subscriptions`，首屏给同尺寸灰圆占位避免跳动，请求失败静默隐藏（只是入口，不打扰影片流） | `web/src/components/PerformerRail.svelte`、`web/src/routes/Feed.svelte` |
| 艺人卡 | 圆形头像 **104px（移动）/ 128px（`sm` 以上）**——桌面端这个尺寸对标 X（推特）个人页头像的 ~133px，比早先的 88/96px 明显大一号；**尺寸由栅格轨道决定**——网格是定宽轨道 `grid-cols-[repeat(auto-fill,6.5rem)] sm:grid-cols-[repeat(auto-fill,8rem)]`，卡里的头像 `w-full` 正好填满一格、不再居中留白，所以**第一列头像的左边缘与 `TopBar` 标题对齐**；列数按可用宽度自动算出，间距只由 `gap-x-6 / sm:gap-x-8` 与 `gap-y-8` 决定（旧做法是响应式列数 3–8 + 头像 `mx-auto` 封顶，格子比头像宽，头像被"缩"进格子里，跟标题错位、也显得间距大）。**卡上不显示新片计数**（右上角那个白药丸已按需求去掉，`newVideoCount` 只留在 `aria-label` 里给读屏）；姓名**加粗**（`font-bold` / Roboto 700，对标 X 上用户名是粗体）且最多两行（`line-clamp-2`，一格只有 104/128px 宽），状态行 `mt-auto` 贴底，同一行卡片的状态行仍对齐 | `web/src/components/PerformerCard.svelte`、`web/src/routes/Performers.svelte` |
| 艺人页资料 | 艺人页头**就是一块面板**，从上到下三层：① 一行 `grid`——头像（**跟艺人卡同一口径：`h-26`＝104px，`sm` 起 `h-32`＝128px**，不再单独放大到 160px；`self-center`，**水平中线落在右边「名字栏 + 资料表」那组的中线上**——那组自己也 `self-center`，资料表的中线因此跟头像共线） │ 名字 + 平台图标 + `N部影片 · N订阅` + **身材** + **排名**（`#269`） │ 资料行网格（`xl` 起跟名字栏并排，窄屏 `col-span-2` 跨整行掉到头像下面）。**名字栏按资料表的行距排**：名字/计数/身材/排名四行各占 20px 行高（就是 `text-ui` 的行高；艺名 28px 用 `leading-5` 压到同一行高——Roboto 的大写高 0.711em ≈ 19.9px 正好填满这一行，上下溢出的是留白、不占位）+ `gap-y-2` 的 8px 行距，四行总高 104px，正好跟四行资料表（4×20 + 3×8）一样高；`xl` 起名字栏和资料表装进同一个 `xl:grid … items-start` 的容器，两边行起点贴同一条上沿，于是**名字 / 计数 / 身材 / 排名逐行对上资料表的第 1 / 2 / 3 / 4 行**（资料表两列六行时也一样，前四行照旧对齐）。窄屏时这层容器是 `display: contents`，名字栏和资料表仍是页头栅格的两个直接子项——资料表才跨得了两列、掉到头像下面，不用为此把名字栏和资料表分开写两遍；② 一道 `border-line` 发丝线；③ 简介（左：「简介」dim 标签 + 站点原文，`whitespace-pre-line` 保留换行、`text-pretty` 收掉「只剩一个 emoji」的孤字行）＋ 站外链接图标（右，**只有图标没有文字**，`xl` 起才跟简介同一行，否则掉到下一行左对齐）。页面上不再有卡片，整页因此少两块面板的高度。**字号比全站默认大一档**（用户要求「资料卡片里的字体改大一点」）：计数行与身材 12→`text-ui`(14)、资料行标签 12→`text-ui`(14)、简介 12→`text-ui`(14)、药丸 12→`text-ui`(14)；资料行值与简介正文保持 `text-ui`(14)，信息量靠颜色分（标签 `text-dim`、值 `text-fg`、简介 `text-muted`）而不是字号；**艺名不动**，仍是 `text-hero`(28px)。资料行**标签前挂一枚 16px 单色字形**（`ProfileIcon.svelte`：按标签关键词挑字形——**感情＝大小两枚心**、取向＝双人、**性别＝♀／♂（按值分：`女性` 画 ♀，`男性` 画 ♂）**、出身地＝定位针、身高＝上下箭头、**体重＝哑铃**、三围/罩杯＝尺子、**种族＝三角+方块+圆（三个基本形）**、语言/国籍＝地球、眼睛＝眼睛、**发色＝一束长发**、其他颜色＝水滴、**纹身＝六角星**、**穿环＝带珠的环**、**胸部＝胸罩**、爱好＝星、年份/出道/年龄＝日历，认不出的兜底一个圆点；页头的 `影片` 与 `排名` 两行也挂同一枚组件——**清单条 + 带播放三角的画面**和**皇冠**）。尺寸口径跟 peach 的资料卡一致：**16px 方框、24 网格、`stroke-width:2`、间距 6px**（那边 `[data-meta-item] > svg` 就是 16/2），字形用 `align-[-0.15em]` 内联对齐，`dt` 保持行内流才跟 `dd` 的基线对齐（改成 flex 会让基线上移到图标底边）。字形有四种来历：① **用户给的 iconfont 图标**（1024 网格、**填充**、无描边）——躯干（身材）、长发（发色）、双心（感情）、三个基本形（种族）、六角星（纹身），那几段上万字符的 `d` 住在 `web/src/lib/profileGlyphs.ts`，画框一律是量出来的**紧框**（iconfont 在画布四周留的呼吸量各不相同，照原画布等比缩，细碎的那几枚在 16px 下会糊），按较宽的那一边撑满 16px 方框：躯干 10.9×16、发色 15.3×16、感情 16×13、种族 15.3×16、纹身 14.1×16；② **照用户给的 PNG 重画的线稿**——胸罩（24px 图：肩带、两个罩杯、下围、中间合成 V）、带珠的环（30px 图：一个环下沿穿一颗实心珠）和页头的影片计数（24px 图：一条 14×2 的清单条 + 20×14 的方框 + 内侧实心播放三角，原件本来就是 24 网格 2px 边，坐标和描边 1:1 照抄，只把清单条两端和方框四角从 `round` 改回 `butt`/`miter`），原图是位图，放大后逐行量出骨点再按 2 描边落成矢量；③ **取自 peach 雪碧图**——哑铃 `#i-dumbbell`、♂ `#i-mars`、皇冠 `#i-crown`（`web/index.html`）；④ 其余是本项目自己那套线稿（取向、定位针、身高、尺子、地球、眼睛、水滴、标签、星、日历）。标签列因此仍是 `5rem`——图标 16 + 间距 6 + 最长的「出身地」42 ≈ 64px；整行高度仍是 20px（16px 字形落在 `text-ui` 的 20px 行里，四行读数照样跟资料表逐行对上）。**标签与值在显示层才改口径**（`web/src/lib/profile.ts` 的 `displayProfile`）：感情状态→感情、喜欢→取向、假奶子→胸部、是否有穿环→穿环、兴趣爱好→爱好，「是／否」本地化成「有／无」（胸部反着来：否＝天然、是＝假奶），体重只留公斤（`111lbs. (50kg)`→`50 kg`，只给磅才按 0.45359237 换算）。库里存的仍是站上原文——改说法不用重扫，跟「身材」那种抓取期规整（`body_json` 存数字、接口只发数字）不是一回事：那是模型层的事实，这里是页面用语。**资料行按列读**：`@sm` 起 `grid-flow-col` + `grid-template-rows: repeat(var(--rows-2|-3), auto)`，行数在组件里按 `ceil(项数/列数)` 算好塞进 `--rows-2` / `--rows-3`（两列一套、三列一套，给各自的容器断点用），前 N 项竖着填满第一列再换列；窄到一列时退回默认的行优先，顺序仍与站点一致。**排名**（`#269`）取艺人主页「排名信息」块里的**模特排名**（`.rankingInfo` 里第一个 `.infoBox`，同块的本周/本月/上月/年度不收），单独存 `performers.model_rank`，取不到就是 0、页面不渲染。**身材**（`167cm · B89 · W64 · H91`）是资料行的归一结果：抓取时认出「身高」「三围」并换算成厘米（身高优先用站点自己写的 `167cm`，只给英制才按 2.54 换算；三围 `35-25-36` 这种靠量级判断单位后换算），**认出来的两行从 `profile` 里摘掉**，同一条数据不在页面上出现两次；认不出来的（如 `1.67 m`）原样留在资料行里。数据全部来自艺人主页自身：简介取 `.aboutMeSection`（`ld+json` 里没有 description，早先那里一直是空的）、资料行取 `.js-infoText .infoPiece`（`span` 是标签、`.smallInfo` 是值，**按站点顺序全量收**，字段因艺人而异——有人有罩杯/眼睛颜色，有人只有性别出身地）、链接取 `.socialList li a`（标签用站点自己的 `.socialText`：`官方网址` / `Twitter` / `Instagram`，站点没给标签时按域名兜底成 `Linktree` 这种名字；只收 `http(s)`，前端一律 `target=_blank` + `rel=noreferrer noopener`）。**解析阶段主动丢掉五类行**：用户明确不要的「会勾起我性欲的是」「会浇灭我性欲的是」，以及「视频播放量 / 主页浏览量 / 已看视频」这三行平台统计（不是个人资料，页头已经有影片数与订阅数；顺带挡住一个坑——`视频播放量` 含「视频」二字，不挡会被当成影片总数）。**不建固定列**：`performers.profile_json` / `social_json` 各存一个 JSON 数组（`[{label,value}]` / `[{label,url}]`），`body_json` 存身材对象（`{heightCm,bust,waist,hips}`，缺项 0），站点加字段不用迁移；同步时整体覆盖，页面没给内容时保留库里旧值。资料行列数按**这一栏自己的宽度**算（`@container` 写在 `dl` 的外层，`@sm`≈24rem 两列、`@3xl`≈48rem 三列）——它 `xl` 分栏后在右侧只有 ~500px，窄屏跨整行时又有 700px 以上，同一份代码两边都得排得好看；`@container` 写在自己的 `dl` 上永远匹配不到（容器查询只看祖先容器）。站外链接**只剩图标**（用户给的三个品牌小标：官方网址＝OnlyFans 蓝标、Twitter＝X 方牌、Instagram＝彩色方牌）：底座 32px 圆形命中区、图标 22px（间距 `gap-2`＝8px，图标之间净空 18px），`title` + `aria-label` 里还带着站点给的标签（悬停看得见、读屏读得到，只是不占版面），`-my-1.5` 把 32px 底座收进正文那一行的 20px 行高里、图标中线落在正文第一行的中线上；辨认靠域名（twitter/x、instagram、onlyfans 或标签含「官方」），认不出的域名退回原来的地球线稿，不硬套只有官方网址才用得上的品牌标。**这两枚原色标是全站唯一的彩色**——之前那条「不引品牌原色」的规矩按用户要求让位：X 那枚原图是「黑色方牌挖空一个 X」，直接放上去就是一坨比画布还黑、看不出形状的方块，所以只对它做一次 `invert`（黑牌变白牌、X 变黑）。 | `web/src/components/PerformerProfile.svelte`（资料行）、`web/src/components/ProfileIcon.svelte`（资料行字形）、`web/src/lib/profile.ts`（资料行口径）、`web/src/components/SocialLinks.svelte`（站外链接图标）、`web/src/routes/Performer.svelte`、`internal/model/body.go`、`internal/platform/pornhub/parse.go`、`internal/store/migrations/0004_performer_profile.sql`、`internal/store/migrations/0005_performer_body.sql`、`internal/store/migrations/0006_performer_rank.sql` |
| 管理页 | **统计 + 关注管理**一页（`/manage`，侧边栏第五项 / 移动端第五格）：整页只打**一个请求**（`GET /api/manage`），上面那排读数、下面的分项与关注列表都是**同一份快照**，不会这块新那块旧（参考 peach 的统计页做法）。结构是「六格读数条 → 页签 → 明细」：读数条 `grid-cols-2 sm:grid-cols-3 xl:grid-cols-6`（关注艺人 / 已收录影片 / 已落盘 / 占用空间 / 下载完成 / 同步异常），每格是名字 + 大数 + 一句脚注（数字用 `tabular-nums`）；页签是药丸分段控件（**统计 / 关注管理**），**不进地址栏**——两个页签看的是同一份数据，刷新回「统计」。统计页＝横向条形排行（`BarList.svelte`：平台、本地库、时长、最高清晰度、发布年份，条形只用白色、按本组最大值归一，**计数为 0 的档不画**）＋ 入库趋势柱状图 ＋ 下载任务读数 ＋ 热门标签药丸 ＋ 最近入库 8 条；关注管理页＝搜索框 + 排序药丸（最近同步 / 未看 / 影片数 / 名字）+ 全部同步 + 每行（40px 圆头像、名字、平台图标、`本地 N 部 · 已落盘 M（体积） · 未看 K`、上次/下次同步、错误文案、同步按钮、启用开关、取消关注）。**取消关注点第一次只把按钮换成「确认取消」**（5 秒后自动退回），不弹窗；「正在同步」读 SSE 的 `live.isSyncing(performerId)`，同步完成时 `live.feedVersion` +1 会让本页静默重取快照。**观看进度**（`resume_sec`）暂时没进统计：播放器还没上报续播位置，这一档永远全是 0，等续播做出来再加。**订阅相关的写操作全部收在这一页**（同步 / 启停 / 取消关注）；艺人主页因此变成**纯只读页**——原来那排动作按钮（立即同步、全量回填、暂停同步、取消订阅）连同「全量回填？」「取消订阅？」两个确认弹窗一起删掉了。艺人页现在是「一块面板＝头像 + 名字/平台图标/元信息/身材 + 资料行，一道发丝线，然后是简介 + 社交药丸；下面是影片列表」，元信息**只留 `N部影片 · N订阅`**（`未看`、上次/下次同步、`已暂停` / `同步出错` 药丸、同步进度条全部去掉）；「N部影片」用 `localVideoCount`（本地收录数，和下面列表一一对应）而不是平台的 `videoCount`——Pornhub 资料页根本没有影片总数，只有播放量与订阅者。**同步进度改在关注管理里就地显示**：正在同步的那一行把「上次/下次同步」换成进度条（`回填历史 · 第 3/18 页` + 1px 白色进度条 + `已入库 42 部`，页数未知时是 1/3 宽的脉冲条），读的是 SSE 的 `live.syncState(performerId)`，20 秒没新事件自动收起。 | `web/src/routes/Manage.svelte`、`web/src/components/StatTile.svelte`、`web/src/components/BarList.svelte`、`web/src/components/SubscriptionRow.svelte`、`internal/store/stats.go`、`internal/httpapi/handlers_manage.go` |
| 复用类 | `.btn` / `.btn-primary`（白底黑字药丸）/ `.btn-ghost` / `.btn-danger` / `.field` / `.panel` / `.chip` / `.label-meta` | `web/src/app.css` |

约定：交互元素一律药丸，媒体与容器一律 12px 圆角；不用边框区分层级，只靠白 5%/9% 的底与间距（悬浮侧边栏也一样：不描边，靠 12px 圆角 + 四边空隙 + 毛玻璃从画布里"浮"起来）；流式页面里的**艺人页**标题由外壳 `TopBar` 渲染（**关注页没有标题**，顶部直接是订阅艺人头像条），**发现 / 下载 / 管理三页是居中单栏**（`mx-auto` + `max-w-2xl / 5xl`），标题连同内容一起放进这一栏里，所以外壳标题留空（**设置不是页面，是弹窗**，见 §7.7）；详情页（影片/艺人）也不渲染外壳标题，改由页面自己带标题；**影片播放页和艺人主页顶部都不再有「返回」那一行**——内容（播放器 / 艺人信息）直接顶到内容区上沿，回退靠浏览器后退、侧边栏或移动端底部 tab（只剩错误态里那个「返回关注 / 返回艺人列表」按钮）。

### 7.4.1 编码偏好：分辨率优先，同分辨率内 AV1 优先

平台给的直链里可能同一分辨率有多个编码（H.264 + AV1 各一条）。选择顺序固定为
**分辨率降序 → 编码偏好（AV1 > HEVC > VP9 > H.264）→ 码率降序**，
唯一定义在 `model.SortVariantsDesc`，三个 adapter、下载任务、播放接口都走它，
不会出现「播放用 AV1、下载却拿 H.264」。

- **分辨率永远第一位**：1080p 的 H.264 仍然排在 720p 的 AV1 前面——不为了省体积降一档清晰度。
- **同分辨率内才谈编码**：1080p AV1（2.2 Mbps）压过 1080p H.264（4.5 Mbps），体积小一半而观感相当。
- **前端有第二道闸**：`canDecodeCodec()` 用 `MediaSource.isTypeSupported` 探一次（按编码族缓存结果），
  浏览器解不了的 AV1/HEVC 直接排到最后，`codecTag()` 也就不显示角标——「优先 AV1」不能变成「黑屏」。
- **变体 id 带编码后缀**：同一分辨率两种编码如果都叫 `1080p`，前端下拉框会撞值，
  所以非 H.264 的 id 是 `1080p.av1` / `1080p.hevc`；H.264 保持 `1080p`，老会话记住的档位不受影响。
- **下载下来的编码就是源编码**：仍然 `ffmpeg -c copy` 不转码，AV1 原样落盘；
  Emby 客户端对 AV1 的直接播放支持参差不齐，遇到播不了的会由 Emby 转码（这是接受了的代价，见 §10）。
- **当前实况**：Pornhub 只给 H.264；xHamster 同时给 AV1 与 H.264 两套清单，**默认档因此常常就是 AV1**
  （实测同片 720p：AV1 1.4 Mbps vs H.264 1.9 Mbps，浏览器能解就直接播、也能直接下）；beeg 目前只给 H.264。
  低分辨率档的 id 也按高度分开（`240p` / `144p`），不再一律叫 `sd`——否则四档低清晰度会撞成同一个 id。

### 7.4.2 播放器实现：Video.js 10 自定义元素

选择 Video.js **10**（`@videojs/html`），不是 v8 也不是 React 版：v10 把「状态」和「渲染器」拆开，
UI 是一组自带 shadow DOM 的自定义元素，Svelte 直接当标签用，皮肤自带一整套控件，不用自己撸进度条。

- **结构**：`<video-player>`（`display: contents`，只做 store 边界）› `<video-skin>`（媒体容器 + 控件 + 快捷键 + 手势）› 媒体元素。
  页面里的圆角/裁剪由外层的 `relative overflow-hidden rounded-xl` 负责，皮肤自己的圆角设 0。
  **加载态与页面浮层**（播放失败）也挂在 `<video-skin>` 里（走它的默认 `<slot>`，和媒体元素并列）：
  全屏元素就是 skin 本身，放外面全屏后它就跟着消失了。
- **中文化**：`<media-i18n>` 包一层，locale 从 `<html lang="zh-CN">` 继承，`@videojs/html` 自带 zh-CN 语言包
  （`画质 / 播放速度 / 字幕 / 静音 / 全屏` 等都是中文）。
- **皮肤样式只能靠变量**：`--media-accent-color`（白 `#f5faff`）、`--media-accent-text-color`（画布黑）、
  `--media-font-family`（Roboto）、`--media-border-radius`、`--media-border-color`，其余交给皮肤，不改它的内部类名。
- **片源策略不变**：每个清晰度仍是**一条独立清单/直链**，切换时读 store 的进度再 `seek` 回去续播。
  皮肤里那个「画质」菜单因此只有当前这一条（自动 + 当前档），不拿它当主入口——**清晰度菜单自己画**：
  分档列表来自后端（顺序即 §7.4.1 的排序），每行 `1080p` + `4.0 Mbps · 最高`，当前档打勾；侧边栏那个下拉框因此删了，
  右侧「清晰度」一行只读显示当前档。这样 §7.4.1 的排序与「默认最高画质」都由后端说了算。
- **两个自绘按钮进底部控制条**（`lib/playerBar.ts`）：皮肤的整套控件在自己的 shadow root 里，`<video-skin>` 只留了一个默认插槽，
  没有「往控制条里加按钮」的口子，公开的 `--media-*` 也只能改颜色/圆角/字体。所以想在底部那一行、和自带的音量/设置/画中画/全屏
  并排，只能把节点**挂进它 shadow root 里的 `.video-controls-secondary`**（右侧按钮组）。挂进去之后是真正的条形成员：
  控件的显隐/上浮/毛玻璃底色/全屏缩放（`--media-scale-unit`）全都自动跟着走，不用再自己数 2.5 秒收回，
  全屏也照样在（全屏元素就是 skin）。两处只依赖皮肤的类名与 `media-*` 公开组件：`.video-controls-secondary` 和
  `.media-button` / `.media-menu-*`；皮肤结构升级导致挂不上时返回 `null`、上层按 50ms 重试 40 次，最坏结果是这两个按钮不出现。
  - **清晰度**：一个显示当前档的按钮（`1080p ⌄`）+ 皮肤自己的 `<media-menu>` 弹层（挂在按钮上方、右对齐，靠 `commandfor` 绑定，
    所以 `aria-expanded`、Esc、点外面关闭、打开时锁住控件不隐藏都由皮肤负责）。列表项用 `<media-menu-item class="media-menu-radio-item">`，
    每行两行文案（`1080p` / `4.0 Mbps · 最高`）+ 当前档的对勾；点一项 → `select` 事件 → 交回页面换片源。
  - **氛围模式**：太阳图标，`aria-pressed` 表示开关，亮/暗用图标透明度表达（不用皮肤那套 `[aria-expanded]` 白底，避免和菜单状态混淆）。
  - 位置：桌面上排在设置齿轮右边、画中画/全屏左边；手机（`< 32rem`）皮肤把这组按钮整体收进右上角的浮动药丸，这两个跟着一起过去，
    底部那行才留得下进度条。
- **氛围模式**（存 `myporn.ambient`，默认开、系统「减少动效」时默认关）：把正在播的那一帧画到
  **32×18 的 `<canvas>`** 上，再靠 CSS 放大 + `blur(28→48px)` 铺到播放器四周当环境光。
  采样优先用 `requestVideoFrameCallback`（有新帧才画，暂停/后台自动停），没有就 rAF + 15fps 节流；
  暂停时靠 `seeked`/`loadeddata` 各补一帧。跨源片源会污染画布，但只 `drawImage` 不读像素，所以照常能糊出来。
  `hlsjs-video` 的内层 `<video>` 在它的 shadow root 里，采样前要先挖出来。光晕画布在裁剪层**外面**，
  这样能溢出到播放器四周；全屏时它自然落在全屏元素之外（全屏不需要环境光）。
- **两个媒体元素**：全是直链 mp4 用原生 `<video>`（连 hls.js 都不下载），只要有一条 HLS 就整页切 `<hlsjs-video>`
  （hls.js 引擎，也能放 mp4，切档后仍是同一个元素）。`engine` 由 `needsHlsEngine()` 在拿到片源时定一次。
- **自动起播的坑**：原生 `<video>` 设完 `src` 立刻 `play()` 就能成；HLS 要等清单解析、MediaSource 挂上，
  之前的 `play()` 会被 `AbortError` 打断。所以首次起播走 `kickPlay()`：120ms 一次重试，最多 40 次，
  被浏览器拦自动播放（`NotAllowedError`）就停在海报上等用户点。
- **别让画面变暗**：皮肤在控件出现时会盖一层**全画幅**暗场（`--media-controls-gradient` 默认 上 50% / 中 20% / 下 40% 黑），
  于是鼠标一动、控制条一露，整块画面就暗下去（实测同一帧平均亮度 118 → 86）。把 `--media-controls-gradient` 改成只在底部
  留一条 26% 高的暗角（`rgb(0 0 0 / .3) → transparent`）：中间和上面一点不动，控制条后面仍然有垫底的渐变。
  这个变量是皮肤自己声明的，只能写在 `.media-container` 的行内样式上（`tuneSkin()`），写在 `<video-skin>` 上会被它的
  `@layer base.preset` 规则盖掉。
- **加载态**（`VideoLoading.svelte`，全站唯一一份）：解析片源、缓冲、切清晰度、拖动进度条卡住时，画面不留在黑屏上，
  而是把一张图糊成一片环境光垫底 + 中间一个细环（2.75rem、1/3 圆弧、0.9s 一圈，`prefers-reduced-motion` 时改成呼吸）。
  四件事一起做：
  - **底图由播放器决定**，组件只负责糊：还没出过画面（首次解析、页面骨架屏）时垫封面；播放中缓冲（拖动进度条、
    卡网络）时垫**上一帧**——所以你看到的是刚才那个画面糊开，不会突然跳回封面。两种底图用 `kind` 分开调，
    因为「要等好几秒」和「卡了一下」是两件事：
  - **封面态（`kind='poster'`）几乎不压暗**：这一步是冷启动里最长的一段（yt-dlp 解析 5.8–7.7 s，之后还要下第一段分片），
    画面必须一眼看出「这是那部片子的封面」。原来两种底图共用一套参数（`blur(6px)` + 径向暗角 40%），
    而这批封面的平均亮度本来就只有 60/255（室内、夜戏居多）——糊完压完，播放器区域实测只剩 40/255，
    看着就是一整块黑加一个小转环（用户报的「起播黑屏」）。现在封面态只糊 3px、提一点亮度（`brightness(1.12)`）、
    暗角收到 22%，同一个画面实测 110–168/255，封面上是谁、在干什么都还认得出。
    **画面上一个字都不写**：只用转环，等得值不值得是封面和转环的事，多一行灰字只是把注意力从封面拽走
    （原来那行「正在解析最高画质…」还带 400 ms 延迟淡入，一秒内就出画面的情况会闪一下半句）。读屏文案仍留着，
    走 `label` 的 sr-only。缓冲态（`kind='frame'`）保持下面这套重糊。
  - **封面取不到不许留一块黑**：后端对抓不到的封面发的是 1×1 透明 PNG（不是 4xx），所以 `onerror` 之外还要看
    `naturalWidth ≤ 1`，命中了就换 `posterFallback`（`maxresdefault` → `hqdefault`）；再没有就露出组件自己的
    冷调渐变底（`radial-gradient`，不是纯黑）——一块纯黑和一张还没出来的图在观感上是两件事。
  - **上一帧是自己抓的，不是靠 `backdrop-filter` 采样视频**：视频是独立的合成层，backdrop 能不能采样到它看浏览器、
    解码路径和当前有没有帧，实测同一串操作有时糊出上一帧、有时糊出黑的——拖动进度条正好撞上「黑」就是一次黑屏事故。
    现在每 250ms（`timeupdate`、`seeked`、`waiting` 时各补一次，节流）把画面缩到 640×360 存进一块离屏画布，
    盖加载层时再 `toDataURL('image/jpeg', .55)` 定格一张给组件当底图：一定是真画面，跟合成器怎么排布无关。
    编码只在每次开始缓冲时做一次，640×360 的 JPEG 也只要几毫秒，看不出卡顿；底图再小就成了「被放大的图」（320 宽铺满一千多像素 = 放大 4 倍），
    所以宁可编码大一点。
  - **糊是设计的一部分**，不是「等图加载完直接铺满」：封面源图经常只有几百像素宽，放大到一千多像素本来就是糊的；
    缓冲态用 `blur(6px)` 盖满、再压一层径向暗角，观感跟氛围模式同一套语言，视觉上像「镜头正在对上焦」。
    模糊量是调出来的：22px 时细节全没了、看着像一张被硬拉大的图；6px 刚好「细节全化开、还认得出画面」。
    底图**不许再放大**（原来是 `transform: scale(1.08)`）：那 8% 会让缓冲时糊的这帧明显比播放中的画面大一圈，
    真画面出来再收回去，就是肉眼可见的「画面缩了一下」。现在改成把外层盒子往外撑 6px（`inset: -6px` +
    `width/height: calc(100% + 12px)`，还要 `max-width: none`，否则 Tailwind 的 `img { max-width: 100% }` 会把宽度夹回去），
    实际相当于放大 2%——刚好够把 blur 的晕开藏进被 `overflow: hidden` 裁掉的那一圈里，又小到看不出与播放尺寸的差别。
    6px 是量出来的：白底画面第一列的亮度，不外扩是 91/169（一圈 12px 的暗边，亮画面上看得出来），
    外扩 6px 是 146/169（2~3px 的软边，看不出来），外扩 12px 是 165/169（干净但放大 4%）。
    手机上播放器只有 200 出头高，同样 6px 的糊按比例算等于桌面的三倍、外扩占的份额也翻倍，所以 `max-width: 639px` 时
    糊和外扩一起折半成 3px（放大 2.9%）。
    环本身带一圈 `drop-shadow` 暗晕，压在亮画面上也看得清。
  - **什么时候收放**：`canplay` / `playing` / `loadeddata` 立刻收（淡出 200ms）；`waiting` 要持续 240ms 才盖上来，
    免得普通拖动（数据就在本地）也闪一下；`timeupdate` 且 `readyState >= 3` 作为兜底，个别引擎只发 `waiting` 不发 `playing`。
    自动播放被浏览器拦下时停在 `canplay`——就是海报 + 皮肤的大播放键，不能一直转圈。
  - **跳进已缓冲的那一段不盖**：`waiting` 里先看目标时间（`currentTime`，拖动时就是手指所在的位置）在不在 `video.buffered` 里
    （留 0.3s 尾巴余量，正好卡在缓冲区末尾时最后几帧可能还没解出来）。在的话把等待门槛从 240ms 放宽到 1.2s，
    并且定时器真正触发时只要 `readyState >= 3` 就直接放弃——数据在本地，这一跳本来就该「接着放」，
    把一次毫无停顿的跳转说成「正在缓冲」比不盖还糟。真卡住（解码没跟上）才轮到加载态兜底。
  这一层 `pointer-events: none`，盖着的时候暂停/全屏照样点得到。
  **底层的 `.video-veil` 底色不能写成不透明**（`var(--color-ink)`）：那就等于把底图整个盖掉，缓冲时看见就是一整块黑。
  **这一层只淡出、不淡入**（`out:fade`，不是 `transition:fade`）：淡入会让底下的黑盒子先露 2~4 帧，
  起播时那一下「黑闪」就是它。
  **这一层的 `z-index` 只能是 5，必须压在控制条下面**：皮肤的控制条内容 `z-index: 30`、它那条暗角是 `10`，
  而加载层盖的只是「画面」。取高了（一开始是 40）拖动进度条时，进度条本身、时间气泡、清晰度/全屏按钮会一起被糊图吃掉——
  手指按在一条看不见的进度条上，是最难受的一种「消失了」。同理皮肤自带的缓冲指示器
  （`.media-buffering-indicator`，带自己的转环）也多余：跟我们那个环叠在一起、粗细和转速还不一样，
  所以往 shadow root 里塞一条 `style[data-myporn-skin]` 把它 `display: none` 掉
  （和 `playerBar` 注入按钮同一招；非分层样式压得住它的 `@layer`）。两者吃的是同一批事件（`waiting` / `seeking`），关掉不丢状态。
- **进度条的「已缓冲」段必须自己调色**：默认皮肤把轨槽 `.media-slider-track:before` 和已缓冲
  `.media-slider-buffer:before` 画成**同一个** `color-mix(in oklab, currentcolor 20%, transparent)`，
  两层又完全重合、圆角和高度都一样——「已缓冲」跟空槽同色，等于不存在（用户报的「当前播放位置后面基本没有
  显示已缓冲的部分」）。几何本身是对的：`--media-slider-buffer` 是**占总时长**的百分比，实测一条 684 秒的片子
  稳定缓冲在 25–32 秒（约每 6 秒推进一格），只有总长的 4.4%，466px 的轨道上约 20px。所以同一段
  `style[data-myporn-skin]` 里把已缓冲提到 `62%`：空槽 20% → 已缓冲 62% → 已播 100%，三档分得开。
  **45% 不够**：轨道直接压在画面上，亮帧时 45% 和 20% 一起糊成浅色（实测亮帧上 206 对 121，45% 那版两块都是 196/171）；
  62% 在亮帧/暗帧两边都读得出来。浏览器自己能缓多少不归我们管——`src=` 的渐进下载缓冲深度是 Chrome 的启发式，
  30 秒上下，想再深只能换成 MediaSource（hls.js）自己排缓冲，为这点收益不值得。
- **错误处理**：皮肤自带错误弹窗会跟本站的「重新解析 / 用代理播放」浮层打架，所以订阅 `store.error` 后立刻
  `dismissError()`，把文案交给页面自己的浮层。
- **快捷键**：皮肤自带一套（空格/K、←→、↑↓、M 静音、F 全屏、0-9 百分比、单击暂停、双击左右 ±10s、双击中间全屏），
  挂在皮肤 shadow root 的 `.media-container` 上；页面再挂一个 window 监听，让焦点不在播放器里时也能用同一套键。
  两侧的步长统一由 `SEEK_STEP`／`VOLUME_STEP` 两个常量决定，现在是 **←→ ±5s、↑↓ ±10%**（`j`/`l` 跟箭头同一个动作，一起改）：
  - 皮肤那套是 shadow root 里一串 `<media-hotkey keys= action= value=>`，`value` 就是单次步长，不写就走包里的默认值
    （`seekStep` 按方向 ±10s、`volumeStep` ±5%，见 `@videojs/core` 的 `getMediaInputActionValue`）。所以不用禁用它重写一套，
    只要在 `tuneSkin()` 里按 `keys` 把 `value` 改成站点的值；元素自己 `update()` 会重新注册监听。
  - 「这次按键归谁管」看**合成路径里有没有皮肤那个 shadow root**，不能看 `host.contains()`：焦点在皮肤内部控件上时
    事件会穿过 shadow root（皮肤管，页面监听直接返回）；但焦点落在 `<video-skin>` 宿主元素本身时（点过播放器再点空白处），
    事件根本不进 shadow DOM，皮肤那套监听不会触发——按 `contains` 判断会连页面监听一起跳过，表现为**左右键完全没反应**。
- **音量记忆**：存 `localStorage` 的 `myporn.player.volume`（`{volume, muted}`），但必须等媒体就绪（`canPlay`）再套用，
  否则会被播放器默认的满音量盖掉，并且会把默认值写回去污染记忆。**没存过也要从第一下就开始记**：媒体就绪时
  把当前值当作基线写进 `lastVolume`，否则「第一次调音量」这条路根本走不到写入分支。

### 7.5 包体预算

| 范围 | 目标（gzip，KiB） | 实测（2026-10） |
|---|---|---|
| 首屏列表页（index + CSS） | ≤60 KB | 48.3 + 9.4 = 57.7 KB |
| 视频页（Video.js 10 播放器 + 清晰度菜单/氛围模式/加载态，不含 HLS 引擎） | ≤95 KB | 84.2 KB |
| 仅 HLS 片源额外下载（hls.js 适配器） | ≤180 KB | 167.1 KB |
| 全部路由合计 | ≤300 KB | ≈ 300 KB |

### 7.6 PWA 的前置坑（必须知道）

Service Worker 要求 **secure context**：从 `http://192.168.x.x` 这类局域网 IP 访问时**无法注册 SW**，PWA 装不上。

- 解法：反代加 HTTPS（Caddy 自签证书 / 内网证书），或在本机用 `localhost` 访问；
- 不解决也能用：降级为普通网页 + 主屏书签，播放/下载/任务全部正常，只是没有离线壳与独立窗口。

### 7.7 设置弹窗（分类 + 侧边栏）

设置不是页面，是**一个能盖在任何页面上的弹窗**：侧边栏右下角的齿轮、下载页的「管理本地库」、
下载对话框里的「去创建库」都只是调用 `ui.openSettings(pane)`（`web/src/lib/ui.svelte.ts` 里的 runes store），
由 `App.svelte` 挂着的 `<SettingsDialog />` 统一渲染。

- **布局**：`max-w-4xl` 的圆角面板，**高度固定** `h-[min(80dvh,38rem)]`（900px 高的窗口上是 608px）（不跟内容变，切分类时不跳高度，内容多了在右侧内部滚），左边 208px 分类栏（`bg-ink/50`，比面板略暗）+ 右边滚动内容，页脚固定在底部；
  没有描边没有投影，靠 `bg-sheet`（不透明面板色）压在 `bg-ink/80` + 毛玻璃的遮罩上分层。
  **面板底色必须不透明**——用 `bg-raise` 这类半透明表面的话，背后页面的模糊会整片透上来。
- **分类**：同步 / 网络与代理 / 上传 / 下载 / 本地库 / 账号。右侧只渲染当前分类（不滚动整页找区块），
  表头显示分类名 + 一句说明；`<sm` 没有左侧栏，换成一条可横滑的胶囊分类条。
- **表单**：前四个分类共用一个 `<form id="settings-form">`，保存按钮在页脚，用 `form="settings-form"` 挂回去；
  本地库（`LibraryPanel`，内部自带 `<form>`）与账号两个分类不在 form 里，页脚也不出现保存按钮。
- **交互**：`Esc` / 点遮罩 / 右上角 ✕ 关闭（保存中不关），打开时锁 `body` 滚动并把焦点交给弹窗；
  齿轮的 `aria-expanded` 与白色高亮跟随弹窗开关。
- **URL**：地址栏是 `/settings`，分类写在 hash 里（`/settings#upload`、`#libraries`…），所以能收藏、能分享，
  刷新后还停在原分类上。地址是唯一事实来源：`App.svelte` 里一个 effect 盯着路由——变成 `/settings` 就打开弹窗
  （切换分类用 `history.replaceState` 改 hash，不堆历史），离开 `/settings` 就静默关掉。
  - 打开时 `navigate('/settings')` push 一条历史 ⇒ **浏览器后退等于关弹窗**；
  - 关闭（Esc / 遮罩 / ✕ / 页脚）用 `navigate(from, { replace: true })` 回到打开前那一页，不留 `/settings` 残余。
  - 弹窗背后保留最后渲染的那个真实页面（`lastPage`），不重新挂载、不重新拉数据。

---

## 8. 代码结构

```
cmd/myporn/main.go            # 启动：config → store → scheduler → http
internal/config/              # env + settings 读取（敏感字段解密）
internal/store/               # sqlite, migrations/*.sql, 各表 repo
internal/platform/            # Adapter 接口
    pornhub/ xhamster/ beeg/  # 每站一目录：parse.go + testdata/*.html
internal/jobs/                # runner(claim/retry), fetch, download, upload
internal/hls/                 # master 解析（Pornhub/xHamster/beeg 共用）+ 分段下载器
internal/media/               # ffmpeg/ffprobe 封装、nfo 生成、图片处理、命名模板
internal/openlist/            # OpenList API client（put/mkdir/list/链接）
internal/httpapi/             # handlers + SSE + auth 中间件
internal/webui/               # go:embed dist/*
web/                          # Svelte 源码，构建输出到 internal/webui/dist
```

## 9. OpenList 集成（已确认参数）

- 地址：`http://192.168.0.108:5244`（内网直连，无需代理）
- 根路径：`/123云盘/资源目录/MyPorn`
- Token：存 `.env.local` / DB（AES-GCM 加密），**不入库文档、不进 git**
- 调用：`/api/fs/mkdir`、`/api/fs/put`（流式 PUT）、`/api/fs/list`、`/api/fs/get`
- strm 直链候选（M3 需 1 天 spike 实测）：
  1. `{base}/d/{path}?token={token}` —— 首选，Emby 侧无需额外配置
  2. 公开分享链接
  3. 本站反代 `{ours}/s/{id}`（兜底，吃带宽）
- **风险**：123 云盘驱动可能限速或有防盗链，直链 302 到云厂商 CDN；若 Emby 拖动体验差，改用方案 3 并加 Range 透传。

## 10. 已知取舍（明确接受）

- 不做 SSR/SEO —— 单用户应用，Svelte SPA 足够。
- 不做多实例/水平扩展 —— SQLite + 单进程是刻意选择，简化为美德。
- 前端不用 SSR/SSG 框架 —— 单用户应用无 SEO 需求，Svelte SPA + `go:embed` 比 SvelteKit 少一层构建与适配器配置。
- 前端不引 UI 组件库 —— Tailwind + 少量自研组件，避免包体膨胀。
- 若未来要多用户公网部署，需迁 Postgres + 引入 Redis 队列，属于重写级别变更。
- 优先 AV1 的代价：落盘文件可能让部分 Emby 客户端播不了（走 Emby 转码），换取同画质约一半的体积；
  真要「绝对能播」，把 `model.CodecRank` 里 av1 的次序挪到 h264 之后即可，一行改动。

## 11. 艺人头像：允许跨站借图

**问题**：Pornhub 的艺人头像只存在于主页 DOM 里，选择器随改版失效，抓不到就只剩占位图。

**决策**：头像与「平台归属」解耦，引入可选接口 `platform.AvatarProvider`：

```go
type AvatarProvider interface {
	Key() string
	ResolveAvatar(ctx context.Context, performerName string) (string, error)
}
```

来源按注册顺序依次尝试，命中即止：

1. `internal/platform/beeg` —— `GET store.externulls.com/tag?slug=<名字压扁>`，
   取该 tag 的 `thumbs[0].id` + `crops[0].id` 拼成 `thumbs.externulls.com/photos/{id}/to.webp?crop_id=…`。
2. `internal/platform/xcom` —— beeg 上查得到人却没有照片（`thumbs` 为空）时退到 X：
   `GET x.com/<猜的账号名>` 的未登录 HTML，从 `og:title`（`显示名 (@handle) on X`）
   和 `og:image`（`pbs.twimg.com` 的 400×400 头像）里取图。

- **必须名字对得上**才采用，宁可留空也不放错人：
  beeg 用 slug 折叠变音符号后全等；X 的账号名只能猜（去空格拼一起 / 下划线连起来，
  且不超过 15 字符），再用页面上的显示名按词比对——一方的词全在另一方里才算同一个人
  （`Charming Milana` ↔ 显示名 `Milana` 命中，同名路人 `Stark` 被挡掉）。
  默认头像（`abs.twimg.com/sticky/default_profile_images`）视为没查到。
- 结果写回 `performers.avatar_url`，命中即不再重查；失败冷却 30 分钟。
- 触发点：订阅预览/创建、每次同步开头、艺人详情与列表接口发现头像为空时后台补（不阻塞响应）。
  手动重试：`./myporn -backfill-avatar`。
- 图片仍走 `/api/img` 代理：Referer 按域名特判（beeg 图床拒绝自家根域 Referer，只认 `beeg.com`），
  跨站图床用各自的代理配置（`externulls.com` → `PLATFORM_PROXY_BEEG`，`twimg.com` → `PLATFORM_PROXY_X`，
  未配置时回落到 `GLOBAL_PROXY`），前端 `w=` 会被翻译成图床的 `size_new` 参数，列表页只拉小图。
  抓取/磁盘缓存/小糊图这一整套在 `internal/imgcache`（`Bytes` / `Small` / `Warm` / `Cached` 四个入口），
  HTTP 代理与同步任务共用同一份缓存目录，别再往 `httpapi` 里塞图片逻辑。

**取舍**：接受「头像可能来自另一个站点」，换取头像不再随平台改版而丢失；
代价是名字匹配不上时仍然没有头像（此时保持占位图，不做模糊猜测）。
X 的账号名猜不出来的（艺名拼起来超过 15 字符、账号名里加了数字或自创缩写之类）同样放弃。

## 12. 多站适配器：xHamster 与 beeg

上层（订阅、同步、播放、下载、元数据）完全不认平台，差异全压在 `internal/platform/<站>/` 里。
两站的取数方式刻意不同，都是「哪条路最不容易被改版打断就走哪条」。

### 12.1 xHamster：读 `window.initials`，播放地址要解密

- **数据源**：艺人页、影片页、列表页的真实数据都在页面里一段 `window.initials = {...}` 的 JSON 里
  （`infoComponent` 艺人资料、`aboutMeComponent` 简介与资料行、`trendingVideoSectionComponent` 影片列表、
  `paginationComponent` 页数、`xplayerSettings` 播放器设置）。**直接解析这段 JSON，不碰 DOM**：
  CSS class 几乎每次改版都变，JSON 结构一年也不动。
  花括号配平扫描即可取出（不能按行正则切，JSON 里有 `}`），并限长 8 MB。
- **播放地址是密文**：`xplayerSettings.sources.standard.<codec>[quality=auto].url` 给的是十六进制串，
  前 5 字节是「PRNG 算法号（1 字节）+ 种子（4 字节小端）」，其后每字节与密钥流 XOR。
  7 种 PRNG（LCG/移位类/乘法异或）各一份实现，解出来是一段 master 清单模板地址。
- **主机要换**：模板里的 host 是占位，按 `hostMap["standard.<codec>.auto.prime"]` 换成本次可用的
  CDN 节点（`video7.xhcdn.com` 之类），只换第一段主机名、端口保留。
- **两套清单都要**：站点把 AV1 与 H.264 分成两个 master（`_TPL_.av1.mp4.m3u8` / `_TPL_.h264.mp4.m3u8`），
  两条都解析、合并后交给 `model.SortVariantsDesc`；清单里的 `CODECS` 缺失时用来源 codec 补上。
  拿不到清单时退到 `downloadDropdownComponent.sources.mp4`（144p–720p 直链）。
- **影片列表一律带 `/newest`**：`/pornstars/<slug>` 默认是评分序（`sort:"rated"`），第 1 页最上面
  是几年前的老片，最新的几部甚至不在我们用的那个分区里（`newestVideoSectionComponent` 只有 4 条）。
  而增量同步是「遇到已入库影片即停」——排序不对就等于老片一挡，**新片永远发现不了**。
  所以列表固定请求 `/pornstars/<slug>/newest`（第 N 页 `/newest/<n>`，`sort:"added"`、按 `created` 倒序、
  可翻页）；2026-10 实测把 `mysaaat` 从「38 条按评分排」纠正成「42 条按时间排」，补回漏掉的 4 部新片。
  顺带记一笔：`/full-length` 不是「完整视频」过滤器而是站点的「长片」分类（riley-reid 上只剩 34 条且不可翻页），
  竖版短视频另有 `/shorts` 页——默认列表与 `/newest` 本来就是横版长片（缩略图 526×298、时长 2 分钟起）。
- **搜索退化**：`/api/front/search/suggest` 被 Cloudflare 挡住（带 csrf token、Referer、Cookie 也是 403），
  `/pornstars?search=` 会被忽略并返回完整 A–Z 列表。所以站内搜索只做「按 slug 精确探测」：
  名字打全（`Riley Reid` → `riley-reid`）能搜到，报出的名字还必须与输入一致，否则当没搜到。

### 12.2 beeg：打它自己前端用的公开 JSON 接口

| 用途 | 请求 |
|---|---|
| 艺人资料 | `GET store.externulls.com/tag?slug=<slug>&get_original=true&with_relations=true` |
| 影片列表 | `GET store.externulls.com/tag/videos/<小写 slug>?limit=48&offset=N` |
| 影片详情 | `GET store.externulls.com/facts/file/<fileId>` |
| 播放地址 | `GET store.externulls.com/video/play_url/<fileId>`（纯文本路径，前面补 `https://video.beeg.com`） |

- slug **大小写不敏感**（`MiniDiva` / `minidiva` 都 200），但**短横线不行**（`riley-reid` → 404），
  所以搜索只把名字压扁去空格、不做连字符化。
- 播放地址给的是 master 清单的相对路径，拼上 CDN 前缀后与其它两站共用 `internal/hls.ParseMasterPlaylist`。
- 标签里的 `{}` 是前端换图标的占位（`Big {Tits}`），入库前去掉括号，避免和「Big Tits」变成两个标签。
- 缩略图在 `thumbs.externulls.com`，该图床**拒绝自家根域的 Referer**，只认 `https://beeg.com/`
  （`refererOverrides` 里特判）。

### 12.3 共同约定

- **播放地址现解现用**：两家都带签名、几小时过期（`end=…`），所以 `ResolvePlayback` 每次都重新解析，
  不缓存、不落库。
- **限速**：每站 500ms 起步 + 0–300ms 抖动；beeg 客户端遇到 429/5xx 歇半秒重试一次
  （同步任务与浏览撞车时会遇到）。
- **失败不拖累界面**：搜索接口失败返回 502 + 平台名，界面只显示提示，不影响已有数据。
- **平台状态提示**：只有「从国内直连一般不通」的 Pornhub 在没配代理时才报 `no_proxy`，
  xHamster / beeg 实测可直连，不再无意义地报警。
- **播放必须过 `/api/stream` 转发**：平台 CDN 校验 Referer/UA，浏览器直连只会 404；服务端补头转发，
  HLS 清单就地改写（分片/子清单/`#EXT-X-MAP` 里的 `URI=` 都换成同源代理地址）。
  清单里的主机可能**不止一个**：xHamster 1080p AV1 那条清单共 319 段，只有 init 段和前两个分片在
  `video7.xhcdn.com`，其余 317 段都在 `ip<数字>.ahcdn.com`——`streamHostSuffixes` 漏掉哪个后缀，
  播放就会「头 8 秒正常、之后每段 400 非法的播放地址」。
- **详情页的播放列表＝本地档 + 线上档**：`GET /api/videos/{id}/playback` 把本地库那一路（若已下载）
  排第一并设为默认，再追加线上解析出来的每个清晰度；**只有两边都空**才是真的没得播
  （只有不注册 adapter 的 YouTube 会走到）。判断时别忘了线上档位是在循环里才追加进返回列表的。

### 12.4 艺人资料：beeg / xHamster 整份借 Pornhub 的

**问题**：beeg 与 xHamster 的艺人页资料很薄——一个名字、一张头像、两个站内统计
（影片数、关注数），简介最多一句当 tag 简介用，**没有**资料标签行、三围、社交链接、排名。
同一个艺人在 Pornhub 上通常有完整档案。

**决策**：新增可选接口 `platform.ProfileProvider`，与头像借图同一套路（名字对得上才采用）：

```go
type ProfileProvider interface {
	Key() string
	ResolveProfile(ctx context.Context, performerName string) (*model.Performer, error)
}
```

- 目前只有 `internal/platform/pornhub` 实现它，注册进 `platform.ProfileRegistry`。
- 找人的顺序：① 按名字推 Pornhub 主页标识（小写、空格换连字符，再试一把压扁连写的）
  直接取 `/model/<slug>`，页面名字归一后（只留字母数字，`Mini Diva` == `MiniDiva`）对得上就采用；
  ② 标识猜不中再走 `/pornstars/search?search=<名字>`，只挑名字/标识完全对得上的那一位。
  名字对不上就报错返回，**绝不取第一个**——给别人安错简介比留空糟得多。
- 换什么、留什么由 `model.Performer.AdoptProfileFrom` 统一口径：
  - 换：简介、资料标签行、三围、社交链接、模特排名；
  - 留：平台、平台标识、主页链接、名字、影片数、订阅数（后两个是**本站这个平台**的统计，
    把 Pornhub 的 1.4M 订阅挂到 beeg 的艺人页上没有意义）；
  - 头像只在本地为空时才借：本站自己的头像是站点正在用的那张，比跨站抠的准；
  - 资料来源的简介如果只是一串网址（Pornhub 上不少老艺人就写个 OnlyFans 地址，
    Riley Reid 是 `Onlyfans.com/rileyreidx3`），不覆盖本网站已有的真简介。
- 触发点：订阅创建、订阅预览、每次同步开头（`jobs.ProfileFiller`）。
  一次性返工老数据：`./myporn -backfill-profile`。
- 顺带修掉的坑：Pornhub 的 `/model/search?search=` 现在会 301 到随机 `/users/<hash>` 主页，
  按链接文字抓名字会抓出一堆无关的人；`/pornstars?search=` 的参数被忽略（永远回热门榜）。
  搜索统一走 `/pornstars/search?search=`，解析 `ul#pornstarsSearchResult` 里的结果卡片
  （结果在前，侧栏热门艳星在后，按 slug 去重），名字不再是 `Gia Derza thumbnail` 这种。

**取舍**：接受「beeg / xHamster 的艺人页展示的是 Pornhub 的档案内容」，
换取资料完整度；代价是两家平台的站内统计与 Pornhub 的编排方式不同步
（例如 Pornhub 的「排名 #269」出现在 beeg 的艺人页上）。

**资料行的取舍口径**：`model.DropProfileLabel` 里那几行（职业状态、工作起始及结束时间）
是**任何平台都不入库**的——解析时丢、写库时再兜一次、老数据在启动时清一次
（`store.pruneProfileLabels`，幂等）。要改这个口径只需改 `model` 里那一张表，不用重扫艺人。


## 13. bilibili：WBI 签名 + 分轨合流

站点形态与另外三家完全不同：没有页面可解析，数据全在 JSON 接口里；而接口分两种脾气——
资料 / 详情 / 播放地址（`/x/web-interface/card`、`/x/web-interface/view`、`/x/player/playurl`）
随便调；空间投稿列表则是全站风控最严的一个，但它怕的不是服务端，而是**没签名**：
同一 IP、同一份 Cookie、同一时刻，未签名的 `/x/space/arc/search` 一页之后 `-799`（再探变 412，
且每探一次像在续期），签名的 `/x/space/wbi/arc/search` 连打 15 页 3.3 秒零失败。
所以列表分「签名主路 / 未签名兜底」，签名细节见 `internal/platform/bilibili/wbi.go`（含官方向量与实测数据）。

### 13.1 列表：增量走 App，回填走 wbi 签名

```
增量（每 6h，第 1 页）  App  /x/v2/space/archive/cursor?appkey=…&sign=md5(q+appsec)
                          无风控；固定 20 条；offset/pn/page 一律忽略（访客拿不到第 2 页）
回填（断点续翻）        wbi  /x/space/wbi/arc/search?ps=20&pn=N&wts=&w_rid=
                          mixin key 取自 nav 的 wbi_img 图片名，缓存 30 分钟；-352 换 key 重试一次
兜底                    web  /x/space/arc/search?ps=20&pn=N  → 1 次 / 20 秒 + 30s/60s 退避
```

签名是 `md5(按键排序的查询串 + mixin key)`，mixin key 是把 nav 给的两张图片名（各 32 位）
拼起来按 64 项固定表重排后取前 32 位。参考实现：[bili-sync](https://github.com/amtoaer/bili-sync)。
它没有 `dm_*` 指纹参数，也不需要真实浏览器 TLS 指纹——之前「服务端调 wbi 一律 -352」的判断是错的。

- 两个来源的页大小都取 **20**：App 接口只肯给 20（传 `ps=30/50` 还是 20），web 侧迁就它。
  页大小不一致会让「第 1 页 20 条 + 第 2 页从第 21 条起」错位，中间那 30 条永远补不上。
- App 接口的签名是 `md5(按 key 排序的查询串 + appSecret)`，`appkey/appSecret` 是安卓客户端里的公开常量，
  不是破解什么；解析出的 20 条里 `goto != "av"` 的（番剧等）直接丢——它们点开播不了。

### 13.2 限流不是错误，是节流

新增跨站哨兵 `platform.ErrRateLimited`（和 `platform.ErrNotFound` 同一套路，各家把私有错误包在它外面），
同步任务对它**不给红色**：

| 情况 | 行为 |
|---|---|
| 第 1 页就被限流 | 任务失败（这一轮什么都没拿到），退避 **3 小时**（其它错误 30 分钟） |
| 翻页途中被限流 | 任务成功：已抓入库、断点留在最后一页、文案「同步被限流中断，新增 N 部（断点已存，稍后自动接着翻）」 |

3 小时这个数字来自实测：B 站的封是滚动的，半小时探一次的节奏足够给它续期，不如歇远一点。

### 13.3 播放：单文件直出，分轨现场合流

`playurl` 同时给 `durl[]`（单文件 mp4）和 `dash{}`（音视频两条独立 m4s）。未登录时 durl 到 720P、
dash 只有 360/480P。合并规则：

- durl 一档照留（单文件，`/api/stream` 直接播，不占合流）；
- DASH 里**与 durl 同分辨率的那一档跳过**（否则下拉框里两条一模一样的 720p），更低/更高的照留。

浏览器播不了分轨，所以 `Variant.AudioURL` 非空时前端拿到的是
`/api/videos/{id}/remux?v=<variantID>`，服务端合流一次后当普通文件播：

```
handler → muxcache.Ensure(key="<videoID>-<variantID>")
            命中磁盘 → http.ServeFile（Range 由标准库处理）
            未命中   → ffmpeg -i video -i audio -map 0:v:0 -map 1:a:0? -c copy -movflags +faststart
                       单飞（浏览器起播会并发打好几个 Range 请求）
                       跑在 context.Background() 上（调用方断开不打断已开始的工作）
                       TTL 7 天，10 分钟扫一次过期
```

签名地址几小时就过期，所以合流前会（在 produce 回调里）重新解析一次播放地址；成品按 key 缓存，
过期的是**上游地址**而不是成品。下载复用同一判定：`best.AudioURL != ""` 就走
`Encoder.RemuxFromURLs`（两路输入 + 每路各自的 `Referer`），估算体积用「视频带宽 + 音轨带宽」
（实测 150,153,388 估算 vs 150,109,750 实际，差 0.03%）。

### 13.4 画质与登录态

未登录时接口照样把 4K / 1080P 列在 `accept_quality` 里，但 `dash.video` 只有 360/480P、
durl 只到 720P——**以实际返回为准**。1080P 以上要填 `SESSDATA`（设置 → B 站），
和 YouTube 的 `yt_cookies` 同一个位置、同一套「加密存、只在用的时候读」的处理。
`normalizeBiliCookie` 认三种输入：整段 Cookie 头、裸的 SESSDATA 值、换行/制表符分隔的键值对，但必须含 SESSDATA。

### 13.5 独立一页，但不另建表

bilibili 和 YouTube 是同一个位置的东西——「订阅一个内容源」，不是「追一个艺人」。所以两者
都有自己的页面（`/bilibili`、`/youtube`）和管理页签，并且在 `/api/videos`（关注流）、
`/api/videos/new`、`/api/performers`（艺人页）、`/api/subscriptions`（关注管理）里被排除，
保证同一件事只有一处入口。

但**没有为它另建 `bili_*` 表**。UP 主就是 `performers` 的一行（`platform='bilibili'`、
`performer_id` = mid），投稿就是 `videos` 的一行；`internal/store/bilibili.go` 只是在
这两张表上按平台做视图（列表、未看计数、已看、未看片单）。理由：

- 一旦有 `bili_videos` 和 `videos` 两份记录，播放（`/api/videos/{id}/playback`）、合流
  （`/remux`）、下载入库（`library_files` / nfo / strm）、统计就都要分叉成两套，同一个 bug 修两遍；
- 现有 `videos` 表已经容得下它：`platform` + `platform_video_id` + `published_at` 就是全部差异，
  连 `is_new` 都正好对应「未看」（B 站是抓到时入库，YouTube 是下载时入库，两条时间轴天然不同）；
- 反过来，YouTube 当年那套 `yt_*` 表是**因为它先有了「频道」这个独立概念**（一个频道 = 多个视频、
  且视频要在入库前筛一遍要不要下）。B 站不需要：UP 主有 `performers` 行，投稿直接是 `videos`。

代价是取数多一层「按平台过滤」，所以每个通用列表接口都显式带着这个条件，并各自有测试
（`internal/store/bilibili_test.go` 里 `TestFeedPlatformFilter` 就是这个约束的看门人）。

### 13.6 筛选状态写在地址栏（?up= / ?channel= / ?mode=）

两页的「选中哪位 UP 主 / 哪个频道」和「全部 / 未看 / 收藏」都不存在组件自己的 `$state` 里当唯一真源，
而是写在 URL 上：`/bilibili?up=10&mode=unseen`、`/youtube?channel=5&mode=saved`。
理由是 B 站墙上点开影片是**整页跳转**（`/video/:id`），退回时路由组件会重新创建，
内存里的选中态就没了——「翻了三屏、选了某位 UP 主，点开一部再退回来，全没了」。

- 写：`patchQuery()`（`lib/router.svelte.ts`）只改指定参数并**默认 replace**，所以换筛选条件不堆历史；
  点影片才是 push。YouTube 的播放弹窗 `?v=` 也走它，因此打开影片不会把 `channel=` 甩掉。
- 读：组件初始化时读一次；另有 `$effect` 盯着 `router.search`，后退/前进/贴链接进来也能跟上
  （值相等就跳过，所以「点胶囊 → 改地址栏 → effect 再跑一次」不会打转）。
- 详情页的「返回」也认来源：卡片链接带 `?back=<编码后的整条路径+查询>`，
  `/video/:id` 据此渲染「返回 B 站 / 返回艺人 / 返回关注」，比写死的「返回关注」靠谱；
  「相关影片」的卡片不带这个参数（否则会链回自己），跨站请求也只认站内绝对路径。
