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
   *   4. **加载态**（见 VideoLoading.svelte）：封面（还没出过帧）或刚才那一帧（缓冲中）垫底 + 中间转环，
   *      出画面就淡出。两种底图的糊/暗程度不同——起播要等好几秒，封面必须一眼认得出。
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
    source: {
      src: string;
      type: string;
      capRenditionToPlayerSize?: boolean;
      /** hls.js 的配置透传口：`{ hlsJs: {...} }`，见 HLS_BUFFER_CONFIG */
      engine?: { hlsJs: Record<string, unknown> } | undefined;
    } | null;
  }

  interface Props {
    /** 同源代理地址（/api/stream?...），变即换片源 */
    src: string;
    /** Video.js 的 source.type：HLS 用 application/vnd.apple.mpegurl，直链用 video/mp4 */
    type: string;
    /** 'hls' 用 `<hlsjs-video>`（也能放 mp4），'native' 用原生 `<video>` */
    engine: 'hls' | 'native';
    poster?: string | null;
    /** 封面取不到时退而求其次的那张（见 VideoLoading 的 posterFallback） */
    posterFallback?: string | null;
    title: string;
    /** 影片标识（变了就把抓帧缓存清掉，别拿上一部片子的画面垫底） */
    videoKey?: string | number;
    /** 清晰度列表（降序）；只有一条或为空时不显示菜单按钮 */
    qualities?: QualityOption[];
    selectedQuality?: string | null;
    onSelectQuality?: (id: string) => void;
    /** 首次真正开始播放（用来上报「已看」） */
    onStarted?: () => void;
    /** 播放失败（含 hls.js 的不可恢复错误），交给页面自己的错误浮层处理 */
    onFailed?: (message: string) => void;
    /**
     * 进度回调（约每秒一次，只在真的走了 1 秒以上才报）。
     *
     * 用途：页面要「先放低一档、等 4K 合好再切过去」时，切过去会换引擎（hls → native）、
     * 整个播放器重建，得靠这个回调把进度接住，再把 `startAt` 塞回来。
     */
    onTime?: (time: number, paused: boolean) => void;
    /** 首次挂载时要从哪一秒开始（<=1 当作从头）；换片源的续播由播放器自己处理 */
    startAt?: number;
    /** 盖在画面上方的页面级浮层（解析中 / 播放失败） */
    overlay?: Snippet;
  }

  let {
    src,
    type,
    engine,
    poster = null,
    posterFallback = null,
    title,
    videoKey = '',
    qualities = [],
    selectedQuality = null,
    onSelectQuality,
    onStarted,
    onFailed,
    onTime,
    startAt = 0,
    overlay,
  }: Props = $props();

  let playerEl = $state<HTMLElement | null>(null);
  let mediaEl = $state<HTMLElement | null>(null);
  let canvasEl = $state<HTMLCanvasElement | null>(null);
  /** 播放器黑盒**里面**的那层氛围光：竖屏视频两侧的黑边靠它填（见下面的「氛围模式」） */
  let innerCanvasEl = $state<HTMLCanvasElement | null>(null);
  /** 黑边在哪一轴、有多宽：由 measureBars 量出来，写进 CSS 自定义属性，mask 靠它裁 */
  let barAxis = $state<'x' | 'y' | 'none'>('none');
  /** 盒内那层画过一帧没有：没画过就还是张黑画布，露出来就是一道黑边 */
  let barsPainted = $state(false);
  let barsEl = $state<HTMLDivElement | null>(null);
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

  /**
   * 快捷键的步长：←→ 一次 5 秒（皮肤默认 10 秒太长——一按就跳掉整整一场戏的开头），
   * ↑↓ 一次 10% 音量。皮肤里那一串 `<media-hotkey>` 和下面窗口级的监听共用这两个数。
   */
  const SEEK_STEP = 5;
  const VOLUME_STEP = 0.1;

  /** 换片源后要恢复的进度/播放状态 */
  let pendingSeek = 0;
  /** 上一次报给 onTime 的秒数（用来节流成「一秒一条」） */
  let lastReportedTime = 0;
  let pendingPlay = false;
  /** 首帧起播还没成功：HLS 引擎要等清单解析完才能 play，所以先挂个待播标记 */
  let wantPlay = false;
  let wantPlayTries = 0;
  let kickTimer: number | undefined;
  let appliedSrc = '';
  let attempts = 0;
  let retryTimer: ReturnType<typeof setTimeout> | undefined;
  let sawError: unknown = null;

  /**
   * 播放管线掉线后的自愈次数。
   *
   * 实测（本机 Chrome、2160p VP9 60fps、`YFsMnJEbV1U`）：硬件解码器会偶发失败，
   * 元素报 `PIPELINE_ERROR_DECODE`（`VTDecompressionOutputCallback`，`-12909`），
   * 之后 hls.js 每次 append 都被拒、`MediaSource.readyState` 变成 `ended`，
   * 页面上看到的就是「站内播放失败（video SourceBuffer error…）」。
   * 这个错误**不会自己好**：元素的 `error` 一旦置上，`hls.recoverMediaError()`
   * 也清不掉（实测调用后 `currentTime` 仍然不动），只有把片源重新装一遍才能重建管线。
   * 所以这里先自己救：同一份片源原地重装、回到原进度接着播；连续救不回来才交给页面弹错。
   */
  const MAX_MEDIA_RECOVERIES = 3;
  let mediaRecoveries = 0;
  let recoverResumeAt = -1;
  /**
   * 自愈后要回到的位置。光设一次 `currentTime` 不保险：刚重装的管线还没缓冲到那里时，
   * 浏览器会把这次 seek 丢掉，于是「救回来」变成「从头再放一遍」（实测就是这样丢的进度）。
   * 所以盯着它，直到真的落在目标附近，或者试满 6 次放弃。
   */
  let seekWatch: { target: number; tries: number; last: number } | null = null;
  let startedSent = false;
  /** 上次的音量/静音：等媒体就绪后再套用一次，避免被播放器默认值（满音量）覆盖 */
  const savedVolume = readVolume();
  let volumeApplied = false;
  let lastVolume = { volume: -1, muted: false };

  let ambient = $state(readAmbient());
  /** 媒体还没法出画面：盖一层「糊封面 + 加载环」，等 playing/canplay 就收掉 */
  let buffering = $state(true);
  /** 加载态用的底图：最近一帧的缩略图（data URL）。没有就退回封面 */
  let veilArt = $state<string | null>(null);
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

  /**
   * hls.js 的缓冲参数。默认值（maxBufferLength 30s / maxBufferSize 60MB）在 4K 下是**自相矛盾**的：
   * 60MB 按 28.2 Mbps（3.5 MB/s）只够 17 秒，于是真正的上限是 17 秒而不是 30 秒。
   *
   * 实测（本机，2160p VP9 60fps，YouTube 分片 21~23 MB/段 5~7 秒）：
   * 链路能跑到 5~8 MB/s，但单段耗时抖动到 2.7~14.4 秒（14.4 秒那次 22.6 MB = 1.6 MB/s，
   * 已经低于实时码率）。17 秒的缓冲撑不住几次这样的抖动，表现就是一分钟里顿几回。
   * 这里按 60 秒给（4K 约 210 MB，本机播放可接受），并把回看缓冲压到 30 秒以免无限涨。
   */
  const HLS_BUFFER_CONFIG = {
    maxBufferLength: 60,
    maxBufferSize: 250 * 1000 * 1000,
    backBufferLength: 30,
  };

  function applySource(el: HTMLElement, next: { src: string; type: string }): void {
    if (el instanceof HTMLVideoElement) {
      el.src = next.src;
      return;
    }
    // capRenditionToPlayerSize 关掉：契约要求「最高画质不设上限」，播放器别偷偷按窗口大小降档
    (el as EngineSourceHost).source = {
      src: next.src,
      type: next.type,
      capRenditionToPlayerSize: false,
      engine: hlsEngineConfig(next.type),
    };
  }

  /** 只有 HLS 才带 hls.js 配置；mp4 直链走原生解码，给了也没人看。 */
  function hlsEngineConfig(type: string) {
    return type.includes('mpegurl') ? { hlsJs: HLS_BUFFER_CONFIG } : undefined;
  }

  /**
   * 重建播放管线：把同一份片源原样再装一遍，保留进度与播放意图。
   *
   * 先把 source 摘掉再装，是因为元素的 `error` 已经置上了——不摘掉的话播放器会认为
   * 「还是同一条片源」，不会重建 MediaSource（见 MAX_MEDIA_RECOVERIES 的注释）。
   * 进度走 pendingSeek 那条老路：等 `canPlay`/`duration` 就绪再 seek，
   * 立刻 seek 会停在「时间点有了、缓冲区还没到」的死等状态（实测就是这么卡住的）。
   */
  function recoverPipeline(at: number): void {
    const el = mediaEl;
    if (!el) return;
    pendingSeek = Number.isFinite(at) && at > 1 ? at : 0;
    pendingPlay = true;
    wantPlay = true;
    wantPlayTries = 0;
    recoverResumeAt = pendingSeek;
    seekWatch = pendingSeek > 0 ? { target: pendingSeek, tries: 0, last: 0 } : null;
    sawError = null;
    appliedSrc = '';
    beginBuffering();
    if (el instanceof HTMLVideoElement) {
      el.removeAttribute('src');
      el.load();
    } else {
      (el as EngineSourceHost).source = null;
    }
    // 给引擎一点时间把上一条 SourceBuffer / hls 实例拆干净；越往后等越久，
    // 因为「立刻重装」实测会在解码器还没复位时又撞一次同样的错
    const wait = recoveryDelay(mediaRecoveries);
    const reattach = () => {
      appliedSrc = '';
      attach();
    };
    if (wait > 0) window.setTimeout(reattach, wait);
    else requestAnimationFrame(reattach);
  }

  /** 第 n 次自愈前的等待：第 1 次立刻，之后 2.5 s、8 s（实测解码器复位需要时间） */
  function recoveryDelay(n: number): number {
    if (n <= 1) return 0;
    if (n === 2) return 2500;
    return 8000;
  }

  /**
   * 皮肤默认在控件出现时盖一层**全画幅**变暗（`--media-controls-gradient`：上 50%、中 20%、下 40% 黑），
   * 于是鼠标一动、控制条一露，画面整块就暗下去。这里只留底部一条浅浅的暗角给控制条垫底，
   * 中间和上面保持原亮度。`--media-*` 是皮肤的公开调色口，改它比覆盖它的内部类名稳。
   */
  function tuneSkin(): void {
    tuneHotkeys();
    patchSkin();
    const container = skinEl?.shadowRoot?.querySelector('.media-container');
    if (!(container instanceof HTMLElement)) return;
    container.style.setProperty(
      '--media-controls-gradient',
      'linear-gradient(to top, rgb(0 0 0 / 0.3), rgb(0 0 0 / 0) 26%)',
    );
  }

  /**
   * 皮肤自带的快捷键是「←→ 一次 10s、↑↓ 一次 5% 音量」。本站要的是 **←→ 5s、↑↓ 10%**。
   * 它落成 shadow root 里一串 `<media-hotkey keys= action= value=>`，`value` 就是这一步的量
   * （不写就走包里的默认值：`seekStep` 按方向 ±10、`volumeStep` ±0.05，见
   * `@videojs/core` 的 `getMediaInputActionValue`）。所以照着键位把 `value` 改掉就行，
   * 元素自己 `update()` 会重新注册监听，不用手动解绑。
   * `j` / `l` 跟左右箭头是同一个动作（快退 / 快进），一起改成 5s——同一件事不该有两个步长。
   */
  function tuneHotkeys(): void {
    const root = skinEl?.shadowRoot;
    if (!root) return;
    const steps: Record<string, number> = {
      arrowright: SEEK_STEP,
      l: SEEK_STEP,
      arrowleft: -SEEK_STEP,
      j: -SEEK_STEP,
      arrowup: VOLUME_STEP,
      arrowdown: -VOLUME_STEP,
    };
    for (const el of root.querySelectorAll<HTMLElement & { value?: number }>('media-hotkey')) {
      const step = steps[(el.getAttribute('keys') ?? '').toLowerCase()];
      if (step === undefined || el.value === step) continue;
      el.value = step;
    }
  }

  /**
   * 皮肤的两处内伤，一段 `<style>` 一起打补丁（这两处都没有 `--media-*` 出口，只能覆盖它的
   * 类名；`.media-slider-buffer` / `.media-buffering-indicator` 是它稳定的公开结构）。
   *
   * ① 皮肤自带的「缓冲中」指示器：半透明底 + 它自己的转环，压在 `z-index: 30`。加载态已经
   *    交给 `.video-veil` 统一接管，两个环会叠在一起、粗细和转速还不一样，所以把皮肤那个
   *    按掉。两者是同一批事件喂出来的（waiting / seeking），按掉不会丢状态。
   * ② **进度条上根本看不出缓冲到哪**：默认皮肤把轨槽 `.media-slider-track:before` 和已缓冲
   *    `.media-slider-buffer:before` 写成同一个 `color-mix(in oklab, currentcolor 20%,
   *    transparent)`，两层又完全重合、圆角和高度都一样，于是「已缓冲」这层跟空槽同色，等于
   *    不存在。实测一条 684 秒的片子稳定缓冲在 25–32 秒，只有总长的 4.4%（466px 的轨道上
   *    约 20px），这点宽度再跟底色一个颜色，就是用户说的「当前播放位置后面基本没显示缓冲」。
   *    把已缓冲提到 62%：空槽 20% → 已缓冲 62% → 已播 100%，同一根白色轴上三档分得开，
   *    也不破坏单色调。45% 实测还是太轻——轨道直接压在画面上，亮帧时 45% 与 20% 都糊成一片
   *    浅色，62% 才在亮帧/暗帧两边都读得出来。
   */
  function patchSkin(): void {
    const root = skinEl?.shadowRoot;
    if (!root || root.querySelector('style[data-myporn-skin]')) return;
    const style = document.createElement('style');
    style.dataset.mypornSkin = '';
    style.textContent = [
      '.media-buffering-indicator{display:none !important}',
      '.media-slider-buffer:before{background-color:color-mix(in oklab,currentcolor 62%,transparent) !important}',
    ].join('\n');
    root.append(style);
  }

  // ---------- 加载态：糊封面 + 加载环 ----------

  /** 已经挂上监听的那个 <video>（原生就是媒体元素本身，HLS 是 hlsjs-video 里那个） */
  let boundVideo: HTMLVideoElement | null = null;
  /** 缓冲要持续一小会儿才盖上去：普通拖动进度条时数据就在本地，盖一下反而闪 */
  let busyTimer: number | undefined;

  /**
   * 自己抓最后一帧当加载态的底图。
   *
   * 本来想省事，用 `backdrop-filter` 让浏览器把下面那一帧糊掉——但视频是独立的合成层，
   * 能不能被 backdrop 采样到看浏览器/解码路径，实测同一串操作有时糊出上一帧、有时糊出黑的，
   * 拖动进度条正好撞上「黑」，就成了用户看到的黑屏。所以这里每 250ms 把画面缩到 640×360
   * 存一份，缓冲时拿它当底图：一定糊的是真画面，跟合成器怎么排布无关。
   *
   * 为什么是 640×360 而不是更小的 320×180：这张图要铺满一千多像素宽的播放器，320 宽等于放大 4 倍，
   * 糊下去也还是一眼看出「这是张被拉大的图」。640 宽只放大 2 倍，糊归糊，不会显出放大感。
   */
  let snapCanvas: HTMLCanvasElement | null = null;
  let snapAt = 0;
  const SNAP_W = 640;
  const SNAP_H = 360;

  function captureFrame(): void {
    const video = boundVideo;
    if (!video || video.readyState < 2 || video.videoWidth === 0) return;
    const now = performance.now();
    if (now - snapAt < 250) return;
    snapAt = now;
    if (!snapCanvas) {
      snapCanvas = document.createElement('canvas');
      snapCanvas.width = SNAP_W;
      snapCanvas.height = SNAP_H;
    }
    try {
      snapCanvas.getContext('2d')?.drawImage(video, 0, 0, SNAP_W, SNAP_H);
    } catch {
      // 画不进去就维持上一张
    }
  }

  /** 盖上加载层之前把快照定格成图片（只在每次开始缓冲时编码一次，很便宜） */
  function freezeArt(): void {
    if (!snapCanvas) return;
    try {
      veilArt = snapCanvas.toDataURL('image/jpeg', 0.55);
    } catch {
      veilArt = null;
    }
  }

  /** 换影片（同一路由实例换了 id）时清掉抓帧缓存：新片子的第一次缓冲不该看到上一部的画面 */
  $effect(() => {
    void videoKey;
    snapCanvas = null;
    snapAt = 0;
    veilArt = null;
    // 换了片子就是新的一条管线：自愈额度重新算
    mediaRecoveries = 0;
    recoverResumeAt = -1;
  });

  /**
   * 目标时间是不是已经在缓冲区里。
   *
   * 拖进度条拖回**已经缓冲过**的那一段时，数据就在本地，解出来就能立刻接着放——这时候还盖一层
   * 「正在缓冲」的糊图，等于把本来毫无停顿的一次跳转说成卡了。所以命中缓冲区就**不许**盖。
   * 留 0.3s 余量：正好落在缓冲区尾巴上时，最后几帧可能还没解出来，那种情况仍按缓冲处理。
   */
  function bufferedAt(video: HTMLVideoElement, time: number): boolean {
    const ranges = video.buffered;
    for (let i = 0; i < ranges.length; i += 1) {
      if (time >= ranges.start(i) && time <= ranges.end(i) - 0.3) return true;
    }
    return false;
  }

  function beginBuffering(): void {
    if (buffering) return;
    freezeArt();
    buffering = true;
  }

  function mediaReady(): void {
    if (busyTimer !== undefined) {
      clearTimeout(busyTimer);
      busyTimer = undefined;
    }
    buffering = false;
  }

  /** 兜底：时间在走就说明数据是通的，别让加载态一直挂着（个别引擎只发 waiting 不发 playing） */
  function mediaTicking(): void {
    captureFrame();
    // 自愈之后又顺畅播了 20 秒：说明这次救回来了，把次数还回去，
    // 免得一条长片子里偶发三次就把额度用光
    if (recoverResumeAt >= 0 && boundVideo !== null && boundVideo.currentTime > recoverResumeAt + 20) {
      recoverResumeAt = -1;
      mediaRecoveries = 0;
      seekWatch = null;
    }
    // 自愈后的回位：没落到位就再要一次（见 seekWatch 的注释）
    if (seekWatch !== null && boundVideo !== null && boundVideo.readyState >= 1) {
      if (Math.abs(boundVideo.currentTime - seekWatch.target) > 1.5) {
        // 每秒最多试一次，最多 10 秒：4K 一段要下两三秒，急着连点只会全被浏览器丢掉
        const nowMs = Date.now();
        if (seekWatch.tries < 10 && nowMs - seekWatch.last > 1000) {
          seekWatch.tries += 1;
          seekWatch.last = nowMs;
          // 先让引擎从目标点开始拉（hls.js 默认从 0 开始，光设 currentTime 它不认）
          const eng = (mediaEl as { engine?: { startLoad?: (at: number) => void } } | null)?.engine;
          eng?.startLoad?.(seekWatch.target);
          boundVideo.currentTime = seekWatch.target;
        } else if (seekWatch.tries >= 10) {
          seekWatch = null;
        }
      } else {
        seekWatch = null;
      }
    }
    if (buffering && boundVideo !== null && boundVideo.readyState >= 3) mediaReady();
  }

  /**
   * 要卡多久才盖上加载态。普通情况 240ms——数据就在本地的一次普通拖动，不该闪一下糊图。
   * 命中缓冲区时更久（1.2s）：这种跳转本来就该「直接接着放」，只有真出了意外（解码卡住）
   * 才轮到加载态兜底，别把一次正常的跳转说成「正在缓冲」。
   */
  const BUSY_DELAY = 240;
  const BUSY_DELAY_BUFFERED = 1200;

  function mediaBusy(): void {
    // 卡住的那一刻画面还在（进度条拖过去也是），先抓一眼再盖上
    captureFrame();
    if (busyTimer !== undefined) return;
    const video = boundVideo;
    const local = video !== null && bufferedAt(video, video.currentTime);
    busyTimer = window.setTimeout(() => {
      busyTimer = undefined;
      const now = boundVideo;
      // 这一刻再看一眼：这段时间里画面已经回来了（跳进缓冲区、或者只是抖了一下）就别盖
      if (now !== null && now.readyState >= 3) return;
      beginBuffering();
    }, local ? BUSY_DELAY_BUFFERED : BUSY_DELAY);
  }

  /** 换片源会把画面清掉：盖上糊底图，别让中间那截黑屏露出来 */
  function mediaCleared(): void {
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
    // 暂停状态下的跳转不会有 waiting：跳完抓一帧，下次缓冲才有正确底图
    video.addEventListener('seeked', captureFrame);
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
    video.removeEventListener('seeked', captureFrame);
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
      beginBuffering();
    }
    applySource(el, { src, type });
    if (!switching) {
      // 首帧挂载：页面可能要求从某个位置起播（见 startAt 的注释）
      if (Number.isFinite(startAt) && startAt > 1) pendingSeek = startAt;
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

  /**
   * 氛围光有**两层**，同一份 16×9 的「颜色场」（每帧按时间常数揉进新画面，见 startAmbient）：
   *   1. `.ambient-glow`：黑盒外面那层，往播放器四周溢出；
   *   2. `.ambient-bars`：盒子里**压在画面上面**、只漏在黑边里的那层，给竖屏/宽银幕片子用。
   *
   * 为什么第 2 层只能压在画面上、不能垫在画面下面：`<video>` 是 `object-fit: contain`
   * （实测 computed 值），竖屏片子（如 `G7nLsgxggrE` 2160×3840）在 1259×708 的盒子里
   * 只占中间 398×708，左右各 430px 是黑边。实测在这块黑边上做过的尝试——
   * 把 `video-skin` 的 `bg-black`、`<video>` 的 `background-color` 全改成 transparent，
   * 再在它下面垫一张有内容的 canvas（平均色 136/124/152，确认不是空画布）——
   * 那 430px 依然是纯黑：Chromium 把 `<video>` 的整个盒子（含 letterbox 区）当一层不透明的
   * 视频层画出来，垫在下面的东西一律照不进来。所以只能反过来：把光画在画面上方，
   * 再用 `mask` 把它裁成只有黑边那两条，画面正中间不糊。
   */

  /** 真正在播的 `<video>`：原生是它自己，hls.js 引擎在 `<hlsjs-video>` 的 shadow root 里 */
  function innerVideo(): HTMLVideoElement | null {
    const el = mediaEl;
    if (!el) return null;
    if (el instanceof HTMLVideoElement) return el;
    return el.shadowRoot?.querySelector('video') ?? null;
  }

  let stopAmbient: (() => void) | null = null;
  /**
   * 颜色场分辨率：16×9。原来 32×18 加上 44px 模糊，放大到 1259×708 之后每一格约 39px，
   * 还留着能认出来的光斑和条纹（截图里那几条竖直亮带就是），看着「乱」。
   * 16×9 一格约 79px，再叠上 90px 的模糊，画面里就只剩色块，跟 Infuse 那种环境光一个观感。
   */
  const AMBIENT_W = 16;
  const AMBIENT_H = 9;
  /**
   * 颜色跟着画面走的快慢（秒）。每帧用 `α = 1 - e^(-dt/τ)` 把新画面揉进颜色场：
   * τ 越小越跟手但会闪（切镜头时整块光跳一下），越大越黏、越像慢慢流动的颜色。
   * 实测（同一段 4 秒画面、每 120ms 采一次平均亮度、看相邻两次的差）：不揉是 7.7 / 最大 25.4，
   * τ=0.45 压到 3.2 / 8.6 还看得出在跳，τ=0.8 是 2.1 / 5.2，τ=1.2 是 1.5 / 3.6。
   * 取 0.8：跳变看不太出来，镜头切过去也还在 1 秒内跟上。
   */
  const AMBIENT_TAU = 0.8;

  function stopAmbientLoop(): void {
    stopAmbient?.();
    stopAmbient = null;
  }

  /**
   * 把「黑边有多宽」量出来交给 CSS：`object-fit: contain` 下画面按 `min(盒宽/视频宽, 盒高/视频高)`
   * 缩放，两条黑边的宽度就是剩下的那一半。竖屏片子走 x、宽银幕走 y，两个都不会有。
   * 量不到（还没 metadata / 不是 contain）就标成 none，这一层直接不显示，宁可不画也别糊住画面。
   */
  function measureBars(): void {
    const box = barsEl;
    const video = innerVideo();
    if (!box || !video) return;
    const rect = video.getBoundingClientRect();
    const { videoWidth: vw, videoHeight: vh } = video;
    let axis: 'x' | 'y' | 'none' = 'none';
    let bar = 0;
    if (
      getComputedStyle(video).objectFit === 'contain' &&
      vw > 0 &&
      vh > 0 &&
      rect.width > 0 &&
      rect.height > 0
    ) {
      const scale = Math.min(rect.width / vw, rect.height / vh);
      const barX = Math.round((rect.width - vw * scale) / 2);
      const barY = Math.round((rect.height - vh * scale) / 2);
      if (barX >= 2) {
        axis = 'x';
        bar = barX;
      } else if (barY >= 2) {
        axis = 'y';
        bar = barY;
      }
    }
    box.style.setProperty('--ambient-bar', `${String(bar)}px`);
    barAxis = axis;
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

    // 两层共用的「颜色场」：离屏 16×9，每帧把新画面按时间常数揉进去，
    // 再原样抄给外面那层和盒内那层。抄的是 16×9 → 16×9，几乎不花钱。
    const field = document.createElement('canvas');
    field.width = AMBIENT_W;
    field.height = AMBIENT_H;
    const fieldCtx = field.getContext('2d');
    if (!fieldCtx) return;
    let fieldAt = 0;

    const inner = innerCanvasEl;
    if (inner) {
      inner.width = AMBIENT_W;
      inner.height = AMBIENT_H;
    }
    const innerCtx = inner?.getContext('2d') ?? null;
    barsPainted = false;

    let stopped = false;
    let handle = 0;
    let raf = 0;
    let last = 0;

    /**
     * 末尾 0.3 秒不再采样。`drawImage` 拿到的并不总是画面：片子快播完、hls.js 开始收
     * SourceBuffer 之后，同一句 `drawImage` 返回的是**纯黑**（实测 `G7nLsgxggrE`：
     * 采样平均色 156/147/180 一路正常，播到 10.91/10.913 秒时掉成 0/0/0，光晕跟着灭）。
     * 与其检测黑帧（要把画布从 GPU 读回来，或者分不清「片子里本来就是黑场」），
     * 不如干脆停在最后一帧好画面上——光晕本来就是氛围，不需要跟着片尾变黑。
     */
    const nearEnd = (): boolean =>
      Number.isFinite(video.duration) && video.duration > 0 && video.currentTime >= video.duration - 0.3;

    const draw = (): void => {
      if (stopped || video.readyState < 2 || video.ended || nearEnd()) return;
      const now = performance.now();
      const dt = fieldAt > 0 ? Math.min(0.25, (now - fieldAt) / 1000) : 0;
      fieldAt = now;
      try {
        // 按 cover 取景，不整帧压扁：黑边里接的是画面**这一条边**的延续，
        // 压扁会把画面中间的人也拖进两侧。
        const vw = video.videoWidth;
        const vh = video.videoHeight;
        if (vw <= 0 || vh <= 0) return;
        const aspect = AMBIENT_W / AMBIENT_H;
        let sw = vw;
        let sh = vh;
        if (vw / vh > aspect) sw = vh * aspect;
        else sh = vw / aspect;
        // 第一帧直接铺满，之后按时间常数叠上去：切镜头时颜色半秒内流过去，逐帧噪声被抹平
        fieldCtx.globalAlpha = dt === 0 ? 1 : 1 - Math.exp(-dt / AMBIENT_TAU);
        fieldCtx.drawImage(video, (vw - sw) / 2, (vh - sh) / 2, sw, sh, 0, 0, AMBIENT_W, AMBIENT_H);
        fieldCtx.globalAlpha = 1;

        ctx.drawImage(field, 0, 0);
        if (innerCtx) {
          innerCtx.drawImage(field, 0, 0);
          if (!barsPainted) barsPainted = true;
        }
      } catch {
        // 跨源污染 / 尺寸未就绪：跳过这一帧
      }
    };

    // 黑边宽度会随窗口缩放、进全屏、换片源变化，都重新量一遍
    const remeasure = (): void => measureBars();
    measureBars();
    video.addEventListener('seeked', draw);
    video.addEventListener('loadeddata', draw);
    video.addEventListener('loadedmetadata', remeasure);
    video.addEventListener('resize', remeasure);
    // 观察真正有布局盒子的 `<video>`：`<hlsjs-video>` 是 `display: contents`，它永远不触发
    // ResizeObserver（进全屏时黑边宽度就停在旧值）。全屏切换那一层没有 resize，单独听一下。
    const ro = typeof ResizeObserver === 'function' ? new ResizeObserver(remeasure) : null;
    ro?.observe(video);
    document.addEventListener('fullscreenchange', remeasure);
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
      video.removeEventListener('loadedmetadata', remeasure);
      video.removeEventListener('resize', remeasure);
      document.removeEventListener('fullscreenchange', remeasure);
      ro?.disconnect();
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
    void innerCanvasEl;
    void barsEl;
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
        // 解码器/MediaSource 掉线先自愈（见 MAX_MEDIA_RECOVERIES），
        // 救不回来才把错误交给页面的浮层——别让人正看着就弹「播放失败」
        if (mediaRecoveries < MAX_MEDIA_RECOVERIES && mediaEl !== null) {
          mediaRecoveries += 1;
          recoverPipeline(s.currentTime);
          s.dismissError();
        } else {
          const detail = s.error.message.trim();
          onFailed?.(detail === '' ? '播放失败，请重试。' : detail);
          // 交给页面的错误浮层（带「重新解析 / 用代理播放」），把内置弹窗收掉
          s.dismissError();
        }
      }

      if (s.started && !startedSent) {
        startedSent = true;
        onStarted?.();
      }

      if (onTime && Math.abs(s.currentTime - lastReportedTime) >= 1) {
        lastReportedTime = s.currentTime;
        onTime(s.currentTime, s.paused);
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
    if (!s) return;
    // 皮肤自己有一套 hotkey，挂在 `.media-container` 上。判断「这次按键归不归它管」要看**合成路径里有没有
    // 那个 shadow root**：焦点在皮肤内部的按钮上时路径会穿过 shadow root（它管，我们不重复处理）；
    // 而焦点落在 `<video-skin>` 这个宿主元素本身时（点过播放器又按 Tab / 点空白处），事件根本不进 shadow DOM，
    // 皮肤那套监听不会触发——用 `host.contains()` 判断会把这情形一起漏掉，结果左右键完全没反应。
    const root = skinEl?.shadowRoot;
    if (root && event.composedPath().includes(root)) return;
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

    /** 音量按 10% 一档走：浮点累加会漂（0.30000000000000004），所以先取整再夹回 0~1 */
    const nudgeVolume = (delta: number): void => {
      const next = Math.round((s.volume + delta) * 100) / 100;
      s.setVolume(Math.min(1, Math.max(0, next)));
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
        seek(-SEEK_STEP);
        break;
      case 'ArrowRight':
        event.preventDefault();
        seek(SEEK_STEP);
        break;
      case 'ArrowUp':
        event.preventDefault();
        nudgeVolume(VOLUME_STEP);
        break;
      case 'ArrowDown':
        event.preventDefault();
        nudgeVolume(-VOLUME_STEP);
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
      <video-player class="player-stack" bind:this={playerEl} poster={poster ?? undefined} aria-label={title}>
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

          <!-- 盒内那层氛围光：压在画面上面、被 mask 裁成只留 `object-fit: contain` 的黑边
               （为什么不能垫在画面下面，见上面 measureBars 的注释）。出画面之前不显示，
               否则一张还没采样的黑画布会把加载态盖黑。
               模糊必须挂在里层 canvas 上、mask 挂在外层 div 上：两者落在同一个元素上时
               Chromium 会先裁后糊，画面那侧会糊进一圈黑边（实测 40px 的模糊就是 40px 的黑缝）。 -->
          <div
            bind:this={barsEl}
            class="ambient-bars"
            data-on={ambient && barsPainted ? 'true' : 'false'}
            data-bars={barAxis}
            aria-hidden="true"
          >
            <canvas bind:this={innerCanvasEl} class="ambient-bars-canvas"></canvas>
          </div>

          <!-- 加载态：糊掉的封面 / 刚才那一帧垫底 + 中间转环（皮肤的大播放键在它上面，照样能点） -->
          {#if buffering}
            <!-- 只淡出、不淡入：淡入会让底下的黑盒先露一帧（起播时那一下黑闪就是它） -->
            <div class="video-veil-host" out:fade={{ duration: 200 }}>
              <VideoLoading
                poster={veilArt ?? poster}
                {posterFallback}
                kind={veilArt ? 'frame' : 'poster'}
              />
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
    filter: blur(40px) saturate(1.3);
    animation: ambient-drift 46s ease-in-out infinite;
  }
  @media (min-width: 640px) {
    .ambient-glow {
      inset: -2rem;
      width: calc(100% + 4rem);
      height: calc(100% + 4rem);
      filter: blur(88px) saturate(1.6) brightness(1.05);
    }
  }
  .ambient-glow[data-on='true'] {
    opacity: 0.5;
  }

  /* 慢慢漂：颜色场本身已经很糊了，再让它极慢地缩放平移，就看不出「一帧一帧」，
     像一块一直在流动的光（Infuse 那种环境光感）。40 多秒一个来回，正常看片几乎察觉不到边界。 */
  @keyframes ambient-drift {
    0% {
      transform: scale(1.18) translate3d(-1.2%, 1%, 0);
    }
    50% {
      transform: scale(1.26) translate3d(1.2%, -1.4%, 0);
    }
    100% {
      transform: scale(1.18) translate3d(-1.2%, 1%, 0);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .ambient-glow,
    .ambient-bars-canvas {
      transition: none;
      animation: none;
    }
  }

  /* ---------- 氛围模式（盒内那层）：填 `object-fit: contain` 留下的黑边 ---------- */
  /* 压在画面上面（z-index:2），比皮肤的控件层（shadow root 里那层 z-index:30）低、
     比加载态（.video-veil-host，z-index:3）低，所以既不挡控件也不糊加载态。
     放大 1.2 倍 + 模糊，是为了让边缘落在裁剪框外面，黑边里不会露出一圈发暗的晕边。 */
  .ambient-bars {
    position: absolute;
    inset: 0;
    z-index: 2;
    pointer-events: none;
    opacity: 0;
    transition: opacity 0.5s ease;
  }
  .ambient-bars-canvas {
    display: block;
    width: 100%;
    height: 100%;
    filter: blur(72px) saturate(1.75) brightness(1.05);
    animation: ambient-drift 46s ease-in-out infinite;
  }
  .ambient-bars[data-on='true'][data-bars='x'] {
    opacity: 0.62;
    -webkit-mask-image: linear-gradient(
      to right,
      #000 0 var(--ambient-bar, 0px),
      transparent var(--ambient-bar, 0px) calc(100% - var(--ambient-bar, 0px)),
      #000 calc(100% - var(--ambient-bar, 0px)) 100%
    );
    mask-image: linear-gradient(
      to right,
      #000 0 var(--ambient-bar, 0px),
      transparent var(--ambient-bar, 0px) calc(100% - var(--ambient-bar, 0px)),
      #000 calc(100% - var(--ambient-bar, 0px)) 100%
    );
  }
  .ambient-bars[data-on='true'][data-bars='y'] {
    opacity: 0.62;
    -webkit-mask-image: linear-gradient(
      to bottom,
      #000 0 var(--ambient-bar, 0px),
      transparent var(--ambient-bar, 0px) calc(100% - var(--ambient-bar, 0px)),
      #000 calc(100% - var(--ambient-bar, 0px)) 100%
    );
    mask-image: linear-gradient(
      to bottom,
      #000 0 var(--ambient-bar, 0px),
      transparent var(--ambient-bar, 0px) calc(100% - var(--ambient-bar, 0px)),
      #000 calc(100% - var(--ambient-bar, 0px)) 100%
    );
  }
  @media (prefers-reduced-motion: reduce) {
    .ambient-bars {
      transition: none;
    }
  }
  .player-stack {
    position: relative;
    z-index: 1;
  }

  /* 加载态在外面套一层只为了让淡出挂在有个盒子的元素上 */
  .video-veil-host {
    position: absolute;
    inset: 0;
    /* 比盒内氛围光（z-index:2）高：还没出画面的那几秒，光晕不该盖在加载态上 */
    z-index: 3;
    pointer-events: none;
  }

  .overlay-layer {
    position: absolute;
    inset: 0;
    z-index: 50;
    pointer-events: none;
  }

</style>
