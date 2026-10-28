<script lang="ts">
  /**
   * YouTube 视频卡片：16:9 封面 + 时长角标 + 频道行。
   *
   * 与影片卡（VideoCard）的差别：YouTube 的视频一律是 16:9，不做 3:4 竖版裁切；
   * 时间可能未知（深度回填走 yt-dlp flat，实测没有时间戳），所以「时间未知」是正常态，
   * 不显示成错误、也不要显示 1970。
   */
  import { imageUrl } from '../lib/img';
  import { formatCount, formatDuration, relativeTime } from '../lib/format';
  import type { YtVideo } from '../lib/types';

  interface Props {
    video: YtVideo;
    /** 点封面：在站内打开播放弹窗 */
    onOpen?: (video: YtVideo) => void;
    /** 悬停/聚焦时才出现的下载按钮被点了 */
    onDownload?: (video: YtVideo) => void;
    /** 悬停即预热：把解析（yt-dlp 4~5 秒）挪到用户真正点下之前 */
    onWarm?: (video: YtVideo) => void;
    /** 正在下载/刚刚排上队 */
    downloading?: boolean;
  }

  let { video, onOpen, onDownload, onWarm, downloading = false }: Props = $props();

  let thumbFailed = $state(false);
  const thumb = $derived(imageUrl(video.thumbUrl, 480));
  const meta = $derived(
    [video.views > 0 ? `${formatCount(video.views)} 次观看` : '', timeLabel(video)]
      .filter((part) => part !== '')
      .join(' · '),
  );

  function timeLabel(v: YtVideo): string {
    return v.publishedAt ? relativeTime(v.publishedAt) : '时间未知';
  }
</script>

<article class="group">
  <button
    type="button"
    class="relative block w-full cursor-pointer overflow-hidden rounded-xl bg-panel text-left"
    aria-label={`播放 ${video.title}`}
    onclick={() => onOpen?.(video)}
    onpointerenter={() => onWarm?.(video)}
    onfocus={() => onWarm?.(video)}
  >
    <div class="aspect-video w-full">
      {#if thumb && !thumbFailed}
        <img
          src={thumb}
          alt=""
          loading="lazy"
          decoding="async"
          class="h-full w-full object-cover transition-transform duration-300 group-hover:scale-[1.02]"
          onerror={() => (thumbFailed = true)}
        />
      {:else}
        <div class="grid h-full w-full place-items-center text-dim">
          <svg viewBox="0 0 24 24" class="h-6 w-6" fill="none" stroke="currentColor" stroke-width="1.5">
            <rect x="3" y="5.5" width="18" height="13" rx="3" />
            <path d="M10.4 9.6v4.8l4-2.4z" fill="currentColor" stroke="none" />
          </svg>
        </div>
      {/if}
    </div>

    {#if !video.seen}
      <span class="absolute left-2 top-2 h-2 w-2 rounded-full bg-hi" aria-label="未看"></span>
    {/if}

    {#if video.durationSec > 0}
      <span
        class="absolute bottom-2 right-2 rounded bg-ink/75 px-1.5 py-0.5 text-[0.6875rem] font-medium tabular-nums text-fg"
      >
        {formatDuration(video.durationSec)}
      </span>
    {/if}
    {#if video.isLive}
      <span class="absolute bottom-2 left-2 rounded bg-brick/90 px-1.5 py-0.5 text-[0.6875rem] font-medium text-fg">
        直播
      </span>
    {/if}
  </button>

  {#if onDownload}
    <button
      type="button"
      class="absolute right-2 top-2 rounded-full bg-ink/70 p-1.5 text-fg opacity-0 transition-opacity
        hover:bg-ink focus-visible:opacity-100 group-hover:opacity-100
        max-sm:opacity-100 {downloading ? 'pointer-events-none opacity-100' : ''}"
      title={video.downloaded ? '已在本地库，再下一次' : '下载到本地库'}
      aria-label="下载"
      onclick={() => onDownload(video)}
    >
      {#if downloading}
        <svg viewBox="0 0 24 24" class="h-4 w-4 animate-spin" fill="none" stroke="currentColor" stroke-width="2">
          <circle cx="12" cy="12" r="8" stroke-opacity="0.25" />
          <path d="M20 12a8 8 0 0 0-8-8" />
        </svg>
      {:else}
        <svg viewBox="0 0 24 24" class="h-4 w-4" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round">
          <path d="M12 4v10" />
          <path d="m8 10.5 4 4 4-4" />
          <path d="M4.5 18.5h15" />
        </svg>
      {/if}
    </button>
  {/if}

  <div class="mt-2 flex gap-2">
    {#if video.channel.avatarUrl}
      <img
        src={imageUrl(video.channel.avatarUrl, 36) ?? ''}
        alt=""
        loading="lazy"
        class="mt-0.5 h-6 w-6 shrink-0 rounded-full object-cover"
      />
    {:else}
      <span class="mt-0.5 grid h-6 w-6 shrink-0 place-items-center rounded-full bg-panel text-[0.625rem] text-dim">
        {video.channel.title.charAt(0).toUpperCase()}
      </span>
    {/if}
    <div class="min-w-0 flex-1">
      <button
        type="button"
        class="line-clamp-2 text-left text-ui font-medium leading-snug text-fg hover:underline"
        title={video.title}
        onclick={() => onOpen?.(video)}
      >
        {video.title}
      </button>
      <p class="mt-1 truncate text-meta text-dim">
        {video.channel.title}{#if meta}<span class="mx-1 opacity-60">·</span>{meta}{/if}
      </p>
    </div>
    {#if video.saved}
      <svg viewBox="0 0 24 24" class="mt-1 h-3.5 w-3.5 shrink-0 text-hi" fill="currentColor" aria-label="已收藏">
        <path d="m12 3.6 2.6 5.3 5.9.9-4.3 4.1 1 5.8-5.2-2.7-5.2 2.7 1-5.8L3.5 9.8l5.9-.9z" />
      </svg>
    {/if}
  </div>
</article>

<style>
  /* 「悬停才出现的按钮」需要一个定位父级；文章本身不设 relative（会让封面圆角溢出），
     所以把相对定位挂在这一层，按钮的 absolute 以它为准。 */
  article {
    position: relative;
  }
</style>
