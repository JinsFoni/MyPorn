/**
 * SSE 封装（EventSource）：单连接、自动重连、页隐藏时断开。
 * 事件与 docs/API.md §5 对齐。
 */
import type {
  JobUpdatedEvent,
  PhotoNewEvent,
  SocialProgressEvent,
  SyncProgress,
  PhotoSavedEvent,
  VideoNewEvent,
  YtNewEvent,
} from './types';

export interface LiveEventMap {
  'job.updated': JobUpdatedEvent;
  'sync.progress': SyncProgress;
  'video.new': VideoNewEvent;
  'photo.new': PhotoNewEvent;
  'photo.saved': PhotoSavedEvent;
  'social.progress': SocialProgressEvent;
  'yt.new': YtNewEvent;
  ping: Record<string, never>;
}

export type LiveHandlers = {
  [K in keyof LiveEventMap]?: (data: LiveEventMap[K]) => void;
};

export interface LiveOptions {
  /** 连接状态变化（顶栏指示 / 离线提示用） */
  onStatus?: (connected: boolean) => void;
  url?: string;
}

const RECONNECT_BASE_MS = 1000;
const RECONNECT_MAX_MS = 20000;
const SILENCE_LIMIT_MS = 70000; // 服务端每 25s 一次 ping，70s 没动静就重连

function parseData(raw: string): unknown {
  try {
    return JSON.parse(raw) as unknown;
  } catch {
    return null;
  }
}

/**
 * 建立实时事件连接。返回清理函数（组件卸载时调用）。
 */
/** 服务端可能推的所有事件名（`bind` 对没有处理函数的名字是空操作，所以列全无害）。 */
const EVENTS = [
  'job.updated',
  'sync.progress',
  'video.new',
  'photo.new',
  'photo.saved',
  'social.progress',
  'yt.new',
  'ping',
] as const satisfies readonly (keyof LiveEventMap)[];

export function connectEvents(handlers: LiveHandlers, options: LiveOptions = {}): () => void {
  const url = options.url ?? '/api/events';
  let source: EventSource | null = null;
  let retryTimer = 0;
  let watchdog = 0;
  let attempts = 0;
  let lastMessageAt = Date.now();
  let stopped = false;

  options.onStatus?.(false);

  const close = (): void => {
    if (source) {
      source.close();
      source = null;
    }
    options.onStatus?.(false);
  };

  const scheduleReconnect = (): void => {
    if (stopped || retryTimer !== 0) return;
    const delay = Math.min(RECONNECT_BASE_MS * 2 ** attempts, RECONNECT_MAX_MS);
    attempts += 1;
    retryTimer = window.setTimeout(() => {
      retryTimer = 0;
      open();
    }, delay);
  };

  const bind = <K extends keyof LiveEventMap>(src: EventSource, name: K): void => {
    src.addEventListener(name, (event) => {
      lastMessageAt = Date.now();
      const data = parseData((event as MessageEvent<string>).data);
      if (data === null) return;
      handlers[name]?.(data as LiveEventMap[K]);
    });
  };

  const open = (): void => {
    if (stopped) return;
    close();
    const src = new EventSource(url);
    source = src;
    src.addEventListener('open', () => {
      attempts = 0;
      lastMessageAt = Date.now();
      options.onStatus?.(true);
    });
    src.addEventListener('error', () => {
      // 浏览器会自己重试，但不可控；这里主动接管，避免僵死连接
      close();
      // 会话来路不明的失败（最常见是登录过期 → 401）不该每 20 秒重试一次，
      // 那只会把控制台刷满；先问一句「还登录着吗」，没登录就彻底停手，
      // 交给普通的 401 流程把人送回登录页。
      void fetch('/api/auth/me', { credentials: 'same-origin' })
        .then((res) => {
          if (stopped) return;
          if (res.status === 401) {
            stopped = true;
            options.onStatus?.(false);
            return;
          }
          scheduleReconnect();
        })
        .catch(() => {
          scheduleReconnect(); // 探测本身失败（断网）仍按原逻辑重连
        });
    });
    // 列全：以前这里是手写的 4 个名字，`photo.new` / `social.progress` / `yt.new` 三个
    // 一直在 LiveEventMap 里躺着却没被 bind——服务端发了、AppShell 也写了处理函数，
    // 就是永远不触发（不报错、不刷新，查起来极费劲）。新增事件记得往这张表里加。
    for (const name of EVENTS) bind(src, name);
  };

  const onVisibility = (): void => {
    if (document.visibilityState === 'hidden') {
      close();
      if (retryTimer !== 0) {
        window.clearTimeout(retryTimer);
        retryTimer = 0;
      }
      return;
    }
    attempts = 0;
    lastMessageAt = Date.now();
    open();
  };

  watchdog = window.setInterval(() => {
    if (stopped) return;
    if (document.visibilityState === 'hidden') return;
    if (Date.now() - lastMessageAt > SILENCE_LIMIT_MS) {
      close();
      scheduleReconnect();
    }
  }, 30000);

  document.addEventListener('visibilitychange', onVisibility);
  open();

  return () => {
    stopped = true;
    document.removeEventListener('visibilitychange', onVisibility);
    window.clearInterval(watchdog);
    if (retryTimer !== 0) window.clearTimeout(retryTimer);
    close();
  };
}
