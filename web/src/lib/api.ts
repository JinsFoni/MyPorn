/**
 * 唯一的网络出口。组件不直接 fetch —— 全部走这里。
 * 契约：docs/API.md（v1 / M1）
 */
import { navigate } from './router.svelte';
import type {
  ApiErrorCode,
  ApiErrorBody,
  BiliChannelsResponse,
  BiliHealth,
  BiliManageResponse,
  BiliQrcode,
  BiliQrcodePoll,
  DownloadAccepted,
  DownloadOptions,
  DownloadsResponse,
  Health,
  JobsPage,
  LibrariesResponse,
  Library,
  LibraryInput,
  LibraryVideosPage,
  PathValidation,
  Performer,
  PerformerPage,
  PlatformInfo,
  PlaybackInfo,
  RemuxStatus,
  ResolvedPerformer,
  SearchPerformersResponse,
  Settings,
  SettingsPatch,
  SubscriptionCreated,
  ManageResponse,
  MomentAccount,
  MomentResolve,
  MomentsAccountsResponse,
  MomentsManageResponse,
  MomentsPage,
  SyncMode,
  YtChannel,
  YtChannelsResponse,
  YtHealth,
  YtManageResponse,
  YtResolve,
  YtStreamInfo,
  YtVideo,
  YtVideosPage,
  Video,
  VideoDetail,
  VideoNeighbors,
  VideoPage,
} from './types';

/** 「上一个 / 下一个」按哪份列表算：字段和 /api/videos 的筛选参数一一对应 */
export interface AdjacentScope {
  performerId?: number;
  platform?: string;
  excludePlatform?: string;
  onlyNew?: boolean;
  q?: string;
  subscribedOnly?: boolean;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: ApiErrorCode | string;
  readonly details: Record<string, unknown> | null;

  constructor(status: number, code: string, message: string, details: Record<string, unknown> | null = null) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.details = details;
  }
}

const FALLBACK_MESSAGES: Record<string, string> = {
  unauthorized: '登录已过期，请重新登录',
  invalid_password: '密码不正确',
  too_many_attempts: '尝试次数过多，请 5 分钟后再试',
  bad_request: '请求参数有误',
  not_found: '内容不存在或已被删除',
  unsupported_platform: '暂不支持这个平台的链接',
  resolve_failed: '平台解析失败，稍后再试',
  rate_limited: '平台限流，稍后再试',
  no_library: '还没有可用的本地库，请先在设置里创建',
  already_queued: '这部影片已经在下载队列里了',
  internal: '服务器出错了，稍后再试',
  network_error: '无法连接服务器',
  aborted: '请求已取消',
};

/** 把任意错误转成一句能展示给用户的中文 */
export function messageFor(error: unknown): string {
  if (error instanceof ApiError) {
    const base = error.message || FALLBACK_MESSAGES[error.code] || '出错了，稍后再试';
    // 限流时后端会给冷却截止时间，补一句「HH:MM 后自动恢复」，
    // 否则用户只会看到「平台限流」然后反复点重试。
    const resetAt = error.code === 'rate_limited' ? error.details?.resetAt : null;
    if (typeof resetAt === 'string') {
      const when = new Date(resetAt);
      if (!Number.isNaN(when.getTime())) {
        const hh = String(when.getHours()).padStart(2, '0');
        const mm = String(when.getMinutes()).padStart(2, '0');
        return `${base}（${hh}:${mm} 之后自动恢复）`;
      }
    }
    return base;
  }
  if (error instanceof Error) return error.message;
  return '出错了，稍后再试';
}

function redirectToLogin(): void {
  const here = `${window.location.pathname}${window.location.search}`;
  if (here.startsWith('/login')) return;
  navigate(`/login?next=${encodeURIComponent(here)}`, { replace: true });
}

function parseErrorBody(body: unknown): { code: string; message: string; details: Record<string, unknown> | null } {
  if (body && typeof body === 'object' && 'error' in body) {
    const raw = (body as ApiErrorBody).error;
    if (raw && typeof raw === 'object') {
      const code = typeof raw.code === 'string' ? raw.code : 'internal';
      const message = typeof raw.message === 'string' && raw.message ? raw.message : FALLBACK_MESSAGES[code] ?? FALLBACK_MESSAGES.internal ?? '出错了';
      const details = raw.details && typeof raw.details === 'object' ? raw.details : null;
      return { code, message, details };
    }
  }
  return { code: 'internal', message: FALLBACK_MESSAGES.internal ?? '出错了', details: null };
}

type QueryValue = string | number | boolean | null | undefined;

function qs(params: Record<string, QueryValue>): string {
  const sp = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === null || value === undefined || value === '') continue;
    sp.set(key, String(value));
  }
  const s = sp.toString();
  return s ? `?${s}` : '';
}

type Method = 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE';

interface RequestOptions {
  method?: Method;
  body?: unknown;
  signal?: AbortSignal;
  /** false = 这个请求的 401 不触发全局跳登录（登录页/启动自检用） */
  authRedirect?: boolean;
}

async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const { method = 'GET', body, signal, authRedirect = true } = opts;
  const headers: Record<string, string> = { Accept: 'application/json' };
  if (body !== undefined) headers['Content-Type'] = 'application/json';

  let res: Response;
  try {
    res = await fetch(path, {
      method,
      headers,
      credentials: 'same-origin',
      signal,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') {
      throw new ApiError(0, 'aborted', FALLBACK_MESSAGES.aborted ?? '请求已取消');
    }
    throw new ApiError(0, 'network_error', FALLBACK_MESSAGES.network_error ?? '无法连接服务器');
  }

  if (res.status === 204) return undefined as unknown as T;

  const text = await res.text();
  let parsed: unknown = null;
  if (text.length > 0) {
    try {
      parsed = JSON.parse(text) as unknown;
    } catch {
      parsed = null;
    }
  }

  if (!res.ok) {
    const { code, message, details } = parseErrorBody(parsed);
    if (res.status === 401 && code === 'unauthorized' && authRedirect) redirectToLogin();
    throw new ApiError(res.status, code, message, details);
  }

  return parsed as T;
}

function id(value: number | string): string {
  return encodeURIComponent(String(value));
}

export interface VideoQuery {
  performerId?: number | string | null;
  cursor?: string | null;
  limit?: number;
  q?: string;
}

export const api = {
  // ---- 鉴权 ----
  login(password: string): Promise<void> {
    return request<void>('/api/auth/login', { method: 'POST', body: { password }, authRedirect: false });
  },
  logout(): Promise<void> {
    return request<void>('/api/auth/logout', { method: 'POST' });
  },
  me(): Promise<{ authenticated: boolean }> {
    return request<{ authenticated: boolean }>('/api/auth/me', { authRedirect: false });
  },

  // ---- 平台 ----
  platforms(): Promise<PlatformInfo[]> {
    return request<PlatformInfo[]>('/api/platforms');
  },

  // ---- 艺人 ----
  performers(params: { q?: string; subscribed?: boolean; page?: number; limit?: number } = {}): Promise<PerformerPage> {
    return request<PerformerPage>(
      `/api/performers${qs({
        q: params.q,
        subscribed: params.subscribed,
        page: params.page,
        limit: params.limit,
      })}`,
    );
  },
  performer(performerId: number | string): Promise<Performer> {
    return request<Performer>(`/api/performers/${id(performerId)}`);
  },
  resolvePerformer(url: string): Promise<ResolvedPerformer> {
    return request<ResolvedPerformer>('/api/performers/resolve', { method: 'POST', body: { url } });
  },
  searchPerformers(params: { q: string; platform?: string; page?: number }): Promise<SearchPerformersResponse> {
    return request<SearchPerformersResponse>(
      `/api/search/performers${qs({ q: params.q, platform: params.platform ?? 'pornhub', page: params.page })}`,
    );
  },

  // ---- 订阅 ----
  subscriptions(): Promise<{ items: Performer[] }> {
    return request<{ items: Performer[] }>('/api/subscriptions');
  },
  subscribe(input: { url: string; backfillLimit?: number }): Promise<SubscriptionCreated> {
    return request<SubscriptionCreated>('/api/subscriptions', {
      method: 'POST',
      body: { url: input.url, backfillLimit: input.backfillLimit ?? 0 },
    });
  },
  updateSubscription(subscriptionId: number | string, patch: { enabled: boolean }): Promise<Performer> {
    return request<Performer>(`/api/subscriptions/${id(subscriptionId)}`, { method: 'PATCH', body: patch });
  },
  unsubscribe(subscriptionId: number | string): Promise<void> {
    return request<void>(`/api/subscriptions/${id(subscriptionId)}`, { method: 'DELETE' });
  },
  syncSubscription(subscriptionId: number | string, mode: SyncMode): Promise<{ jobId: number }> {
    return request<{ jobId: number }>(`/api/subscriptions/${id(subscriptionId)}/sync`, {
      method: 'POST',
      body: { mode },
    });
  },

  // ---- 管理 ----
  /** 统计 + 关注管理，整页一个请求（共用同一份快照） */
  manage(): Promise<ManageResponse> {
    return request<ManageResponse>('/api/manage');
  },

  // ---- 影片 ----
  videos(params: VideoQuery = {}): Promise<VideoPage> {
    return request<VideoPage>(
      `/api/videos${qs({
        performerId: params.performerId,
        cursor: params.cursor,
        limit: params.limit,
        q: params.q,
      })}`,
    );
  },
  newVideos(
    params: { since?: string | null; cursor?: string | null; limit?: number; performerId?: number | null } = {},
  ): Promise<VideoPage> {
    return request<VideoPage>(
      `/api/videos/new${qs({
        since: params.since,
        cursor: params.cursor,
        limit: params.limit,
        performerId: params.performerId,
      })}`,
    );
  },
  video(videoId: number | string): Promise<VideoDetail> {
    return request<VideoDetail>(`/api/videos/${id(videoId)}`);
  },
  markSeen(videoId: number | string): Promise<void> {
    return request<void>(`/api/videos/${id(videoId)}/seen`, { method: 'POST' });
  },
  playback(videoId: number | string): Promise<PlaybackInfo> {
    return request<PlaybackInfo>(`/api/videos/${id(videoId)}/playback`);
  },
  /**
   * 播放器「上一个 / 下一个」用：这条影片在**当前这份列表**里的左右邻居。
   *
   * 传的是「那份列表的筛选条件」，所以要跟列表接口同口径：关注流传 `excludePlatform=bilibili`，
   * 艺人页传 `performerId`，B 站墙传 `platform=bilibili`（可再加 `performerId` / `onlyNew`）。
   * 到列表头/尾那一侧是 null——按下去就没了，不会绕回第一条。
   */
  adjacent(videoId: number | string, scope: AdjacentScope = {}): Promise<VideoNeighbors> {
    return request<VideoNeighbors>(
      `/api/videos/${id(videoId)}/adjacent${qs({
        performerId: scope.performerId,
        platform: scope.platform,
        excludePlatform: scope.excludePlatform,
        onlyNew: scope.onlyNew === true ? 1 : undefined,
        q: scope.q,
        subscribedOnly: scope.subscribedOnly === true ? 1 : undefined,
      })}`,
    );
  },
  /**
   * 分轨档位（B 站 1080P+/4K）的合流进度。
   *
   * 第一次问就顺手把合流启动起来，之后每次问只是取快照；state 变 ready 才值得把
   * `/remux?v=…` 交给 <video>——否则那个请求要挂好几分钟才回第一个字节。
   */
  remuxStatus(videoId: number | string, variantId: string): Promise<RemuxStatus> {
    return request<RemuxStatus>(
      `/api/videos/${id(videoId)}/remux${qs({ v: variantId, status: 1 })}`,
    );
  },

  // ---- 任务 ----
  jobs(params: { type?: string; status?: string; limit?: number } = {}): Promise<JobsPage> {
    return request<JobsPage>(`/api/jobs${qs({ type: params.type, status: params.status, limit: params.limit })}`);
  },
  cancelJob(jobId: number | string): Promise<void> {
    return request<void>(`/api/jobs/${id(jobId)}/cancel`, { method: 'POST' });
  },

  // ---- 本地库（M2）----
  libraries(): Promise<LibrariesResponse> {
    return request<LibrariesResponse>('/api/libraries');
  },
  createLibrary(input: LibraryInput): Promise<Library> {
    return request<Library>('/api/libraries', { method: 'POST', body: input });
  },
  patchLibrary(libraryId: number | string, input: LibraryInput): Promise<Library> {
    return request<Library>(`/api/libraries/${id(libraryId)}`, { method: 'PATCH', body: input });
  },
  deleteLibrary(libraryId: number | string): Promise<void> {
    return request<void>(`/api/libraries/${id(libraryId)}`, { method: 'DELETE' });
  },
  validateLibraryPath(rootPath: string): Promise<PathValidation> {
    return request<PathValidation>('/api/libraries/validate', { method: 'POST', body: { rootPath } });
  },

  // ---- 下载（M2）----
  downloadOptions(videoId: number | string): Promise<DownloadOptions> {
    return request<DownloadOptions>(`/api/videos/${id(videoId)}/download-options`);
  },
  createDownload(
    videoId: number | string,
    body: { libraryId?: number; overwrite?: boolean } = {},
  ): Promise<DownloadAccepted> {
    return request<DownloadAccepted>(`/api/videos/${id(videoId)}/download`, { method: 'POST', body });
  },
  downloads(params: { limit?: number; status?: string } = {}): Promise<DownloadsResponse> {
    return request<DownloadsResponse>(`/api/downloads${qs({ limit: params.limit, status: params.status })}`);
  },

  /**
   * 媒体库：磁盘上已经下载下来的影片（不是任务队列，队列在 downloads）。
   * 排序/筛选/搜索都走服务端，游标分页。
   */
  libraryVideos(
    params: {
      cursor?: string | null;
      libraryId?: number | null;
      platform?: string | null;
      q?: string | null;
      sort?: string | null;
      limit?: number;
    } = {},
  ): Promise<LibraryVideosPage> {
    return request<LibraryVideosPage>(
      `/api/library/videos${qs({
        cursor: params.cursor,
        libraryId: params.libraryId,
        platform: params.platform,
        q: params.q,
        sort: params.sort,
        limit: params.limit,
      })}`,
    );
  },

  /** 某个库封面图（下载时落盘的 thumb.jpg）；给 <img src> 直接用，不需要鉴权头 */
  libraryThumb(videoId: number, libraryId: number): string {
    return `/api/videos/${id(videoId)}/art?libraryId=${id(libraryId)}&k=thumb`;
  },
  pauseJob(jobId: number | string): Promise<void> {
    return request<void>(`/api/jobs/${id(jobId)}/pause`, { method: 'POST' });
  },
  resumeJob(jobId: number | string): Promise<void> {
    return request<void>(`/api/jobs/${id(jobId)}/resume`, { method: 'POST' });
  },
  retryJob(jobId: number | string): Promise<void> {
    return request<void>(`/api/jobs/${id(jobId)}/retry`, { method: 'POST' });
  },
  deleteJob(jobId: number | string): Promise<void> {
    return request<void>(`/api/jobs/${id(jobId)}`, { method: 'DELETE' });
  },

  // ---- 社交动态（M5）----
  /** 照片墙（keyset 分页） */
  moments(
    params: {
      cursor?: string | null;
      accountId?: number | null;
      performerId?: number | null;
      platform?: string | null;
      limit?: number;
    } = {},
  ): Promise<MomentsPage> {
    return request<MomentsPage>(
      `/api/moments${qs({
        cursor: params.cursor,
        accountId: params.accountId,
        performerId: params.performerId,
        platform: params.platform,
        limit: params.limit,
      })}`,
    );
  },
  momentAccounts(): Promise<MomentsAccountsResponse> {
    return request<MomentsAccountsResponse>('/api/moments/accounts');
  },
  /** 只探测（不写库），用于添加前预览 */
  resolveMoment(url: string): Promise<MomentResolve> {
    return request<MomentResolve>('/api/moments/resolve', { method: 'POST', body: { url } });
  },
  addMoment(url: string): Promise<{ account: MomentAccount; jobId: number | null }> {
    return request<{ account: MomentAccount; jobId: number | null }>('/api/moments/accounts', {
      method: 'POST',
      body: { url },
    });
  },
  patchMomentAccount(
    accountId: number | string,
    patch: { enabled?: boolean; performerId?: number; mode?: 'full' },
  ): Promise<MomentAccount> {
    return request<MomentAccount>(`/api/moments/accounts/${id(accountId)}`, { method: 'PATCH', body: patch });
  },
  deleteMomentAccount(accountId: number | string): Promise<void> {
    return request<void>(`/api/moments/accounts/${id(accountId)}`, { method: 'DELETE' });
  },
  syncMomentAccount(accountId: number | string, mode?: 'auto' | 'full'): Promise<{ jobId: number | null; queued: boolean }> {
    return request<{ jobId: number | null; queued: boolean }>(`/api/moments/accounts/${id(accountId)}/sync`, {
      method: 'POST',
      body: { mode: mode ?? 'auto' },
    });
  },
  /** 手动解除本地冷却（换了 Cookie / 换了出口 IP 之后不该再等上一轮的 15 分钟） */
  clearMomentCooldown(platform?: string): Promise<{ cleared: string[] }> {
    return request<{ cleared: string[] }>('/api/moments/cooldown', {
      method: 'POST',
      body: { platform: platform ?? '' },
    });
  },
  markMomentsSeen(): Promise<{ unseen: number }> {
    return request<{ unseen: number }>('/api/moments/seen', { method: 'POST' });
  },
  saveMomentPhotos(photoIds: number[], libraryId?: number): Promise<{ jobId: number; count: number; libraryId: number }> {
    return request<{ jobId: number; count: number; libraryId: number }>('/api/moments/photos/save', {
      method: 'POST',
      body: { photoIds, libraryId: libraryId ?? 0 },
    });
  },
  manageMoments(): Promise<MomentsManageResponse> {
    return request<MomentsManageResponse>('/api/manage/moments');
  },

  // ---- B 站（M7）----
  /**
   * UP 主列表（含未看计数）。B 站的投稿同步时就落进 videos，
   * 所以墙上翻页用的是通用的 /api/videos 那套形状，只是多一个 platform 过滤。
   */
  biliChannels(): Promise<BiliChannelsResponse> {
    return request<BiliChannelsResponse>('/api/bilibili/channels');
  },
  biliVideos(
    params: { cursor?: string | null; channelId?: number | null; unseen?: boolean; limit?: number } = {},
  ): Promise<{ items: Video[]; nextCursor: string | null }> {
    return request<{ items: Video[]; nextCursor: string | null }>(
      `/api/bilibili/videos${qs({
        cursor: params.cursor,
        channelId: params.channelId,
        unseen: params.unseen ? 1 : null,
        limit: params.limit,
      })}`,
    );
  },
  /** ids 只标这几条（点开某一条时用）；channelId 只标这位 UP 主的；都不给 = 全站 B 站 */
  biliSeen(payload: { ids?: number[]; channelId?: number }): Promise<{ unseen: number }> {
    return request<{ unseen: number }>('/api/bilibili/seen', { method: 'POST', body: payload });
  },
  /** 把未看的投稿批量排进下载队列（B 站一条条点太慢：480P 要先合流） */
  biliDownloadUnseen(channelId: number | string, limit?: number): Promise<{ queued: number; libraryId: number }> {
    return request<{ queued: number; libraryId: number }>(`/api/bilibili/channels/${id(channelId)}/download-unseen`, {
      method: 'POST',
      body: { limit: limit ?? 0 },
    });
  },
  biliHealth(): Promise<BiliHealth> {
    return request<BiliHealth>('/api/bilibili/health');
  },
  /** 扫码登录第 1 步：拿二维码 */
  biliQrcodeGenerate(): Promise<BiliQrcode> {
    return request<BiliQrcode>('/api/bilibili/login/qrcode', { method: 'POST' });
  },
  /** 扫码登录第 2 步：轮询；success 时服务端已经落库 */
  biliQrcodePoll(key: string): Promise<BiliQrcodePoll> {
    return request<BiliQrcodePoll>(`/api/bilibili/login/qrcode${qs({ key })}`);
  },
  manageBili(): Promise<BiliManageResponse> {
    return request<BiliManageResponse>('/api/manage/bilibili');
  },

  // ---- YouTube 订阅（M6）----
  /** 视频墙（keyset 分页）；时间未知的条目排在最后 */
  ytVideos(
    params: {
      cursor?: string | null;
      channelId?: number | null;
      unseen?: boolean;
      saved?: boolean;
      limit?: number;
    } = {},
  ): Promise<YtVideosPage> {
    return request<YtVideosPage>(
      `/api/yt/videos${qs({
        cursor: params.cursor,
        channelId: params.channelId,
        unseen: params.unseen ? 1 : null,
        saved: params.saved ? 1 : null,
        limit: params.limit,
      })}`,
    );
  },
  ytChannels(): Promise<YtChannelsResponse> {
    return request<YtChannelsResponse>('/api/yt/channels');
  },
  /** 只探测（不写库），用于添加前预览 */
  ytResolve(url: string): Promise<YtResolve> {
    return request<YtResolve>('/api/yt/resolve', { method: 'POST', body: { url } });
  },
  ytAdd(url: string): Promise<{ channel: YtChannel; created: boolean }> {
    return request<{ channel: YtChannel; created: boolean }>('/api/yt/channels', {
      method: 'POST',
      body: { url },
    });
  },
  ytPatch(channelId: number | string, patch: { enabled?: boolean }): Promise<YtChannel> {
    return request<YtChannel>(`/api/yt/channels/${id(channelId)}`, { method: 'PATCH', body: patch });
  },
  ytDelete(channelId: number | string): Promise<void> {
    return request<void>(`/api/yt/channels/${id(channelId)}`, { method: 'DELETE' });
  },
  /** deep=true 走 yt-dlp 深度回填（默认列 200 条），否则走 RSS 增量（最近 15 条） */
  ytSync(channelId: number | string, deep = false): Promise<{ jobId: number }> {
    return request<{ jobId: number }>(`/api/yt/channels/${id(channelId)}/sync`, {
      method: 'POST',
      body: { deep },
    });
  },
  ytDownload(videoId: number | string, opts: { libraryId?: number; overwrite?: boolean } = {}): Promise<{
    jobId: number;
    libraryId: number;
  }> {
    return request<{ jobId: number; libraryId: number }>(`/api/yt/videos/${id(videoId)}/download`, {
      method: 'POST',
      body: { libraryId: opts.libraryId ?? 0, overwrite: opts.overwrite ?? false },
    });
  },
  ytDownloadUnseen(channelId: number | string, limit?: number): Promise<{ queued: number; jobIds: number[] }> {
    return request<{ queued: number; jobIds: number[] }>(`/api/yt/channels/${id(channelId)}/download-unseen`, {
      method: 'POST',
      body: { limit: limit ?? 0 },
    });
  },
  ytSeen(payload: { ids?: number[]; channelId?: number }): Promise<{ unseen: number }> {
    return request<{ unseen: number }>('/api/yt/videos/seen', { method: 'POST', body: payload });
  },
  /** 单条视频按需补全信息（深度回填来的条目没有发布时间） */
  ytRefresh(videoId: number | string): Promise<YtVideo> {
    return request<YtVideo>(`/api/yt/videos/${id(videoId)}/refresh`, { method: 'POST' });
  },
  ytSave(videoId: number | string, saved: boolean): Promise<{ ok: boolean; saved: boolean }> {
    return request<{ ok: boolean; saved: boolean }>(`/api/yt/videos/${id(videoId)}/save`, {
      method: 'POST',
      body: { saved },
    });
  },
  /** 记住这条视频要顺时针转多少度显示（0/90/180/270） */
  ytRotate(videoId: number | string, deg: number): Promise<{ ok: boolean; rotateDeg: number }> {
    return request<{ ok: boolean; rotateDeg: number }>(`/api/yt/videos/${id(videoId)}/rotate`, {
      method: 'POST',
      body: { deg },
    });
  },
  /** 按 YouTube 视频 id 单取一条（深链接 /youtube?v=… 用：墙上那一页未必包含这条） */
  ytVideoLookup(videoId: string): Promise<YtVideo> {
    return request<YtVideo>(`/api/yt/videos/lookup?videoId=${encodeURIComponent(videoId)}`);
  },
  /** 站内在线播放：解析上游 HLS 清单（后端跑一次 yt-dlp，结果缓存半小时） */
  ytStream(videoId: number | string): Promise<YtStreamInfo> {
    return request<YtStreamInfo>(`/api/yt/videos/${id(videoId)}/stream`);
  },
  /** yt-dlp 能不能用、版本多新（「过期」是最常见的失败原因） */
  ytHealth(): Promise<YtHealth> {
    return request<YtHealth>('/api/yt/health');
  },
  /**
   * 给缺元信息的那批视频补一遍（发布时间 / 时长 / 播放量）：后台任务，一条一个普通 GET。
   * total = 当前要补多少条；jobId = 0 表示本来就一条不缺。
   * 两样缺口分开数：RSS 通道不给时长、yt-dlp flat 通道不给时间戳。
   */
  ytFillMeta(): Promise<{ jobId: number; total: number; missingDates: number; missingDurations: number }> {
    return request<{ jobId: number; total: number; missingDates: number; missingDurations: number }>(
      '/api/yt/fill-meta',
      { method: 'POST' },
    );
  },
  manageYt(): Promise<YtManageResponse> {
    return request<YtManageResponse>('/api/manage/youtube');
  },

  // ---- 设置 ----
  settings(): Promise<Settings> {
    return request<Settings>('/api/settings');
  },
  updateSettings(patch: SettingsPatch): Promise<Settings> {
    return request<Settings>('/api/settings', { method: 'PUT', body: patch });
  },

  // ---- 健康检查（Vite 代理直通，无需鉴权） ----
  health(): Promise<Health> {
    return request<Health>('/healthz', { authRedirect: false });
  },
};
