<script lang="ts">
  /**
   * 影片卡（关注流 / B 站墙 / 艺人页 / 播放页「相关影片」共用）。
   *
   * 样式以 YouTube 卡（YtCard）为模板，两边共用同一套原子组件：
   * `CoverArt`（封面 + 悬停放大 + 竖屏两侧虚化）、`CardDuration`（右下角时长）、
   * `CardDownloadButton`（右上角下载按钮）、`CardByline`（36px 头像 / 单行标题 / 副行）。
   * 要改卡片长相就改那几个组件，别在这张卡里单独调——2026-11 之前三条墙各写一份，
   * 时长一半在左上、一半在右下，黑度还不一样，一换页就跳。
   *
   * 这张卡比 YouTube 卡多出来的都是「站内播放」这一侧的事：
   * - 整张卡是一个 `<a href="/video/:id?back=...">`（YouTube 卡是弹窗，所以它用按钮）；
   * - 悬停放预览片（`previewUrl`），滚动中/隐私模式下不放；
   * - 下载走 `DownloadDialog`（选画质），不是后台直接排队。
   */
  import CardByline from './CardByline.svelte';
  import CardDownloadButton from './CardDownloadButton.svelte';
  import CardDuration from './CardDuration.svelte';
  import CoverArt from './CoverArt.svelte';
  import DownloadDialog from './DownloadDialog.svelte';
  import { api } from '../lib/api';
  import { router } from '../lib/router.svelte';
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

  /**
   * 点进影片时把「现在这一页」记进链接：`/video/62?back=%2Fbilibili%3Fup%3D10`。
   * 详情页的「返回」就用它——从 B 站墙（带 ?up= / ?mode=）点进去，退回来还是同一个视角。
   * 影片页里的「相关影片」不加（否则会链回自己）。
   */
  const backQuery = $derived(
    router.path.startsWith('/video/')
      ? ''
      : `?back=${encodeURIComponent(`${router.path}${router.search}`)}`,
  );

  let previewOn = $state(false);
  let previewEl = $state<HTMLVideoElement | null>(null);
  let previewTimer = 0;
  let downloadOpen = $state(false);
  // 预览片的签名会过期（抓取时写死的 URL），失效一次就别再反复拉：
  // 每次悬停都去请求一个必然失败的地址，纯属浪费带宽和解码。
  let previewBroken = $state(false);

  // `w=` 只是「希望多大」的意图参数，不会真的缩图：`/api/img` 把它交给图床自己认，
  // 而现有四个站里只有 beeg 的图床认它（实测 bilibili 封面在 w=480 与 w=640 下取回的是
  // 同一份 960×540 / 61,592 字节的 webp；YouTube 的 i.ytimg 同理）。所以保持 480 不动——
  // 改成 640 对像素和字节都没影响，只会让 beeg 多拉一档大图。
  const thumb = $derived(imageUrl(video.thumbUrl, 480));
  const performerLine = $derived(video.performers.map((p) => p.name).join('、'));
  const firstPerformer = $derived(video.performers[0] ?? null);
  // 头像表里存的是后端代理地址，也要过一遍 imageUrl：隐私模式下它得换成小糊图
  // 优先用这一行自带的头像（videos 接口现在会跟着 performers 一起给，B 站墙离不开它：
  // 「订阅艺人头像表」只拉得到非 B 站的艺人），没有再退回全局头像表。
  const avatar = $derived(
    imageUrl(
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
    href={`/video/${String(video.id)}${backQuery}`}
    data-link
    class="block"
    aria-label={`${video.title}，时长 ${formatDuration(video.durationSec)}`}
    onpointerenter={startPreview}
    onpointerdown={prefetchPlayback}
    onpointerleave={stopPreview}
    onfocusin={prefetchPlayback}
  >
    <div class="relative aspect-video overflow-hidden rounded-xl bg-panel">
      <CoverArt src={thumb} {eager} scaleClass="transition-transform duration-300 group-hover:scale-[1.02]" />

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

      <CardDuration seconds={video.durationSec} />
    </div>

    <CardByline
      title={video.title}
      name={performerLine || '未知艺人'}
      avatarUrl={avatar}
      {monogram}
      views={video.viewCount}
      viewsLabel="次播放"
      dateLabel={formatDate(video.publishedAt)}
      dateTitle={relativeTime(video.publishedAt)}
    />
  </a>

  <CardDownloadButton
    label={`下载 ${video.title}`}
    title="下载到本地库"
    onclick={openDownload}
  />

  <DownloadDialog videoId={video.id} open={downloadOpen} onClose={() => (downloadOpen = false)} />
</article>
