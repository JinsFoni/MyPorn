<script lang="ts">
  /**
   * YouTube 视频卡片：16:9 封面 + 时长角标 + 频道行。
   *
   * 这张卡是三条墙（关注流 / B 站 / YouTube）的**样式模板**：封面、时长、下载按钮、头像/标题/副行
   * 都拆成了共享组件（CoverArt / CardDuration / CardDownloadButton / CardByline），
   * 影片卡 VideoCard 用的是同一套，别再往这张卡里单独调样式——要改就改共享组件。
   *
   * YouTube 独有的部分只有两处状态标：未看小圆点（左上）与直播标（左下）；
   * 时间可能未知（深度回填走 yt-dlp flat，实测没有时间戳），所以「时间未知」是正常态，
   * 不显示成错误、也不要显示 1970。想让它有日期：点开单条会补，墙上的「补齐」整批补（jobs 的 yt_meta）。
   * 时长同理——RSS 通道不给时长，也是这条任务补上（卡片上没有时长就是还没补过）。
   */
  import CardByline from './CardByline.svelte';
  import CardDownloadButton from './CardDownloadButton.svelte';
  import CardDuration from './CardDuration.svelte';
  import CoverArt from './CoverArt.svelte';
  import { imageUrl } from '../lib/img';
  import { formatDate } from '../lib/format';
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
  const avatar = $derived(imageUrl(video.channel.avatarUrl, 36));
</script>

<article class="group relative">
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

    <CardDuration seconds={video.durationSec} />

    {#if video.isLive}
      <!-- 左下角让给下载按钮（`CardDownloadButton`，`left-2` 起 28px）：有按钮时右移 40px -->
      <span
        class="absolute bottom-2 {onDownload ? 'left-10' : 'left-2'} rounded bg-brick/90 px-1.5 py-0.5 text-[0.6875rem] font-medium text-fg"
      >
        直播
      </span>
    {/if}
  </button>

  {#if onDownload}
    <CardDownloadButton
      label="下载"
      title={video.downloaded ? '已在本地库，再下一次' : '下载到本地库'}
      {downloading}
      onclick={() => onDownload(video)}
    />
  {/if}

  <CardByline
    title={headline}
    titleHint={video.title}
    name={video.channel.title}
    avatarUrl={avatar}
    monogram={video.channel.title.charAt(0).toUpperCase()}
    views={video.views}
    viewsLabel="次观看"
    dateLabel={timeLabel(video)}
    onTitleClick={() => onOpen?.(video)}
  >
    {#snippet trailing()}
      {#if video.saved}
        <svg viewBox="0 0 24 24" class="mt-1 h-3.5 w-3.5 shrink-0 text-hi" fill="currentColor" aria-label="已收藏">
          <path d="m12 3.6 2.6 5.3 5.9.9-4.3 4.1 1 5.8-5.2-2.7-5.2 2.7 1-5.8L3.5 9.8l5.9-.9z" />
        </svg>
      {/if}
    {/snippet}
  </CardByline>
</article>
