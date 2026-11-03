<script lang="ts">
  /**
   * 媒体库的卡片：**和关注流 / B 站 / YouTube 那三条墙同一套模板**
   * （`CoverArt` 封面 + `CardDuration` 右下时长 + `CardByline` 圆头像/一行标题/副行
   * + `CardDownloadButton` 左下角悬停动作）。要改卡片长相就改那几个组件，别在这里单独调。
   *
   * 这张卡和三条墙的差别只有「它代表的是磁盘上的一个文件」：
   * - 圆头像和副行名字是**频道 / UP 主**（和三条墙同一位置放同一件事）：平台影片取第一位
   *   艺人、YouTube 影片取频道（服务端已代理成 /api/img）；两样都没有才退回库名与首字母。
   *   只有一个库时副行只写体积，多库时才在体积前带上库名。
   * - 封面优先用下载时落盘的 thumb.jpg（`localThumbUrl`），远端封面地址过期也照样有图；
   *   隐私模式下**必须**换回 /api/img 的小糊图——本地那张是按原尺寸发的，没有糊化那一层。
   * - 封面左上角是 `2160p`（落盘时 probe 的尺寸）：三条墙不知道分辨率所以没有这一格，
   *   而这一页的每一行都带着分辨率，是「值不值得留」的依据之一。
   * - 左下角那颗按钮是**复制文件路径**（已经下到盘上了，再下一次没有意义），
   *   用的还是 `CardDownloadButton`，只换图标。
   * - 文件被挪走 / 删掉时整条压暗、盖一条砖红色的「文件已不在磁盘上」（和 YouTube 卡的
   *   「直播」标同一个位置同一个做法），链接直接不渲染——点过去只会 404。
   */
  import CardByline from './CardByline.svelte';
  import CardDownloadButton from './CardDownloadButton.svelte';
  import CardDuration from './CardDuration.svelte';
  import CoverArt from './CoverArt.svelte';
  import { formatBytes, formatDate, relativeTime } from '../lib/format';
  import { imageUrl } from '../lib/img';
  import { live } from '../lib/live.svelte';
  import { privacy } from '../lib/privacy.svelte';
  import { router } from '../lib/router.svelte';
  import type { LibraryVideo } from '../lib/types';

  interface Props {
    item: LibraryVideo;
    /** 有多个库时才在副行写库名（只有一个库时那是噪声，和三条墙的副行形状也一样） */
    showLibrary?: boolean;
  }

  let { item, showLibrary = false }: Props = $props();

  /** 「2160p」：只写高度，和播放器画质菜单同一套叫法；尺寸未知就不画 */
  const quality = $derived(item.height > 0 ? `${String(item.height)}p` : '');

  /**
   * 封面：本地那张只有「不是隐私模式」时才用。
   * `imageUrl(localThumbUrl)` 会原样返回（站内相对路径），所以这里要显式判断隐私模式。
   */
  const cover = $derived(
    privacy.on || !item.localThumbUrl ? imageUrl(item.thumbUrl, 480) : item.localThumbUrl,
  );

  const back = $derived(`?back=${encodeURIComponent(`${router.path}${router.search}`)}`);

  /**
   * 副行左边的名字 = 频道 / UP 主（和三条墙同一个位置放同一件事）；没有就退回库名。
   * 头像同理走 `imageUrl`——隐私模式下它得换成小糊图，本地/远端一视同仁。
   */
  const whoName = $derived(item.channelName || item.libraryName);
  const avatar = $derived(imageUrl(item.avatarUrl, 36));
  const avatarMonogram = $derived(whoName.trim().charAt(0).toUpperCase());
  /**
   * 体积前面要不要带库名：只有「副行名字显示的是频道」时才有意义（多库时才知道这一行在哪个盘上）；
   * 名字位本来就退化成库名的行再写一遍就成了「副库 副库 · 907 KB」。
   */
  const metaLine = $derived(
    showLibrary && item.channelName
      ? `${item.libraryName} · ${formatBytes(item.sizeBytes)}`
      : formatBytes(item.sizeBytes),
  );

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

<article class="group relative min-w-0 [contain-intrinsic-size:auto_16rem] [content-visibility:auto]">
  <a
    href={item.missing ? undefined : `/video/${String(item.videoId)}${back}`}
    data-link
    class="block"
    class:pointer-events-none={item.missing}
    aria-label={`${item.title}，${quality || '尺寸未知'}，${formatBytes(item.sizeBytes)}`}
  >
    <div class="relative aspect-video overflow-hidden rounded-xl bg-panel">
      <!-- CoverArt 自己是「铺满父容器」的一层，压暗只能套在外面（它没有 class 入参） -->
      <div class="absolute inset-0 {item.missing ? 'opacity-40' : ''}">
        <CoverArt src={cover} scaleClass="transition-transform duration-300 group-hover:scale-[1.02]" />
      </div>

      {#if quality}
        <span
          class="pointer-events-none absolute left-2 top-2 rounded bg-ink/70 px-1.5 py-0.5 text-[0.6875rem] font-medium tabular-nums text-fg"
        >
          {quality}
        </span>
      {/if}

      <CardDuration seconds={item.durationSec} />

      {#if item.missing}
        <!-- 左下角让给「复制路径」按钮（`CardDownloadButton`，`left-2` 起 28px）：右移 40px -->
        <span
          class="absolute bottom-2 left-10 rounded bg-brick/90 px-1.5 py-0.5 text-[0.6875rem] font-medium text-fg"
        >
          文件已不在磁盘上
        </span>
      {/if}
    </div>

    <CardByline
      title={item.title}
      name={whoName}
      avatarUrl={avatar}
      monogram={avatarMonogram}
      meta={metaLine}
      dateLabel={formatDate(item.downloadedAt)}
      dateTitle={relativeTime(item.downloadedAt)}
    />
  </a>

  <CardDownloadButton
    label={`复制 ${item.fileName} 的路径`}
    title={copied ? '已复制' : '复制文件路径'}
    icon={copied ? 'check' : 'copy'}
    onclick={(event) => void copyPath(event)}
  />
</article>
