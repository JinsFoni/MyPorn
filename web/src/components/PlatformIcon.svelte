<script lang="ts">
  import pornhubIcon from '../assets/platform/pornhub.png';
  import xhamsterIcon from '../assets/platform/xhamster.png';
  import beegIcon from '../assets/platform/beeg.svg';

  interface Props {
    /** 后端 platform 字段（pornhub / xhamster / beeg …） */
    platform: string;
    /** 平台显示名，只用于 alt / title / 读屏，不显示成文字 */
    label: string;
    /** md = 32px 底座（标题旁）；sm = 20px（12px 元信息行里用，跟文字同高） */
    size?: 'md' | 'sm';
    class?: string;
  }

  let { platform, label, size = 'md', class: className = '' }: Props = $props();

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

  // 底座留 4px（md）/ 3px（sm）边给玻璃圈透出来；两档的圆角都按「外圆角 − 边距」取，
  // 内外两圈同心，看着才是一块玻璃托着一块牌。
  const PLATE: Record<'md' | 'sm', string> = {
    md: 'h-8 w-8 rounded-[0.625rem]',
    sm: 'h-5 w-5 rounded-[0.3125rem]',
  };
  const IMG: Record<'md' | 'sm', string> = {
    md: 'h-6 w-6',
    sm: 'h-3.5 w-3.5',
  };
</script>

{#if icon}
  <!-- 毛玻璃底座：深色底上 backdrop-blur 没有内容可糊，质感靠半透明白底 + 内描边
       + 顶部一道内高光叠出来，blur 留着是为了图标将来压在封面/图片上时也成立。 -->
  <span
    class="inline-flex shrink-0 items-center justify-center bg-gradient-to-b from-white/[0.13] to-white/[0.05] ring-1 ring-inset ring-white/15 shadow-[inset_0_1px_0_rgba(255,255,255,0.14)] backdrop-blur-xl {PLATE[
      size
    ]} {className}"
  >
    <img
      src={icon}
      alt={label}
      title={label}
      class="{IMG[size]} rounded-[22%] object-cover"
      loading="lazy"
      decoding="async"
    />
  </span>
{:else}
  <!-- 不认识的平台回退成原来那个文字药丸 -->
  <span class="chip {className}">{label}</span>
{/if}
