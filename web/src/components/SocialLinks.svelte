<script lang="ts">
  import SocialBrandIcon from './SocialBrandIcon.svelte';
  import { socialBrandOf } from '../lib/socialBrand';
  import type { SocialLink } from '../lib/types';

  interface Props {
    /** 站外链接（官方网址 / Twitter / Instagram …） */
    social: SocialLink[];
    /** 外层布局用的类（比如在简介那一行里靠右） */
    class?: string;
  }

  let { social, class: className = '' }: Props = $props();

  /* 只有图标、不带文字。名字不消失，退到 `title` 和 `aria-label` 里——鼠标悬停看得见，
     读屏也读得到，只是不再占版面。品牌图标本体（含「按墨迹对齐尺寸」那套）在
     `SocialBrandIcon.svelte`，动态页的平台筛选也用同一个。 */
</script>

{#if social.length}
  <!-- `-my-1.5`：每个圆底座 32px、图标 22px，比旁边 20px 的正文行高一圈；上下各收 6px，
       图标中线才落在正文第一行的中线上（那一行是 `items-start` 排的）。 -->
  <ul class="flex flex-wrap items-center gap-2 -my-1.5 {className}">
    {#each social as link (link.url)}
      <li>
        <!-- 外链一律新窗口 + noopener：这些地址来自上游页面，不能让它拿到 window.opener。
             底座 32px 见方：悬停只给一层白雾，不改图标本身。 -->
        <a
          href={link.url}
          target="_blank"
          rel="noreferrer noopener"
          title={link.label}
          aria-label={link.label}
          class="grid h-8 w-8 place-items-center rounded-full transition-colors hover:bg-white/10"
        >
          <SocialBrandIcon kind={socialBrandOf(link.url, link.label)} />
        </a>
      </li>
    {/each}
  </ul>
{/if}
