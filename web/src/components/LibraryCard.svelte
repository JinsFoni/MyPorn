<script lang="ts">
  /**
   * 媒体库的卡片：封面 + 时长 + 画质角标 + 「文件名 / 体积 / 库 / 下载时间」。
   *
   * 和关注流那张卡（VideoCard）的差别都来自「这行是磁盘上的一个文件」：
   * - 封面优先用下载时落盘的 thumb.jpg（`localThumbUrl`），远端封面地址过期也照样有图；
   *   隐私模式下**必须**换回 /api/img 的小糊图——本地那张是按原尺寸发的，没有糊化那一层。
   * - 角标是 `2160p`（落盘时 probe 的尺寸），不是平台的清晰度档位名。
   * - 文件被挪走/删掉时整条压暗并写一句「文件已不在磁盘上」：记录还在，点进去会 404。
   * - 右上角那颗按钮是「复制文件路径」（媒体库最常见的动作是把路径贴到别处去）。
   */
  import CardDuration from './CardDuration.svelte';
  import CoverArt from './CoverArt.svelte';
  import { imageUrl } from '../lib/img';
  import { privacy } from '../lib/privacy.svelte';
  import { formatBytes, relativeTime } from '../lib/format';
  import { router } from '../lib/router.svelte';
  import { live } from '../lib/live.svelte';
  import type { LibraryVideo } from '../lib/types';

  interface Props {
    item: LibraryVideo;
  }

  let { item }: Props = $props();

  /** 「2160p」：只写高度，和播放器画质菜单同一套叫法；尺寸未知就不画 */
  const quality = $derived(item.height > 0 ? `${String(item.height)}p` : '');

  /**
   * 封面：本地那张只有「不是隐私模式」时才用。
   * `imageUrl(localThumbUrl)` 会原样返回（站内相对路径），所以这里要显式判断隐私模式。
   */
  const cover = $derived(
    privacy.on || !item.localThumbUrl
      ? imageUrl(item.thumbUrl, 480)
      : item.localThumbUrl,
  );

  const back = $derived(`?back=${encodeURIComponent(`${router.path}${router.search}`)}`);
  const when = $derived(relativeTime(item.downloadedAt));

  let copied = $state(false);

  async function copyPath(event: MouseEvent): Promise<void> {
    event.preventDefault();
    event.stopPropagation();
    try {
      await navigator.clipboard.writeText(item.videoPath);
      copied = true;
      window.setTimeout(() => (copied = false), 1600);
      live.toast('路径已复制', 'success');
    } catch {
      live.toast('复制失败，请手动选择路径', 'error');
    }
  }
</script>

<article class="group relative min-w-0">
  <a
    href={item.missing ? undefined : `/video/${String(item.videoId)}${back}`}
    data-link
    class="block {item.missing ? 'pointer-events-none' : ''}"
    aria-label={`${item.title}，${quality || '尺寸未知'}，${formatBytes(item.sizeBytes)}`}
  >
    <div class="relative aspect-video overflow-hidden rounded-xl bg-panel {item.missing ? 'opacity-40' : ''}">
      <CoverArt src={cover} scaleClass="transition-transform duration-300 group-hover:scale-[1.02]" />

      {#if quality}
        <span
          class="pointer-events-none absolute left-2 top-2 rounded-md bg-ink/70 px-1.5 py-0.5 text-[0.6875rem] font-medium tabular-nums text-fg"
        >
          {quality}
        </span>
      {/if}

      <CardDuration seconds={item.durationSec} />

      {#if item.missing}
        <span
          class="absolute inset-x-0 bottom-0 bg-ink/85 px-2 py-1 text-center text-[0.6875rem] text-brick"
        >
          文件已不在磁盘上
        </span>
      {/if}
    </div>

    <div class="mt-2.5 min-w-0">
      <h3 class="truncate text-ui font-medium leading-snug text-fg group-hover:underline" title={item.title}>
        {item.title}
      </h3>
      <p class="mt-1 flex min-w-0 items-center gap-x-2 text-meta tabular-nums text-muted">
        <span class="shrink-0">{formatBytes(item.sizeBytes)}</span>
        {#if item.performers.length > 0}
          <span class="min-w-0 truncate">{item.performers.join('、')}</span>
        {/if}
      </p>
      <p class="mt-0.5 flex min-w-0 items-center gap-x-2 text-meta text-dim">
        <span class="min-w-0 truncate">{item.libraryName}</span>
        <span class="shrink-0">{when}</span>
      </p>
    </div>
  </a>

  <button
    type="button"
    class="absolute right-2 top-2 z-10 rounded-full bg-ink/70 p-1.5 text-fg opacity-0 transition-opacity
      hover:bg-ink focus-visible:opacity-100 group-hover:opacity-100 group-focus-within:opacity-100
      max-lg:opacity-100"
    aria-label={`复制 ${item.fileName} 的路径`}
    title="复制文件路径"
    onclick={(event) => void copyPath(event)}
  >
    {#if copied}
      <svg viewBox="0 0 24 24" class="h-4 w-4" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round">
        <path d="m5 12.5 4.5 4.5L19 7.5" />
      </svg>
    {:else}
      <svg viewBox="0 0 24 24" class="h-4 w-4" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">
        <rect x="9" y="9" width="11" height="11" rx="2.5" />
        <path d="M15 5.5A2.5 2.5 0 0 0 12.5 3H6.5A3.5 3.5 0 0 0 3 6.5v6A2.5 2.5 0 0 0 5.5 15" />
      </svg>
    {/if}
  </button>
</article>
