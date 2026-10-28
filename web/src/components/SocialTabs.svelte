<script lang="ts">
  /**
   * 「动态」与「YouTube」共用的顶部切换。
   *
   * 为什么做成常驻的分段控件而不是再加一个侧边栏条目：移动端底部只有 6 格
   * （见 AppShell 的注释），第 7 个会把每格挤到 45px。这两个页面都是「订阅的内容流」，
   * 属于同一件事的两种形态，用同一个入口 + 顶部切换比塞进底部导航更清楚。
   *
   * 现在只有 `/moments` 用它（`active="moments"`）：`/youtube` 那边去掉了。
   * 切换只留在入口这一侧，进了 YouTube 就不必再把「动态」摆出来跟它并排。
   */
  import { live } from '../lib/live.svelte';

  interface Props {
    active: 'moments' | 'youtube';
  }

  let { active }: Props = $props();

  const ITEMS = [
    { key: 'moments', label: '动态', href: '/moments' },
    { key: 'youtube', label: 'YouTube', href: '/youtube' },
  ] as const;

  function badgeFor(key: 'moments' | 'youtube'): number {
    return key === 'moments' ? live.momentsUnseen : live.ytUnseen;
  }
</script>

<nav class="inline-flex shrink-0 rounded-full bg-panel p-0.5" aria-label="内容流切换">
  {#each ITEMS as item (item.key)}
    {@const badge = badgeFor(item.key)}
    <a
      href={item.href}
      data-link
      aria-current={active === item.key ? 'page' : undefined}
      class="flex items-center gap-1.5 rounded-full px-3.5 py-1.5 text-meta font-medium transition-colors
        {active === item.key ? 'bg-raise text-fg' : 'text-muted hover:text-fg'}"
    >
      {item.label}
      {#if badge > 0}
        <span class="min-w-[1.125rem] rounded-full bg-hi px-1 text-center text-[0.625rem] font-semibold leading-[1.125rem] text-ink tabular-nums">
          {badge > 99 ? '99+' : badge}
        </span>
      {/if}
    </a>
  {/each}
</nav>
