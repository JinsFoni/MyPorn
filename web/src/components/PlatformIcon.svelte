<script lang="ts">
  import pornhubIcon from '../assets/platform/pornhub.png';
  import xhamsterIcon from '../assets/platform/xhamster.png';
  import beegIcon from '../assets/platform/beeg.svg';

  interface Props {
    /** 后端 platform 字段（pornhub / xhamster / beeg …） */
    platform: string;
    /** 平台显示名，只用于 alt / title / 读屏，不显示成文字 */
    label: string;
    /**
     * 三档，按「和它并排的文字有多大」选：
     * md = 32px 底座（本来给 28px 标题用；艺人页那个 28px 名字现改用 `sm`，跟名字同一行、中线对齐）、
     * sm = 24px（14px 名字旁、艺人页的 28px 名字旁）、xs = 20px（12px 元信息行里）
     */
    size?: 'md' | 'sm' | 'xs';
    /**
     * 传了就变成外链：点图标直接开平台主页（艺人页就是这么用的，
     * 省掉一行「平台主页」按钮）。不传就还是纯图标。
     */
    href?: string | null;
    class?: string;
  }

  let { platform, label, size = 'md', href = null, class: className = '' }: Props = $props();

  /* 直接放各家自己的 app icon（方形小图标），不做单色化：
     pornhub = 橙底黑 PH（从站方 apple-touch-icon 抠出来的橙色方块）
     xhamster = 深色方砖 + 白色仓鼠头（站方 180px touch icon）
     beeg = 自带渐变底 + 白色字标的 favicon.svg
     都缩到 64px 左右。这也是 peach 那边角标里装的东西（它取各站 favicon），
     两边差的是外面托底那一圈玻璃，不是图本身。 */
  const ICONS: Record<string, string> = {
    pornhub: pornhubIcon,
    xhamster: xhamsterIcon,
    beeg: beegIcon,
  };

  let icon = $derived(ICONS[platform.toLowerCase()]);

  // 托底照 peach 影片卡片左上角那枚来源角标（`[data-media-badge]`）：
  // 暗色半透明玻璃 + 只留顶部一道 1px 内高光 + 一圈软投影，四周不画白描边。
  // peach 的填充是深色（暗色主题 rgba(24,27,33,.24)），压在封面上就够；我们的底色是 #020408，
  // 照抄会整块看不见，所以 alpha 抬到 0.58、再补两道投影把边描出来。
  // 圆角按 peach 的比例折算（26px 底座 10px 圆角 ≈ 0.385）：32→12、24→9、20→8。
  // 图不再自己切圆角、改 `object-contain`：peach 的角标只由底座圆，里面的方块是直角。
  const PLATE: Record<'md' | 'sm' | 'xs', string> = {
    md: 'h-8 w-8 rounded-xl',
    sm: 'h-6 w-6 rounded-[0.5625rem]',
    xs: 'h-5 w-5 rounded-lg',
  };
  const IMG: Record<'md' | 'sm' | 'xs', string> = {
    md: 'h-6 w-6',
    sm: 'h-4 w-4',
    xs: 'h-3 w-3',
  };
</script>

{#snippet plate(extra: string)}
  <!-- 玻璃托底：暗色半透明 + 顶部内高光 + 软投影（peach 角标那一套）。blur 留着是为了
       压在封面/头图上也成立，深色画布上没有内容可糊时靠那层填充和投影撑住分量。 -->
  <span
    class="inline-flex shrink-0 items-center justify-center bg-[rgba(26,30,37,0.58)] shadow-[inset_0_1px_0_rgba(255,255,255,0.16),0_1px_2px_rgba(0,0,0,0.4),0_10px_20px_rgba(0,0,0,0.3)] backdrop-blur-xl backdrop-saturate-[1.45] transition-colors duration-150 group-hover:bg-[rgba(42,48,58,0.72)] {PLATE[
      size
    ]} {extra}"
  >
    <img
      src={icon}
      alt={label}
      class="{IMG[size]} object-contain"
      loading="lazy"
      decoding="async"
    />
  </span>
{/snippet}

{#if icon}
  {#if href}
    <!-- 图标本身就是「去平台主页」的入口：悬停时底座提亮一档，明确它是可点的。 -->
    <a
      {href}
      target="_blank"
      rel="noreferrer noopener"
      title="打开{label}主页"
      aria-label="打开{label}主页"
      class="group inline-flex outline-none focus-visible:[&>span]:ring-2 focus-visible:[&>span]:ring-white/45"
    >
      {@render plate(className)}
    </a>
  {:else}
    {@render plate(className)}
  {/if}
{:else if href}
  <a {href} target="_blank" rel="noreferrer noopener" class="chip transition-colors hover:text-fg" title="打开{label}主页"
    >{label}</a
  >
{:else}
  <!-- 不认识的平台回退成原来那个文字药丸 -->
  <span class="chip {className}">{label}</span>
{/if}
