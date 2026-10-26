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
| 播放器 | **Video.js 10**：`<video-player>`+`<video-skin>` 自定义元素（皮肤自带进度条/音量/倍速/画中画/全屏/快捷键/手势），皮肤只用 `--media-*` 变量调成站点的单色冷调；左上角自绘**清晰度菜单**与**氛围模式**（画布取帧 → 放大模糊当环境光），音量与氛围开关都记在 `localStorage`；mp4 用原生 `<video>`，HLS 用 `<hlsjs-video>` 且 `await import()` 按需加载；**编码偏好见 §7.4.1** |
| 图片 | `loading=lazy` + 宽高占位防跳动；缩略图走 `/api/img?u=`（后端代理 + 磁盘缓存，绕开 Referer/403） |
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
| 布局 | **桌面端**：**悬浮式**侧边导航——`fixed left-3 top-3 bottom-3`、240px 宽的圆角玻璃块（`rounded-xl` + 白 5% 底 + `backdrop-blur`，四边留 12px 空隙、不贴视口边缘，自身可滚动），内容区让出 `16.5rem`（`lg:pl-[16.5rem]`）；网格按「扣掉侧边栏后的剩余宽度」定档——1440 宽 4 列、1536 以上 5 列。导航图标一律 22px（`h-[1.375rem]`）、`viewBox 0 0 24 24`、描边 1.5，**按光学校齐**：图形外框统一落在 13.5–14.5 之间（`getBBox` 实测，占 24 方框的 55–60%）——「管理」那把扳手是斜的，原样画会顶满整个方框（19×19，比兄弟图标大 30%），所以单独缩到 `scale(0.75)` 并用 `non-scaling-stroke` 保持 1.5 描边，外框变成 14.25×14.25，和「下载」的 15×13.5 齐平；换任何新图标先看它的外框，别只看 `viewBox`。导航项有**关注 / 艺人 / 发现 / 下载 / 管理**五项，**「设置」单独摆在栏底右下角，只显示齿轮图标**（36px 圆形命中区，`aria-label` + `title` 兜住可访问性；**不铺底色**，未选中白 60%、弹窗打开时白 95%），栏底只留这一个齿轮，不再显示实时连接状态（连接状态只在移动端顶栏留一个小圆点）。齿轮点开的是**弹窗**而不是页面，见 §7.7。**移动端**：隐藏侧边栏，保留 48px 顶栏（`backdrop-blur` + 上深下淡的渐变底，免得压出一条硬边）与底部 6 个图标 tab（`grid-cols-6`，设置仍占一格，带文字） | `web/src/components/AppShell.svelte` |
| 环境光 | `.canvas-glow`：照搬 beeg 的实现——`web/src/assets/glow.webp`（1000×563、8KB，取自 `beeg.com/dist/assets/glow.webp`）铺满顶部 **49vh** 高的色带（`background-size: cover`），上面压一层 `linear-gradient(to bottom, rgba(0,0,0,.6), 画布色)`。`absolute` 定位，**跟着内容一起滚走**（不是 `fixed`）；承载容器必须 `isolate`，否则 `-z-10` 会被 `body` 的画布底色盖掉 | `web/src/app.css`、`web/src/assets/glow.webp`、`web/src/components/AppShell.svelte`、`web/src/routes/Login.svelte` |
| 卡片 | 封面（16:9、r12）→ 左上**时长纯文字**（`text-meta` + 三层 `text-shadow`，**不带胶囊底**，亮封面上也读得清）+ 新片白药丸 → 36px 圆形艺人头像 + 标题 + `艺人 · 时间`；**不显示「已存」标识**（`hasLocalFile` 只用于下载按钮文案）。栅格 2 / 3 / 4 / 5 列（`sm` / `xl` / `2xl` 递增），间距 `gap-x-4 gap-y-7`——**列间距 16px 与 beeg 影片栅格的 16px 一致**；行间距 28px 比 beeg 的 12px 松，因为我们的文字块只有 ~48px 高、beeg 是 ~90px，行间留 28px 才有同等呼吸感 | `web/src/components/VideoCard.svelte`、`web/src/components/InfiniteList.svelte` |
| 平台标识 | 艺人主页标题旁边**直接放平台自己的 app icon**（`PlatformIcon.svelte`）：16px 方形（`sm` 档，见下）、`rounded-[22%]`、**保留原色**（这是全站唯一允许出现品牌色的地方之一，跟顶部环境光同级别）——pornhub = 橙底黑 `PH`（从站方 apple-touch-icon 抠出的橙色方块，64px PNG 2.2KB）、xhamster = 深色方砖 + 白色仓鼠头（站方 180px touch icon，64px PNG 5.8KB）、beeg = 官方 `favicon.svg`（渐变底 + 白色字标）；图标坐在一块 **毛玻璃圆角底座**上（`sm` 档 `h-6 w-6` + `rounded-[0.5rem]`、`md` 档 `h-8 w-8` + `rounded-[0.625rem]`：半透明白色上下渐变 13%→5%、`ring-1 ring-inset ring-white/15`、顶部一道内高光 `inset 0 1px 0 rgba(255,255,255,.14)`、`backdrop-blur-xl`）——底座比图标每边大 4px，留出来那圈就是玻璃边；深色底上 blur 没有内容可糊，毛玻璃质感实际由那三层叠加给出，blur 保留是为了图标将来压在封面/图片上时也成立；**三档尺寸按并排文字的大小选**：`md` 32px 底座 + 24px 图标（预留给更大的标题，目前没人用）、`sm` 24px + 16px（艺人主页的 28px 艺名旁、管理页关注行的 14px 名字旁）、`xs` 20px + 12px（12px 元信息行，最近入库列表）；**艺人页那个 28px 艺名用的是 `sm` 而不是 `md`**：名字那一行只有 20px 高（对齐资料表的行距，见「艺人页资料」），32/24px 的底座会把行撑高、下面三行就整体错开，所以那里传 `-my-0.5` 把底座上下各 2px 的溢出抵掉，行高仍是 20px、底座中线仍落在名字的中线上（比名字的大写带上下各多 2px）；`alt` / `title` 仍是平台名，读屏与悬停都不丢信息；**不认识的平台回退成文字药丸**（带 `href` 时就变成可点的文字药丸）。**艺人主页上这个图标本身就是外链**（传 `href={performer.url}`，`target=_blank` + `rel=noreferrer noopener`，悬停时底座从 13%→5% 提到 22%→12% 提示可点，`aria-label` =「打开{平台}主页」），所以原来动作行里那个**「平台主页」文字按钮已去掉**：平台图标就贴在艺人名字后面，想去平台主页点它最自然，不用在一排同步按钮里再认出第四个按钮。影片页的「平台」是键值行，仍显示文字。（第一版做的是单色剪影，已按需求改成原色 app icon。） | `web/src/components/PlatformIcon.svelte`、`web/src/assets/platform/`、`web/src/routes/Performer.svelte` |
| 头像条 | 关注页顶部（`TopBar` 之上，因为关注页外壳标题为空）横向滚动的**已订阅艺人头像条**：72px 圆头像（`h-18 w-18`）+ 96px 宽的姓名（`text-meta`，单行截断），**不显示任何计数**；点某个头像进 `/performer/<id>`。容器 `flex gap-3 overflow-x-auto no-scrollbar`，用 `-mx-3 px-3 sm:-mx-4 sm:px-4 lg:mx-0 lg:px-0` 做贴边滚动，**首列头像仍与内容左边缘（296 / 12px）对齐**；按 `lastSyncAt` 倒序，数据来自 `GET /api/subscriptions`，首屏给同尺寸灰圆占位避免跳动，请求失败静默隐藏（只是入口，不打扰影片流） | `web/src/components/PerformerRail.svelte`、`web/src/routes/Feed.svelte` |
| 艺人卡 | 圆形头像 **104px（移动）/ 128px（`sm` 以上）**——桌面端这个尺寸对标 X（推特）个人页头像的 ~133px，比早先的 88/96px 明显大一号；**尺寸由栅格轨道决定**——网格是定宽轨道 `grid-cols-[repeat(auto-fill,6.5rem)] sm:grid-cols-[repeat(auto-fill,8rem)]`，卡里的头像 `w-full` 正好填满一格、不再居中留白，所以**第一列头像的左边缘与 `TopBar` 标题对齐**；列数按可用宽度自动算出，间距只由 `gap-x-6 / sm:gap-x-8` 与 `gap-y-8` 决定（旧做法是响应式列数 3–8 + 头像 `mx-auto` 封顶，格子比头像宽，头像被"缩"进格子里，跟标题错位、也显得间距大）。**卡上不显示新片计数**（右上角那个白药丸已按需求去掉，`newVideoCount` 只留在 `aria-label` 里给读屏）；姓名**加粗**（`font-bold` / Roboto 700，对标 X 上用户名是粗体）且最多两行（`line-clamp-2`，一格只有 104/128px 宽），状态行 `mt-auto` 贴底，同一行卡片的状态行仍对齐 | `web/src/components/PerformerCard.svelte`、`web/src/routes/Performers.svelte` |
| 艺人页资料 | 艺人页头**就是一块面板**，从上到下三层：① 一行 `grid`——头像（**跟艺人卡同一口径：`h-26`＝104px，`sm` 起 `h-32`＝128px**，不再单独放大到 160px；`self-center`，**水平中线落在右边「名字栏 + 资料表」那组的中线上**——那组自己也 `self-center`，资料表的中线因此跟头像共线） │ 名字 + 平台图标 + `N部影片 · N订阅` + **身材** + **排名**（`#269`） │ 资料行网格（`xl` 起跟名字栏并排，窄屏 `col-span-2` 跨整行掉到头像下面）。**名字栏按资料表的行距排**：名字/计数/身材/排名四行各占 20px 行高（就是 `text-ui` 的行高；艺名 28px 用 `leading-5` 压到同一行高——Roboto 的大写高 0.711em ≈ 19.9px 正好填满这一行，上下溢出的是留白、不占位）+ `gap-y-2` 的 8px 行距，四行总高 104px，正好跟四行资料表（4×20 + 3×8）一样高；`xl` 起名字栏和资料表装进同一个 `xl:grid … items-start` 的容器，两边行起点贴同一条上沿，于是**名字 / 计数 / 身材 / 排名逐行对上资料表的第 1 / 2 / 3 / 4 行**（资料表两列六行时也一样，前四行照旧对齐）。窄屏时这层容器是 `display: contents`，名字栏和资料表仍是页头栅格的两个直接子项——资料表才跨得了两列、掉到头像下面，不用为此把名字栏和资料表分开写两遍；② 一道 `border-line` 发丝线；③ 简介（左：「简介」dim 标签 + 站点原文，`whitespace-pre-line` 保留换行、`text-pretty` 收掉「只剩一个 emoji」的孤字行）＋ 站外链接药丸（右，`xl` 起才跟简介同一行，否则掉到下一行左对齐）。页面上不再有卡片，整页因此少两块面板的高度。**字号比全站默认大一档**（用户要求「资料卡片里的字体改大一点」）：计数行与身材 12→`text-ui`(14)、资料行标签 12→`text-ui`(14)、简介 12→`text-ui`(14)、药丸 12→`text-ui`(14)；资料行值与简介正文保持 `text-ui`(14)，信息量靠颜色分（标签 `text-dim`、值 `text-fg`、简介 `text-muted`）而不是字号；**艺名不动**，仍是 `text-hero`(28px)。资料行**标签前挂一枚 14px 单色字形**（`ProfileIcon.svelte`：按标签关键词挑字形——感情＝心、取向＝双人、性别＝单人、出身地＝定位针、身高＝上下箭头、体重＝**哑铃**、三围/罩杯＝尺子、种族＝**脚印**、语言/国籍＝地球、发色等颜色＝水滴、眼睛＝眼睛、胸部＝标签、纹身＝笔、穿环＝环、爱好＝星、年份/出道/年龄＝日历，认不出的兜底一个圆点）。其中**只有「体重」的哑铃和「种族」的脚印两枚取自 https://github.com/JinsFoni/peach 的资料卡**（它 `web/index.html` 的 `#i-dumbbell` / `#i-footprints`），其余都是本项目自己那套 1.6 描边的线稿——用户明确只要换这两枚，不要整套搬。标签列因此仍是 `5rem`——图标 14 + 间距 6 + 最长的「出身地」42 ≈ 62px；字形用 `align-[-0.15em]` 内联对齐，`dt` 保持行内流才跟 `dd` 的基线对齐（改成 flex 会让基线上移到图标底边）。**标签与值在显示层才改口径**（`web/src/lib/profile.ts` 的 `displayProfile`）：感情状态→感情、喜欢→取向、假奶子→胸部、是否有穿环→穿环、兴趣爱好→爱好，「是／否」本地化成「有／无」（胸部反着来：否＝天然、是＝假奶），体重只留公斤（`111lbs. (50kg)`→`50 kg`，只给磅才按 0.45359237 换算）。库里存的仍是站上原文——改说法不用重扫，跟「身材」那种抓取期规整（`body_json` 存数字、接口只发数字）不是一回事：那是模型层的事实，这里是页面用语。**资料行按列读**：`@sm` 起 `grid-flow-col` + `grid-template-rows: repeat(var(--rows-2|-3), auto)`，行数在组件里按 `ceil(项数/列数)` 算好塞进 `--rows-2` / `--rows-3`（两列一套、三列一套，给各自的容器断点用），前 N 项竖着填满第一列再换列；窄到一列时退回默认的行优先，顺序仍与站点一致。**排名**（`#269`）取艺人主页「排名信息」块里的**模特排名**（`.rankingInfo` 里第一个 `.infoBox`，同块的本周/本月/上月/年度不收），单独存 `performers.model_rank`，取不到就是 0、页面不渲染。**身材**（`167cm · B89 · W64 · H91`）是资料行的归一结果：抓取时认出「身高」「三围」并换算成厘米（身高优先用站点自己写的 `167cm`，只给英制才按 2.54 换算；三围 `35-25-36` 这种靠量级判断单位后换算），**认出来的两行从 `profile` 里摘掉**，同一条数据不在页面上出现两次；认不出来的（如 `1.67 m`）原样留在资料行里。数据全部来自艺人主页自身：简介取 `.aboutMeSection`（`ld+json` 里没有 description，早先那里一直是空的）、资料行取 `.js-infoText .infoPiece`（`span` 是标签、`.smallInfo` 是值，**按站点顺序全量收**，字段因艺人而异——有人有罩杯/眼睛颜色，有人只有性别出身地）、链接取 `.socialList li a`（标签用站点自己的 `.socialText`：`官方网址` / `Twitter` / `Instagram`，站点没给标签时按域名兜底成 `Linktree` 这种名字；只收 `http(s)`，前端一律 `target=_blank` + `rel=noreferrer noopener`）。**解析阶段主动丢掉五类行**：用户明确不要的「会勾起我性欲的是」「会浇灭我性欲的是」，以及「视频播放量 / 主页浏览量 / 已看视频」这三行平台统计（不是个人资料，页头已经有影片数与订阅数；顺带挡住一个坑——`视频播放量` 含「视频」二字，不挡会被当成影片总数）。**不建固定列**：`performers.profile_json` / `social_json` 各存一个 JSON 数组（`[{label,value}]` / `[{label,url}]`），`body_json` 存身材对象（`{heightCm,bust,waist,hips}`，缺项 0），站点加字段不用迁移；同步时整体覆盖，页面没给内容时保留库里旧值。资料行列数按**这一栏自己的宽度**算（`@container` 写在 `dl` 的外层，`@sm`≈24rem 两列、`@3xl`≈48rem 三列）——它 `xl` 分栏后在右侧只有 ~500px，窄屏跨整行时又有 700px 以上，同一份代码两边都得排得好看；`@container` 写在自己的 `dl` 上永远匹配不到（容器查询只看祖先容器）。社交图标只做三档单色字形（X、Instagram、地球）——全站只有冷白一个色相，引品牌原色会炸。 | `web/src/components/PerformerProfile.svelte`（资料行）、`web/src/components/ProfileIcon.svelte`（资料行字形）、`web/src/lib/profile.ts`（资料行口径）、`web/src/components/SocialLinks.svelte`（药丸）、`web/src/routes/Performer.svelte`、`internal/model/body.go`、`internal/platform/pornhub/parse.go`、`internal/store/migrations/0004_performer_profile.sql`、`internal/store/migrations/0005_performer_body.sql`、`internal/store/migrations/0006_performer_rank.sql` |
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
- **当前实况**：Pornhub 只给 H.264，库里 168 条变体全是 `avc1`，所以这条逻辑现在不改变任何选择，
  是为 xHamster / beeg 这类可能给 AV1 的平台预先铺好的。

### 7.4.2 播放器实现：Video.js 10 自定义元素

选择 Video.js **10**（`@videojs/html`），不是 v8 也不是 React 版：v10 把「状态」和「渲染器」拆开，
UI 是一组自带 shadow DOM 的自定义元素，Svelte 直接当标签用，皮肤自带一整套控件，不用自己撸进度条。

- **结构**：`<video-player>`（`display: contents`，只做 store 边界）› `<video-skin>`（媒体容器 + 控件 + 快捷键 + 手势）› 媒体元素。
  页面里的圆角/裁剪由外层的 `relative overflow-hidden rounded-xl` 负责，皮肤自己的圆角设 0。
  **本站自己加的两块（清晰度/氛围模式药丸、页面浮层）也挂在 `<video-skin>` 里**（走它的默认 `<slot>`，和媒体元素并列）：
  全屏元素就是 skin 本身，放外面全屏后这两块会跟着消失。
- **中文化**：`<media-i18n>` 包一层，locale 从 `<html lang="zh-CN">` 继承，`@videojs/html` 自带 zh-CN 语言包
  （`画质 / 播放速度 / 字幕 / 静音 / 全屏` 等都是中文）。
- **皮肤样式只能靠变量**：`--media-accent-color`（白 `#f5faff`）、`--media-accent-text-color`（画布黑）、
  `--media-font-family`（Roboto）、`--media-border-radius`、`--media-border-color`，其余交给皮肤，不改它的内部类名。
- **片源策略不变**：每个清晰度仍是**一条独立清单/直链**，切换时读 store 的进度再 `seek` 回去续播。
  皮肤里那个「画质」菜单因此只有当前这一条（自动 + 当前档），不拿它当主入口——**清晰度菜单改成播放器左上角自己画**：
  分档列表来自后端（顺序即 §7.4.1 的排序），每行 `1080p` + `4.0 Mbps · 最高`，当前档打勾；侧边栏那个下拉框因此删了，
  右侧「清晰度」一行只读显示当前档。这样 §7.4.1 的排序与「默认最高画质」都由后端说了算。
- **药丸什么时候露头**：跟皮肤自己的控件同一节奏——指针在画面上动就显示（2.5 秒不动收回，暂停时保持露出），
  触屏（`pointer: coarse`）常驻。**不能靠 CSS `:hover`**：全屏时 hover 命中不到全屏子树外面的 `.group` 包裹层，
  所以由 JS 在 skin 上监听 `pointermove/pointerdown/pointerleave` 切换 `data-open`。
- **氛围模式**（左上角太阳按钮，存 `myporn.ambient`，默认开、系统「减少动效」时默认关）：把正在播的那一帧画到
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
- **错误处理**：皮肤自带错误弹窗会跟本站的「重新解析 / 用代理播放」浮层打架，所以订阅 `store.error` 后立刻
  `dismissError()`，把文案交给页面自己的浮层。
- **快捷键**：皮肤自带（空格/K、←→ ±10s、↑↓ 音量、M 静音、F 全屏、单击暂停、双击左右 ±10s、双击中间全屏）；
  页面再挂一个 window 监听，让焦点不在播放器里时也能用同一套键。
- **音量记忆**：存 `localStorage` 的 `myporn.player.volume`（`{volume, muted}`），但必须等媒体就绪（`canPlay`）再套用，
  否则会被播放器默认的满音量盖掉，并且会把默认值写回去污染记忆。**没存过也要从第一下就开始记**：媒体就绪时
  把当前值当作基线写进 `lastVolume`，否则「第一次调音量」这条路根本走不到写入分支。

### 7.5 包体预算

| 范围 | 目标（gzip） | 实测（2026-10） |
|---|---|---|
| 首屏列表页（index + CSS） | ≤60 KB | 49.0 + 9.8 = 58.8 KB |
| 视频页（Video.js 10 播放器 + 清晰度菜单/氛围模式，不含 HLS 引擎） | ≤95 KB | 85.5 KB |
| 仅 HLS 片源额外下载（hls.js 适配器） | ≤180 KB | 173.0 KB |
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
internal/hls/                 # master 解析 + 分段下载器
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

**取舍**：接受「头像可能来自另一个站点」，换取头像不再随平台改版而丢失；
代价是名字匹配不上时仍然没有头像（此时保持占位图，不做模糊猜测）。
X 的账号名猜不出来的（艺名拼起来超过 15 字符、账号名里加了数字或自创缩写之类）同样放弃。
