# 上线前必须验证的解析点（Pornhub）

> **状态（2026-10）**：已在真实站点（`cn.pornhub.com`）核对并通过——艺人主页资料、影片列表第 1 页、
> 视频页 `mediaDefinitions`、播放地址解析、真机下载全部实测正常。下文保留为「站点改版后的自查清单」。
> 所有解析逻辑集中在 `internal/platform/pornhub/parse.go`，改选择器不需要动业务代码。

## 一、用调试命令逐层验证

```bash
make backend           # 先编译
./bin/myporn -root . -debug-parse "https://cn.pornhub.com/model/mini-diva"
./bin/myporn -root . -debug-video "abc123viewkey"
```

`-debug-parse` 会打印：艺人名/头像/视频数/订阅数、第 1 页条数、总页数、前 5 条影片。
`-debug-video` 会打印：标题/时长/标签 + 解析出的所有清晰度（应从 2160p 降序排列）。

任何一项是空的，就按下表核对。

## 二、逐项核对清单

### 1. 艺人主页 `GET /model/{slug}`

| 字段 | 当前实现 | 核对方式 |
|---|---|---|
| 名称 | JSON-LD `Person.name` → `.nameSubscribe h1` → `h1` | 若 JSON-LD 无 `Person`，需改选择器 |
| 头像 | JSON-LD `Person.image` → `.avatarWrapper img[data-src]` → `img[src*='/av/']` | 注意懒加载属性可能是 `data-src` / `data-image` |
| 简介 | JSON-LD `description` → `.aboutMeText` | — |
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
