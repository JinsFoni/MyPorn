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

  /* 直接放各家自己的 app icon（方形圆角小图标），不做单色化：
     pornhub = 橙底黑 PH（从站方 apple-touch-icon 抠出来的橙色方块）
     xhamster = 深色方砖 + 白色仓鼠头（站方 180px touch icon）
     beeg = 自带渐变底 + 白色字标的 favicon.svg
     都缩到 64px 左右。 */
  const ICONS: Record<string, string> = {
    pornhub: pornhubIcon,
    xhamster: xhamsterIcon,
    beeg: beegIcon,
  };

  let icon = $derived(ICONS[platform.toLowerCase()]);

  // 底座比图标每边大 4px，留出那圈玻璃；圆角按「外圆角 − 边距」取，
  // 内外两圈同心，看着才是一块玻璃托着一块牌。
  const PLATE: Record<'md' | 'sm' | 'xs', string> = {
    md: 'h-8 w-8 rounded-xl',
    sm: 'h-6 w-6 rounded-[0.625rem]',
    xs: 'h-5 w-5 rounded-[0.375rem]',
  };
  // 图标比底座每边只小 3px：这几家的 logo 自带留白（beeg 的字标、xHamster 的仓鼠头
  // 在原图里都只占七成），再按「底座 − 8px」留白缩下去，16px 下就只剩一条细线了。
  const IMG: Record<'md' | 'sm' | 'xs', string> = {
    md: 'h-[1.625rem] w-[1.625rem]',
    sm: 'h-[1.125rem] w-[1.125rem]',
    xs: 'h-[0.875rem] w-[0.875rem]',
  };
</script>

{#snippet plate(extra: string)}
  <!-- 毛玻璃底座：深色底上 backdrop-blur 没有内容可糊，质感靠半透明白底 + 顶部一道内高光叠出来，
       blur 留着是为了图标将来压在封面/图片上时也成立。四周不描边——那圈白线画上去像个按钮，
       而且底座自己比图标亮一档，边已经由明暗差给出来了。 -->
  <span
    class="inline-flex shrink-0 items-center justify-center bg-gradient-to-b from-white/[0.13] to-white/[0.05] shadow-[inset_0_1px_0_rgba(255,255,255,0.14)] backdrop-blur-xl transition-[background-color,box-shadow] duration-150 group-hover:from-white/[0.22] group-hover:to-white/[0.12] {PLATE[
      size
    ]} {extra}"
  >
    <img
      src={icon}
      alt={label}
      class="{IMG[size]} rounded-[22%] object-cover"
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
