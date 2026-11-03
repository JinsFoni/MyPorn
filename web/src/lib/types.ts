/**
 * 与 docs/API.md（契约 v1 / M1）一一对应的类型。
 * 改这里之前先看契约文档；不要凭空加字段。
 */

/**
 * 已注册的平台。M1 只有 Pornhub，之后逐个加：xHamster / beeg / x.com（M4）、
 * bilibili（M7）。列表里没有的新平台仍会照原样显示代号，不至于露出空白。
 */
export type PlatformKey = 'pornhub' | 'xhamster' | 'beeg' | 'xcom' | 'bilibili' | 'youtube';

export type SyncMode = 'incremental' | 'full';

export type PlatformStatus = 'ok' | 'no_proxy' | 'error';

export interface PlatformInfo {
  key: string;
  name: string;
  enabled: boolean;
  baseUrl: string;
  hasProxy: boolean;
  hasCookie: boolean;
  status: PlatformStatus;
}

export interface BackfillProgress {
  donePages: number;
  totalPages: number | null;
}

/** 艺人资料里的一行，如 {性别, 女性}。字段因艺人而异，所以是数组不是固定属性。 */
export interface ProfileField {
  label: string;
  value: string;
}

/** 艺人主页上的站外链接。 */
export interface SocialLink {
  label: string;
  url: string;
}

/** 身材：身高 + 三围，后端抓取时已归一到厘米；0 = 平台没给或没认出来。 */
export interface Body {
  heightCm: number;
  bust: number;
  waist: number;
  hips: number;
}

export interface Performer {
  id: number;
  platform: PlatformKey;
  performerId: string;
  name: string;
  /** 平台主页 */
  url: string;
  /** 已经过本地 /api/img 代理 */
  avatarUrl: string | null;
  bio: string | null;
  /** 资料区块（性别、身高、出身地……），顺序即平台页面顺序；可能为空数组 */
  profile: ProfileField[];
  /** 平台主页上挂的站外链接（Twitter / Instagram / 官方网址）；可能为空数组 */
  social: SocialLink[];
  /** 身材（身高 + 三围，厘米制）；四项全 0 表示没解析到，此时不显示「身材」那一行 */
  body: Body;
  /** 平台给的模特排名（越小越靠前）；0 表示没解析到，此时不显示「排名」那一行 */
  rank: number;
  /** 平台声明的影片总数（Pornhub 资料页其实不给，常为 null） */
  videoCount: number | null;
  /** 本地已收录的影片数（艺人详情接口才给；「N 部影片」显示的是这个） */
  localVideoCount?: number;
  subscriberCount: number | null;
  /** null = 未订阅 */
  subscriptionId: number | null;
  enabled: boolean;
  backfillDone: boolean;
  backfillProgress: BackfillProgress | null;
  /** RFC3339 */
  lastSyncAt: string | null;
  nextSyncAt: string | null;
  newVideoCount: number;
  lastError: string | null;
  /** 挂在这位艺人名下的博主（艺人详情接口才给；没有就 undefined） */
  socialAccounts?: MomentAccount[];
  /** 这些博主最近 12 张照片（艺人详情接口才给） */
  socialPhotos?: MomentPhoto[];
}

export interface PerformerPage {
  items: Performer[];
  total: number;
  page: number;
  limit: number;
}

/** POST /api/performers/resolve 的返回：远程解析，不入库 */
export interface ResolvedPerformer {
  platform: PlatformKey;
  performerId: string;
  name: string;
  avatarUrl: string | null;
  url: string;
  videoCount: number | null;
  /** B 站给的是粉丝数；别的平台不一定有，所以可选 */
  subscriberCount?: number | null;
}

/** GET /api/search/performers 的条目 */
export interface RemotePerformer {
  platform: PlatformKey;
  performerId: string;
  name: string;
  avatarUrl: string | null;
  url: string;
  videoCount: number | null;
  subscriberCount: number | null;
  /** null = 尚未订阅；否则为本地订阅 id */
  subscribedId: number | null;
}

export interface SearchPerformersResponse {
  items: RemotePerformer[];
  page: number;
  hasMore: boolean;
}

export interface SubscriptionCreated {
  subscriptionId: number;
  performer: Performer;
  jobId: number;
}

export interface VideoPerformerRef {
  id: number | null;
  name: string;
  url: string | null;
  /** 已代理；B 站墙上每张卡左侧那颗圆头像就是它（UP 主本人） */
  avatarUrl?: string | null;
}

export interface Video {
  id: number;
  platform: PlatformKey;
  platformVideoId: string;
  title: string;
  /** 平台观看页 */
  url: string;
  /** 已代理 */
  thumbUrl: string | null;
  /** 短预览 webm/mp4，已代理；悬停播放用 */
  previewUrl: string | null;
  durationSec: number;
  /** RFC3339 */
  publishedAt: string | null;
  viewCount: number | null;
  /** 0-100 */
  rating: number | null;
  tags: string[];
  performers: VideoPerformerRef[];
  /** 同步后新增且未看 */
  isNew: boolean;
  /** 是否已在某个本地库里落盘（M2） */
  hasLocalFile: boolean;
  /** 已落盘的库 id 列表（M2） */
  localLibraryIds: number[];
  /**
   * 播放时画面顺时针转多少度（0/90/180/270）。
   * 给「躺着拍」的片子用：像素本身就是横的、容器里也没有 rotation 元数据，
   * 只能由用户点一次、把角度记在 `videos.rotate_deg` 上（见 `/api/videos/{id}/rotate`）。
   * 缺省当 0 处理（离线预览那几份 mock 数据不带这个字段）。
   */
  rotateDeg?: number;
}

// ---- B 站（M7）----
/**
 * 一位 UP 主。没有单独的 bili_channels 表：它就是 performers 的一行 + 订阅行 +
 * 本地计数（详见 internal/store/bilibili.go 顶部）。
 */
export interface BiliChannel {
  id: number;
  /** space.bilibili.com/<mid> */
  mid: string;
  name: string;
  avatarUrl: string | null;
  url: string;
  subscriberCount: number | null;
  /** 本地已收录（不是 UP 主的总投稿数） */
  videoCount: number;
  unseenCount: number;
  /** 已落盘到本地库的条数 */
  downloaded: number;
  subscriptionId: number;
  enabled: boolean;
  backfillDone: boolean;
  /** 已经翻到第几页（B 站列表接口每页 20 条） */
  backfillPage: number;
  totalPages: number | null;
  lastSyncAt: string | null;
  /** 被限流时会被推到几小时之后 */
  nextSyncAt: string | null;
  lastError: string;
}

export interface BiliChannelsResponse {
  items: BiliChannel[];
  stats: { unseen: number };
}

/** B 站「为什么画质只有 720P / 为什么这档放不了」的答案 */
export interface BiliHealth {
  /** 库里存了 Cookie（不等于登录成功，见 loggedIn） */
  hasCookie: boolean;
  /**
   * 存着，但服务端用当前 APP_ENCRYPTION_KEY 解不开（密钥换过 / 库是别处拷来的）。
   * 这时候重填 Cookie 没用，得先让服务端用回当初那把密钥——所以绝不能显示成「未配置」。
   */
  cookieBroken: boolean;
  /** 真拿这条 Cookie 问过 nav：能不能用。配了 Cookie 却是 false，多半是粘错了格式 */
  loggedIn: boolean;
  /** nav 回的昵称（登录成功才有） */
  uname: string;
  /**
   * 上一次 nav 明确确认「已登录」的时间。有它才能区分「从没成功过（粘错了）」
   * 和「曾经有效、后来被 B 站作废」——后者除了重填没别的办法，而且 Cookie 里
   * 自带的到期时间说明不了问题（2026-10-30 实测：内嵌到期 2027-04-25，第 5 天就回 -101）。
   */
  lastLoginOk?: string;
  /** nav 请求本身失败（网络 / 风控）：这时 loggedIn=false 不代表 Cookie 失效 */
  navError?: string;
  /** 库里这条 Cookie 的写入时间（配合「B 站现在说未登录」说明是哪条、什么时候填的） */
  cookieSetAt?: string;
  /** 分轨档位要靠 ffmpeg 合流 */
  ffmpegOk: boolean;
  hasProxy: boolean;
  /** 库里这条凭据带 refresh_token（只有扫码登录拿得到）→ 能自动续期，不用等作废后重填 */
  canRefresh: boolean;
}

/** 扫码登录：`url` 转成二维码图片，`key` 用来轮询 */
export interface BiliQrcode {
  url: string;
  key: string;
}

/** 轮询一次的结果；success 时服务端已经写库，前端不需要回传任何凭据 */
export interface BiliQrcodePoll {
  status: 'pending' | 'scanned' | 'success' | 'expired';
  message?: string;
  /** 登录成功后 nav 回显的昵称 */
  uname?: string;
}

export interface BiliStats {
  channels: number;
  videos: number;
  unseen: number;
  downloaded: number;
  errors: number;
}

export interface BiliManageResponse {
  stats: BiliStats;
  channels: BiliChannel[];
}

export interface VideoDetail extends Video {
  description: string;
  related: Video[];
}

/** keyset 游标分页 */
export interface VideoPage {
  items: Video[];
  nextCursor: string | null;
}

/**
 * 播放器「上一个 / 下一个」的两个邻居（`/api/videos/{id}/adjacent`）。
 *
 * 方向按列表的视觉顺序：prev 是排在它上面那条（更新），next 是下面那条（更旧）。
 * 那一侧到底了就为 null——按钮据此置灰，不绕回第一条。
 */
export interface VideoNeighbors {
  prev: Video | null;
  next: Video | null;
}

export interface PlaybackVariant {
  id: string;
  height: number;
  width: number;
  bandwidth: number;
  codec: string;
  /** 已经过 /api/stream 同源代理的播放地址（dash 档位这条是**视频轨**） */
  url: string;
  /** true = HLS 清单（走 hls.js），false = 直链 mp4（原生 video） */
  hls: boolean;
  /** true = 音画分轨（B 站 1080P+/4K）：`url` 是视频轨，另给 `audioUrl`，页面用 MSE 拼 */
  dash?: boolean;
  /** dash 档位的音频轨（同样过了 /api/stream 代理） */
  audioUrl?: string;
  /** dash 档位走服务端合流时的地址（MSE 不可用时兜底） */
  remuxUrl?: string;
  isDefault: boolean;
  /** true = 走本地库里已下载的文件（不经过平台，也不经过 /api/stream） */
  local?: boolean;
  libraryId?: number;
  fileName?: string;
  sizeBytes?: number;
}

export interface PlaybackInfo {
  videoId: number;
  /** 按 (height, bandwidth) 降序 */
  variants: PlaybackVariant[];
  /** 即最高画质 */
  defaultVariantId: string;
  /** 直链失败时的降级入口，M1 可能为 null */
  proxyPlaybackUrl: string | null;
  expiresAt: string;
  /** 解析详情页时顺带拿到的标签（列表页没有，第一次打开就能显示） */
  tags?: string[];
}

/**
 * 分轨合流（B 站 1080P+/4K）的准备进度。
 *
 * 这些档位的片源是画面、声音两条流，必须服务端合流成一份 mp4 才能播；
 * 4K 一档要整条流下完（554 MB ~ 1.77 GB），头一次点要等几分钟。
 */
export interface RemuxStatus {
  state: 'idle' | 'merging' | 'ready' | 'failed';
  /** 0~100，只有 merging 时有意义 */
  percent: number;
  elapsedSec: number;
  /** state = failed 时的原因 */
  error?: string;
}

export interface OpenListSettings {
  baseUrl: string;
  root: string;
  /** 只读：是否已保存 token（永不回显明文） */
  hasToken: boolean;
}

/** 社交动态（M5） */
export interface SocialSettings {
  /** 多久抓一轮（小时） */
  syncHours: number;
  /** Instagram 是否已配 Cookie（Cookie 本身不回显） */
  hasInstagramCookie: boolean;
}

export interface Settings {
  syncIntervalHours: number;
  globalProxy: string;
  /** 按平台覆盖代理；空字符串 = 直连 */
  /** 平台专用代理；instagram / x 决定抓动态时的出口 IP */
  platformProxy: {
    pornhub: string;
    xhamster: string;
    beeg: string;
    instagram: string;
    x: string;
    youtube: string;
    bilibili: string;
  };
  namingTemplate: string;
  downloadConcurrency: number;
  /** 单任务下载限速（KB/s），0 = 不限 */
  downloadRateKb: number;
  /** 同步到新片后是否自动下载；默认 false（只手动下载） */
  autoDownloadNew: boolean;
  /** 当前默认本地库 id；0 = 还没有库 */
  defaultLibraryId: number;
  libraryRoot: string;
  /** 服务端 ffmpeg 是否可用（不可用时下载会在封装步骤失败） */
  ffmpegAvailable: boolean;
  openlist: OpenListSettings;
  social: SocialSettings;
  youtube: YouTubeSettings;
  bilibili: BilibiliSettings;
}

/** M7 B 站订阅的设置。Cookie 同样只写不读 */
export interface BilibiliSettings {
  /** 配了 SESSDATA 才能拿 1080P 以上（未登录最高 720P） */
  hasCookie: boolean;
  /** 存着但不是用当前加密密钥写的：解不开，重填之前得先把服务端密钥修回来 */
  cookieBroken: boolean;
}

/** M6 YouTube 订阅的设置。Cookie 只写不读，只告诉前端「有没有配」 */
export interface YouTubeSettings {
  syncHours: number;
  /** 深度回填一次列多少条（yt-dlp） */
  backfillLimit: number;
  /** yt-dlp 格式串；空表示用默认（AV1 最高画质 + 最高音质） */
  format: string;
  /** 留空 = 从 PATH 里找 yt-dlp */
  ytdlpPath: string;
  hasCookies: boolean;
  /** 同 BilibiliSettings.cookieBroken：存着但解不开 */
  cookiesBroken: boolean;
}

/** PUT /api/settings：字段均可选；token 单独写，响应不回显 */
export interface SettingsPatch {
  syncIntervalHours?: number;
  globalProxy?: string;
  platformProxy?: {
    pornhub?: string;
    xhamster?: string;
    beeg?: string;
    instagram?: string;
    x?: string;
    youtube?: string;
    bilibili?: string;
  };
  namingTemplate?: string;
  autoDownloadNew?: boolean;
  downloadConcurrency?: number;
  downloadRateKb?: number;
  openlist?: { baseUrl?: string; root?: string; token?: string };
  /** instagramCookie 传空串表示清除 */
  social?: { syncHours?: number; instagramCookie?: string };
  /** cookies 传空串表示清除 */
  youtube?: {
    syncHours?: number;
    backfillLimit?: number;
    format?: string;
    ytdlpPath?: string;
    cookies?: string;
  };
  /** cookie 传空串表示清除 */
  bilibili?: { cookie?: string };
}

export type JobType =
  | 'sync'
  | 'download'
  | 'upload'
  | 'strm'
  | 'social_sync'
  | 'social_save'
  | 'yt_sync'
  | 'yt_download'
  | 'yt_meta';
export type JobStatus = 'queued' | 'running' | 'paused' | 'done' | 'failed' | 'canceled';

export interface Job {
  id: number;
  type: JobType;
  status: JobStatus;
  /** 0-100 */
  progress: number;
  message: string | null;
  error: string | null;
  /** 目标实体 id（下载任务 = videoId） */
  targetId: number;
  bytesDone: number;
  bytesTotal: number;
  /** 字节/秒 */
  speedBps: number;
  createdAt: string | null;
  startedAt: string | null;
  finishedAt: string | null;
  payload: Record<string, unknown> | null;
}

export interface JobsPage {
  items: Job[];
}

/** SSE: job.updated */
export interface JobUpdatedEvent {
  id: number;
  type: JobType;
  status: JobStatus;
  progress: number;
  message: string | null;
  error: string | null;
  targetId?: number;
  bytesDone?: number;
  bytesTotal?: number;
  speedBps?: number;
}

/** SSE: sync.progress */
export interface SyncProgress {
  subscriptionId: number;
  performerId: number;
  page: number;
  totalPages: number | null;
  videosFound: number;
  phase: 'backfill' | 'incremental';
}

/** SSE: video.new */
/** SSE：有新照片入库（照片墙据此插入/刷新） */
export interface PhotoNewEvent {
  photoId: number;
  accountId: number;
  platform: string;
  handle: string;
}

/** SSE：某个博主的抓取进度 */
export interface SocialProgressEvent {
  accountId: number;
  handle: string;
  page: number;
  newPhotos: number;
  backfilling: boolean;
}

export interface VideoNewEvent {
  videoId: number;
  performerId: number;
  /** 哪个平台的投稿：B 站要刷自己的墙，「关注」流则不该动 */
  platform?: PlatformKey;
  title: string;
}

// ---------------- M2：本地库 / 下载 ----------------

export type UploadPolicy = 'local' | 'local_remote' | 'strm';

export interface Library {
  id: number;
  name: string;
  rootPath: string;
  enabled: boolean;
  isDefault: boolean;
  uploadPolicy: UploadPolicy;
  openlistPath: string;
  namingTemplate: string;
  exists: boolean;
  writable: boolean;
  freeBytes: number;
  totalBytes: number;
  spaceMessage: string;
  fileCount: number;
  sizeBytes: number;
}

export interface LibrariesResponse {
  items: Library[];
  totalFileCount: number;
  totalSizeBytes: number;
  /** 未创建任何库时给前端预填的目录 */
  suggestedRootPath: string;
}

export interface LibraryInput {
  name?: string;
  rootPath?: string;
  enabled?: boolean;
  isDefault?: boolean;
  uploadPolicy?: UploadPolicy;
  namingTemplate?: string;
  openlistPath?: string;
}

export interface PathValidation {
  ok: boolean;
  exists: boolean;
  writable: boolean;
  freeBytes: number;
  totalBytes: number;
  message: string;
}

export interface LocalCopy {
  libraryId: number;
  path: string;
  sizeBytes: number;
  resolution: string;
}

/** GET /api/videos/{id}/download-options */
export interface DownloadOptions {
  videoId: number;
  libraries: Library[];
  defaultLibraryId: number | null;
  variantId: string | null;
  /** 码率 × 时长估算；未知为 null */
  estimatedBytes: number | null;
  durationSec: number;
  localCopies: LocalCopy[];
}

/** POST /api/videos/{id}/download 的返回 */
export interface DownloadAccepted {
  skipped: boolean;
  jobId: number | null;
  libraryId: number;
  videoId?: number;
  title?: string;
  reason?: string;
  localPath?: string;
  sizeBytes?: number;
}

export interface DownloadItem {
  id: number;
  type: JobType;
  status: JobStatus;
  progress: number;
  message: string | null;
  error: string | null;
  targetId: number;
  bytesDone: number;
  bytesTotal: number;
  speedBps: number;
  createdAt: string | null;
  startedAt: string | null;
  finishedAt: string | null;
  libraryId?: number;
  libraryName?: string;
  videoId?: number;
  title?: string;
  thumbUrl?: string | null;
  durationSec?: number;
  platform?: PlatformKey;
  localPath?: string;
  sizeBytes?: number;
}

export interface DownloadsResponse {
  items: DownloadItem[];
  activeCount: number;
  totalSizeBytes: number;
}

/** 媒体库的排序：落盘时间 / 体积 / 时长 / 文件名 */
export type LibrarySort = 'recent' | 'size' | 'duration' | 'title';

/**
 * 媒体库里的一行 = 一部**已经躺在磁盘上**的影片（library_files 一条记录）。
 * 和 DownloadItem 不是一回事：那边是任务（还在跑的、失败的），这边是文件。
 */
export interface LibraryVideo {
  videoId: number;
  libraryId: number;
  libraryName: string;
  title: string;
  /** 磁盘上的文件名（下载时按命名模板改过，和 title 不一定一样） */
  fileName: string;
  platform: PlatformKey;
  /** 平台影片才有艺人名；YouTube / B 站这类是空的 */
  performers: string[];
  /**
   * 卡片副行左边那个名字：平台影片是第一位艺人（B 站 = UP 主），YouTube 是频道名，
   * 都没有就是空串（卡片退回显示库名）。
   */
  channelName: string;
  /** 上面那个名字的头像（已过 /api/img 代理）；空 = 没有，卡片退化到首字母 */
  avatarUrl: string | null;
  /** 落盘时 probe 出来的时长，0 表示未知 */
  durationSec: number;
  width: number;
  height: number;
  sizeBytes: number;
  /** 落盘时间（RFC3339）；理论上有，解析不出来时是 null */
  downloadedAt: string | null;
  /** 远端封面（走 /api/img 代理）：本地封面缺失时的兜底，隐私模式也走它 */
  thumbUrl: string;
  /** 下载时一起落盘的 thumb.jpg，只有文件真在磁盘上时才给 */
  localThumbUrl: string | null;
  /** 库内绝对路径，用于「复制路径」 */
  videoPath: string;
  relDir: string;
  /** 文件已经不在磁盘上（记录还在）：卡片要标出来，别让人点了才发现播不动 */
  missing: boolean;
  /** 本地下发地址（Range 续传） */
  fileUrl: string;
}

export interface LibraryVideosPage {
  items: LibraryVideo[];
  nextCursor: string | null;
  /** 当前筛选下的总条数与总占用 */
  total: number;
  totalSizeBytes: number;
  /** 每个库有多少部（筛选胶囊上的数字；不跟随库筛选，跟随搜索词） */
  byLibrary: { libraryId: number; name: string; fileCount: number; sizeBytes: number }[];
  /**
   * 顶部头像条的来源：平台影片的**艺人**（kind=performer）与 YouTube 影片的**频道**（kind=channel）。
   * 同样不跟随自己那条筛选轴（筛了艺人，头像条照样列出全部来源）。按「有几部」从多到少。
   */
  sources: LibrarySource[];
}

export interface LibrarySource {
  kind: 'performer' | 'channel';
  id: number;
  name: string;
  avatarUrl: string | null;
  fileCount: number;
}

export interface Health {
  ok: boolean;
  version: string;
  uptimeSec: number;
}

/** 错误响应体 */
export interface ApiErrorBody {
  error: { code: string; message?: string; details?: Record<string, unknown> };
}

export type ApiErrorCode =
  | 'unauthorized'
  | 'invalid_password'
  | 'too_many_attempts'
  | 'bad_request'
  | 'not_found'
  | 'unsupported_platform'
  | 'resolve_failed'
  | 'rate_limited'
  | 'no_library'
  | 'already_queued'
  | 'internal'
  /** 前端本地补充：网络不可达 / 请求被取消 */
  | 'network_error'
  | 'aborted';

// ---------------- 管理页（GET /api/manage，契约见 docs/API.md） ----------------

/**
 * 一个「代号 + 计数」的桶：平台、时长/清晰度/进度分档、月份、标签都用它。
 * 代号是机器可读的（`pornhub` / `lt5` / `2026-10` / 标签原文），中文名字在前端翻。
 */
export interface ManageCount {
  key: string;
  n: number;
  /** 只有平台、库这类分项才带体积，其余是 0 */
  bytes: number;
}

export interface ManageLibrary {
  id: number;
  name: string;
  videos: number;
  bytes: number;
}

export interface ManageRecentVideo {
  id: number;
  title: string;
  platform: string;
  /** 已代理 */
  thumbUrl: string | null;
  publishedAt: string | null;
  firstSeenAt: string | null;
}

/** 统计的一半：数据库此刻的样子 */
export interface ManageStats {
  performers: number;
  performersEnabled: number;
  /** last_error 非空的订阅数 */
  performersFailed: number;
  videos: number;
  videosNew: number;
  /** 已落盘到任意本地库的影片数 */
  videosWithFile: number;
  files: number;
  bytes: number;
  byPlatform: ManageCount[];
  byLibrary: ManageLibrary[];
  byDuration: ManageCount[];
  byQuality: ManageCount[];
  /** 发布年份（最近 6 个），时间正序；unknown = 没有发布时间的那些 */
  byYear: ManageCount[];
  /** 最近 12 个有数据的月份，时间正序 */
  ingest: ManageCount[];
  tags: ManageCount[];
  downloads: { done: number; failed: number; active: number };
  recent: ManageRecentVideo[];
}

/** 关注管理的一行：订阅 + 本地收录量（平台的 videoCount 不是库里的数） */
export interface ManageSubscription extends Performer {
  localVideoCount: number;
  localFileCount: number;
  localBytes: number;
}

export interface ManageResponse {
  generatedAt: string;
  stats: ManageStats;
  subscriptions: ManageSubscription[];
}

// ---- 社交动态（M5）----

/** 一个被订阅的社交博主 */
export interface MomentAccount {
  id: number;
  platform: string;
  handle: string;
  displayName: string;
  avatarUrl: string;
  profileUrl: string;
  /** 关联的艺人的 id（0 = 未关联） */
  performerId: number;
  performerName: string;
  enabled: boolean;
  /** 历史帖子是否已经回填完 */
  backfillDone: boolean;
  postCount: number;
  photoCount: number;
  lastSyncedAt: string | null;
  /** 最近一次失败原因；空串 = 正常 */
  lastError: string;
}

/** 照片墙上的一格 */
export interface MomentPhoto {
  id: number;
  /** 已经过 /api/img 代理的地址 */
  url: string;
  thumbUrl: string;
  width: number;
  height: number;
  postedAt: string | null;
  /** 是否已存进某个本地库 */
  saved: boolean;
  accountId: number;
  handle: string;
  /** 博主自己写的名字（空串＝没给），照片墙的悬停浮层与灯箱显示它而不是 @handle */
  displayName: string;
  platform: string;
  postUrl: string;
  caption: string;
}

export interface MomentsPage {
  items: MomentPhoto[];
  nextCursor?: string;
}

export interface SocialPlatformInfo {
  key: string;
  name: string;
  needsCookie: boolean;
  hasCookie: boolean;
  hint: string;
}

export interface MomentsAccountsResponse {
  items: MomentAccount[];
  /** 上次看过之后新入库的照片数 */
  unseen: number;
  platforms: SocialPlatformInfo[];
}

/** 添加博主前的探测结果 */
export interface MomentResolve {
  /** 链接已经躺在某个艺人的社交链接里时，给出会关联到谁 */
  performerId: number;
  performerName: string;
  platform: string;
  platformName: string;
  handle: string;
  displayName: string;
  avatarUrl: string;
  profileUrl: string;
  /** 非 0 表示库里已经有了 */
  existingId: number;
  needsCookie: boolean;
  postCount: number;
  warning?: string;
}

export interface MomentsManageResponse {
  items: MomentAccount[];
  stats: { accounts: number; posts: number; photos: number; errors: number };
  hasInstagramCookie: boolean;
}

// ---------- YouTube 订阅（M6） ----------

export interface YtChannelBrief {
  id: number;
  /** 频道 id（UC…），不是行 id */
  channelId: string;
  handle: string;
  title: string;
  /** 已过 /api/img 代理 */
  avatarUrl: string;
}

export interface YtChannel extends YtChannelBrief {
  profileUrl: string;
  enabled: boolean;
  videoCount: number;
  unseenCount: number;
  /** 首次深度回填完成时间 */
  backfilledAt: string | null;
  lastSyncedAt: string | null;
  lastError: string;
}

export interface YtVideo {
  id: number;
  videoId: string;
  title: string;
  /** 原站链接 */
  url: string;
  thumbUrl: string;
  /** maxresdefault，卡片大图/详情用；可能 404（老视频） */
  posterUrl: string;
  embedUrl: string;
  durationSec: number;
  views: number;
  /** 深度回填来的条目没有发布时间（yt-dlp flat 不给），此时为 null */
  publishedAt: string | null;
  dateKnown: boolean;
  isLive: boolean;
  seen: boolean;
  saved: boolean;
  /** 显示时顺时针转多少度（0/90/180/270）：给「躺着拍」的上传用，见 `/api/yt/videos/{id}/rotate` */
  rotateDeg: number;
  downloaded: boolean;
  description: string;
  channel: YtChannelBrief;
}

/** 站内播放的一档画质（后端从 HLS master 里折出来的） */
export interface YtStreamQuality {
  height: number;
  /** 例如 1080p */
  label: string;
  bandwidth: number;
  codecs: string;
  /**
   * 这一档走「单文件 HLS」：AV1 分片 mp4 按 sidx 切成 HLS 分片发（`/dash.m3u8`）。
   *
   * 目前只有 2160p 为 true。HLS 清单里 4K 只有 VP9 28.2 Mbps（实测 40 秒卡 13 次），
   * 而 DASH 的 AV1 只要 13.46 Mbps；分片放出来之后起播只要第一段（约 1.5 秒），
   * 拖到哪取到哪，也不用先在服务端把整条流下完。
   */
  dash?: boolean;
}

/** 站内在线播放的解析结果：一个 HLS 地址 + 可选画质 */
export interface YtStreamInfo {
  videoId: string;
  title: string;
  durationSec: number;
  /** 站内 master 清单地址（/api/yt/videos/{id}/stream.m3u8） */
  src: string;
  /** 按高度降序 */
  qualities: YtStreamQuality[];
  /** 默认档（最高） */
  default: number;
  /** 上游签名到期时间（RFC3339） */
  expiresAt: string;
  /** 4K 那条「单文件 HLS」的 master 地址（/api/yt/videos/{id}/dash.m3u8） */
  dash?: string;
}

export interface YtVideosPage {
  items: YtVideo[];
  nextCursor?: string;
}

export interface YtStats {
  channels: number;
  videos: number;
  /** 全部未看视频数（侧边栏角标） */
  unseen: number;
  new24h: number;
  /** 上次抓取失败的频道数 */
  errors: number;
}

export interface YtChannelsResponse {
  items: YtChannel[];
  stats: YtStats;
}

export interface YtManageResponse {
  stats: YtStats;
  channels: YtChannel[];
}

/** POST /api/yt/resolve：只解析不入库 */
export interface YtResolve {
  channelId: string;
  handle: string;
  title: string;
  avatarUrl: string;
  profileUrl: string;
  /** 这个频道已经订阅过 */
  subscribed: boolean;
  id?: number;
  /** yt-dlp 是否可用（不可用时只能抓 RSS，不能下载/回填） */
  ytdlpOk: boolean;
}

/** SSE: yt.new —— 一次同步里某个频道抓到了新视频 */
export interface YtNewEvent {
  /** yt_channels 的行 id */
  channelId: number;
  title: string;
  count: number;
}

/** GET /api/yt/health：yt-dlp 的版本健康度 */
export interface YtHealth {
  path: string;
  found: boolean;
  version: string;
  ageDays: number;
  /** 超过 90 天：下载大概率 403 */
  stale: boolean;
  ffmpegOk: boolean;
  /** 「配了路径但用不了」的原因（文件没了 / 不可执行 / --version 跑不起来），正常时为空 */
  error: string;
  /** 还没有发布时间的视频条数（墙上显示「补齐」那一条用） */
  missingDates: number;
  /** 还没有时长的视频条数（只跑过 RSS 的那批；直播不算） */
  missingDurations: number;
}
