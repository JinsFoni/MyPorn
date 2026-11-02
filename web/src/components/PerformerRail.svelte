<script lang="ts">
  import { api } from '../lib/api';
  import { imageUrl } from '../lib/img';
  import { live } from '../lib/live.svelte';
  import type { Performer } from '../lib/types';

  /**
   * 横向头像条：关注页顶部的**筛选器**。
   *
   * 2026-11-02 改：点谁筛谁，不再跳 `/performer/<id>`。理由是同位置的
   * B 站页（`?up=`）和 YouTube 页（`?channel=`）都是「一排头像当过滤器 + 下面一面墙」，
   * 只有关注页按下去整页跳走，三处手势不一致。艺人主页的入口还在「艺人」页、
   * 管理页、播放页作者行——那里点进去看的才是资料。
   *
   * 选中态用 2px 白环（本站唯一的强调色就是纯白），不显示任何计数。
   */
  interface Props {
    /** 当前筛的艺人 id；地址栏 `?performer=` 是唯一真源，这里只负责高亮 */
    selected?: number | null;
    /** 点一个头像：选中就回 id，再点一次回 null（取消筛选） */
    onSelect?: (id: number | null) => void;
    /** 列表拉到/刷新后回吐一份，页面拿它把 id 翻回名字（说明栏要写「只看谁」） */
    onLoad?: (items: Performer[]) => void;
  }

  let { selected = null, onSelect, onLoad }: Props = $props();

  let items = $state<Performer[]>([]);
  let loaded = $state(false);
  let failed = $state<Record<number, boolean>>({});
  let lastBump = 0;

  const sorted = $derived(
    [...items].sort((a, b) => {
      const at = a.lastSyncAt ? Date.parse(a.lastSyncAt) : 0;
      const bt = b.lastSyncAt ? Date.parse(b.lastSyncAt) : 0;
      return bt - at;
    }),
  );

  async function refresh(): Promise<void> {
    try {
      const res = await api.subscriptions();
      items = res.items;
      onLoad?.(items);
    } catch {
      /* 头像条只是筛选器，被限流/断网时静默降级，不打扰影片流 */
    } finally {
      loaded = true;
    }
  }

  function pick(id: number): void {
    onSelect?.(selected === id ? null : id);
  }

  $effect(() => {
    void refresh();
  });

  $effect(() => {
    const version = live.feedVersion;
    if (version === lastBump) return;
    lastBump = version;
    void refresh();
  });
</script>

{#if sorted.length > 0}
  <nav class="mb-4 lg:mb-5" aria-label="按艺人筛选">
    <ul class="no-scrollbar -mx-3 flex gap-3 overflow-x-auto px-3 pb-1 sm:-mx-4 sm:px-4 lg:mx-0 lg:px-0">
      {#each sorted as performer (performer.id)}
        <li class="shrink-0">
          <button
            type="button"
            onclick={() => pick(performer.id)}
            aria-pressed={selected === performer.id}
            class="group flex w-24 flex-col items-start gap-1.5 text-left"
            title={selected === performer.id
              ? `${performer.name}（再点一次看全部）`
              : `只看 ${performer.name}`}
          >
            <span
              class="h-18 w-18 overflow-hidden rounded-full bg-panel {selected === performer.id
                ? 'ring-2 ring-fg ring-offset-2 ring-offset-ink'
                : ''}"
            >
              {#if performer.avatarUrl && !failed[performer.id]}
                <img
                  data-media
                  src={imageUrl(performer.avatarUrl, 180) ?? ''}
                  onerror={() => (failed = { ...failed, [performer.id]: true })}
                  alt=""
                  width="180"
                  height="180"
                  loading="lazy"
                  decoding="async"
                  class="h-full w-full object-cover transition duration-300 {selected === performer.id
                    ? 'opacity-100'
                    : 'opacity-90 group-hover:opacity-100'}"
                />
              {:else}
                <span class="flex h-full w-full items-center justify-center text-title text-dim">
                  {performer.name.trim().charAt(0).toUpperCase()}
                </span>
              {/if}
            </span>
            <span
              class="w-full truncate text-meta transition-colors {selected === performer.id
                ? 'font-medium text-fg'
                : 'text-dim group-hover:text-fg'}"
            >
              {performer.name}
            </span>
          </button>
        </li>
      {/each}
    </ul>
  </nav>
{:else if !loaded}
  <!-- 首屏占位：跟真实头像同尺寸，避免加载完成时整页跳动 -->
  <div class="mb-4 flex gap-3 lg:mb-5" aria-hidden="true">
    {#each Array.from({ length: 6 }, (_, i) => i) as i (i)}
      <div class="h-18 w-18 shrink-0 rounded-full bg-panel"></div>
    {/each}
  </div>
{/if}
