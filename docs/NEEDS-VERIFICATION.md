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
| 资料行的 `value` 形态 | **不是字符串**了也算正常：2026-10 起「Interests and fetishes」是对象数组 `[{id,slug,name,…}]`（`aboutMeComponent.personalInfoList` 与 `appearanceInfoList` 都会有），解析时用 `json.RawMessage` 收下来再宽松取文本 | 报 `cannot unmarshal array into Go struct field … of type string`，表现是「这类艺人一个都加不进来」 |
| 主页路径 | `/pornstars/<slug>` 现在 **301** 到 `/creators/<slug>`（客户端自动跟随，两种粘贴都能用） | 抓资料 404 → 先看是不是又被换了路径 |
| 列表排序（**必须带 `/newest`**） | 默认的 `/pornstars/<slug>` 是 `sort:"rated"`（按评分/默认序），第 1 页最上面是几年前的老片，最新的那几部甚至**不在同一个分区里**（`newestVideoSectionComponent` 只有 4 条，我们用的大分区 `trendingVideoSectionComponent` 是 38 条按评分排的）。带 `/newest` 才是 `sort:"added"`、按 `created` 严格倒序、条数也齐（2026-10 实测 `mysaaat`：默认 38 条 + 漏掉最新 4 条 → `/newest` 42 条 = 页面自称的 `videoCount`）。增量同步是「遇到已入库影片即停」，排序不对就等于老片一挡、**新片永远发现不了** | 订阅之后再也看不到新片、日志里「新增 0 部」→ 先 curl 一下 `/pornstars/<slug>/full-length/newest` 看 `sort` 字段是不是 `added` |
| 列表分页 | 路径跟着排序走：`/pornstars/<slug>/newest`、第 N 页 `/pornstars/<slug>/newest/<n>`，都只到末页为止，**翻过头回 404 而不是空页**；没有分页组件就是只有一页（2026-11-04 实测 `riley-reid` 的 `/full-length/newest`：34 条、`paginationComponent: null`、第 2 页直接 404）。适配器把「越界 404」和「无分页组件」都当「到头了」（`ListPerformerVideos` 里两个分支，单测 `TestListPerformerVideosNotFoundEndsPagination` / `TestListPerformerVideosSinglePageWhenNoPagination` 守着） | 回填卡在「未完成」且日志有「分页抓取中断 page=2」→ 看是不是末页 404 没被当成「到头了」 |
| HLS 分片主机 | 清单里**不止一个主机**：开头的 init 段 + 前两个分片在 `video7.xhcdn.com`，之后整条片子（实测 1080p AV1：319 段里 317 段）改由 `ip<数字>.ahcdn.com` 分发。`streamHostSuffixes` 两个后缀都得有 | 播放在第 8 秒（两段 4 秒）之后每段都 400「非法的播放地址」，控制台里分片请求全是 4xx |
| 默认档 | 同片同时给 AV1 与 H.264，按 §7.4.1 排序后默认档通常是 `1080p.av1`（3.5 Mbps） | 播不动/黑屏 → 先用 `canDecodeCodec` 看浏览器能不能解 AV1 |
| 片花不入库：`<180s` 与「同名片花」两道闸门；**别拿 `/full-length` 当「完整版」过滤器**（2026-11-04 实测，翻案又翻回来） | 第一轮：`hot-pearl` 的 `/newest` 第 1 页混着 8 条 60–163s 片花（`- Trailer`、`Private com -`），以为站点的 `/full-length` 分类就是「完整版」，把列表路径改了过去。第二轮把**整份列表**拉下来逐条对照，才发现它是更小的子集、不是过滤器：`/newest` 12 页 **545 条**（正好等于页面自称的 `videoCount=545`），`/full-length/newest` 只有 5 页 **200 条**；545 条里 **345 条不在 full-length**，按桶是 360–600s 145 条、600–1200s 139 条、≥1200s 10 条、180–360s 26 条、<180s 25 条。抽被剔掉的 1126s 那条（xh2grDz）把 HLS 清单下下来数：**281 段、合计 1125.9 秒、带 `#EXT-X-ENDLIST`**，是完整的 18.8 分钟正片、无付费墙——换成 `full-length` 等于静默丢掉三分之二正片，所以路径保持 `/newest`。真正要挡的是两类：① `<180s`（`minVideoDuration`）——实测 25 条，标题是 `- Trailer` / `Private com -` / 账号名垃圾；② **同名片花**：同一部片还会以 372 / 492 / 582 / 612 秒这几档固定长度再发一遍、标题一字不差、id 不同（实测 46 组同名、30 组一长一短，最长那份 663–1849s 才是正片），入库前用 `HasLongerSameTitle` 挡掉，数量进 `Page.Skipped` / 同步日志「跳过短片」「跳过同名片花」 | 墙上出现几十秒的 `… - Trailer`、`Private com -` → 看 `minVideoDuration`；同一部片在墙上出现 2–3 次 → 看 `HasLongerSameTitle` 那道闸；艺人影片数突然少一大半 → 看路径里是不是混进了 `full-length` |

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


## 五、社交动态（M5，`internal/social/`）

| 点 | 现状（2026-10 实测） | 改版后怎么发现 |
|---|---|---|
| X 抓取入口 | `syndication.twitter.com/srv/timeline-profile/screen-name/<handle>`，数据在 `__NEXT_DATA__.props.pageProps.timeline.entries[]` | 502 `stale_parser`「页面结构变了」，或 `hasResults:false` |
| X 协议版本 | **必须 HTTP/2**：同一地址 HTTP/1.1 一律 429（还带假的 `x-rate-limit-reset`），HTTP/2 才 200。`platform.NewHTTPClient` 里 `ForceAttemptHTTP2: true` 是硬要求 | 突然持续 429 而 curl 正常 → 先查协议版本 |
| X 条数 | 固定 20 条、**没有游标**，翻页无从谈起；`?name=orig` 拿原图 | 想拿更多只能上 Cookie/GraphQL（本轮没做） |
| X 限流 | 约 30 次/15 分钟（按 IP）。`remaining:0` 但 `reset` 已过期是缓存的旧头，不能当限流 | 冷却到点仍 429 → 可能配额被同 IP 的其它工具占满 |
| X 转推 | 原帖在 `retweeted_status` 里，按原帖 id 去重 | 同一张照片入库两次 → 去重键失效 |
| IG 免登录 | **已完全关闭**（`401 require_login` / `429 spam`），必须 Cookie（`sessionid` / `csrftoken` / `ds_user_id`） | 探测报 409 `needs_auth` 属正常 |
| IG 429 二义 | `api/v1/users/web_profile_info` 对**有效 Cookie**回 429 + HTML 拦截页（`<title>Page Not Found</title>`，含 `logged-in` class）；对无 Cookie 请求回 JSON 401 `require_login`。**2026-10-28 实测**：换 UA / Origin / Sec-Fetch / priority 全一样，说明是会话或 IP 被标记 | 若某天拦截页消失、又能拿到 JSON，说明 IG 放松了该 IP；届时 `blocked` 分支自然不再触发，不必改代码 |
| IG 出口 IP | 抓 IG 建议固定住宅代理：设置 → 网络与代理 → Instagram 专用代理（保存即生效，`proxy_instagram`），环境变量 `PLATFORM_PROXY_INSTAGRAM` / `GLOBAL_PROXY` 只作兜底。机房 IP 容易被直接拦 | 配了代理仍报 `blocked` → 该代理 IP 也脏，换一个（可在同一处直接改，不用重启） |
| IG Cookie 写法 | 三种都认：Netscape cookies.txt（`# Netscape HTTP Cookie File`，7 个制表符字段，`#HttpOnly_` 前缀）、整段 Cookie 头、JSON。非 `instagram.com` 的行丢掉 | 贴上整份导出仍报「没找到 sessionid」→ 导出文件的列数/分隔符变了（应把原始行贴出来核对） |
| IG 首屏 | `web_profile_info`（`?__a=1&__d=dis` 那套已废） | 502 `stale_parser` |
| IG 翻页 | `feed/user/<id>/?max_id=`，`more_available` 决定是否继续 | 只抓到第一页 |
| IG 图片地址 | 带签名、**几小时内过期**。缓存过的照常显示，没缓存的过期就 404（走占位图） | 老照片成片变灰 → 正常现象，想要就点存库 |
| 与艺人关联 | 只比对 `performers.social_json` 里已有链接的规范化 URL，命中才关联；绝不按名字猜 | 艺人页没有「社交动态」区块 → 要么没关联，要么艺人资料里没这条链接 |

## 六、YouTube 订阅（M6，`internal/ytsub/`）

> 代码不在 `internal/social/` 里：那里的 `Adapter` 抽象是为「博主 → 帖子 → 照片」设计的，
> YouTube 是「频道 → 视频 → 下载文件」，形状不同（见 `internal/ytsub/ytsub.go` 包头注释）。

| 点 | 现状（2026-10-28 实测） | 改版后怎么发现 |
|---|---|---|
| 增量通道 | RSS `feeds/videos.xml?channel_id=UC…`：**固定 15 条**，含标题/发布时间/观看数/封面/简介，**没有时长、没有 Shorts 标记** | 首屏突然只有个位数或没有新条目 → 先看 RSS 是否还 200 |
| 回填通道 | `yt-dlp -J --flat-playlist --playlist-end N`：200 条 5.8 秒，**时长 200/200 有、`timestamp` 0/200 没有** | 墙上会出现「时间未知」→ flat 的固有缺陷：点开单条会补，墙上的「补齐」按钮整批补（观看页 1.5s/条） |
| 单条补全 | `yt-dlp -J --no-playlist <watch?v=>`：4.1 秒，给 `timestamp` / `upload_date` / `description` / 时长 / 观看数 | 「查一下发布时间」一直失败 → yt-dlp 版本或出口 IP 出问题 |
| 频道 id 解析 | 频道页 `"externalId":"UC…"` 或 `<link rel="canonical" href="…/channel/UC…">`。页里还有几十个别的 `"channelId":"UC…"`（推荐位），**只有这两处可信** | 订阅进来的频道名像是别人的 → 用了推荐位的 id |
| 视频链接反查频道 | `watch?v=` 页里取 `"channelId"`，**必须再抓一次频道页**才能拿到频道名/头像（视频页的 `og:title` 是那条视频的） | 频道名是一串视频标题 → 少抓了第二步 |
| RSS 的 `yt:channelId` | 22 位、**不带 `UC` 前缀**（`q-Fj5jknLsUf-MWSy4_brA`），入库前补 `UC` | 频道 id 以 UC 之外的字符开头 → 归一化退回旧逻辑 |
| 播放能力 | 观看页 `streamingData` 里**没有 `formats`**，只有 26 个 `adaptiveFormats`（全是 av01 纯视频轨 144p–2160p）；唯一的混流文件是 itag 18（360p） | 想直接「下一个现成文件」是做不到的，这是 YouTube 的现状，不是解析缺失 |
| 合轨 | `ffmpeg -c copy` 只换容器、不转码，实测 **0.052 秒**；默认取 AV1 视频轨 + 最高音质音轨 → mp4 | 个别播放器无声 → mp4 里的 Opus 轨；设置里把格式改成 `bv*+ba[ext=m4a]/bv*+ba` |
| yt-dlp 版本 | **这是最常见的失败原因**：2026.07.04 全线 `HTTP Error 403: Forbidden`，2026.10.26 正常。`/api/yt/health` 的 `stale` 判据是版本日期超 90 天 | 下载全失败但网页能播 → 先看 health 的版本号 |
| `player_client` 取巧 | `mweb` 只给 itag 18；`android` 没有 AV1；`tv` 报「The page needs to be reloaded」。**不要试图绕开合轨** | 又想走「现成文件」的捷径时会踩这三个坑 |
| 出口 IP | 抓页面被要求人机验证会回 403 → `blocked`（不设冷却，换 IP 后立刻重试即可）。设置 → 网络与代理 → YouTube 专用代理，保存即生效 | 一直都是 `blocked` → 该出口 IP/代理也脏 |
| 封面尺寸 | `i.ytimg.com/vi/<id>/mqdefault.jpg` 是 320×180 的 16:9，**适合卡片**；下载入库时改用 `maxresdefault` 给 Emby 当 fanart，没有 maxres 的老视频会退回 mq | 卡片封面被裁成方图 → 没用 mqdefault 这一档 |
| 限流 | 页面/RSS 之间固定 1200ms 间隔（`social.NewThrottle`）；429 按 `Retry-After`（1–3600 秒，缺省 15 分钟）冷却 | 批量同步时偶发 429 → 间隔调大或换出口 |
| 取消语义 | 下载期间每秒查一次取消/暂停标志，`SIGINT` 给 yt-dlp（`.part` 留着续传），`WaitDelay=3s` 兜底 kill | 点了取消没反应 → 先看是不是又变成"只等管道 EOF"的写法：ffmpeg 这类孙进程攥着管道时，EOF 要等它们退出才来 |
| 本地播放 | 下载完的影片走 `GET /api/videos/{id}/file`（`http.ServeFile`，206 Range），不再依赖平台解析 | 详情页黑屏/「平台未注册」→ 检查 `library_files.video_path` 是否还在磁盘上、是否在库根之内 |
| 嵌播（iframe） | `youtube-nocookie.com/embed/<id>` 对**一批版权音乐视频直接拒播**（「此视频不能观看」），裸 HTML 页面也能复现，与本应用无关；`lTRiuFIWV54`、`jNQXAC9IVRw` 正常；`oEmbed` 全部 200（不是上传者关了嵌入） | 弹窗黑框/拒播 → 别看前端，直接拿 id 拼一张裸 HTML 试嵌；同时确认站内播放（下一条）是否可用 |
| 站内播放（HLS 代理） | `yt-dlp -j` 的 `formats[]` 里有 `protocol=m3u8_native` + `manifest_url`（签名 `hls_variant`，寿命约 6 小时、**绑出口 IP**）；master 24 KB、子清单 877 KB/1369 行、分片 `Content-Type` 常是 `application/octet-stream` 且**忽略 Range**；实测 Chrome 4K 起播 `readyState=4`、`videoWidth=3840` | 全部报 `no_ytdlp`/`upstream` → 先看 `GET /api/yt/health` 的版本；单条报「没有可用的 HLS 清单」→ 该视频可能直播/会员/分区限制；分片 410 → 会话过期，前端「重试」会重新解析 |
| 站内播放的音轨 | 视频变体与音轨在 master 里是**分开列**的（`AUDIO="233"`/`"234"`，233=Opus、234=AAC）；改写时必须把 `#EXT-X-MEDIA` 一起搬过去 | 画面有、声音没有 → 改写的 master 里丢了 `EXT-X-MEDIA` 或 `AUDIO="aud"` 没对上 |
| 4K 分片与预热上限 | 2160p VP9 60fps 28 220 650 bps，单分片 10.4–23.2 MB / 5.1–7.0 s；`maxWarmObject` 现为 64 MiB 且**超限直接不缓存**（以前 16 MiB + `io.LimitReader` 会把首分片截成 16777216 字节，hls.js 反复 `bufferAppendError`、`currentTime` 钉在 0）；预热预算 128 MB / 8 条视频 | 4K 一开就转圈、`buffered` 一直是空、`currentTime` 不动 → 先 `curl -s -o /dev/null -w '%{size_download}'` 量首分片是否恰好等于 16777216（是就说明截断又回来了）；`MYPORN_YT_WARM=0` 可关预热做二分 |
| 分片 403 重试 | googlevideo 会偶发对单个分片回 403（1 秒后重试 200）；`openUpstream` 对 403/408/429/500/502/503/504 在写入浏览器前重试 3 次（150/400 ms），410 不重试 | 播放中偶发 `502` + hls.js `fragLoadError` → 看日志里该分片是不是只试了一次；410 也重试说明 `retryableStatus` 被改宽了 |
| hls.js 缓冲 | 只对 HLS 注入 `maxBufferLength=60`、`maxBufferSize=250 MB`、`backBufferLength=30`（`VideoPlayer.svelte` 的 `hlsEngineConfig`）；默认 30 s / 60 MB 在 4K 下只等于 ~17 秒 | 4K 播放时缓冲长期在 10 秒上下抖、走两步停一下 → 先确认 `el.engine.config.maxBufferLength === 60`，再看链路是否掉到 3.5 MB/s 以下 |
| 4K 单文件 HLS（AV1 + sidx） | HLS 的 2160p 只有 VP9 28 220 650 bps（1.06 GB）；DASH 的 itag 401 是 `av01.0.13M.08` **13 461 975 bps**（540 420 903 B），且是分片 mp4：`ftyp`+`moov`+`sidx` 之后第一段从偏移 **1417** 开始，`sidx` 里 57 段（音轨 33 段、第一段偏移 1151）。`GET /api/yt/videos/{id}/dash.m3u8?q=2160` 把它当 HLS 发（`#EXT-X-MAP` + 57 条 `#EXT-X-BYTERANGE`），起播只要第一段 8.84 MB。取数 3 路并发（实测 3×20 MB = 9.31 MB/s，4 路反而掉到 1.19 MB/s），每段取完后台再预取 4 段（约 45 MB 一次）。真机 60 秒采样：0 次重缓冲、缓冲涨到 26.31 秒、3840×2160 | 4K 卡/起不来 → 先看 `/stream` 返回的那一档有没有 `"dash": true`、日志里有没有 `4K 单文件 HLS 建不起来`（有就是回退到 VP9 HLS 了，原因写在 err 里）；`4K 分片取回` 一行超过 10 秒就是上游在限流（见下一条） |
| yt-dlp 的 n 挑战 | 不带 `--remote-components ejs:github` 时 4K AV1 与 m4a 地址「头几 MB 能下、随后一律 403」（yt-dlp 自己 1.9% 处 403）；带上就能连续下完 540 MB。本机 deno 提供运行时，求解脚本首次从 GitHub 拉取 | 「准备失败：exit status 8 … 403」→ 先看 yt-dlp 有没有带这个参数；老版本不认它就整段失效 → `run`/`Download` 已会自动去掉重跑（`rcBad`） |
| googlevideo 的 IP 风控 | 连续解析十几次之后同一批地址会在几分钟内全部 403，冷却约 6 分钟恢复（换解析会换 CDN 边，但 IP 级限流时换哪条都一样）。另一种更阴的表现是**接了连接不发数据**：不设超时的话这一段永远等下去（实测 60 秒 0 字节），所以每块取数都带 25 秒超时 + 3 次重试 | 排查时别把「刚 403、过会儿又好」当代码 bug：判据是**同一条地址是否立刻 403**；`MYPORN_LOG_LEVEL=debug` 下看 `4K 分片取回 elapsed=`，正常 3–7 秒，超过 10 秒就是在限流 |
| 签名寿命 | 清单地址里的 `expire` 在**路径**里（`/api/manifest/hls_variant/expire/1793159341/…`），只有分片地址放进查询串；只读查询串会读不到寿命 | 每次播放都重跑一遍 yt-dlp（4 秒）→ `expireFromURL` 又只看 `Query()` 了 |

真机验收（2026-10-28，直连）：添加 `UCq-Fj5jknLsUf-MWSy4_brA`（T-Series）→ 深度回填 200 条 6 秒 →
RSS 同步补齐最新 15 条时间 → 单条 refresh 补回 `2026-09-30` →
下载 8K（4320p）AV1 + Opus → `-c copy` 合轨 → 358 MB 落盘 `YouTube/<频道>/<标题>/`，
poster 1000×1500 / thumb 1280×720 / fanart 1920×1080 / nfo（含 `uniqueid type="youtube"`）齐全，
`GET /api/downloads` 能看到该任务与本地路径。（2026-11-02 起这三档尺寸是**上限**：底图不够大时按底图出，
同一批片子重生成后是 poster 480×720 / thumb 1280×720 / fanart 1280×720，见 `internal/jobs/artwork.go`。）

站内播放验收（2026-10-28，真机 Chrome，`127.0.0.1:18081`）：点开 `s8OtOH_Vfa0` →
后端 4.9 秒解析出 8 档画质（最高 2160p VP9 18.7 Mbps）→ 播放器以 3840×2160 起播、
`readyState=4`、7 个分片请求全部指向 `/api/yt/seg/<会话>/<n>` → 画质菜单切 720p 后
`videoWidth=1280` 且播放不中断 → 隐私模式下同样能播、页面里没有 iframe。

## 七、bilibili 订阅（M7，`internal/platform/bilibili/`）

> 2026-10-28 已真机核对（大陆家宽直连、未登录）：解析 / 订阅 / 列表第 1 页 / 播放 / 合流 / 下载全部走通；
> 2026-10-29 补测登录态与 wbi 签名（1080P 到手、939 部投稿 47 页秒级回填）。
> 记录见 `docs/M7-BILIBILI.md` §7。下表是站点改版或换机器（换出口 IP）时的自查点。

| 核对点 | 当前实现 | 对不上时的症状 |
|---|---|---|
| UP 主主页 | `space.bilibili.com/<mid>`、`m.bilibili.com/space/<mid>`、纯数字 mid 都认；`/video` 之类子路径忽略，跟踪参数（`?spm_id_from=`）忽略；`/video/BV…` 这种会明确报错「不支持按视频链接订阅」 | 贴了主页链接说解析失败 → 先看是不是 `space.bilibili.com/<mid>/upload/video` 这种没见过的路径 |
| 资料 | `/x/web-interface/card?mid=`：`name` / `face` / `sign` / `follower` / `archive_count`；资料行只填「性别」「等级」 | 名字或粉丝数变成 0 → card 接口换了字段名（`data.card.name` 这一层不能看错） |
| 增量列表 | App `/x/v2/space/archive/cursor`，签名 `md5(排序查询串 + appSecret)`，固定 20 条 | 报「B 站接口返回的不是 JSON」/code 非 0 → `appkey`/`appSecret` 被官方换掉，或接口加强校验（此时回落到 web 列表，会变慢但能跑） |
| 回填列表 | **wbi 签名** `/x/space/wbi/arc/search?ps=20&pn=N&order=pubdate&order_avoided=true&platform=web&web_location=1550101&wts=&w_rid=`；mixin key 取自 `/x/web-interface/nav` 的 `wbi_img` 两张图片名，缓存 30 分钟 | 又回到「一页就 `-799`」→ 先确认请求里带没带 `w_rid`；带上了还被拒（`-352`）→ 图片名重排表变了，看 `wbi.go` 的 `mixinKeyEncTab`；签名彻底用不了时会自动回落未签名版（1 次 / 20 秒，慢但能跑） |
| 「翻完了」的判定 | `count` / `ps` 算总页数，`pn > 总页数` 时接口回空 `vlist` | 回填一直卡在最后一页 → `count` 字段位置变了 |
| 详情 | `/x/web-interface/view?bvid=`：title / pubdate / duration / tname / owner / pages[] | 封面或时长变空 → `data.pages[0].cid` 取错（多 P 投稿只取第 1 个 cid） |
| 播放地址 | `/x/player/playurl?bvid=&cid=&qn=127&fnval=4048&fourk=1`：`durl[]` + `dash{video,audio}` | 只剩 360P/480P → Cookie 没生效（`/api/bilibili/health` 的 `hasCookie` 只说明「库里非空」，要看 nav 是否 `isLogin=true`）；`accept_quality` 列着 4K 但 dash 不返回 → 正常，4K/HDR 要大会员 |
| 合并规则 | durl 保底；DASH 跳过与 durl 同分辨率的档，其余照留；每档带上最高码率的音轨地址 | 下拉框出现两条同名档 → 合并规则被改坏 |
| 媒体 CDN | `/api/stream` 放行 `bilivideo.com` / `bilivideo.cn`，补 `Referer: https://www.bilibili.com/` | 403 / 播放立刻中断 → 新 CDN 域名没加进 `streamHostSuffixes`，或 Referer 被去掉 |
| CDN 选路 | `pickMediaURL` 在 `primary + backup[]` 里按 rank 选：0=官方（`upos-*` 等）、1=未知第三方、2=PCDN（`*.mcdn.bilivideo.cn:8082`、`*.edge.mountaintoys.cn:4483`），同 rank 保持原顺序 | 4K 合流忽快忽慢、动不动卡在某个体积不动 → 又选了 PCDN（看 `video_streams.url` 的 host 是不是 `*.mcdn.*:8082`） |
| 封面 | `i0.hdslb.com/...jpg@960w_540h_1c.webp`（不校验 Referer，67 KB） | 卡片封面裂开 → `thumbURL` 的缩放后缀变了 |
| 分轨合流 | `ffmpeg -i video -i audio -map 0:v:0 -map 1:a:0? -c copy -movflags +faststart`，产物缓存 7 天；输入侧带 `-rw_timeout 30s` + `-reconnect*` | 切到 480P 黑屏/一直转 → 看日志有没有 `分轨合流完成` / `分轨合流失败`；都没有就是 playurl 或 ffmpeg 参数问题 |
| 合流进度 | `GET /remux?v=X&status=1` 立刻回 `{state,percent,elapsedSec}` 并在后台开始合流；前端轮询到 `ready` 才把地址交给 `<video>` | 4K 只转圈不涨百分比 → 看这条接口是否 200、`state` 是否卡在 `idle`（`idle` 说明 `muxcache.Begin` 没跑起来）；`failed` 里带原因 |
| 4K 冷启动耗时 | AV1 554 MB 约 49 秒（官方 CDN）／7 分半（PCDN）；HEVC 657 MB 约 75 秒；H.264 1.77 GB 约 3 分钟 | 超过 10 分钟 → `504 mux_timeout`，同时清该片画质缓存；换线路重试 |
| 登录态 | 设置 → B 站 → Cookie，`normalizeBiliCookie` 认三种：整段 Cookie 头 / 裸 `SESSDATA` 值 / 浏览器导出的 `cookies.txt`（Netscape，含 `#HttpOnly_` 行）；同名只留最后一个 | 填了还是 720P → 解密库里那条看是不是 `SESSDATA=…; buvid3=…` 这种正经键值对；出现几十上百个同名 `SESSDATA=` 就是又被按「裸值」拆了 |
| 限流语义 | `platform.ErrRateLimited`：翻页途中被限流不算失败（已抓入库、断点保留、退避 3 小时） | 每天红一次且回填不动 → 哨兵没被 `errors.Is` 认出（各家必须包在 `platform.ErrRateLimited` 外面） |
