# 上线前必须验证的解析点（Pornhub / xHamster / beeg）

> **状态（2026-10）**：已在真实站点（`cn.pornhub.com`）核对并通过——艺人主页资料（含身材、简介、资料区块与站外链接）、影片列表第 1 页、
> 视频页 `mediaDefinitions`、播放地址解析、真机下载全部实测正常。下文保留为「站点改版后的自查清单」。
> 所有解析逻辑集中在 `internal/platform/<站>/`，改选择器不需要动业务代码。
> xHamster 与 beeg 的核对点见本文末尾附录。

## 一、用调试命令逐层验证

```bash
make backend           # 先编译
./bin/myporn -root . -debug-parse "https://cn.pornhub.com/model/mini-diva"
./bin/myporn -root . -debug-video "abc123viewkey"
```

`-debug-parse` 会打印：艺人名/头像/视频数/订阅数、身材（`身材: 167cm B89 W64 H91`）、模特排名（`排名: 269`）、简介、资料行（`资料: 标签: 值`）、站外链接（`链接: 标签 → 地址`）、第 1 页条数、总页数、前 5 条影片。
`-debug-video` 会打印：标题/时长/标签 + 解析出的所有清晰度（应从 2160p 降序排列）。

任何一项是空的，就按下表核对。

## 二、逐项核对清单

### 1. 艺人主页 `GET /model/{slug}`

| 字段 | 当前实现 | 核对方式 |
|---|---|---|
| 名称 | JSON-LD `Person.name` → `.nameSubscribe h1` → `h1` | 若 JSON-LD 无 `Person`，需改选择器 |
| 头像 | JSON-LD `Person.image` → `.avatarWrapper img[data-src]` → `img[src*='/av/']` | 注意懒加载属性可能是 `data-src` / `data-image` |
| 简介 | JSON-LD `description` → `.aboutMeText` → `.aboutMeSection`（跳过 `.title`，取后面那个 div 的正文） | 2026-10 已核对：中文站 `ld+json` 里没有 description，正文只在 `.aboutMeSection` 里；换行是页面原有的，前端按 `pre-line` 保留 |
| 资料行 | `.js-infoText .infoPiece`（回退 `.infoPiece`）：第一个 `span` 是标签（去掉尾部 `:`/`：`）、`.smallInfo` 是值；**全量按顺序收**，只丢下面「不收的行」和「身材」认走的两行 | 2026-10 已核对：Mini Diva 收到 14 行（感情状态/喜欢/性别/出身地/三围/身高/体重/种族/发色/假奶子/纹身/是否有穿环/兴趣爱好），Charming Milana 只有 3 行——字段本来就是因人而异的 |
| 排名 | `.rankingInfo` 里第一个 `.infoBox` 的 `.big` 文本（后面几个 `rankDetails` 是本周/本月/上月/年度，不收） | 2026-10 已核对：Mini Diva `模特排名 269`（同页本周 254 / 本月 269 / 上月 266 / 年度 225，均未采用）；取不到时 `rank=0`，艺人页不出「排名」行 |
| 身材 | 从资料行里认「身高」「三围」并归一成厘米（`internal/model/body.go`）：身高优先取站点写的 `167cm`，只给英制按 2.54 换算；三围按「写了 cm/in 就照写、没写看量级（≤60 是英寸）」判断 | 2026-10 已核对：Mini Diva 的 `5' 6" (167cm)` + `35-25-36` 得到 `167cm · B89 · W64 · H91`，两行随即从资料行里摘掉；Charming Milana 页面没有这两行，页头就不出「身材」。若哪天站点把三围写成 `1.67 m` 这类没单位的格式，会原样留在资料行里（不猜单位） |
| 站外链接 | `.socialList li a` 的 `href` + `span.socialText`；没有标签时按域名兜底（`socialHostLabels`）；只收 `http(s)`，同一地址去重 | 2026-10 已核对：`官方网址`（onlyfans）/ `Twitter` / `Instagram` 三条；顶部那个「关于我的更多信息」按钮是引流位，不算社交账号，不收 |
| 不收的行 | 「会勾起我性欲的是」「会浇灭我性欲的是」（用户要求），以及「视频播放量 / 主页浏览量 / 已看视频」（平台统计，不是个人资料） | 中英文文案都匹配（`profileDropLabels`）。**注意**：`视频播放量` 含「视频」二字，不挡住会被下面「视频数」那一行当成影片总数 |
| 订阅数 | `.infoBox` 的 `data-title`（"订阅者: 1,430,831"）里冒号后的数字，取不到时退到 `.big` 文本（"1.4M"） | 2026-10 已核对：头部三块读数是「模特排名 / 视频播放量 / 订阅者」，`data-title` 里的完整数字比 `1.4M` 精确 |
| 视频数 | 遍历 `.infoPiece` / `.info`（**不再含 `li`**），按含 "video"/"视频" 的文本就近取数 | 2026-10 核对结论：**资料页没有「影片总数」**，只有播放量，所以这个字段常为 `NULL`；早先带上 `li` 会把影片卡片里的播放量当成总数（库里 48949 这种数字就是这么来的）。界面上的「N 部影片」用的是本地收录数 `localVideoCount` |

### 2. 影片列表 `GET /model/{slug}/videos?page=N`

| 字段 | 当前实现 | 备注 |
|---|---|---|
| 条目容器 | `li.pcVideoListItem` / `li[data-video-vkey]` / `div.videoBox` | 首选 `li.pcVideoListItem` |
| viewkey | `data-video-vkey` 或链接 `href` 里的 `?viewkey=` | — |
| 标题 | 链接 `title` 属性 → `.title a` → 链接文本 | — |
| 缩略图 | `img[data-mediumthumb]` → `data-src` → `src` | — |
| 悬停预览 | `img[data-mediabook]` 或 `video[src]` | 无则前端不显示预览 |
| 时长 | `.duration` | — |
| 播放量 | `.views` | — |
| 评分 | `.value`（形如 `88%`） | 存 0–100 |
| 发布时间 | `.added` 文本（"2 days ago" / "3 天前"）→ 现在减差值 | **近似值**，只用于排序 |
| 总页数 | 分页 `.pagination3 li a` 的最大数字，或正文 `page X of Y` | 决定全量回填何时结束 |

> 若 `.added` 的文案格式没被识别（`reRelDate` 只支持 秒/分钟/小时/天/周/月/年 与英文同义），
> 影片 `publishedAt` 会是 `NULL`，排序会退化为按本地入库 id 倒序 —— 功能可用，但顺序不精确。

### 3. 视频页 `GET /view_video.php?viewkey=...`

| 字段 | 当前实现 | 备注 |
|---|---|---|
| flashvars | 正则 `var flashvars_\d+ = {...};` | **最关键**，取不到就没有播放地址 |
| 流定义 | `mediaDefinitions[]`：`format` / `quality` / `videoUrl` | hls 项会再拉 master.m3u8 展开 |
| 回退 | 裸 `"mediaDefinitions":[...]` 正则 | 用于新版页面把数据放到 `window.__*` 的情况 |
| 标题 | JSON-LD `VideoObject.name` → `h1.title span.inlineFreeform` | — |
| 时长 | JSON-LD `duration`(ISO8601) → `.duration span` | — |
| 标签 | `a[href*="/video?search="]` / `.tagsWrapper a` | — |

如新版页面把播放数据放在 `window.__INITIAL_STATE__` / `__NEXT_DATA__` 之类的 JSON 里，
在 `parseMediaDefinitions()` 里加一条正则即可，其它代码不用动。

### 4. 艺人搜索 `GET /model/search?search=q`（回退 `/pornstars?search=q`）

| 字段 | 当前实现 | 备注 |
|---|---|---|
| 结果容器 | 全页扫 `a[href*="/model/"]`、`a[href*="/pornstar/"]` | 较宽松，通常能命中；但要确认抓到的是艺人卡而不是导航链接 |
| 名称 | 链接文本 | — |
| 头像 | 链接内 `img[data-src]` | — |
| hasMore | `.pagination a[rel=next]` | — |

## 二·补、实测记录（2026-10，`cn.pornhub.com`）

| 项 | 结论 |
|---|---|
| 艺人主页 / 列表页 | `li.pcVideoListItem` + `data-video-vkey` 命中；中文站发布时间文案被 `reRelDate` 正确识别 |
| 列表分页 | 第 1 页正常；`?page=2` 目前返回 404（站点对未登录翻页的限制），回填会在第 1 页后停止而非报错 |
| `mediaDefinitions` | **真实页面里 mp4 条目的 `quality` 是空数组** `[]`，早期用 `string` 接导致整个数组反序列化失败、表现为「未解析到任何可用流地址」；已改为 `json.RawMessage` |
| 嵌套数组 | `cookies: []`、`quality: []` 出现过，正则需要能跳过嵌套 `[]/{}`，已换成括号感知的平衡扫描 |
| HLS | master 清单可拉取，`BANDWIDTH` 与实际体积吻合（1080p ≈ 1.35 Mbps），体积预估用它 |
| 直链 mp4 | `/video/get_media` JSON 接口可拿 240/480/720/1080 直链；**必须带页面 Cookie**（适配器已内置 cookie jar）+ `platform: PC` + `Referer/Origin`，否则返回 `[]` |
| 4K | 未登录时 `get_media` 不返回 2160p；有 Cookie 时才会出现（`mediaDefinitions` 里的 2160 条目形如 `format:"mp4"`） |
| 签名有效期 | HLS 与直链签名约 2 小时（`validfrom`/`validto`），**不能缓存太久**：解析结果只复用 5 分钟，过期由前端「重新解析」按钮兜底 |
| CDN 防盗链 | 直链 mp4 不带 `Referer: https://cn.pornhub.com/` 直接 **404**；因此浏览器播放统一走 `/api/stream` 代理 |
| 签名与会话 | HLS 清单签名（`?h=…&e=…`）与页面会话绑定：**换一个没有 sessid 的客户端会得到 410**，同一签名被重复使用时也可能 410。因此下载/代理都复用适配器那份带 cookie jar 的客户端，失败就重新解析页面拿新签名 |
| 相对地址 | master 里的子清单是相对路径且自身不带签名，解析时必须把 master 的查询串带过去（`hls.ResolveURL` 已处理，否则分片全部 410） |

## 三、验证顺序建议

1. `-debug-parse` 能打出艺人名 + 第一页 ≥1 条影片 → 订阅链路可用
2. `-debug-video` 能打出 ≥1 个清晰度且首个是最高画质 → 播放链路可用
3. 打开前端，实际点进一个视频，确认能播、能切档
4. 订阅一个 300+ 部影片的艺人，观察回填分页是否正常结束、进度条是否推进

## 四、已知限制

- 发布时间为相对时间换算的近似值
- 播放地址 2 小时后失效；下载任务若卡在「已解析画质」阶段过久，重新触发即可拿到新签名
- 需要登录/会员才能看的内容无法解析（不做付费墙绕过）
- 站点改版后只影响本目录内的解析文件，业务层与数据库无需迁移

---

# 附：xHamster / beeg 的待核对点

两站都没有官方 API，用的都是「站点自己前端正在用的那份数据」，所以下列位置一旦改版就会失效。
排查顺序同上：先 `-debug-parse`，再 `-debug-video`，最后才看界面。

## 一、xHamster（`internal/platform/xhamster`）

| 点 | 现状（2026-10 实测） | 改版后怎么发现 |
|---|---|---|
| `window.initials` | 艺人/影片/列表数据全在这段 JSON 里，花括号配平扫描取出 | `-debug-parse` 报「解析 window.initials 失败」 |
| 字段名 | `infoComponent.pornstarTop`（名字/影片数/头像）、`aboutMeComponent.personalInfoList`、`trendingVideoSectionComponent.videoListProps.videoThumbProps`、`paginationComponent.lastPageNumber` | 名字或影片数为 0、列表为空 |
| 播放地址密文 | `xplayerSettings.sources.standard.<codec>[quality=auto].url`，头 5 字节是算法号 + 种子 | `-debug-video` 报「xHamster 播放地址解密失败」 |
| PRNG | 目前见到的 7 种算法都在 `decrypt.go`；出现第 8 种会解密失败（错误信息会带着原始密文前 32 字符） | 同上 |
| `hostMap` | 键形如 `standard.<codec>.auto.prime`，值是 CDN 主机首段（`video7`） | 解出来的地址 404 → 主机没换对 |
| 签名 | master 地址里 `end=<unix>`，约几小时后过期 | 播放 403/404 且地址里的 `end` 已过 → 重新解析即可（本来就每次现解） |
| 搜索 | `/api/front/search/suggest` 被 Cloudflare 拦（服务端带 token/Referer/Cookie 也是 403），站内搜索退化为 slug 精确探测 | 搜索一直是空结果但粘贴链接能订阅 → 正常现象 |

## 二、beeg（`internal/platform/beeg`）

| 点 | 现状（2026-10 实测） | 改版后怎么发现 |
|---|---|---|
| 接口 | `store.externulls.com`：`/tag?slug=`、`/tag/videos/<slug>`、`/facts/file/<id>`、`/video/play_url/<id>` | 502 + 「beeg 接口返回 N」 |
| slug | 大小写不敏感，**短横线不行**（`riley-reid` → 404） | 订阅报「找不到该艺人」 |
| 播放地址 | `play_url` 返回纯文本相对路径，前面补 `https://video.beeg.com` | 播放 404 |
| 缩略图 | `thumbs.externulls.com/videos/{fileId}/{thumb}.webp?size=480x270`，头像 `photos/{thumbId}/to.webp?crop_id=` | 封面全空 |
| Referer | 图床拒绝自家根域 Referer，只认 `https://beeg.com/`（`refererOverrides`） | 图片 403 |
| 限流 | 撞车时会 429/5xx，客户端已重试一次 | 偶发 502，重试或稍后再点 |
| 标签 | `tg_name` 里的 `{}` 是图标占位，入库前去括号 | 标签里又出现 `{…}` 说明清理函数没跑 |

## 三、两站共同注意

- **播放地址带签名、几小时过期**：不要缓存、不要写库，`ResolvePlayback` 每次现解。
- 两站都**没有**「新片通知」「Emby 自动扫库」相关接口约定，本轮也没做这两件事。
- 真机验收（2026-10）：xHamster 订阅 → 回填 46 部/页 × 34 页 → 默认档 720p **AV1** 播放正常 →
  下载 69.6 MB（AV1 720p）落盘 + nfo/poster/thumb/fanart；
  beeg 订阅 → 回填 48 部/页 → 默认档 1080p 播放正常 → 下载 385 MB（H.264 1080p）落盘 + 元数据。

## 四、Pornhub 作为资料源（beeg / xHamster 借资料时打到的接口）

| 点 | 现状（2026-10 实测） | 改版后怎么发现 |
|---|---|---|
| 主页标识 | 名字小写好推（`Riley Reid` → `/model/riley-reid`），连写的要两种都试（`MiniDiva` → `minidiva` / `mini-diva`） | 日志「Pornhub 上没找到同名艺人」但站点上明明有 |
| `/model/<slug>` 跳转 | 认证艳星 301 → `/pornstar/<slug>`；普通用户 301 → `/users/<slug>`（那类页面解析不出资料，会当作没找到）；不存在的标识 301 → 热门榜，解析出的名字对不上，同样算没找到 | 艺人资料一直借不到，`-backfill-profile` 打印「没找到同名艺人」 |
| 搜索 | `/pornstars/search?search=<名字>`（结果在 `ul#pornstarsSearchResult` 里，卡片名字带隐藏的「thumbnail」后缀要清掉）。**`/model/search?search=` 已废（301 到随机 `/users/<hash>`），`/pornstars?search=` 的参数被忽略（永远回热门榜）** | 搜索结果全是无关的「热门艳星」，说明搜索入口又变了 |
| 搜索无结果 | 搜索页直接回 **404**（客户端把它当「没搜到」，不是错误） | 报「上游 404」说明这个约定变了 |
| 艺人头像 | 资料页的 DOM 头像**解析不出来**（ld+json 里没有 image），所以借资料时头像字段通常是空的，头像继续走 §11 的跨站借图 | 不改也不影响 |
| 简介质量 | 不少老艺人的简介只是一串 OnlyFans 地址（Riley Reid = `Onlyfans.com/rileyreidx3`），比本网站的简介还少，所以「纯网址简介」不覆盖本地 | 艺人页简介变成一行网址 → 清理规则失效 |

