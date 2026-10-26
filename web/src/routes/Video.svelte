<script lang="ts">
  import { untrack } from 'svelte';
  import DownloadDialog from '../components/DownloadDialog.svelte';
  import VideoCard from '../components/VideoCard.svelte';
  import EmptyState from '../components/EmptyState.svelte';
  import Skeleton from '../components/Skeleton.svelte';
  import { api, messageFor } from '../lib/api';
  import { formatBandwidth, formatCount, formatDate, formatDuration, relativeTime } from '../lib/format';
  import { imageUrl } from '../lib/img';
  import { live } from '../lib/live.svelte';
  import { platformLabel } from '../lib/platform';
  import {
    codecTag,
    levelIndexForHeight,
    loadHls,
    pickVariant,
    rememberHeight,
    rememberedHeight,
    sortVariantsDesc,
    supportsNativeHls,
    toggleFullscreen,
    variantLabel,
  } from '../lib/player';
  import { navigate } from '../lib/router.svelte';
  import type { PlaybackInfo, VideoDetail } from '../lib/types';

  interface Props {
    id: string;
  }

  let { id }: Props = $props();

  type HlsInstance = import('hls.js').default;

  /** 仅 DEV：后端未就绪时的示例数据（不请求任何外部地址） */
  const MOCK_VIDEO: VideoDetail = {
    id: 1,
    platform: 'pornhub',
    platformVideoId: 'mock-1',
    title: 'Slow morning, warm light',
    url: 'https://example.invalid/view/1',
    thumbUrl: null,
    previewUrl: null,
    durationSec: 742,
    publishedAt: new Date(Date.now() - 36e5).toISOString(),
    viewCount: 128400,
    rating: 88,
    tags: ['slow', 'morning'],
    performers: [{ id: 1, name: 'Mini Diva', url: null }],
    isNew: true,
    hasLocalFile: false,
    localLibraryIds: [],
    description: '示例数据：后端未连接，播放器不会有真实片源。',
    related: [],
  };

  const videoId = $derived(Number.parseInt(id, 10));

  let video = $state<VideoDetail | null>(null);
  let playback = $state<PlaybackInfo | null>(null);
  let loadingVideo = $state(true);
  let loadingPlayback = $state(true);
  let pageError = $state('');
  let playError = $state('');
  let proxyAvailable = $state(false);
  let usingProxy = $state(false);
  let selectedVariantId = $state<string | null>(null);
  let activeHeight = $state<number | null>(null);
  let levelHeights = $state<number[]>([]);
  let mockNote = $state('');
  let downloadOpen = $state(false);
  /** 标签默认只露这么多，剩下的折在「+N」里 */
  const TAG_PREVIEW = 8;
  let tagsExpanded = $state(false);

  let videoEl = $state<HTMLVideoElement | null>(null);
  let playerWrap = $state<HTMLDivElement | null>(null);
  let hls: HlsInstance | null = null;
  let seenSent = false;
  let suppressMediaError = false;

  const variants = $derived(playback ? sortVariantsDesc(playback.variants) : []);
  const selectedVariant = $derived(variants.find((v) => v.id === selectedVariantId) ?? null);
  const poster = $derived(video ? imageUrl(video.thumbUrl, 1280) : null);

  async function loadVideo(): Promise<void> {
    loadingVideo = true;
    pageError = '';
    try {
      const detail = await api.video(videoId);
      video = detail;
      mockNote = '';
    } catch (err) {
      if (import.meta.env.DEV) {
        video = MOCK_VIDEO;
        mockNote = messageFor(err);
      } else {
        pageError = messageFor(err);
      }
    } finally {
      loadingVideo = false;
    }
  }

  async function loadPlayback(): Promise<void> {
    loadingPlayback = true;
    playError = '';
    try {
      const info = await api.playback(videoId);
      playback = info;
      // 标签只在详情页里有：解析播放地址时一起带回来，别让用户看到空标签或刷新才出现。
      if (info.tags?.length && video && video.tags.length === 0) {
        video = { ...video, tags: info.tags };
      }
      const preferred = pickVariant(info, rememberedHeight());
      selectedVariantId = preferred?.id ?? info.defaultVariantId;
      activeHeight = preferred?.height ?? null;
      proxyAvailable = info.proxyPlaybackUrl !== null;
    } catch (err) {
      playError = messageFor(err);
    } finally {
      loadingPlayback = false;
    }
  }

  function destroyPlayer(): void {
    suppressMediaError = true;
    if (hls) {
      hls.destroy();
      hls = null;
    }
    levelHeights = [];
    const el = videoEl;
    if (el) {
      el.pause();
      el.removeAttribute('src');
      el.load();
    }
    window.setTimeout(() => {
      suppressMediaError = false;
    }, 0);
  }

  function handleFatal(detail?: string): void {
    destroyPlayer();
    if (playback?.proxyPlaybackUrl && !usingProxy) {
      proxyAvailable = true;
      playError = '直链播放失败，通常是 Referer/UA 或跨域限制。可以试试代理播放。';
      return;
    }
    playError = detail ? `播放失败（${detail}）` : '播放失败，请重试。';
  }

  function onMediaError(): void {
    if (suppressMediaError) return;
    handleFatal();
  }

  async function attachSource(): Promise<void> {
    const el = videoEl;
    const info = playback;
    const variantId = untrack(() => selectedVariantId);
    if (!el || !info || variantId === null) return;
    const variant = info.variants.find((v) => v.id === variantId) ?? pickVariant(info, null);
    if (!variant) {
      playError = '没有可用的播放地址';
      return;
    }
    destroyPlayer();
    playError = '';
    activeHeight = variant.height;
    // 播放地址已由后端同源代理（/api/stream），这里只需区分清单与直链：
    // 直链 mp4 交给原生 video，交给 hls.js 反而会报“不是清单”。
    const src = usingProxy && info.proxyPlaybackUrl ? info.proxyPlaybackUrl : variant.url;
    const isPlaylist = usingProxy ? true : variant.hls;

    if (!isPlaylist) {
      el.src = src;
      void el.play().catch(() => {
        /* 自动播放被拦截，用户点一下即可 */
      });
      return;
    }

    if (supportsNativeHls(el)) {
      el.src = src;
      void el.play().catch(() => {
        /* 自动播放被拦截，用户点一下即可 */
      });
      return;
    }

    const Hls = await loadHls();
    if (!Hls.isSupported()) {
      el.src = src;
      return;
    }

    const instance = new Hls({ enableWorker: true, maxBufferLength: 30, backBufferLength: 30 });
    hls = instance;
    instance.on(Hls.Events.MANIFEST_PARSED, () => {
      levelHeights = instance.levels.map((level) => level.height);
      const index = levelIndexForHeight(instance.levels, variant.height);
      if (index >= 0) instance.currentLevel = index;
      void el.play().catch(() => {
        /* 同上 */
      });
    });
    instance.on(Hls.Events.LEVEL_SWITCHED, (_event, data) => {
      const level = instance.levels[data.level];
      if (level) activeHeight = level.height;
    });
    instance.on(Hls.Events.ERROR, (_event, data) => {
      if (data.fatal) handleFatal(typeof data.details === 'string' ? data.details : undefined);
    });
    instance.loadSource(src);
    instance.attachMedia(el);
  }

  function onQualityChange(event: Event): void {
    const target = event.currentTarget;
    if (!(target instanceof HTMLSelectElement)) return;
    applyVariant(target.value);
  }

  function applyVariant(variantId: string): void {
    const info = playback;
    if (!info) return;
    const variant = info.variants.find((v) => v.id === variantId);
    if (!variant) return;
    selectedVariantId = variantId;
    rememberHeight(variant.height);
    const instance = hls;
    if (instance && !usingProxy) {
      const index = levelIndexForHeight(instance.levels, variant.height);
      if (index >= 0) {
        instance.currentLevel = index;
        activeHeight = variant.height;
        return;
      }
    }
    void attachSource();
  }

  function switchToProxy(): void {
    if (!playback?.proxyPlaybackUrl) return;
    usingProxy = true;
    proxyAvailable = false;
    live.toast('已切换到代理播放', 'info');
  }

  function onPlaying(): void {
    if (seenSent) return;
    seenSent = true;
    void api
      .markSeen(videoId)
      .then(() => {
        if (video) video.isNew = false;
      })
      .catch(() => {
        /* 上报失败不影响播放 */
      });
  }

  function onKeydown(event: KeyboardEvent): void {
    const target = event.target;
    if (target instanceof HTMLElement) {
      const tag = target.tagName;
      if (
        tag === 'INPUT' ||
        tag === 'TEXTAREA' ||
        tag === 'SELECT' ||
        tag === 'BUTTON' ||
        tag === 'A' ||
        target.isContentEditable
      ) {
        return;
      }
    }
    const el = videoEl;
    if (!el) return;
    if (event.key === ' ') {
      event.preventDefault();
      if (el.paused) void el.play().catch(() => {});
      else el.pause();
    } else if (event.key === 'ArrowLeft') {
      event.preventDefault();
      el.currentTime = Math.max(0, el.currentTime - 5);
    } else if (event.key === 'ArrowRight') {
      event.preventDefault();
      el.currentTime = Math.min(el.duration > 0 ? el.duration : el.currentTime + 5, el.currentTime + 5);
    } else if (event.key === 'f' || event.key === 'F') {
      const wrap = playerWrap;
      if (wrap) {
        event.preventDefault();
        void toggleFullscreen(wrap);
      }
    }
  }

  const specs = $derived.by((): { label: string; value: string }[] => {
    const v = video;
    if (!v) return [];
    const list = [
      { label: '时长', value: formatDuration(v.durationSec) },
      {
        label: '清晰度',
        value: selectedVariant
          ? `${variantLabel(selectedVariant)} · ${formatBandwidth(selectedVariant.bandwidth)}`
          : '—',
      },
      { label: '发布', value: formatDate(v.publishedAt) },
      { label: '播放量', value: formatCount(v.viewCount) },
      { label: '平台', value: platformLabel(v.platform) },
    ];
    return list;
  });

  $effect(() => {
    void loadVideo();
    void loadPlayback();
  });

  // 片源变化（playback / 代理开关 / DOM 挂载）才重新挂；单纯切档位走 applyVariant
  $effect(() => {
    const info = playback;
    const el = videoEl;
    if (!info || !el) return;
    void attachSource();
  });

  $effect(
    () => () => {
      destroyPlayer();
    },
  );
</script>

<svelte:window onkeydown={onKeydown} />

{#if pageError}
  <EmptyState title="打不开这个影片" hint={pageError} icon="error">
    {#snippet actions()}
      <button type="button" class="btn" onclick={() => void loadVideo()}>重试</button>
      <a data-link href="/" class="btn btn-ghost">返回关注</a>
    {/snippet}
  </EmptyState>
{:else if loadingVideo && !video}
  <div class="mx-auto max-w-[72rem] space-y-4">
    <Skeleton class="aspect-video w-full" rounded="rounded-xl" />
    <Skeleton class="h-6 w-2/3" />
    <Skeleton class="h-3.5 w-1/3" />
  </div>
{:else if video}
  <article>
    {#if mockNote}
      <p class="mb-3 rounded-xl bg-panel px-3 py-2 text-meta text-dim">
        离线预览：正在使用内置示例数据（后端未连接：{mockNote}）
      </p>
    {/if}

    <div class="relative overflow-hidden rounded-xl bg-black" bind:this={playerWrap}>
      <video
        bind:this={videoEl}
        class="block aspect-video w-full bg-black"
        controls
        playsinline
        preload="metadata"
        poster={poster ?? undefined}
        aria-label={video.title}
        onplaying={onPlaying}
        onerror={onMediaError}
      ></video>

      {#if loadingPlayback}
        <div class="pointer-events-none absolute inset-0 flex items-center justify-center bg-ink/50">
          <p class="text-ui text-muted">正在解析播放地址…</p>
        </div>
      {/if}

      {#if playError}
        <div class="absolute inset-0 flex flex-col items-center justify-center gap-3 bg-ink/85 px-6 text-center">
          <p class="max-w-md text-ui text-fg/90">{playError}</p>
          <div class="flex flex-wrap items-center justify-center gap-2">
            <button type="button" class="btn" onclick={() => void loadPlayback()}>重新解析</button>
            {#if proxyAvailable || usingProxy}
              {#if !usingProxy}
                <button type="button" class="btn btn-primary" onclick={switchToProxy}>用代理播放</button>
              {/if}
            {/if}
          </div>
          <p class="text-meta text-dim">空格 暂停 · ← → 快退快进 5 秒 · F 全屏</p>
        </div>
      {/if}
    </div>

    <div class="mt-5 grid gap-6 lg:grid-cols-[minmax(0,1fr)_17rem]">
      <div class="min-w-0">
        <h2 class="text-display font-normal leading-snug tracking-[-0.01em] text-fg lg:text-hero">{video.title}</h2>

        <p class="mt-2 flex flex-wrap items-center gap-x-2 gap-y-1 text-ui text-muted">
          {#each video.performers as person, index (person.name + String(index))}
            {#if person.id !== null}
              <a
                data-link
                href={`/performer/${String(person.id)}`}
                class="text-fg/85 underline-offset-4 transition-colors hover:text-hi hover:underline"
              >
                {person.name}
              </a>
            {:else}
              <span class="text-fg/85">{person.name}</span>
            {/if}
            {#if index < video.performers.length - 1}
              <span class="text-dim" aria-hidden="true">/</span>
            {/if}
          {/each}
          <span class="text-dim" aria-hidden="true">·</span>
          <span class="text-meta text-dim">{relativeTime(video.publishedAt)}</span>
        </p>

        <button
          type="button"
          class="btn btn-primary mt-3 h-9 lg:hidden"
          onclick={() => (downloadOpen = true)}
          disabled={playback === null && loadingPlayback}
        >
          {video.hasLocalFile ? '再下载一份' : '下载到本地库'}
        </button>

        {#if video.tags.length > 0}
          <ul class="mt-3 flex flex-wrap items-center gap-1.5" aria-label="标签">
            {#each tagsExpanded ? video.tags : video.tags.slice(0, TAG_PREVIEW) as tag (tag)}
              <li class="chip">{tag}</li>
            {/each}
            {#if video.tags.length > TAG_PREVIEW}
              <li>
                <button
                  type="button"
                  class="chip text-fg transition-colors hover:bg-raise"
                  aria-expanded={tagsExpanded}
                  onclick={() => (tagsExpanded = !tagsExpanded)}
                >
                  {tagsExpanded ? '收起' : `+${String(video.tags.length - TAG_PREVIEW)}`}
                </button>
              </li>
            {/if}
          </ul>
        {/if}

        {#if video.description}
          <p class="mt-4 max-w-[68ch] whitespace-pre-line text-ui leading-relaxed text-muted">{video.description}</p>
        {/if}
      </div>

      <aside class="lg:pt-1">
        <div>
          <label for="quality" class="label-meta mb-1.5">清晰度</label>
          <select
            id="quality"
            class="field"
            value={selectedVariantId ?? ''}
            onchange={onQualityChange}
            disabled={variants.length === 0}
          >
            {#if variants.length === 0}
              <option value="">无可用片源</option>
            {/if}
            {#each variants as variant (variant.id)}
              <option value={variant.id}>
                {variantLabel(variant)}{codecTag(variant) ? ` · ${codecTag(variant)}` : ''}{variant.id ===
                playback?.defaultVariantId
                  ? '（最高）'
                  : ''} · {formatBandwidth(variant.bandwidth)}
              </option>
            {/each}
          </select>
          {#if levelHeights.length > 1}
            <p class="mt-1.5 text-meta text-dim">
              当前播放 {activeHeight !== null ? `${String(activeHeight)}p` : '—'}
            </p>
          {/if}
        </div>

        <dl class="mt-4 divide-y divide-line">
          {#each specs as spec (spec.label)}
            <div class="flex items-baseline justify-between gap-3 py-2">
              <dt class="text-meta text-dim">{spec.label}</dt>
              <dd class="text-right text-ui tabular-nums text-fg/85">{spec.value}</dd>
            </div>
          {/each}
        </dl>

        <button
          type="button"
          class="btn btn-primary mt-4 w-full"
          onclick={() => (downloadOpen = true)}
          disabled={playback === null && loadingPlayback}
        >
          <svg viewBox="0 0 20 20" class="h-3.5 w-3.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true">
            <path d="M10 3.5v8" />
            <path d="m6.75 8.5 3.25 3.25L13.25 8.5" />
            <path d="M4 15.5h12" />
          </svg>
          {video.hasLocalFile ? '再下载一份' : '下载到本地库'}
        </button>
        {#if video.hasLocalFile}
          <p class="mt-1.5 text-meta text-sage">已在本地 {video.localLibraryIds.length} 个库中</p>
        {/if}

        <a
          href={video.url}
          target="_blank"
          rel="noreferrer noopener"
          class="btn mt-2 w-full"
          aria-label="在平台打开原始页面"
        >
          在 {platformLabel(video.platform)} 打开
        </a>

        <p class="mt-4 text-meta leading-relaxed text-dim">
          空格 暂停 · ← → 快退快进 5 秒 · F 全屏
        </p>
      </aside>
    </div>

    {#if video.related.length > 0}
      <section class="mt-10">
        <div class="mb-3 flex items-center justify-between">
          <h3 class="text-title font-medium text-fg">相关影片</h3>
          <button type="button" class="btn btn-ghost h-8" onclick={() => navigate('/')}>回关注</button>
        </div>
        <div class="grid grid-cols-2 gap-x-3 gap-y-6 sm:grid-cols-3 xl:grid-cols-4">
          {#each video.related.slice(0, 8) as related (related.id)}
            <VideoCard video={related} />
          {/each}
        </div>
      </section>
    {/if}
  </article>
{/if}

{#if video}
  <DownloadDialog videoId={video.id} open={downloadOpen} onClose={() => (downloadOpen = false)} />
{/if}
