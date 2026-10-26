# MyPorn Web (M1)

单用户自托管影片库的纯 SPA 前端。产物是静态文件（`web/dist`），由 Go 后端用 `go:embed` 打包进二进制，
所有数据都走 `/api/*`（契约见 `../docs/API.md`）。**没有 SSR，没有 Node 运行时。**

## 技术栈

| 关注点 | 选择 | 说明 |
| --- | --- | --- |
| 框架 | Svelte 5（runes） | 只用 `$state/$derived/$effect/$props`，无 store 库 |
| 构建 | Vite + `@sveltejs/vite-plugin-svelte` | `build.target: es2022` |
| 样式 | Tailwind CSS v4（`@tailwindcss/vite`，CSS-first） | 设计变量在 `src/app.css` 的 `@theme` 里 |
| 播放 | `hls.js` | 仅在影片页 `import('hls.js')`，独立 chunk |
| PWA | `vite-plugin-pwa`（`registerType: 'prompt'`） | 只预缓存应用外壳，不缓存视频/接口 |

禁止项（架构文档要求）：UI 组件库、路由库、状态库、axios、zod、图表库。全部手写。

## 开发

```bash
pnpm install
pnpm dev        # http://localhost:5173，/api 与 /healthz 反代到 http://localhost:8080
pnpm check      # svelte-check（.svelte + .ts 类型检查）
pnpm typecheck  # tsc --noEmit
pnpm build      # 产出 web/dist
```

后端没跑时页面不会白屏：每个页面都有 `import.meta.env.DEV` 守卫的内置示例数据，
并显示"离线预览"提示条。生产构建里这些分支会被完全移出 bundle。

## 目录

```
src/
  main.ts              # installRouter() → mount(App)，PROD 下动态注册 SW
  App.svelte           # 路由分发 + 启动会话自检；详情页按需懒加载
  app.css              # Tailwind v4 @theme 设计变量、base 层、@utility 组件类
  lib/
    api.ts             # 唯一网络出口：fetch 封装、ApiError、中文兜底文案、401 跳登录
    types.ts           # 严格对应 docs/API.md 的类型（无 any）
    router.svelte.ts   # 手写 history 路由：pushState/popstate、路径参数、滚动记忆、标题
    sse.ts             # EventSource 封装：指数退避、页面不可见时断开、70s 静默看门狗
    live.svelte.ts     # 跨页面响应式状态（runes 不能写在 .ts 里，故用 .svelte.ts）
    player.ts          # hls 懒加载、画质选择/记忆、全屏、reduced-motion 判断
    img.ts             # 统一走 /api/img?u=&w= 的图片 URL 构造
    format.ts          # 时长/日期/相对时间/计数/码率格式化（Intl + 手写相对时间）
  components/
    AppShell.svelte    # 桌面侧栏 + 移动底部导航 + SSE 事件接入 + Toast 容器
    InfiniteList.svelte# 游标分页 + IntersectionObserver + 离屏页 DOM 回收
    VideoCard.svelte / PerformerCard.svelte / TopBar.svelte / Toasts.svelte
    EmptyState.svelte / Skeleton.svelte
  routes/
    Login / Feed / Performers / Performer / Discover / Video / Settings / NotFound
```

## 设计约定

- 深色是唯一主题；近黑背景 + 中性灰分层 + 一个低饱和琥珀色作为唯一强调色（不用霓虹/大红大紫）。
- 排版层级：`hero 28 / display 22 / title 17 / body 14 / ui 13 / meta 11`，标题紧字距，元信息小号灰。
- 圆角统一 `rounded-xl`（12px），间距走 4px 栅格；按钮/输入框 8px。
- 移动端优先：底部导航带 `env(safe-area-inset-bottom)`，栅格 2 → 3 → 4 → 5 列。
- 所有异步界面都有骨架屏 / 空状态 / 错误重试三态；可键盘操作，焦点可见（琥珀色描边），
  尊重 `prefers-reduced-motion`（关掉封面预览、过渡与脉冲）。

## 与 docs/API.md 的关系

契约是唯一事实来源，前端不新增字段。实现中遇到文档没写清的地方，都按"最小假设 + 不改文档"处理：

- `Settings.platformProxy` 建模为 `{ pornhub: string }`；若后端实际返回对象或三态值，只需改 `types.ts`。
- `PUT /api/settings` 返回体按 `Settings` 假设；实现上保存后**重新 GET 一次**，以服务端状态为准
  （也顺带保证 write-only 的 token 不被回显）。
- `GET /api/videos/new` 文档只写了 `since`，实现按"与 `/api/videos` 同结构"传 `cursor/limit`。
- `Performer.videoCount` 语义含糊（平台影片数 vs 粉丝量），UI 标签写死为"平台影片"。
- 没有批量同步端点，因此"全部同步"这类入口没有实现；`POST /api/subscriptions/{id}/sync` 逐个触发。
- 没有 OpenList 连通性测试端点，设置页未提供"测试连接"。

## 包体预算（gzip，`pnpm build` 实测）

| 资源 | 大小 |
| --- | --- |
| 首屏 JS（`index-*.js`） | ≈35.6 KB |
| CSS | ≈6.4 KB |
| `index.html` | ≈0.5 KB |
| 影片页 hls.js chunk（按需） | ≈179 KB |

首屏合计 ≈42.5 KB（预算 ≤45 KB，不含 hls.js）。详情页（`Video`/`Performer`）拆成独立 chunk，
首屏只加载外壳 + 影片流。

## PWA

`registerType: 'prompt'`：新版本就绪后只在应用内弹一条提示，不自动刷新（避免打断播放）。
`devOptions.enabled = false`，`runtimeCaching` 为空——视频与接口不进缓存，`/api/*`、`/healthz`
也被排除在 `navigateFallback` 之外。
