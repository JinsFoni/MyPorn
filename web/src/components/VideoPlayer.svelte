<script lang="ts">
/**
 * Video.js 10 播放器外壳。
 *
 * 这里是全站唯一的播放器实现：`<video-player>`（状态）＋ `<video-skin>`（整套控件：大播放键、
 * 进度条与缓冲、时间、音量、倍速、字幕、设置菜单、画中画、全屏、错误弹窗、手势与快捷键）。
 * 皮肤用 `--media-*` 自定义属性调成站点的单色冷调，不引入第二套视觉语言。
 *
 * 片源策略（见 lib/player.ts）：每个清晰度是**一条独立的清单/直链**，由后端解析后交给这里，
 * 所以播放器一次只放一条，切档＝换 source 并保留进度；不用 hls.js 的自适应 ABR。
 * HLS 走 `<hlsjs-video>`（hls.js 引擎，AV1/HEVC 都能过），mp4 走原生 `<video>`（不下载 hls.js）。
 */
  import { onMount } from 'svelte';
  import '@videojs/html/video/player';
  import '@videojs/html/video/skin';
  import '@videojs/html/i18n';

  /** hlsjs-video 的 `source` 属性（原生 <video> 没有；按需断言） */
  interface EngineSourceHost extends HTMLElement {
    source: { src: string; type: string; capRenditionToPlayerSize?: boolean } | null;
  }

  interface Props {
    /** 同源代理地址（/api/stream?...），变即换片源 */
    src: string;
    /** Video.js 的 source.type：HLS 用 application/vnd.apple.mpegurl，直链用 video/mp4 */
    type: string;
    /** 'hls' 用 <hlsjs-video>（也能放 mp4），'native' 用原生 <video> */
    engine: 'hls' | 'native';
    poster?: string | null;
    title: string;
    /** 首次真正开始播放（用来上报「已看」） */
    onStarted?: () => void;
    /** 播放失败（含 hls.js 的不可恢复错误），交给页面自己的错误浮层处理 */
    onFailed?: (message: string) => void;
  }

  let { src, type, engine, poster = null, title, onStarted, onFailed }: Props = $props();

  let playerEl = $state<HTMLElement | null>(null);
  let mediaEl = $state<HTMLElement | null>(null);

  /** 播放器 store：字段见 @videojs/html 的 player-store 参考（paused / currentTime / error …） */
  type Store = {
    target: unknown;
    error: { code: number; message: string } | null;
    currentTime: number;
    duration: number;
    paused: boolean;
    started: boolean;
    canPlay: boolean;
    volume: number;
    muted: boolean;
    play: () => Promise<void>;
    pause: () => void;
    seek: (time: number) => Promise<number>;
    setVolume: (volume: number) => number;
    setMuted: (muted: boolean) => number | boolean;
    requestFullscreen: () => Promise<void>;
    exitFullscreen: () => Promise<void>;
    dismissError: () => void;
    subscribe: (fn: () => void) => () => void;
  };

  const VOLUME_KEY = 'myporn.player.volume';

  /** 换片源后要恢复的进度/播放状态 */
  let pendingSeek = 0;
  let pendingPlay = false;
  /** 首帧起播还没成功：HLS 引擎要等清单解析完才能 play，所以先挂个待播标记 */
  let wantPlay = false;
  let wantPlayTries = 0;
  let kickTimer: number | undefined;
  let appliedSrc = '';
  let attempts = 0;
  let retryTimer: ReturnType<typeof setTimeout> | undefined;
  let sawError: unknown = null;
  let startedSent = false;
  /** 上次的音量/静音：等媒体就绪后再套用一次，避免被播放器默认值（满音量）覆盖 */
  const savedVolume = readVolume();
  let volumeApplied = false;
  let lastVolume = { volume: -1, muted: false };

  function store(): Store | undefined {
    return (playerEl as unknown as { store?: Store } | null)?.store;
  }

  function readVolume(): { volume: number; muted: boolean } | null {
    try {
      const raw = window.localStorage.getItem(VOLUME_KEY);
      if (!raw) return null;
      const v: unknown = JSON.parse(raw);
      if (typeof v !== 'object' || v === null) return null;
      const { volume, muted } = v as { volume?: unknown; muted?: unknown };
      if (typeof volume !== 'number' || !Number.isFinite(volume) || typeof muted !== 'boolean') return null;
      return { volume, muted };
    } catch {
      return null;
    }
  }

  function writeVolume(volume: number, muted: boolean): void {
    try {
      window.localStorage.setItem(VOLUME_KEY, JSON.stringify({ volume, muted }));
    } catch {
      /* 隐私模式忽略 */
    }
  }

  function applySource(el: HTMLElement, next: { src: string; type: string }): void {
    if (el instanceof HTMLVideoElement) {
      el.src = next.src;
      return;
    }
    // capRenditionToPlayerSize 关掉：契约要求「最高画质不设上限」，播放器别偷偷按窗口大小降档
    (el as EngineSourceHost).source = { src: next.src, type: next.type, capRenditionToPlayerSize: false };
  }

  /** 把当前 src/type 装进播放器；播放器还没挂上媒体（store.target 为空）时稍后重试 */
  function attach(): void {
    const s = store();
    const el = mediaEl;
    if (!s || !el) return;
    if (!s.target) {
      if (retryTimer === undefined && attempts < 60) {
        attempts += 1;
        retryTimer = setTimeout(() => {
          retryTimer = undefined;
          attach();
        }, 50);
      }
      return;
    }
    attempts = 0;

    if (src === appliedSrc) return;
    const switching = appliedSrc !== '';
    appliedSrc = src;
    // 只有「切清晰度/切代理」才续播；第一次挂载交给自动播放（被拦就停在海报上）
    if (switching) {
      const at = s.currentTime;
      pendingSeek = Number.isFinite(at) && at > 1 ? at : 0;
      pendingPlay = !s.paused;
    }
    applySource(el, { src, type });
    if (!switching) {
      wantPlay = true;
      wantPlayTries = 0;
      // 立刻试一次（mp4 会马上起播）；HLS 这时引擎还没接上媒体，失败后由 kickPlay 重试
      kickPlay();
    }
  }

  /** 调 play()，把「同步抛错 / rejected promise」都归一成回调；NotAllowedError 单独给你 */
  function tryPlay(s: Store, onOk: () => void, onFail: () => void, onBlocked: () => void): void {
    let result: unknown;
    try {
      result = s.play();
    } catch {
      onFail();
      return;
    }
    if (result === null || typeof result !== 'object' || typeof (result as Promise<void>).then !== 'function') {
      // 没有 promise 可等：当成已经开播，别在这里空转
      onOk();
      return;
    }
    void (result as Promise<void>).then(onOk, (err: unknown) => {
      // 浏览器拦自动播放：别跟用户较劲，停在海报上等他点播放
      if ((err as { name?: string } | null)?.name === 'NotAllowedError') onBlocked();
      else onFail();
    });
  }

  function scheduleKick(): void {
    if (!wantPlay) return;
    if (wantPlayTries >= 40) {
      wantPlay = false;
      return;
    }
    wantPlayTries += 1;
    if (kickTimer !== undefined) clearTimeout(kickTimer);
    kickTimer = window.setTimeout(() => {
      kickTimer = undefined;
      kickPlay();
    }, 120);
  }

  /**
   * 首帧起播。原生 mp4 一次就能成；HLS 引擎要先解析清单、挂上 MediaSource，
   * 在这之前 play() 会被 AbortError 打断，所以这里按 120ms 的间隔重试，直到真正开播、
   * 被浏览器拦自动播放，或者试够 40 次为止（约 5 秒）。
   */
  function kickPlay(): void {
    const s = store();
    if (!s || !wantPlay) return;
    if (!s.paused && s.currentTime > 0) {
      wantPlay = false;
      return;
    }
    if (!s.canPlay && !(s.duration > 0)) {
      scheduleKick();
      return;
    }
    tryPlay(
      s,
      () => {
        wantPlay = false;
      },
      scheduleKick,
      () => {
        wantPlay = false;
      },
    );
  }

  // src / type 一变（换清晰度、切代理、重新解析）就走 attach；改名换 movie 也走同一条路
  $effect(() => {
    void src;
    void type;
    attach();
  });

  onMount(() => {
    const s = store();
    if (!s) return;
    const unsubscribe = s.subscribe(() => {
      attach();

      if (s.error && s.error !== sawError) {
        sawError = s.error;
        const detail = s.error.message.trim();
        onFailed?.(detail === '' ? '播放失败，请重试。' : detail);
        // 交给页面的错误浮层（带「重新解析 / 用代理播放」），把内置弹窗收掉
        s.dismissError();
      }

      if (s.started && !startedSent) {
        startedSent = true;
        onStarted?.();
      }

      // 首次起播：媒体一就绪就催一次，剩下的交给 kickPlay 的定时重试
      if (wantPlay) {
        if (!s.paused && s.currentTime > 0) wantPlay = false;
        else if (s.canPlay || s.duration > 0) kickPlay();
      }

      if ((pendingSeek > 0 || pendingPlay) && (s.canPlay || s.duration > 0)) {
        const at = pendingSeek;
        const resume = pendingPlay;
        pendingSeek = 0;
        pendingPlay = false;
        if (at > 0) {
          void s.seek(at).catch(() => {
            /* 目标点还不可 seek 时忽略，继续播下去 */
          });
        }
        if (resume) {
          tryPlay(
            s,
            () => {},
            () => {},
            () => {},
          );
        }
      }

      // 音量记忆：媒体真的就绪后再套用，之后才允许把用户的选择写回 localStorage
      if (!volumeApplied && savedVolume !== null && (s.canPlay || s.duration > 0)) {
        volumeApplied = true;
        s.setVolume(savedVolume.volume);
        s.setMuted(savedVolume.muted);
        lastVolume = { volume: savedVolume.volume, muted: savedVolume.muted };
      }
      if (volumeApplied && (s.volume !== lastVolume.volume || s.muted !== lastVolume.muted)) {
        lastVolume = { volume: s.volume, muted: s.muted };
        writeVolume(s.volume, s.muted);
      }
    });
    window.addEventListener('keydown', onKeydown);
    return () => {
      unsubscribe();
      window.removeEventListener('keydown', onKeydown);
      if (retryTimer !== undefined) clearTimeout(retryTimer);
      if (kickTimer !== undefined) clearTimeout(kickTimer);
    };
  });

  /** 页面级快捷键：焦点不在播放器里时也能用（在播放器里由皮肤自己的 hotkey 接管，这里不重复处理） */
  function onKeydown(event: KeyboardEvent): void {
    const s = store();
    const host = playerEl;
    if (!s || !host) return;
    const target = event.target;
    if (target instanceof Node && host.contains(target)) return;
    if (target instanceof HTMLElement) {
      const tag = target.tagName;
      if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || target.isContentEditable) return;
    }
    if (event.metaKey || event.ctrlKey || event.altKey) return;

    const seek = (delta: number): void => {
      const max = s.duration > 0 ? s.duration : s.currentTime + Math.abs(delta);
      void s.seek(Math.min(max, Math.max(0, s.currentTime + delta))).catch(() => {});
    };

    switch (event.key) {
      case ' ':
      case 'k':
      case 'K':
        event.preventDefault();
        if (s.paused) {
          tryPlay(
            s,
            () => {},
            () => {},
            () => {},
          );
        } else s.pause();
        break;
      case 'ArrowLeft':
        event.preventDefault();
        seek(-10);
        break;
      case 'ArrowRight':
        event.preventDefault();
        seek(10);
        break;
      case 'ArrowUp':
        event.preventDefault();
        s.setVolume(Math.min(1, Math.round((s.volume + 0.1) * 10) / 10));
        break;
      case 'ArrowDown':
        event.preventDefault();
        s.setVolume(Math.max(0, Math.round((s.volume - 0.1) * 10) / 10));
        break;
      case 'm':
      case 'M':
        event.preventDefault();
        s.setMuted(!s.muted);
        break;
      case 'f':
      case 'F':
        event.preventDefault();
        if (document.fullscreenElement) void s.exitFullscreen().catch(() => {});
        else void s.requestFullscreen().catch(() => {});
        break;
      default:
        break;
    }
  }
</script>

<!-- 皮肤的 --media-* 是它的公开调色口：全部压到站点的单色冷调（纯白当强调色、Roboto、12px 圆角）。
     lang 从 <html lang="zh-CN"> 继承，控件文案走 @videojs/html 自带的 zh-CN 语言包。 -->
<media-i18n>
  <video-player bind:this={playerEl} poster={poster ?? undefined} aria-label={title}>
    <video-skin
      class="block aspect-video w-full bg-black"
      style="--media-accent-color:#f5faff;--media-accent-text-color:#020408;--media-border-radius:0;--media-border-color:transparent;--media-font-family:var(--font-sans)"
    >
      {#if engine === 'hls'}
        <hlsjs-video bind:this={mediaEl} playsinline preload="metadata" aria-label={title}></hlsjs-video>
      {:else}
        <video bind:this={mediaEl} playsinline preload="metadata" aria-label={title}></video>
      {/if}
    </video-skin>
  </video-player>
</media-i18n>
