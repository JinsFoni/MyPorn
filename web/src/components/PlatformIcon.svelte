<script lang="ts">
  import pornhubIcon from '../assets/platform/pornhub.png';
  import xhamsterIcon from '../assets/platform/xhamster.png';
  import beegIcon from '../assets/platform/beeg.svg';

  interface Props {
    /** 后端 platform 字段（pornhub / xhamster / beeg …） */
    platform: string;
    /** 平台显示名，只用于 alt / title / 读屏，不显示成文字 */
    label: string;
    class?: string;
  }

  let { platform, label, class: className = '' }: Props = $props();

  /* 直接放各家自己的 app icon（方形圆角小图标），不做单色化：
     pornhub = 橙底黑 PH（从站方 apple-touch-icon 抠出来的橙色方块）
     xhamster = 深色方砖 + 白色仓鼠头（站方 180px touch icon）
     beeg = 自带渐变底 + 白色字标的 favicon.svg
     都缩到 64px 左右，显示尺寸 24px（跟原来的文字药丸同高，行布局不位移）。 */
  const ICONS: Record<string, string> = {
    pornhub: pornhubIcon,
    xhamster: xhamsterIcon,
    beeg: beegIcon,
  };

  let icon = $derived(ICONS[platform.toLowerCase()]);
</script>

{#if icon}
  <img
    src={icon}
    alt={label}
    title={label}
    class="h-6 w-6 shrink-0 rounded-[22%] object-cover {className}"
    loading="lazy"
    decoding="async"
  />
{:else}
  <!-- 不认识的平台回退成原来那个文字药丸 -->
  <span class="chip {className}">{label}</span>
{/if}
