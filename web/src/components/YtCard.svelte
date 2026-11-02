<script lang="ts">
  /**
   * YouTube 视频卡片：16:9 封面 + 时长角标 + 频道行。
   *
   * 与影片卡（VideoCard）的差别：YouTube 的视频一律是 16:9，不做 3:4 竖版裁切；
   * 时间可能未知（深度回填走 yt-dlp flat，实测没有时间戳），所以「时间未知」是正常态，
   * 不显示成错误、也不要显示 1970。想让它有日期：点开单条会补，墙上的「补齐」整批补（jobs 的 yt_meta）。
   */
  import CoverArt from './CoverArt.svelte';
  import { imageUrl } from '../lib/img';
  import { formatCount, formatDate, formatDuration } from '../lib/format';
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

  // 640：墙在 2xl 断点是 4 列（每张 ~316px CSS），DPR2 要 632px 才不糊。
  // 上游给的是 hq720（1280×720），i.ytimg 不认 w=，所以这里只是把尺寸意图写清楚。
  const thumb = $derived(imageUrl(video.thumbUrl, 640));
  // 观看数不再写成「679 次观看」：副行只有 12px，三个中文字占掉一格宽度、还把日期挤出去，
  // 改成一个空心播放三角 + 数字（`▷ 679`，三角只描边不填色，和旁边 12px 的字一行重量）。
  // 读屏仍念「679 次观看」，见下面的 aria-label。
  const views = $derived(video.views > 0 ? formatCount(video.views) : '');

  /**
   * 卡片上的发布时间用绝对日期（2026-10-31），不用「1个月前」。
   *
   * 这面墙是拿来判断「这条我看过没有 / 是不是新的」的：相对时间在这种列表里全是
   * 「1天前」「2天前」，两天内的条目彼此长得一模一样，扫一眼分不出先后；
   * 而且它一天一变，卡片上的字会自己漂。绝对日期一眼可比、也不会变。
   * 相对时间的说法还留在详情页（`formatDateTime（relativeTime）` 两个一起给）。
   */
  function timeLabel(v: YtVideo): string {
    return v.publishedAt ? formatDate(v.publishedAt) : '时间未知';
  }

  /**
   * 卡片标题去掉末尾那串 `#标签`。
   *
   * YouTube 的标题习惯把 hashtag 堆在最后（实测 `/youtube` 首屏 36 张卡里 21 张带，
   * 且全部在末尾）：「新丝路泳装秀25号沈梦颖#catwalktour #model #show #beauty #catwalk」
   * 54 个字，削完只剩 12 个字的片名——不削的话一行标题里大半是标签，看不出这条是什么。
   * 只削**末尾连续的一段**：标题中间的 `#`（比如 `C# 教程 第一课`）原样留着，
   * 整条标题都是标签（削完为空，比如只有 `#shorts`）时也不动它。
   * 原始标题仍在 `title`（悬停提示）、`aria-label` 和详情页上，一个字符都没丢。
   */
  function stripHashTags(title: string): string {
    const stripped = title.replace(/(?:\s*#[\p{L}\p{N}_-]+)+\s*$/u, '').trim();
    return stripped === '' ? title : stripped;
  }

  const headline = $derived(stripHashTags(video.title));
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
      <!-- 竖屏视频的封面两侧烘着站方的暗色填充，这里换成本身的虚化（见 CoverArt） -->
      <CoverArt src={thumb} scaleClass="transition-transform duration-300 group-hover:scale-[1.02]" />
    </div>

    {#if !video.seen}
      <span class="absolute left-2 top-2 h-2 w-2 rounded-full bg-hi" aria-label="未看"></span>
    {/if}

    {#if video.durationSec > 0}
      <!-- 时长不带黑色药丸底（按用户要求去掉），只给一层文字阴影保证亮封面上也读得清——
           和 B 站墙 `VideoCard` 同一套 `text-shadow`，只是留在右下角（YouTube 自己的位置）。
           右上角是下载按钮、左上角是未看小圆点，两个角都占了。 -->
      <span
        class="pointer-events-none absolute bottom-2 right-2 text-meta font-medium tabular-nums text-fg [text-shadow:0_1px_5px_rgba(0,0,0,0.8),0_0_12px_rgba(0,0,0,0.7),0_0_3px_rgba(0,0,0,0.8)]"
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

  <!-- 头像 / 标题 / 副行的比例照 B 站墙的 VideoCard 来：36px 圆头像（`h-9 w-9`，
       imageUrl 也按 36 取，不糊）、标题一行截断、副行小字。
       原来是 24px 头像 + 两行标题——两行标题挤得整张卡比 B 站那面墙高一截，
       而标题的第二行大半是 #标签（见 stripHashTags）。 -->
  <div class="mt-2.5 flex items-start gap-2.5">
    {#if video.channel.avatarUrl}
      <img
        src={imageUrl(video.channel.avatarUrl, 36) ?? ''}
        alt=""
        loading="lazy"
        class="h-9 w-9 shrink-0 rounded-full object-cover"
      />
    {:else}
      <span class="grid h-9 w-9 shrink-0 place-items-center rounded-full bg-panel text-meta text-dim">
        {video.channel.title.charAt(0).toUpperCase()}
      </span>
    {/if}
    <div class="min-w-0 flex-1">
      <button
        type="button"
        class="block w-full truncate text-left text-ui font-medium leading-snug text-fg hover:underline"
        title={video.title}
        onclick={() => onOpen?.(video)}
      >
        {headline}
      </button>
      <!-- 副行的排法照 B 站卡：频道名吃掉剩下的宽度并截断，观看数和日期 `shrink-0` 不被挤掉
           （以前整行一个 `truncate`，先被截掉的是日期） -->
      <p class="mt-1 flex items-center gap-1.5 text-meta text-dim">
        <span class="truncate">{video.channel.title}</span>
        {#if views}
          <span
            class="flex shrink-0 items-center gap-1"
            title={`${views} 次观看`}
            aria-label={`${views} 次观看`}
          >
            <svg
              viewBox="0 0 24 24"
              class="h-3 w-3 shrink-0"
              fill="none"
              stroke="currentColor"
              stroke-width="2"
              stroke-linejoin="round"
              aria-hidden="true"
            >
              <path d="M7.5 4.2v15.6L20.4 12z" />
            </svg>
            <span class="tabular-nums">{views}</span>
          </span>
          <span class="shrink-0" aria-hidden="true">·</span>
        {/if}
        <span class="shrink-0">{timeLabel(video)}</span>
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
