<script lang="ts">
  /**
   * 卡片封面下面那一段：36px 圆头像 + 单行标题 + 副行（名字 ▷ 观看数 日期）。
   *
   * 三条墙（关注流 / B 站 / YouTube）和媒体库共用这一个组件，样式口径以 YouTube 卡（YtCard）为准，
   * 理由都是量出来的：
   * - 头像 36px（`h-9 w-9`，imageUrl 也按 36 取，DPR2 下 72px 才不糊）。24px 那版既小、
   *   又和隔壁墙的卡片对不齐。
   * - 标题**一行**截断。两行标题会把整张卡撑高，而第二行大半是 `#标签`（YouTube 实测
   *   `/youtube` 首屏 36 张里 21 张带）。原始标题仍在 `title` 悬停提示里，一个字没丢。
   * - 副行里名字吃剩下的宽度并截断，观看数和日期 `shrink-0`——以前整行一个 `truncate`，
   *   先被截掉的偏偏是日期。
   * - 观看数画成「▷ 679」而不是「679 次观看」：副行只有 12px，三个中文字占掉一格宽度、
   *   还是把日期挤出去了；空心三角只描边不填色，和旁边 12px 的字一个重量。
   *   读屏仍念「679 次观看」（aria-label）。
   * - 媒体库那张卡在副行中间多一格**体积**（`meta`）：那一页每行都是磁盘上的一个文件，
   *   体积和日期一样是「判断要不要留」的依据；没有另开一套副行样式，多出来的就是这一段。
   * - 三段之间**只靠 8px 间距分隔，不画「·」**（2026-11-02 用户要求去掉那颗点：
   *   它夹在两串数字中间，比两边的字还抢眼）。原来那里是 `gap-1.5` + 一颗 4px 的点，
   *   间距等于 6+4+6；现在统一成 `gap-x-2`，名字与计数、计数与日期都是 8px。
   */
  import type { Snippet } from 'svelte';
  import { formatCount } from '../lib/format';

  interface Props {
    /** 卡片上显示的那一行标题（YouTube 卡会把末尾的 `#标签` 削掉） */
    title: string;
    /** 悬停提示里的标题：默认同 `title`，削过标签的卡要传原始标题，一个字都不丢 */
    titleHint?: string;
    /** 副行左边：频道名 / 艺人名 */
    name: string;
    /** 已经过代理的头像地址；为空就不画图，退化到首字母 */
    avatarUrl?: string | null;
    /** 头像兜底：名字首字母（空着就留一个空圆，不留白洞） */
    monogram?: string;
    /** 观看数；0 / null 就整段不画（连它前面那段间距一起，日期直接贴到名字后面） */
    views?: number | null;
    /** 量词，读屏和悬停提示用：B 站「次播放」、YouTube「次观看」 */
    viewsLabel?: string;
    /**
     * 副行里额外一段文字（媒体库用它放体积，如 `391 MB`）。有 `views` 就排在计数后面，
     * 否则直接接在名字后面——三段共用同一套 12px 灰字与 8px 间距。
     */
    meta?: string;
    /** 副行右边：绝对日期（2026-10-31），不是「1个月前」——卡片墙要能一眼比先后 */
    dateLabel: string;
    /** 日期悬停提示（相对时间之类） */
    dateTitle?: string;
    /** 给了就把标题画成按钮（弹窗式卡片）；不给就是 `<h3>`（整张卡是一个链接） */
    onTitleClick?: () => void;
    /** 行尾标记（收藏星之类） */
    trailing?: Snippet;
  }

  let {
    title,
    titleHint = undefined,
    name,
    avatarUrl = null,
    monogram = '',
    views = null,
    viewsLabel = '次观看',
    meta = '',
    dateLabel,
    dateTitle = undefined,
    onTitleClick = undefined,
    trailing = undefined,
  }: Props = $props();

  // 头像挂了就退回首字母。记的是「哪一条 URL 挂了」而不是一个布尔：换卡片（列表复用节点）
  // 或换了地址要自动恢复，不然一条坏头像会让后面所有卡片都只剩首字母。
  let brokenAvatar = $state<string | null>(null);
  const showAvatar = $derived(!!avatarUrl && brokenAvatar !== avatarUrl);
  const count = $derived(views && views > 0 ? formatCount(views) : '');

  // 悬浮时**不加下划线**（2026-11-03 用户要求）：卡片整块都是可点的，
  // 标题下面那条线一出现就把「标题」变成「链接」，卡片看着像普通网页链接列表。
  const TITLE = 'block w-full truncate text-left text-ui font-medium leading-snug text-fg';
</script>

<div class="mt-2.5 flex items-start gap-2.5">
  <span
    class="grid h-9 w-9 shrink-0 place-items-center overflow-hidden rounded-full bg-raise text-meta text-dim"
    aria-hidden="true"
  >
    {#if showAvatar}
      <img
        data-media
        src={avatarUrl}
        alt=""
        width="36"
        height="36"
        loading="lazy"
        decoding="async"
        class="h-full w-full object-cover"
        onerror={() => (brokenAvatar = avatarUrl)}
      />
    {:else if monogram}
      {monogram}
    {/if}
  </span>

  <div class="min-w-0 flex-1">
    {#if onTitleClick}
      <button type="button" class={TITLE} title={titleHint ?? title} onclick={onTitleClick}>{title}</button>
    {:else}
      <h3 class={TITLE} title={titleHint ?? title}>{title}</h3>
    {/if}

    <p class="mt-1 flex items-center gap-x-2 text-meta text-dim">
      <span class="truncate">{name}</span>
      {#if count}
        <span
          class="flex shrink-0 items-center gap-1"
          title={`${count} ${viewsLabel}`}
          aria-label={`${count} ${viewsLabel}`}
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
          <span class="tabular-nums">{count}</span>
        </span>
      {/if}
      {#if meta}
        <span class="shrink-0 tabular-nums">{meta}</span>
      {/if}
      <span class="shrink-0" title={dateTitle}>{dateLabel}</span>
    </p>
  </div>

  {#if trailing}
    {@render trailing()}
  {/if}
</div>
