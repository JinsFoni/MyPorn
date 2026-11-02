<script lang="ts">
  import { imageUrl } from '../lib/img';
  import type { RailItem } from '../lib/rail';

  /**
   * 头像条：横向滚动的圆头像＝**过滤器**（关注页 / B 站页 / YouTube 页共用一套）。
   *
   * 口径来自关注页（2026-11-02）：72px 圆头像（`h-18 w-18`）+ 96px 宽的姓名
   * （`text-meta`，单行截断），**不显示任何计数**；点头像＝把 `id` 交回去，由页面写地址栏；
   * 选中态是头像外 2px 白环 + 姓名 `font-medium`，再点同一个取消。
   *
   * 容器 `flex gap-3 overflow-x-auto no-scrollbar`，用 `-mx-3 px-3 sm:-mx-4 sm:px-4 lg:mx-0 lg:px-0`
   * 做贴边滚动，首列头像与内容左边缘（桌面 296 / 移动 12px）对齐。
   * 首屏给同尺寸灰圆占位，避免加载完成时整页跳动。
   *
   * 这个组件只管画：谁来、怎么拉由页面决定（关注页是 PerformerRail，另两页各自有频道列表）。
   */
  interface Props {
    items: RailItem[];
    /** 无障碍标签：「按艺人筛选」/「按 UP 主筛选」/「按频道筛选」 */
    label: string;
    /** 当前选中的 id（地址栏是唯一真源，这里只负责高亮） */
    selected?: number | null;
    /** 点一个头像：选中就回 id，再点一次回 null（取消筛选） */
    onSelect?: (id: number | null) => void;
    /** 列表还在路上：画同样数量的灰圆占位 */
    loading?: boolean;
  }

  let { items, label, selected = null, onSelect, loading = false }: Props = $props();

  /** 头像会过期（平台 CDN 的签名/防盗链），失败的那一张退回首字母，别再反复拉 */
  let failed = $state<Record<number, boolean>>({});
  let listEl = $state<HTMLUListElement | null>(null);

  function pick(id: number): void {
    onSelect?.(selected === id ? null : id);
  }

  /**
   * 选中项滚进视野。深链接进来（`/bilibili?up=89`、`/?performer=38`、复制给别人）时，
   * 被筛的那位可能排在第四五行、已经滚出右边——只画白环的话，用户看到的是「筛了但不知道筛了谁」。
   * 用 rect 差算 scrollLeft 而不是 `scrollIntoView()`：后者会连整页一起竖着滚一下。
   */
  $effect(() => {
    const el = listEl;
    const id = selected;
    if (!el || id === null) return;
    const btn = el.querySelector<HTMLElement>(`[data-rail-id="${String(id)}"]`);
    if (!btn) return;
    const rect = btn.getBoundingClientRect();
    const box = el.getBoundingClientRect();
    if (rect.left < box.left) el.scrollLeft += rect.left - box.left - 12;
    else if (rect.right > box.right) el.scrollLeft += rect.right - box.right + 12;
  });
</script>

{#if items.length > 0}
  <nav class="mb-4 lg:mb-5" aria-label={label}>
    <ul
      bind:this={listEl}
      class="no-scrollbar -mx-3 flex gap-3 overflow-x-auto px-3 pb-1 sm:-mx-4 sm:px-4 lg:mx-0 lg:px-0"
    >
      {#each items as item (item.id)}
        <li class="shrink-0">
          <button
            type="button"
            onclick={() => pick(item.id)}
            data-rail-id={item.id}
            aria-pressed={selected === item.id}
            class="group flex w-24 flex-col items-start gap-1.5 text-left"
            title={selected === item.id
              ? `${item.title ?? item.name}（再点一次看全部）`
              : (item.title ?? item.name)}
          >
            <span class="relative block">
              <span
                class="block h-18 w-18 overflow-hidden rounded-full bg-panel {selected === item.id
                  ? 'ring-2 ring-fg ring-offset-2 ring-offset-ink'
                  : ''}"
              >
                {#if item.avatarUrl && !failed[item.id]}
                  <img
                    data-media
                    src={imageUrl(item.avatarUrl, 180) ?? ''}
                    onerror={() => (failed = { ...failed, [item.id]: true })}
                    alt=""
                    width="180"
                    height="180"
                    loading="lazy"
                    decoding="async"
                    class="h-full w-full object-cover transition duration-300 {item.dimmed
                      ? 'opacity-50'
                      : selected === item.id
                        ? 'opacity-100'
                        : 'opacity-90 group-hover:opacity-100'}"
                  />
                {:else}
                  <span class="flex h-full w-full items-center justify-center text-title text-dim">
                    {item.name.trim().charAt(0).toUpperCase()}
                  </span>
                {/if}
              </span>
              {#if item.busy}
                <span
                  class="absolute right-0 top-0 h-2 w-2 animate-pulse rounded-full bg-hi ring-2 ring-ink"
                  title="正在同步"
                ></span>
              {/if}
            </span>
            <span
              class="w-full truncate text-meta transition-colors {selected === item.id
                ? 'font-medium text-fg'
                : 'text-dim group-hover:text-fg'} {item.dimmed ? 'opacity-50' : ''}"
            >
              {item.name}
            </span>
          </button>
        </li>
      {/each}
    </ul>
  </nav>
{:else if loading}
  <div class="mb-4 flex gap-3 lg:mb-5" aria-hidden="true">
    {#each Array.from({ length: 6 }, (_, i) => i) as i (i)}
      <div class="h-18 w-18 shrink-0 rounded-full bg-panel"></div>
    {/each}
  </div>
{/if}
