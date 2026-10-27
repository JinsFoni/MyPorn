<script lang="ts">
  import { api } from '../lib/api';
  import { imageUrl } from '../lib/img';
  import { live } from '../lib/live.svelte';
  import type { Performer } from '../lib/types';

  /** 横向头像条：关注页顶部的入口，点谁进谁的主页。不显示任何计数。 */
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
    } catch {
      /* 头像条只是入口，被限流/断网时静默降级，不打扰影片流 */
    } finally {
      loaded = true;
    }
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
  <nav class="mb-4 lg:mb-5" aria-label="订阅的艺人">
    <ul class="no-scrollbar -mx-3 flex gap-3 overflow-x-auto px-3 pb-1 sm:-mx-4 sm:px-4 lg:mx-0 lg:px-0">
      {#each sorted as performer (performer.id)}
        <li class="shrink-0">
          <a
            data-link
            href={`/performer/${String(performer.id)}`}
            class="group flex w-24 flex-col items-start gap-1.5"
            title={performer.name}
          >
            <span class="h-18 w-18 overflow-hidden rounded-full bg-panel">
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
                  class="h-full w-full object-cover opacity-90 transition duration-300 group-hover:opacity-100"
                />
              {:else}
                <span class="flex h-full w-full items-center justify-center text-title text-dim">
                  {performer.name.trim().charAt(0).toUpperCase()}
                </span>
              {/if}
            </span>
            <span class="w-full truncate text-meta text-dim transition-colors group-hover:text-fg">
              {performer.name}
            </span>
          </a>
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
