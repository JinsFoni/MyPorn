<script lang="ts">
  import DownloadDialog from '../components/DownloadDialog.svelte';
  import VideoCard from '../components/VideoCard.svelte';
  import EmptyState from '../components/EmptyState.svelte';
  import Skeleton from '../components/Skeleton.svelte';
  import VideoPlayer from '../components/VideoPlayer.svelte';
  import VideoLoading from '../components/VideoLoading.svelte';
  import { api, messageFor } from '../lib/api';
  import {
    formatBandwidth,
    formatBytes,
    formatCount,
    formatDate,
    formatDuration,
    relativeTime,
  } from '../lib/format';
  import { imageUrl } from '../lib/img';
  import { live } from '../lib/live.svelte';
  import { platformLabel } from '../lib/platform';
  import { privacy } from '../lib/privacy.svelte';
  import {
    codecTag,
    needsHlsEngine,
    needsRemuxPrep,
    pickVariant,
    rememberHeight,
    rememberedHeight,
    sortVariantsDesc,
    sourceTypeFor,
    variantLabel,
    type QualityOption,
  } from '../lib/player';
  import { router } from '../lib/router.svelte';
  import type { PlaybackInfo, PlaybackVariant, RemuxStatus, VideoDetail } from '../lib/types';

  interface Props {
    id: string;
  }

  let { id }: Props = $props();

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
  let mockNote = $state('');
  let downloadOpen = $state(false);
  /**
   * 「正在准备这一档」的进度：只有分轨档位（B 站 1080P+/4K）会用到。
   * 这些档位要先在服务端把音画两条流合成一份 mp4，4K 一档 554 MB~1.77 GB，
   * 头一次点要等 1~3 分钟。以前这段时间是一个不回头的 HTTP 请求，用户只看到转圈，
   * 最后当成「放不了」——现在换成轮询进度 + 明确百分比。
   */
  let preparing = $state<{
    variantId: string;
    label: string;
    percent: number;
    bytes: number;
    elapsedSec: number;
  } | null>(null);
  /** 换档位/离开页面时 +1，让还在跑的轮询自己退出 */
  let prepToken = 0;
  /** 「重新解析」时 +1，用来重建播放器（地址没变也要重新挂） */
  let attempt = $state(0);
  /** 标签默认只露这么多，剩下的折在「+N」里 */
  const TAG_PREVIEW = 8;
  let tagsExpanded = $state(false);
  let seenSent = false;

  const variants = $derived(playback ? sortVariantsDesc(playback.variants) : []);
  const selectedVariant = $derived(variants.find((v) => v.id === selectedVariantId) ?? null);
  /**
   * 播放器的 poster（就是封面）。隐私模式下干脆不给：这个封面是 <video> 自己的属性，
   * CSS 没法只糊它而不糊画面。宁可在解析片源的这一两秒里黑着 + 转环，
   * 也不把一张高清封面亮在屏幕正中央——这页本来就是为了看片，一闪而过的封面没有信息价值。
   */
  const poster = $derived(video && !privacy.on ? imageUrl(video.thumbUrl, 1280) : null);
  /** 准备中的那一行字：`正在准备 4K AV1 · 62%` */
  const prepHeadline = $derived(
    preparing ? `${preparing.label} · ${String(Math.round(preparing.percent))}%` : '',
  );
  /**
   * 副行：体积 + 已经等了多久 + 「只等这一次」。
   * 最后这半句很重要——不说清楚，用户会以为每次点 4K 都要等这么久。
   */
  const prepDetail = $derived.by(() => {
    if (!preparing) return '';
    const size = preparing.bytes > 0 ? `约 ${formatBytes(preparing.bytes)} · ` : '';
    return `${size}已等 ${String(preparing.elapsedSec)} 秒 · 合好之后同一档秒开`;
  });
  const prepVeilDetail = $derived(preparing ? `${prepHeadline} · ${prepDetail}` : null);

  /** 这批片源里只要有一条 HLS，就整个用 hls.js 引擎（它也能放 mp4） */
  const engine = $derived(needsHlsEngine(playback?.variants ?? []) ? 'hls' : 'native');
  const source = $derived.by((): { src: string; type: string } | null => {
    const info = playback;
    const variant = selectedVariant;
    if (!info || !variant) return null;
    const isHls = usingProxy ? true : variant.hls;
    const src = usingProxy && info.proxyPlaybackUrl ? info.proxyPlaybackUrl : variant.url;
    return { src, type: sourceTypeFor(isHls) };
  });

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
    // 换了一部片子（或重新解析）：上一次的准备进度、还在跑的轮询都作废。
    preparing = null;
    prepToken += 1;
    try {
      const info = await api.playback(videoId);
      // HLS 引擎是单独一个 chunk（含 hls.js）：只有真要放 HLS 时才下载
      if (needsHlsEngine(info.variants)) await import('@videojs/html/media/hlsjs-video');
      playback = info;
      // 标签只在详情页里有：解析播放地址时一起带回来，别让用户看到空标签或刷新才出现。
      if (info.tags?.length && video && video.tags.length === 0) {
        video = { ...video, tags: info.tags };
      }
      const preferred = pickVariant(info, rememberedHeight());
      // 本地文件本来就是同源直发，"用代理播放"对它是空操作。
      const chosen = preferred ?? info.variants.find((v) => v.id === info.defaultVariantId) ?? null;
      proxyAvailable = info.proxyPlaybackUrl !== null && chosen?.local !== true;
      if (chosen && needsRemuxPrep(chosen)) {
        // 分轨档位（4K/1080P+）要先在服务端合流：地址先压着，进度到 ready 再交给播放器。
        selectedVariantId = null;
        void activateVariant(chosen);
      } else {
        selectedVariantId = chosen?.id ?? info.defaultVariantId;
      }
    } catch (err) {
      playError = messageFor(err);
    } finally {
      loadingPlayback = false;
    }
  }

  function onFailed(detail: string): void {
    if (playback?.proxyPlaybackUrl && !usingProxy) {
      proxyAvailable = true;
      playError = '直链播放失败，通常是 Referer/UA 或跨域限制。可以试试代理播放。';
      return;
    }
    playError = detail === '' ? '播放失败，请重试。' : `播放失败（${detail}）`;
  }

  function retryPlayback(): void {
    playError = '';
    preparing = null;
    prepToken += 1;
    attempt += 1;
    void loadPlayback();
  }

  function applyVariant(variantId: string): void {
    const variant = playback?.variants.find((v) => v.id === variantId);
    if (!variant) return;
    void activateVariant(variant);
  }

  /**
   * 切到某一档。
   *
   * 分轨档位（B 站 1080P+ / 4K）要先等服务端把音画两轨合成一份 mp4——4K 一档
   * 554 MB~1.77 GB，头一次要等 1~3 分钟。所以这里先轮询进度、把百分比画在画面上，
   * 合好才改 selectedVariantId（这时 <video> 拿到的是一个已经就绪的文件，秒开）。
   */
  async function activateVariant(variant: PlaybackVariant): Promise<void> {
    const token = ++prepToken;
    playError = '';
    if (needsRemuxPrep(variant)) {
      const ready = await waitForRemux(variant, token);
      if (!ready || token !== prepToken) return;
    }
    selectedVariantId = variant.id;
    rememberHeight(variant.height);
  }

  /** 轮询合流进度，ready 时返回 true；失败/取消返回 false（原因写进 playError）。 */
  async function waitForRemux(variant: PlaybackVariant, token: number): Promise<boolean> {
    const codec = codecTag(variant);
    const label = `正在准备 ${variantLabel(variant)}${codec === null ? '' : ` ${codec}`}`;
    // 体积按码率 × 时长估：菜单上只有 Mbps，用户看到「约 554 MB」才知道自己在等什么。
    const bytes =
      variant.bandwidth > 0 && video !== null ? (variant.bandwidth / 8) * video.durationSec : 0;
    for (;;) {
      let status: RemuxStatus;
      try {
        status = await api.remuxStatus(videoId, variant.id);
      } catch (err) {
        if (token === prepToken) {
          preparing = null;
          playError = messageFor(err);
        }
        return false;
      }
      if (token !== prepToken) return false;
      if (status.state === 'ready') {
        preparing = null;
        return true;
      }
      if (status.state === 'failed') {
        preparing = null;
        playError = `这一档准备失败：${status.error ?? '未知原因'}。换个档位，或点「重新解析」再来一次。`;
        return false;
      }
      preparing = {
        variantId: variant.id,
        label,
        percent: status.percent,
        bytes,
        elapsedSec: status.elapsedSec,
      };
      await new Promise((resolve) => setTimeout(resolve, 1200));
    }
  }

  function switchToProxy(): void {
    if (!playback?.proxyPlaybackUrl) return;
    usingProxy = true;
    proxyAvailable = false;
    playError = '';
    live.toast('已切换到代理播放', 'info');
  }

  function onStarted(): void {
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

  /** 播放器左上角清晰度菜单的条目（顺序即 sortVariantsDesc 的顺序，最高在最上） */
  const qualities = $derived<QualityOption[]>(
    variants.map((variant) => ({
      id: variant.id,
      label: variantLabel(variant),
      hint: [
        codecTag(variant),
        formatBandwidth(variant.bandwidth),
        variant.local === true ? '本地文件' : null,
        variant.local !== true && variant.id === playback?.defaultVariantId ? '最高' : null,
      ]
        .filter((part) => part !== null && part !== '')
        .join(' · '),
    })),
  );

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

  // 离开这一页就把轮询停掉：prepToken 一变，waitForRemux 下一轮自己退出。
  $effect(() => () => {
    prepToken += 1;
  });

  // 先把封面塞进浏览器缓存：播放器挂载时会立刻要这张图，否则会闪一下它自带的占位插画
  $effect(() => {
    const url = poster;
    if (!url) return;
    const img = new Image();
    img.decoding = 'async';
    img.src = url;
  });
  /**
   * 「返回」去哪儿：优先看点进来的那一页（卡片链接带的 `?back=`），
   * 没有就落回「关注」。只认站内绝对路径（以单个 / 开头），别的一律忽略，免得被带去外站。
   */
  const backTo = $derived.by(() => {
    const raw = router.query.get('back') ?? '';
    return raw.startsWith('/') && !raw.startsWith('//') ? raw : '/';
  });
  const backLabel = $derived.by(() => {
    if (backTo.startsWith('/bilibili')) return '返回 B 站';
    if (backTo.startsWith('/youtube')) return '返回 YouTube';
    if (backTo.startsWith('/performer')) return '返回艺人';
    if (backTo.startsWith('/downloads')) return '返回下载';
    if (backTo === '/') return '返回关注';
    return '返回上一页';
  });
</script>

{#if pageError}
  <EmptyState title="打不开这个影片" hint={pageError} icon="error">
    {#snippet actions()}
      <button type="button" class="btn" onclick={() => void loadVideo()}>重试</button>
      <a data-link href={backTo} class="btn btn-ghost">{backLabel}</a>
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

    {#if source}
      {#key attempt}
        <VideoPlayer
          src={source.src}
          type={source.type}
          engine={engine}
          poster={poster}
          title={video.title}
          videoKey={videoId}
          {qualities}
          selectedQuality={selectedVariantId}
          onSelectQuality={applyVariant}
          onStarted={onStarted}
          onFailed={onFailed}
        >
          {#snippet overlay()}
            {#if preparing}
              <div
                class="absolute inset-0 flex flex-col items-center justify-center gap-2 bg-ink/80 px-6 text-center"
              >
                <span
                  class="h-6 w-6 animate-spin rounded-full border-2 border-fg/25 border-t-fg"
                  aria-hidden="true"
                ></span>
                <p class="text-ui tabular-nums text-fg/90">{prepHeadline}</p>
                <p class="text-meta text-dim">{prepDetail}</p>
              </div>
            {:else if playError}
              <div class="pointer-events-auto absolute inset-0 flex flex-col items-center justify-center gap-3 bg-ink/85 px-6 text-center">
                <p class="max-w-md text-ui text-fg/90">{playError}</p>
                <div class="flex flex-wrap items-center justify-center gap-2">
                  <button type="button" class="btn" onclick={retryPlayback}>重新解析</button>
                  {#if proxyAvailable && !usingProxy}
                    <button type="button" class="btn btn-primary" onclick={switchToProxy}>用代理播放</button>
                  {/if}
                </div>
                <p class="text-meta text-dim">空格 暂停 · ← → 快退快进 5 秒 · ↑ ↓ 音量 · F 全屏 · M 静音</p>
              </div>
            {/if}
          {/snippet}
        </VideoPlayer>
      {/key}
    {:else}
      <div class="relative aspect-video w-full overflow-hidden rounded-xl bg-black">
        {#if loadingPlayback || preparing}
          <VideoLoading
            {poster}
            kind="poster"
            label={preparing ? prepHeadline : '正在解析播放地址'}
            detail={prepVeilDetail}
          />
        {:else if playError}
          <div class="absolute inset-0 flex flex-col items-center justify-center gap-3 px-6 text-center">
            <p class="max-w-md text-ui text-fg/90">{playError}</p>
            <button type="button" class="btn" onclick={retryPlayback}>重新解析</button>
          </div>
        {/if}
      </div>
    {/if}

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
          播放器快捷键：空格/K 播放暂停 · ← → 快退快进 5 秒 · ↑ ↓ 音量（10%） · M 静音 · F 全屏；画面单击暂停，双击左右两侧快退快进、双击中间全屏。
        </p>
      </aside>
    </div>

    {#if video.related.length > 0}
      <section class="mt-10">
        <div class="mb-3 flex items-center justify-between">
          <h3 class="text-title font-medium text-fg">相关影片</h3>
          <a data-link href={backTo} class="btn btn-ghost h-8">{backLabel}</a>
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
