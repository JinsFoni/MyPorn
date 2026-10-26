<script lang="ts">
  import PerformerCard from '../components/PerformerCard.svelte';
  import EmptyState from '../components/EmptyState.svelte';
  import Skeleton from '../components/Skeleton.svelte';
  import { api, messageFor } from '../lib/api';
  import { live } from '../lib/live.svelte';
  import type { Performer } from '../lib/types';

  /** 仅 DEV：后端未就绪时的示例数据 */
  const MOCK: Performer[] = [
    {
      id: 1,
      platform: 'pornhub',
      performerId: 'mini-diva',
      name: 'Mini Diva',
      url: 'https://example.invalid/model/mini-diva',
      avatarUrl: null,
      bio: null,
      videoCount: 412,
      subscriberCount: 45600,
      subscriptionId: 11,
      enabled: true,
      backfillDone: true,
      backfillProgress: null,
      lastSyncAt: new Date(Date.now() - 32e5).toISOString(),
      nextSyncAt: new Date(Date.now() + 18e6).toISOString(),
      newVideoCount: 3,
      lastError: null,
    },
    {
      id: 2,
      platform: 'pornhub',
      performerId: 'lena-kova',
      name: 'Lena Kova',
      url: 'https://example.invalid/model/lena-kova',
      avatarUrl: null,
      bio: null,
      videoCount: 87,
      subscriberCount: 12400,
      subscriptionId: 12,
      enabled: true,
      backfillDone: false,
      backfillProgress: { donePages: 2, totalPages: 5 },
      lastSyncAt: new Date(Date.now() - 864e5).toISOString(),
      nextSyncAt: null,
      newVideoCount: 0,
      lastError: null,
    },
    {
      id: 3,
      platform: 'pornhub',
      performerId: 'aya-sato',
      name: 'Aya Sato',
      url: 'https://example.invalid/model/aya-sato',
      avatarUrl: null,
      bio: null,
      videoCount: 233,
      subscriberCount: 88900,
      subscriptionId: 13,
      enabled: false,
      backfillDone: true,
      backfillProgress: null,
      lastSyncAt: null,
      nextSyncAt: null,
      newVideoCount: 0,
      lastError: '抓取超时：pornhub.com 响应 504',
    },
  ];

  type SortBy = 'recent' | 'new';

  let items = $state<Performer[]>([]);
  let loading = $state(true);
  let error = $state('');
  let mockNote = $state('');
  let sortBy = $state<SortBy>('recent');
  let lastBump = 0;

  const sorted = $derived(
    [...items].sort((a, b) => {
      if (sortBy === 'new') return b.newVideoCount - a.newVideoCount;
      const at = a.lastSyncAt ? Date.parse(a.lastSyncAt) : 0;
      const bt = b.lastSyncAt ? Date.parse(b.lastSyncAt) : 0;
      return bt - at;
    }),
  );

  async function refresh(showSkeleton = true): Promise<void> {
    if (showSkeleton) loading = true;
    error = '';
    try {
      const res = await api.subscriptions();
      items = res.items;
      mockNote = '';
    } catch (err) {
      if (import.meta.env.DEV) {
        items = MOCK;
        mockNote = messageFor(err);
      } else {
        error = messageFor(err);
      }
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void refresh();
  });

  $effect(() => {
    const version = live.feedVersion;
    if (version === lastBump) return;
    lastBump = version;
    void refresh(false);
  });
</script>

<div class="mb-3 flex items-center gap-2 lg:mb-5">
  <button
    type="button"
    class="chip h-8 px-3.5 text-ui {sortBy === 'recent' ? 'bg-hi text-ink hover:bg-hi' : 'hover:bg-raise'}"
    aria-pressed={sortBy === 'recent'}
    onclick={() => (sortBy = 'recent')}
  >
    最近同步
  </button>
  <button
    type="button"
    class="chip h-8 px-3.5 text-ui {sortBy === 'new' ? 'bg-hi text-ink hover:bg-hi' : 'hover:bg-raise'}"
    aria-pressed={sortBy === 'new'}
    onclick={() => (sortBy = 'new')}
  >
    新片最多
  </button>
  <p class="ml-auto shrink-0 text-meta text-dim">
    <span class="tabular-nums">{items.length}</span> 位
  </p>
</div>

{#if mockNote}
  <p class="mb-3 rounded-xl bg-panel px-3 py-2 text-meta text-dim">
    离线预览：正在使用内置示例数据（后端未连接：{mockNote}）
  </p>
{/if}

{#if error}
  <div class="flex flex-col items-center gap-3 py-16 text-center" role="alert">
    <p class="text-ui text-muted">{error}</p>
    <button type="button" class="btn" onclick={() => refresh()}>重试</button>
  </div>
{:else if loading && items.length === 0}
  <div class="grid grid-cols-[repeat(auto-fill,5.5rem)] gap-x-6 gap-y-8 sm:gap-x-8 sm:grid-cols-[repeat(auto-fill,6rem)]">
    {#each Array.from({ length: 12 }, (_, i) => i) as i (i)}
      <div class="min-w-0">
        <Skeleton class="aspect-square w-full" rounded="rounded-full" />
        <Skeleton class="mx-auto mt-3 h-3 w-3/4" rounded="rounded-full" />
        <Skeleton class="mx-auto mt-2 h-2.5 w-1/2" rounded="rounded-full" />
      </div>
    {/each}
  </div>
{:else if sorted.length === 0}
  <EmptyState
    title="还没有订阅任何艺人"
    hint="在「发现」里搜索艺人，或者直接粘贴艺人主页链接。"
    icon="user"
  >
    {#snippet actions()}
      <a data-link href="/discover" class="btn btn-primary">发现艺人</a>
    {/snippet}
  </EmptyState>
{:else}
  <div class="grid grid-cols-[repeat(auto-fill,5.5rem)] gap-x-6 gap-y-8 sm:gap-x-8 sm:grid-cols-[repeat(auto-fill,6rem)]">
    {#each sorted as performer (performer.id)}
      <PerformerCard {performer} eager={performer.id === sorted[0]?.id} />
    {/each}
  </div>
{/if}
