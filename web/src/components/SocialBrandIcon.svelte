<script lang="ts">
  import officialIcon from '../assets/social/official.png';
  import instagramIcon from '../assets/social/instagram.png';
  import xIcon from '../assets/social/twitter.png';
  import type { SocialBrand } from '../lib/socialBrand';

  interface Props {
    kind: SocialBrand;
  }

  let { kind }: Props = $props();

  /* 用户给的三枚品牌标（OnlyFans 蓝标 / X 方牌 / Instagram 彩色方牌）。品牌标本来就不是
     单色，硬压成我们那套线稿会认不出来，所以这里放原色图。 */
  const MARKS: Partial<Record<SocialBrand, string>> = {
    official: officialIcon,
    x: xIcon,
    instagram: instagramIcon,
  };
</script>

{#if MARKS[kind]}
  <!-- X 那枚原图是「黑色方牌挖空一个 X」：直接放上去等于一块比画布还黑的方块，在 #020408 上
       根本看不出形状，所以只对这一个做反相（黑牌变白牌、X 变黑）。

       尺寸按**墨迹**对齐，不按画布：三张 PNG 都是 48/50 见方、四周留白各不相同
       （2026-11-04 在 `/performer/1` 上量的墨迹占比 —— OnlyFans 横向 100%、X 84%、
       Instagram 只有 75%）。给同一个 22px 画布时，X 的图案 18.48px、Instagram 只有
       16.50px，所以 IG 看着小一圈。IG 单独给 26px 画布 → 19.5px 墨迹，比 X 的 18.48px
       略大一档：IG 是圆角描边（墨迹像素占比 54.6%），X 是实心方牌（65.8%），同样大小下
       描边那个一定显小，得找回来一点。 -->
  <img
    src={MARKS[kind]}
    alt=""
    width="48"
    height="48"
    class="{kind === 'instagram' ? 'h-[26px] w-[26px]' : 'h-[22px] w-[22px]'} {kind === 'x'
      ? 'invert'
      : ''}"
    loading="lazy"
    decoding="async"
  />
{:else}
  <!-- 认不出的域名退回地球线稿（22px 画布：这枚线稿的墨迹只占画布 71%，比品牌标偏小）。 -->
  <svg
    viewBox="0 0 24 24"
    class="h-[22px] w-[22px] text-muted"
    aria-hidden="true"
    fill="none"
    stroke="currentColor"
    stroke-width="1.8"
    stroke-linecap="round"
    stroke-linejoin="round"
  >
    <circle cx="12" cy="12" r="8.5" />
    <path d="M3.5 12h17" />
    <path d="M12 3.5c2.3 2.4 3.5 5.3 3.5 8.5S14.3 18.1 12 20.5c-2.3-2.4-3.5-5.3-3.5-8.5S9.7 5.9 12 3.5Z" />
  </svg>
{/if}
