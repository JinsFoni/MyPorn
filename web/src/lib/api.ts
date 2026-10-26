/**
 * 唯一的网络出口。组件不直接 fetch —— 全部走这里。
 * 契约：docs/API.md（v1 / M1）
 */
import { navigate } from './router.svelte';
import type {
  ApiErrorCode,
  ApiErrorBody,
  DownloadAccepted,
  DownloadOptions,
  DownloadsResponse,
  Health,
  JobsPage,
  LibrariesResponse,
  Library,
  LibraryInput,
  PathValidation,
  Performer,
  PerformerPage,
  PlatformInfo,
  PlaybackInfo,
  ResolvedPerformer,
  SearchPerformersResponse,
  Settings,
  SettingsPatch,
  SubscriptionCreated,
  ManageResponse,
  SyncMode,
  VideoDetail,
  VideoPage,
} from './types';

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
    return error.message || FALLBACK_MESSAGES[error.code] || '出错了，稍后再试';
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
  newVideos(params: { since?: string | null; cursor?: string | null; limit?: number } = {}): Promise<VideoPage> {
    return request<VideoPage>(
      `/api/videos/new${qs({ since: params.since, cursor: params.cursor, limit: params.limit })}`,
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
