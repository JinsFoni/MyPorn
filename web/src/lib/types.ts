/**
 * 与 docs/API.md（契约 v1 / M1）一一对应的类型。
 * 改这里之前先看契约文档；不要凭空加字段。
 */

/** M1 只打通 Pornhub；adapter 化后这里扩展 */
export type PlatformKey = 'pornhub';

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
  videoCount: number | null;
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

export interface PlaybackVariant {
  id: string;
  height: number;
  width: number;
  bandwidth: number;
  codec: string;
  /** 已经过 /api/stream 同源代理的播放地址 */
  url: string;
  /** true = HLS 清单（走 hls.js），false = 直链 mp4（原生 video） */
  hls: boolean;
  isDefault: boolean;
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

export interface OpenListSettings {
  baseUrl: string;
  root: string;
  /** 只读：是否已保存 token（永不回显明文） */
  hasToken: boolean;
}

export interface Settings {
  syncIntervalHours: number;
  globalProxy: string;
  /** 按平台覆盖代理；空字符串 = 直连 */
  platformProxy: { pornhub: string };
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
}

/** PUT /api/settings：字段均可选；token 单独写，响应不回显 */
export interface SettingsPatch {
  syncIntervalHours?: number;
  globalProxy?: string;
  platformProxy?: { pornhub?: string };
  namingTemplate?: string;
  autoDownloadNew?: boolean;
  downloadConcurrency?: number;
  downloadRateKb?: number;
  openlist?: { baseUrl?: string; root?: string; token?: string };
}

export type JobType = 'sync' | 'download' | 'upload' | 'strm';
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
export interface VideoNewEvent {
  videoId: number;
  performerId: number;
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
