<script lang="ts">
  /**
   * Video.js 10 播放器外壳。
   *
   * 这里是全站唯一的播放器实现：`<video-player>`（状态）＋ `<video-skin>`（整套控件：大播放键、
   * 进度条与缓冲、时间、音量、倍速、字幕、设置菜单、画中画、全屏、错误弹窗、手势与快捷键）。
   * 皮肤用 `--media-*` 自定义属性调成站点的单色冷调，不引入第二套视觉语言。
   *
   * 皮肤之外，这里还补了三样皮肤给不了的东西：
   *   1. **清晰度菜单**与**氛围模式**开关：两个按钮挂进皮肤底部控制条右侧那一组里（见 lib/playerBar.ts），
   *      和自带的音量/设置/画中画/全屏排在同一行、一起显隐；
   *   2. **氛围模式**本身：把当前画面缩到 32×18 的画布上再放大模糊，给播放器四周蒙一层同色光；
   *   3. **音量记忆**（`localStorage`，等媒体就绪才套用，否则会被播放器默认的满音量盖掉）；
   *   4. **加载态**（见 VideoLoading.svelte）：糊掉的封面垫底 + 中间转环，出画面就淡出。
   *
   * 片源策略（见 lib/player.ts）：每个清晰度是**一条独立的清单/直链**，由后端解析后交给这里，
   * 所以播放器一次只放一条，切档＝换 source 并保留进度；不用 hls.js 的自适应 ABR。
   * HLS 走 `<hlsjs-video>`（hls.js 引擎，AV1/HEVC 都能过），mp4 走原生 `<video>`（不下载 hls.js）。
   */
  import { onMount, type Snippet } from 'svelte';
  import '@videojs/html/video/player';
  import '@videojs/html/video/skin';
  import '@videojs/html/i18n';
  import { prefersReducedMotion, type QualityOption } from '../lib/player';
  import { mountPlayerBar, type PlayerBar, type PlayerBarState } from '../lib/playerBar';
  import VideoLoading from './VideoLoading.svelte';
  import { fade } from 'svelte/transition';

  /** hlsjs-video 的 `source` 属性（原生 <video> 没有；按需断言） */
  interface EngineSourceHost extends HTMLElement {
    source: { src: string; type: string; capRenditionToPlayerSize?: boolean } | null;
  }

  interface Props {
    /** 同源代理地址（/api/stream?...），变即换片源 */
    src: string;
    /** Video.js 的 source.type：HLS 用 application/vnd.apple.mpegurl，直链用 video/mp4 */
    type: string;
    /** 'hls' 用 `<hlsjs-video>`（也能放 mp4），'native' 用原生 `<video>` */
    engine: 'hls' | 'native';
    poster?: string | null;
    title: string;
    /** 清晰度列表（降序）；只有一条或为空时不显示菜单按钮 */
    qualities?: QualityOption[];
    selectedQuality?: string | null;
    onSelectQuality?: (id: string) => void;
    /** 首次真正开始播放（用来上报「已看」） */
    onStarted?: () => void;
    /** 播放失败（含 hls.js 的不可恢复错误），交给页面自己的错误浮层处理 */
    onFailed?: (message: string) => void;
    /** 盖在画面上方的页面级浮层（解析中 / 播放失败） */
    overlay?: Snippet;
  }

  let {
    src,
    type,
    engine,
    poster = null,
    title,
    qualities = [],
    selectedQuality = null,
    onSelectQuality,
    onStarted,
    onFailed,
    overlay,
  }: Props = $props();

  let playerEl = $state<HTMLElement | null>(null);
  let mediaEl = $state<HTMLElement | null>(null);
  let canvasEl = $state<HTMLCanvasElement | null>(null);
  let skinEl = $state<HTMLElement | null>(null);

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
  const AMBIENT_KEY = 'myporn.ambient';

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

  let ambient = $state(readAmbient());
  /** 媒体还没法出画面：盖一层「糊封面 + 加载环」，等 playing/canplay 就收掉 */
  let buffering = $state(true);
  /** 媒体已经能出画面了吗：能的话加载态就只糊当前这一帧，不再拿封面顶上 */
  let hasFrame = $state(false);
  /** 底部控制条里的自定义按钮（清晰度 / 氛围模式），皮肤 shadow root 渲染好之后才挂得上 */
  let bar: PlayerBar | null = null;
  let barTries = 0;
  let barTimer: number | undefined;

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

  function readAmbient(): boolean {
    try {
      const raw = window.localStorage.getItem(AMBIENT_KEY);
      if (raw === '1') return true;
      if (raw === '0') return false;
    } catch {
      /* 隐私模式忽略 */
    }
    // 默认开；系统设了「减少动效」就不折腾
    return !prefersReducedMotion();
  }

  function toggleAmbient(): void {
    ambient = !ambient;
    try {
      window.localStorage.setItem(AMBIENT_KEY, ambient ? '1' : '0');
    } catch {
      /* 隐私模式忽略 */
    }
  }

  function chooseQuality(id: string): void {
    if (id !== selectedQuality) onSelectQuality?.(id);
  }

  /**
   * 把清晰度/氛围模式两个按钮挂进皮肤的控制条（细节见 lib/playerBar.ts）：
   * 皮肤的 shadow root 有时比组件的 onMount 晚一步，所以按 50ms 重试，最多 40 次。
   */
  function mountBar(): void {
    if (bar || !skinEl) return;
    const handle = mountPlayerBar(skinEl, { selectQuality: chooseQuality, toggleAmbient });
    if (!handle) {
      if (barTries < 40) {
        barTries += 1;
        barTimer = window.setTimeout(() => {
          barTimer = undefined;
          mountBar();
        }, 50);
      }
      return;
    }
    bar = handle;
    bar.update({ qualities, selected: selectedQuality, ambient });
  }

  function applySource(el: HTMLElement, next: { src: string; type: string }): void {
    if (el instanceof HTMLVideoElement) {
      el.src = next.src;
      return;
    }
    // capRenditionToPlayerSize 关掉：契约要求「最高画质不设上限」，播放器别偷偷按窗口大小降档
    (el as EngineSourceHost).source = { src: next.src, type: next.type, capRenditionToPlayerSize: false };
  }

  /**
   * 皮肤默认在控件出现时盖一层**全画幅**变暗（`--media-controls-gradient`：上 50%、中 20%、下 40% 黑），
   * 于是鼠标一动、控制条一露，画面整块就暗下去。这里只留底部一条浅浅的暗角给控制条垫底，
   * 中间和上面保持原亮度。`--media-*` 是皮肤的公开调色口，改它比覆盖它的内部类名稳。
   */
  function tuneSkin(): void {
    const container = skinEl?.shadowRoot?.querySelector('.media-container');
    if (!(container instanceof HTMLElement)) return;
    container.style.setProperty(
      '--media-controls-gradient',
      'linear-gradient(to top, rgb(0 0 0 / 0.3), rgb(0 0 0 / 0) 26%)',
    );
  }

  // ---------- 加载态：糊封面 + 加载环 ----------

  /** 已经挂上监听的那个 <video>（原生就是媒体元素本身，HLS 是 hlsjs-video 里那个） */
  let boundVideo: HTMLVideoElement | null = null;
  /** 缓冲要持续一小会儿才盖上去：普通拖动进度条时数据就在本地，盖一下反而闪 */
  let busyTimer: number | undefined;

  function mediaReady(): void {
    if (busyTimer !== undefined) {
      clearTimeout(busyTimer);
      busyTimer = undefined;
    }
    hasFrame = true;
    buffering = false;
  }

  /** 兜底：时间在走就说明数据是通的，别让加载态一直挂着（个别引擎只发 waiting 不发 playing） */
  function mediaTicking(): void {
    if (buffering && boundVideo !== null && boundVideo.readyState >= 3) mediaReady();
  }

  function mediaBusy(): void {
    if (busyTimer !== undefined) return;
    busyTimer = window.setTimeout(() => {
      busyTimer = undefined;
      buffering = true;
    }, 240);
  }

  /** 换片源会把画面清掉：退回「糊封面」，别让中间那截黑屏露出来 */
  function mediaCleared(): void {
    hasFrame = false;
    mediaBusy();
  }

  /** 媒体元素一出现就挂上（HLS 的内层 <video> 要等 shadow root 有；attach 会反复进来，总会挂上） */
  function bindMediaLoad(): void {
    const video = innerVideo();
    if (!video || video === boundVideo) return;
    unbindMediaLoad();
    boundVideo = video;
    // 能放 / 正在放 / 有画面了 → 收掉；等数据 / 被清空 → 盖回来
    video.addEventListener('canplay', mediaReady);
    video.addEventListener('playing', mediaReady);
    video.addEventListener('loadeddata', mediaReady);
    video.addEventListener('waiting', mediaBusy);
    video.addEventListener('timeupdate', mediaTicking);
    video.addEventListener('emptied', mediaCleared);
    video.addEventListener('loadstart', mediaCleared);
  }

  function unbindMediaLoad(): void {
    const video = boundVideo;
    boundVideo = null;
    if (busyTimer !== undefined) {
      clearTimeout(busyTimer);
      busyTimer = undefined;
    }
    if (!video) return;
    video.removeEventListener('canplay', mediaReady);
    video.removeEventListener('playing', mediaReady);
    video.removeEventListener('loadeddata', mediaReady);
    video.removeEventListener('waiting', mediaBusy);
    video.removeEventListener('timeupdate', mediaTicking);
    video.removeEventListener('emptied', mediaCleared);
    video.removeEventListener('loadstart', mediaCleared);
  }

  /** 把当前 src/type 装进播放器；播放器还没挂上媒体（store.target 为空）时稍后重试 */
  function attach(): void {
    tuneSkin();
    const s = store();
    const el = mediaEl;
    if (!s || !el) return;
    bindMediaLoad();
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
      // 换片源（切清晰度 / 切代理）必然要重新缓冲，立刻盖上，别让中间那截黑屏露出来
      buffering = true;
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
    // 第一次尝试常因引擎还没挂上媒体而失败：继续重试；只有被浏览器拦自动播放才放弃
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

  // ---------- 氛围模式 ----------

  /** 真正在播的 `<video>`：原生是它自己，hls.js 引擎在 `<hlsjs-video>` 的 shadow root 里 */
  function innerVideo(): HTMLVideoElement | null {
    const el = mediaEl;
    if (!el) return null;
    if (el instanceof HTMLVideoElement) return el;
    return el.shadowRoot?.querySelector('video') ?? null;
  }

  let stopAmbient: (() => void) | null = null;
  /** 采样分辨率：越小越省，反正最后要放大再模糊 */
  const AMBIENT_W = 32;
  const AMBIENT_H = 18;

  function stopAmbientLoop(): void {
    stopAmbient?.();
    stopAmbient = null;
  }

  function startAmbient(): void {
    stopAmbientLoop();
    const canvas = canvasEl;
    const video = innerVideo();
    if (!canvas || !video) return;
    const ctx = canvas.getContext('2d');
    if (!ctx) return;
    canvas.width = AMBIENT_W;
    canvas.height = AMBIENT_H;

    let stopped = false;
    let handle = 0;
    let raf = 0;
    let last = 0;

    const draw = (): void => {
      if (stopped || video.readyState < 2) return;
      try {
        ctx.drawImage(video, 0, 0, AMBIENT_W, AMBIENT_H);
      } catch {
        // 跨源污染 / 尺寸未就绪：跳过这一帧
      }
    };

    // 暂停状态下的跳转也要更新一次光晕
    video.addEventListener('seeked', draw);
    video.addEventListener('loadeddata', draw);
    draw();

    type FrameVideo = HTMLVideoElement & {
      requestVideoFrameCallback?: (cb: () => void) => number;
      cancelVideoFrameCallback?: (id: number) => void;
    };
    const frameVideo = video as FrameVideo;

    if (
      typeof frameVideo.requestVideoFrameCallback === 'function' &&
      typeof frameVideo.cancelVideoFrameCallback === 'function'
    ) {
      // 有新帧才画：暂停 / 后台标签页时自动停，不用自己判断
      const loop = (): void => {
        if (stopped) return;
        draw();
        handle = frameVideo.requestVideoFrameCallback?.(loop) ?? 0;
      };
      handle = frameVideo.requestVideoFrameCallback(loop) ?? 0;
    } else {
      // 没有 rVFC 的浏览器：rAF + 15fps 节流
      const loop = (now: number): void => {
        if (stopped) return;
        if (now - last > 66) {
          last = now;
          draw();
        }
        raf = requestAnimationFrame(loop);
      };
      raf = requestAnimationFrame(loop);
    }

    stopAmbient = () => {
      stopped = true;
      video.removeEventListener('seeked', draw);
      video.removeEventListener('loadeddata', draw);
      if (handle) frameVideo.cancelVideoFrameCallback?.(handle);
      if (raf) cancelAnimationFrame(raf);
    };
  }

  // 开关、片源切换、媒体元素变化，都要重新挂采样循环
  $effect(() => {
    const on = ambient;
    void src;
    void engine;
    void canvasEl;
    void mediaEl;
    if (on && canvasEl) startAmbient();
    else stopAmbientLoop();
    return stopAmbientLoop;
  });

  // src / type 一变（换清晰度、切代理、重新解析）就走 attach；改名换 video 也走同一条路
  $effect(() => {
    void src;
    void type;
    attach();
  });

  // 清晰度列表 / 当前档 / 氛围开关变了，把状态推给控制条里的两个按钮
  $effect(() => {
    const state: PlayerBarState = { qualities, selected: selectedQuality, ambient };
    bar?.update(state);
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

      // 音量记忆：媒体真的就绪后再套用；此后用户怎么调就怎么记（没存过也要开始记）
      if (!volumeApplied && (s.canPlay || s.duration > 0)) {
        volumeApplied = true;
        if (savedVolume !== null) {
          s.setVolume(savedVolume.volume);
          s.setMuted(savedVolume.muted);
          lastVolume = { volume: savedVolume.volume, muted: savedVolume.muted };
        } else {
          lastVolume = { volume: s.volume, muted: s.muted };
        }
      }
      if (volumeApplied && (s.volume !== lastVolume.volume || s.muted !== lastVolume.muted)) {
        lastVolume = { volume: s.volume, muted: s.muted };
        writeVolume(s.volume, s.muted);
      }
    });
    window.addEventListener('keydown', onKeydown);
    mountBar();
    return () => {
      unsubscribe();
      window.removeEventListener('keydown', onKeydown);
      if (retryTimer !== undefined) clearTimeout(retryTimer);
      if (kickTimer !== undefined) clearTimeout(kickTimer);
      if (barTimer !== undefined) clearTimeout(barTimer);
      bar?.destroy();
      bar = null;
      unbindMediaLoad();
      stopAmbientLoop();
    };
  });

  /** 页面级快捷键：焦点不在播放器里时也能用（在播放器里由皮肤自己的 hotkey 接管，这里不重复处理） */
  function onKeydown(event: KeyboardEvent): void {
    const s = store();
    const host = playerEl;
    if (!s || !host) return;
    // 皮肤控件和我们的两个按钮都在 shadow root 里，`contains` 跨不过 shadow 边界，所以看合成路径
    if (event.composedPath().some((node) => node instanceof Node && host.contains(node))) return;
    const target = event.target;
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

<!-- 氛围模式的光晕在裁剪层外面，才能往播放器四周溢出 -->
<div class="relative">
  <canvas
    bind:this={canvasEl}
    class="ambient-glow"
    data-on={ambient ? 'true' : 'false'}
    aria-hidden="true"
  ></canvas>

  <div class="relative overflow-hidden rounded-xl bg-black">
    <!-- 皮肤的 --media-* 是它的公开调色口：全部压到站点的单色冷调（纯白当强调色、Roboto、12px 圆角）。
         lang 从 <html lang="zh-CN"> 继承，控件文案走 @videojs/html 自带的 zh-CN 语言包。 -->
    <media-i18n>
      <video-player bind:this={playerEl} poster={poster ?? undefined} aria-label={title}>
        <video-skin
          bind:this={skinEl}
          class="block aspect-video w-full bg-black"
          style="--media-accent-color:#f5faff;--media-accent-text-color:#020408;--media-border-radius:0;--media-border-color:transparent;--media-font-family:var(--font-sans)"
        >
          {#if engine === 'hls'}
            <hlsjs-video bind:this={mediaEl} playsinline preload="metadata" aria-label={title}></hlsjs-video>
          {:else}
            <video bind:this={mediaEl} playsinline preload="metadata" aria-label={title}></video>
          {/if}

          <!-- 加载态：糊掉的封面垫底 + 中间转环（皮肤的大播放键在它上面，照样能点） -->
          {#if buffering}
            <div class="video-veil-host" transition:fade={{ duration: 200 }}>
              <VideoLoading poster={hasFrame ? null : poster} />
            </div>
          {/if}

          <!-- 页面浮层：默认不吃指针事件；自己需要交互的地方自己加 pointer-events-auto -->
          {#if overlay}
            <div class="overlay-layer">{@render overlay()}</div>
          {/if}
        </video-skin>
      </video-player>
    </media-i18n>

  </div>
</div>

<style>
  /* ---------- 氛围模式：把 32×18 的画面拉满、放大、模糊，当播放器四周的光 ---------- */
  /* 手机上播放器已经是通栏，再往外扩就会撑出横向滚动条：靠 blur 自己往外晕就够了 */
  .ambient-glow {
    position: absolute;
    inset: 0;
    z-index: -10;
    width: 100%;
    height: 100%;
    border-radius: 1.5rem;
    pointer-events: none;
    opacity: 0;
    transition: opacity 0.5s ease;
    filter: blur(28px) saturate(1.35);
  }
  @media (min-width: 640px) {
    .ambient-glow {
      inset: -2rem;
      width: calc(100% + 4rem);
      height: calc(100% + 4rem);
      filter: blur(48px) saturate(1.35);
    }
  }
  .ambient-glow[data-on='true'] {
    opacity: 0.5;
  }
  @media (prefers-reduced-motion: reduce) {
    .ambient-glow {
      transition: none;
    }
  }

  /* 加载态在外面套一层只为了让淡出挂在有个盒子的元素上 */
  .video-veil-host {
    position: absolute;
    inset: 0;
    pointer-events: none;
  }

  .overlay-layer {
    position: absolute;
    inset: 0;
    z-index: 50;
    pointer-events: none;
  }

</style>
