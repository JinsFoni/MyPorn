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
| 前端 | **Svelte 5 + Vite + Tailwind**，产物 `go:embed` 进二进制 | 编译期无运行时框架、包体小（详见 §7）；配 `hls.js`（唯一重依赖 ~150KB gzip） |
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

**Svelte 5（runes）+ TypeScript + Vite 6 + Tailwind CSS 4 + hls.js**，构建产物 `go:embed` 进二进制，无 SSR、无 Node 运行时。

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

- 运行依赖：`svelte`、`hls.js`
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
| 播放器 | `<video>` + hls.js，`import('hls.js')` 动态加载，只在视频页下载 |
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
| 布局 | **桌面端**：**悬浮式**侧边导航——`fixed left-3 top-3 bottom-3`、240px 宽的圆角玻璃块（`rounded-xl` + 白 5% 底 + `backdrop-blur`，四边留 12px 空隙、不贴视口边缘，自身可滚动），内容区让出 `16.5rem`（`lg:pl-[16.5rem]`）；网格按「扣掉侧边栏后的剩余宽度」定档——1440 宽 4 列、1536 以上 5 列。导航项只有**关注 / 艺人 / 发现 / 下载**四项，**「设置」单独摆在栏底右下角，只显示齿轮图标**（36px 圆形命中区，`aria-label` + `title` 兜住可访问性；**不铺底色**，未选中白 60%、弹窗打开时白 95%），栏底只留这一个齿轮，不再显示实时连接状态（连接状态只在移动端顶栏留一个小圆点）。齿轮点开的是**弹窗**而不是页面，见 §7.7。**移动端**：隐藏侧边栏，保留 48px 顶栏（`backdrop-blur` + 上深下淡的渐变底，免得压出一条硬边）与底部 5 个图标 tab（设置仍占一格，带文字） | `web/src/components/AppShell.svelte` |
| 环境光 | `.canvas-glow`：照搬 beeg 的实现——`web/src/assets/glow.webp`（1000×563、8KB，取自 `beeg.com/dist/assets/glow.webp`）铺满顶部 **49vh** 高的色带（`background-size: cover`），上面压一层 `linear-gradient(to bottom, rgba(0,0,0,.6), 画布色)`。`absolute` 定位，**跟着内容一起滚走**（不是 `fixed`）；承载容器必须 `isolate`，否则 `-z-10` 会被 `body` 的画布底色盖掉 | `web/src/app.css`、`web/src/assets/glow.webp`、`web/src/components/AppShell.svelte`、`web/src/routes/Login.svelte` |
| 卡片 | 封面（16:9、r12）→ 左上**时长纯文字**（`text-meta` + 三层 `text-shadow`，**不带胶囊底**，亮封面上也读得清）+ 新片白药丸 → 36px 圆形艺人头像 + 标题 + `艺人 · 时间`；**不显示「已存」标识**（`hasLocalFile` 只用于下载按钮文案）。栅格 2 / 3 / 4 / 5 列（`sm` / `xl` / `2xl` 递增），间距 `gap-x-4 gap-y-7`——**列间距 16px 与 beeg 影片栅格的 16px 一致**；行间距 28px 比 beeg 的 12px 松，因为我们的文字块只有 ~48px 高、beeg 是 ~90px，行间留 28px 才有同等呼吸感 | `web/src/components/VideoCard.svelte`、`web/src/components/InfiniteList.svelte` |
| 平台标识 | 艺人主页标题旁边**直接放平台自己的 app icon**（`PlatformIcon.svelte`）：24px 方形、`rounded-[22%]`、**保留原色**（这是全站唯一允许出现品牌色的地方之一，跟顶部环境光同级别）——pornhub = 橙底黑 `PH`（从站方 apple-touch-icon 抠出的橙色方块，64px PNG 2.2KB）、xhamster = 深色方砖 + 白色仓鼠头（站方 180px touch icon，64px PNG 5.8KB）、beeg = 官方 `favicon.svg`（渐变底 + 白色字标）；尺寸取 24px 是为了**和原来的文字药丸同高**，标题行的布局零位移；`alt` / `title` 仍是平台名，读屏与悬停都不丢信息；**不认识的平台回退成文字药丸**。影片页的「平台」是键值行，仍显示文字。（第一版做的是单色剪影，已按需求改成原色 app icon。） | `web/src/components/PlatformIcon.svelte`、`web/src/assets/platform/`、`web/src/routes/Performer.svelte` |
| 头像条 | 关注页顶部（`TopBar` 之上，因为关注页外壳标题为空）横向滚动的**已订阅艺人头像条**：72px 圆头像（`h-18 w-18`）+ 96px 宽的姓名（`text-meta`，单行截断），**不显示任何计数**；点某个头像进 `/performer/<id>`。容器 `flex gap-3 overflow-x-auto no-scrollbar`，用 `-mx-3 px-3 sm:-mx-4 sm:px-4 lg:mx-0 lg:px-0` 做贴边滚动，**首列头像仍与内容左边缘（296 / 12px）对齐**；按 `lastSyncAt` 倒序，数据来自 `GET /api/subscriptions`，首屏给同尺寸灰圆占位避免跳动，请求失败静默隐藏（只是入口，不打扰影片流） | `web/src/components/PerformerRail.svelte`、`web/src/routes/Feed.svelte` |
| 艺人卡 | 圆形头像 **104px（移动）/ 128px（`sm` 以上）**——桌面端这个尺寸对标 X（推特）个人页头像的 ~133px，比早先的 88/96px 明显大一号；**尺寸由栅格轨道决定**——网格是定宽轨道 `grid-cols-[repeat(auto-fill,6.5rem)] sm:grid-cols-[repeat(auto-fill,8rem)]`，卡里的头像 `w-full` 正好填满一格、不再居中留白，所以**第一列头像的左边缘与 `TopBar` 标题对齐**；列数按可用宽度自动算出，间距只由 `gap-x-6 / sm:gap-x-8` 与 `gap-y-8` 决定（旧做法是响应式列数 3–8 + 头像 `mx-auto` 封顶，格子比头像宽，头像被"缩"进格子里，跟标题错位、也显得间距大）。**卡上不显示新片计数**（右上角那个白药丸已按需求去掉，`newVideoCount` 只留在 `aria-label` 里给读屏）；姓名**加粗**（`font-bold` / Roboto 700，对标 X 上用户名是粗体）且最多两行（`line-clamp-2`，一格只有 104/128px 宽），状态行 `mt-auto` 贴底，同一行卡片的状态行仍对齐 | `web/src/components/PerformerCard.svelte`、`web/src/routes/Performers.svelte` |
| 复用类 | `.btn` / `.btn-primary`（白底黑字药丸）/ `.btn-ghost` / `.btn-danger` / `.field` / `.panel` / `.chip` / `.label-meta` | `web/src/app.css` |

约定：交互元素一律药丸，媒体与容器一律 12px 圆角；不用边框区分层级，只靠白 5%/9% 的底与间距（悬浮侧边栏也一样：不描边，靠 12px 圆角 + 四边空隙 + 毛玻璃从画布里"浮"起来）；流式页面里的**艺人页**标题由外壳 `TopBar` 渲染（**关注页没有标题**，顶部直接是订阅艺人头像条），**发现 / 下载两页是居中单栏**（`mx-auto` + `max-w-2xl / 5xl`），标题连同内容一起放进这一栏里，所以外壳标题留空（**设置不是页面，是弹窗**，见 §7.7）；详情页（影片/艺人）也不渲染外壳标题，改由页面自己带标题；**影片播放页和艺人主页顶部都不再有「返回」那一行**——内容（播放器 / 艺人信息）直接顶到内容区上沿，回退靠浏览器后退、侧边栏或移动端底部 tab（只剩错误态里那个「返回关注 / 返回艺人列表」按钮）。

### 7.5 包体预算

| 范围 | 目标（gzip） |
|---|---|
| 首屏列表页 | ≤45 KB |
| 视频页（含动态加载的 hls.js） | ≤195 KB |
| 全部路由合计 | ≤220 KB |

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
