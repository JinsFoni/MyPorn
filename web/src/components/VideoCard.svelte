<script lang="ts">
  import DownloadDialog from './DownloadDialog.svelte';
  import { api } from '../lib/api';
  import { imageUrl } from '../lib/img';
  import { privacy } from '../lib/privacy.svelte';
  import { formatDate, formatDuration, relativeTime } from '../lib/format';
  import { canHover, prefersReducedMotion } from '../lib/player';
  import { scrollingRecently } from '../lib/scroll';
  import { live } from '../lib/live.svelte';
  import type { Video } from '../lib/types';

  interface Props {
    video: Video;
    /** 首屏前几条用 eager，其余 lazy */
    eager?: boolean;
  }

  let { video, eager = false }: Props = $props();

  let previewOn = $state(false);
  let previewEl = $state<HTMLVideoElement | null>(null);
  let previewTimer = 0;
  let downloadOpen = $state(false);
  // 缩略图签名会过期、个别影片也可能压根没有封面：加载失败时退回占位图标，别留一块空白。
  let thumbFailed = $state(false);
  let avatarFailed = $state(false);
  // 预览片的签名会过期（抓取时写死的 URL），失效一次就别再反复拉：
  // 每次悬停都去请求一个必然失败的地址，纯属浪费带宽和解码。
  let previewBroken = $state(false);

  const thumb = $derived(imageUrl(video.thumbUrl, 480));
  const performerLine = $derived(video.performers.map((p) => p.name).join('、'));
  const firstPerformer = $derived(video.performers[0] ?? null);
  // 头像表里存的是后端代理地址，也要过一遍 imageUrl：隐私模式下它得换成小糊图
  // 优先用这一行自带的头像（videos 接口现在会跟着 performers 一起给，B 站墙离不开它：
  // 「订阅艺人头像表」只拉得到非 B 站的艺人），没有再退回全局头像表。
  const avatar = $derived(
    avatarFailed
      ? undefined
      : imageUrl(
          firstPerformer?.avatarUrl ?? (firstPerformer?.id != null ? live.performerAvatars[firstPerformer.id] : null),
          36,
        ),
  );
  const monogram = $derived(firstPerformer ? firstPerformer.name.trim().charAt(0).toUpperCase() : '');

  function openDownload(event: MouseEvent): void {
    event.preventDefault();
    event.stopPropagation();
    stopPreview();
    downloadOpen = true;
  }

  // 鼠标一碰卡片就先把播放地址解析好（服务端有缓存，重复请求很便宜），
  // 这样点进去基本不用等起播解析。每部片只预取一次。
  const prefetched = new Set<number>();
  function prefetchPlayback(): void {
    if (prefetched.has(video.id)) return;
    prefetched.add(video.id);
    void api.playback(video.id).catch(() => {});
  }

  // 滚动经过时不预取也不预览：等真正停下（再顺延一小会儿）之后才干活
  function startPreview(): void {
    if (!video.previewUrl || previewBroken || !canHover() || prefersReducedMotion()) return;
    schedulePreview(450);
  }

  function schedulePreview(delay: number, attempt = 0): void {
    window.clearTimeout(previewTimer);
    previewTimer = window.setTimeout(() => {
      // 隐私模式：封面是糊的就别再悬停放片——鼠标一扫整段画面就出来了，
      // 比不糊封面还露骨
      if (privacy.on) return;
      // 指针扫过或列表还在滚动：顺延，别和滚动抢主线程和解码器
      if (scrollingRecently()) {
        if (attempt < 6) schedulePreview(300, attempt + 1);
        return;
      }
      prefetchPlayback();
      previewOn = true;
    }, delay);
  }

  function stopPreview(): void {
    window.clearTimeout(previewTimer);
    previewOn = false;
  }

  // 正预览着的时候把隐私模式打开：立刻收掉，不用等指针离开
  $effect(() => {
    if (privacy.on) stopPreview();
  });

  // 预览元素挂载后再 play；失败静默降级回缩略图
  $effect(() => {
    const el = previewEl;
    if (!el) return;
    const promise = el.play();
    if (promise) promise.catch(() => (previewOn = false));
  });
</script>

<article class="group relative min-w-0 [contain-intrinsic-size:auto_16rem] [content-visibility:auto]">
  <a
    href={`/video/${String(video.id)}`}
    data-link
    class="block"
    aria-label={`${video.title}，时长 ${formatDuration(video.durationSec)}`}
    onpointerenter={startPreview}
    onpointerdown={prefetchPlayback}
    onpointerleave={stopPreview}
    onfocusin={prefetchPlayback}
  >
    <div class="relative aspect-video overflow-hidden rounded-xl bg-panel">
      {#if thumb && !thumbFailed}
        <img
          data-media
          src={thumb}
          onerror={() => (thumbFailed = true)}
          alt=""
          width="480"
          height="270"
          loading={eager ? 'eager' : 'lazy'}
          decoding="async"
          class="h-full w-full object-cover"
        />
      {:else}
        <div class="flex h-full w-full items-center justify-center">
          <svg
            viewBox="0 0 24 24"
            class="h-6 w-6 text-dim"
            fill="none"
            stroke="currentColor"
            stroke-width="1.4"
            aria-hidden="true"
          >
            <rect x="3" y="5.5" width="18" height="13" rx="2.5" />
            <path d="M8 5.5v13M16 5.5v13" />
          </svg>
        </div>
      {/if}

      {#if previewOn && video.previewUrl}
        <video
          bind:this={previewEl}
          src={video.previewUrl}
          class="absolute inset-0 h-full w-full object-cover"
          muted
          loop
          playsinline
          preload="none"
          aria-hidden="true"
          onerror={() => {
            previewBroken = true;
            previewOn = false;
          }}
        ></video>
      {/if}

      <!-- 时长直接压在封面上（不要黑色药丸底），只给一层文字阴影保证亮封面上也能读；
           封面左上角只有时长这一项——新片白药丸已按需求去掉，已存也从来不做标识 -->
      <div class="pointer-events-none absolute left-0 top-0 flex items-center p-2">
        <span
          class="text-meta font-medium text-fg [text-shadow:0_1px_5px_rgba(0,0,0,1),0_0_12px_rgba(0,0,0,0.95),0_0_3px_rgba(0,0,0,1)]"
        >
          {formatDuration(video.durationSec)}
        </span>
      </div>
    </div>

    <div class="mt-2.5 flex items-start gap-2.5">
      <span class="h-9 w-9 shrink-0 overflow-hidden rounded-full bg-raise" aria-hidden="true">
        {#if avatar}
          <img
            data-media
            src={avatar}
            onerror={() => (avatarFailed = true)}
            alt=""
            width="36"
            height="36"
            loading="lazy"
            decoding="async"
            class="h-full w-full object-cover"
          />
        {:else if monogram}
          <span class="flex h-full w-full items-center justify-center text-meta font-medium text-muted">
            {monogram}
          </span>
        {/if}
      </span>

      <div class="min-w-0 flex-1">
        <h3 class="truncate text-ui text-fg/95 group-hover:text-fg">{video.title}</h3>
        <p class="mt-0.5 flex items-center gap-1.5 text-meta text-dim">
          <span class="truncate">{performerLine || '未知艺人'}</span>
          <span aria-hidden="true">·</span>
          <span class="shrink-0" title={relativeTime(video.publishedAt)}>{formatDate(video.publishedAt)}</span>
        </p>
      </div>
    </div>
  </a>

  <button
    type="button"
    class="absolute right-2 top-2 z-10 flex h-7 w-7 items-center justify-center rounded-full bg-ink/80 text-fg opacity-0 transition hover:bg-ink focus-visible:opacity-100 group-hover:opacity-100 group-focus-within:opacity-100 max-lg:opacity-100"
    aria-label={`下载 ${video.title}`}
    title="下载到本地库"
    onclick={openDownload}
  >
    <svg
      viewBox="0 0 20 20"
      class="h-3.5 w-3.5"
      fill="none"
      stroke="currentColor"
      stroke-width="1.7"
      stroke-linecap="round"
      aria-hidden="true"
    >
      <path d="M10 3.5v8" />
      <path d="m6.75 8.5 3.25 3.25L13.25 8.5" />
      <path d="M4 15.5h12" />
    </svg>
  </button>

  <DownloadDialog videoId={video.id} open={downloadOpen} onClose={() => (downloadOpen = false)} />
</article>
