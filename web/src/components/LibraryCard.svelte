<script lang="ts">
  /**
   * 媒体库的卡片：**和关注流 / B 站 / YouTube 那三条墙同一套模板**
   * （`CoverArt` 封面 + `CardDuration` 右下时长 + `CardByline` 圆头像/一行标题/副行）。
   * 要改卡片长相就改那几个组件，别在这里单独调。
   *
   * 2026-11-04 用户要求「媒体库视频卡片的复制按钮去掉」：这一页**没有悬停动作按钮**了
   * （`CardDownloadButton` 只在三条墙上用）。理由站得住：这一页的东西都已经在盘上，
   * 复制路径是个极低频的小工具，却占着封面左下角、还得跟分辨率角标 / 缺失提示抢地方。
   *
   * 这张卡和三条墙的差别只有「它代表的是磁盘上的一个文件」：
   * - 圆头像和副行名字是**频道 / UP 主**（和三条墙同一位置放同一件事）：平台影片取第一位
   *   艺人、YouTube 影片取频道（服务端已代理成 /api/img）；两样都没有才退回库名与首字母。
   *   只有一个库时副行只写体积，多库时才在体积前带上库名。
   * - 封面优先用下载时落盘的 thumb.jpg（`localThumbUrl`），远端封面地址过期也照样有图；
   *   隐私模式下**必须**换回 /api/img 的小糊图——本地那张是按原尺寸发的，没有糊化那一层。
   * - 封面**左下角**是 `2160p`（落盘时 probe 的尺寸）：三条墙不知道分辨率所以没有这一格，
   *   而这一页的每一行都带着分辨率，是「值不值得留」的依据之一。
   *   2026-11-04 用户要求「分辨率标识放在左下角，不要显示黑色背景」，从左上角搬到这里，
   *   同时去掉 `bg-ink/70` 药丸底——改成和右下角时长完全一致的写法
   *   （`text-meta` 500 + `tabular-nums` + 三层 `text-shadow`），同一行两端一个样。
   * - 文件被挪走 / 删掉时整条压暗、**右上角**盖一条砖红色的「文件已不在磁盘上」，
   *   链接直接不渲染——点过去只会 404。（原来在左下角，得给复制按钮让位；
   *   复制按钮去掉后左下角归分辨率角标，提示挪到空出来的右上角，谁都不用挤。）
   */
  import CardByline from './CardByline.svelte';
  import CardDuration from './CardDuration.svelte';
  import CoverArt from './CoverArt.svelte';
  import { formatBytes, formatDate, relativeTime } from '../lib/format';
  import { imageUrl } from '../lib/img';
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
        <!-- 左下角、不带药丸底：写法抄 `CardDuration`（右下一模一样的字号/字重/三层阴影） -->
        <span
          class="pointer-events-none absolute bottom-2 left-2 text-meta font-medium tabular-nums text-fg [text-shadow:0_1px_5px_rgba(0,0,0,0.8),0_0_12px_rgba(0,0,0,0.7),0_0_3px_rgba(0,0,0,0.8)]"
        >
          {quality}
        </span>
      {/if}

      <CardDuration seconds={item.durationSec} />

      {#if item.missing}
        <!-- 左下角归分辨率角标、右下角归时长，警告放空出来的右上角 -->
        <span
          class="absolute right-2 top-2 rounded bg-brick/90 px-1.5 py-0.5 text-[0.6875rem] font-medium text-fg"
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
</article>
