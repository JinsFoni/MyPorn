/**
 * 跨页面共享的运行时状态（runes 模块，无状态库）。
 * AppShell 建立 SSE 后写这里；各页面读这里。
 */
import { api } from './api';
import type { DownloadItem, Job, SyncProgress } from './types';

export type ToastKind = 'info' | 'success' | 'error';

export interface Toast {
  id: number;
  message: string;
  kind: ToastKind;
}

export interface SyncState extends SyncProgress {
  /** 客户端收到时间，用于判断"是否还在同步" */
  at: number;
}

let toastSeq = 0;

/**
 * 「下载」页里的两种任务：`download`（本地库下载）与 `yt_download`（YouTube 存进本地库）。
 * **两个都要收**——`/api/downloads` 两种都返回，用户在下载页看到的是同一件事。
 * 2026-11-02 这里只放行 `download`，于是 YouTube 那条的进度条从排队到完成一直冻在 1%
 * （实测：服务端 1.0 → 50.0 → 90 → done，界面上始终是「正在解析流… 1.0%」，
 * 因为整条 SSE 事件被那一行丢掉了，只有任务落定的整表刷新才会跳一下）。
 */
const DOWNLOAD_TYPES: ReadonlySet<string> = new Set(['download', 'yt_download']);

class LiveStore {
  toasts = $state<Toast[]>([]);
  /** 任何影响列表的事件都 +1，列表页据此刷新 */
  feedVersion = $state(0);
  connected = $state(false);
  /** performerId -> 最新同步进度 */
  sync = $state<Record<number, SyncState>>({});
  jobs = $state<Record<number, Job>>({});
  /** 下载队列（AppShell 与下载页共享，避免两处各拉一次） */
  downloads = $state<DownloadItem[]>([]);
  downloadsLoaded = $state(false);
  totalDownloadBytes = $state(0);
  /** 社交动态：上次看过之后新入库的照片数（侧边栏角标） */
  momentsUnseen = $state(0);
  /** 有新照片进来就 +1，照片墙据此刷新 */
  momentsVersion = $state(0);
  /**
   * 已经落盘的照片 id。抓完就自动下载入库（2026-11-04），照片墙靠这个把「待存」
   * 角标当场清掉——不重拉整条墙，免得把人弹回第一屏。
   */
  savedPhotoIds = $state<Set<number>>(new Set());
  /** YouTube：上次看过之后新入库的视频数（侧边栏角标） */
  ytUnseen = $state(0);
  /** 有新视频进来就 +1，YouTube 墙据此刷新 */
  ytVersion = $state(0);
  /** B 站：还有多少条投稿没点开过（侧边栏角标） */
  biliUnseen = $state(0);
  /** 有新投稿进来就 +1，B 站墙据此刷新 */
  biliVersion = $state(0);
  /** performerId -> 头像（影片卡左侧那颗圆头像；只拉一次订阅艺人） */
  performerAvatars = $state<Record<number, string>>({});
  private avatarsRequested = false;
  private downloadTimer = 0;
  private downloadPending = false;

  toast(message: string, kind: ToastKind = 'info'): void {
    const id = (toastSeq += 1);
    this.toasts = [...this.toasts, { id, message, kind }].slice(-3);
    window.setTimeout(() => this.dismiss(id), kind === 'error' ? 7000 : 4200);
  }

  dismiss(id: number): void {
    this.toasts = this.toasts.filter((t) => t.id !== id);
  }

  bumpFeed(): void {
    this.feedVersion += 1;
  }

  bumpMoments(): void {
    this.momentsVersion += 1;
  }

  /** 服务端一批照片落盘完就报一次，这里记下来（刷新页面会重新拉，不需要持久化）。 */
  markPhotosSaved(ids: number[]): void {
    if (ids.length === 0) return;
    const next = new Set(this.savedPhotoIds);
    for (const id of ids) next.add(id);
    this.savedPhotoIds = next;
  }

  setMomentsUnseen(n: number): void {
    this.momentsUnseen = n;
  }

  /** 拉一次「动态」的未看计数（启动时与进入页面后用）。失败静默：红点不值得弹错。 */
  async refreshMomentsUnseen(): Promise<void> {
    try {
      const res = await api.momentAccounts();
      this.momentsUnseen = res.unseen;
    } catch {
      /* 忽略 */
    }
  }

  bumpYt(): void {
    this.ytVersion += 1;
  }

  setYtUnseen(n: number): void {
    this.ytUnseen = n;
  }

  /** 拉一次 YouTube 的未看计数（启动时与进入页面后用）。失败静默：红点不值得弹错。 */
  async refreshYtUnseen(): Promise<void> {
    try {
      const res = await api.ytChannels();
      this.ytUnseen = res.stats.unseen;
    } catch {
      /* 忽略 */
    }
  }

  bumpBili(): void {
    this.biliVersion += 1;
  }

  setBiliUnseen(n: number): void {
    this.biliUnseen = n;
  }

  /** 拉一次 B 站的未看计数（启动时与进入页面后用）。失败静默：角标不值得弹错。 */
  async refreshBiliUnseen(): Promise<void> {
    try {
      const res = await api.biliChannels();
      this.biliUnseen = res.stats.unseen;
    } catch {
      /* 忽略 */
    }
  }

  setConnected(value: boolean): void {
    this.connected = value;
  }

  applySync(progress: SyncProgress): void {
    this.sync = { ...this.sync, [progress.performerId]: { ...progress, at: Date.now() } };
  }

  /** 最近 20s 内收到过该艺人的进度 = 正在同步 */
  isSyncing(performerId: number): boolean {
    const s = this.sync[performerId];
    return s !== undefined && Date.now() - s.at < 20000;
  }

  syncState(performerId: number): SyncState | undefined {
    const s = this.sync[performerId];
    return s && Date.now() - s.at < 20000 ? s : undefined;
  }

  recordJob(job: Job): void {
    this.jobs = { ...this.jobs, [job.id]: job };
  }

  /** 排队 + 运行 + 暂停中的下载数量（导航角标用） */
  get activeDownloads(): number {
    return this.downloads.filter((d) => d.status === 'queued' || d.status === 'running' || d.status === 'paused')
      .length;
  }

  /** 拉取下载队列；连续事件用 1.2s 合并，避免风暴式请求 */
  refreshDownloads(immediate = false): void {
    if (immediate) {
      window.clearTimeout(this.downloadTimer);
      this.downloadTimer = 0;
      void this.fetchDownloads();
      return;
    }
    if (this.downloadTimer !== 0) {
      this.downloadPending = true;
      return;
    }
    this.downloadTimer = window.setTimeout(() => {
      this.downloadTimer = 0;
      void this.fetchDownloads().finally(() => {
        if (this.downloadPending) {
          this.downloadPending = false;
          this.refreshDownloads();
        }
      });
    }, 1200);
  }

  /** 本地先行更新一条任务（SSE 到达时立即反映到 UI，无需等整表刷新） */
  applyJobEvent(event: {
    id: number;
    type: string;
    status: string;
    progress: number;
    message: string | null;
    error: string | null;
    bytesDone?: number;
    bytesTotal?: number;
    speedBps?: number;
  }): void {
    if (!DOWNLOAD_TYPES.has(event.type)) return;
    const idx = this.downloads.findIndex((d) => d.id === event.id);
    if (idx === -1) {
      this.refreshDownloads();
      return;
    }
    const next = [...this.downloads];
    const current = next[idx];
    if (!current) return;
    next[idx] = {
      ...current,
      status: event.status as DownloadItem['status'],
      progress: event.progress,
      message: event.message,
      error: event.error,
      bytesDone: event.bytesDone ?? current.bytesDone,
      bytesTotal: event.bytesTotal ?? current.bytesTotal,
      speedBps: event.speedBps ?? current.speedBps,
    };
    this.downloads = next;
    // 状态切换会带来 activeCount/本地路径等变化，稍后再对齐一次服务端
    const settled =
      event.status === 'done' || event.status === 'failed' || event.status === 'canceled';
    if (settled) {
      this.refreshDownloads();
      if (event.status === 'done') this.bumpFeed();
    }
  }

  /** 拉一次订阅艺人的头像表（影片卡片用）。失败静默：头像只是装饰 */
  async ensurePerformerAvatars(): Promise<void> {
    if (this.avatarsRequested) return;
    this.avatarsRequested = true;
    try {
      const res = await api.performers({ subscribed: true, limit: 200 });
      const map: Record<number, string> = {};
      for (const p of res.items) {
        if (p.avatarUrl) map[p.id] = p.avatarUrl;
      }
      this.performerAvatars = map;
    } catch {
      this.avatarsRequested = false;
    }
  }

  private async fetchDownloads(): Promise<void> {
    try {
      const res = await api.downloads({ limit: 50 });
      this.downloads = res.items;
      this.totalDownloadBytes = res.totalSizeBytes;
      this.downloadsLoaded = true;
    } catch {
      /* 拉取失败保留旧数据，SSE 会再触发 */
    }
  }
}

export const live = new LiveStore();
