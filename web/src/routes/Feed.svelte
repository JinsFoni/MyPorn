<script lang="ts">
  import InfiniteList from '../components/InfiniteList.svelte';
  import PerformerRail from '../components/PerformerRail.svelte';
  import VideoCard from '../components/VideoCard.svelte';
  import EmptyState from '../components/EmptyState.svelte';
  import { api, messageFor } from '../lib/api';
  import { live } from '../lib/live.svelte';
  import { patchQuery, router } from '../lib/router.svelte';
  import type { Performer, Video } from '../lib/types';

  const LIMIT = 40;

  /** 仅 DEV：后端未就绪时用内置示例数据，不要在 PROD 出现 */
  const MOCK: Video[] = [
    {
      id: 1,
      platform: 'pornhub',
      platformVideoId: 'mock-1',
      title: 'Slow morning, warm light',
      url: 'https://example.invalid/view/1',
      thumbUrl: null,
      previewUrl: null,
      durationSec: 742,
      publishedAt: new Date(Date.now() - 36e5).toISOString(),
      viewCount: 128400,
      rating: 88,
      tags: ['slow', 'morning'],
      performers: [{ id: 1, name: 'Mini Diva', url: null }],
      isNew: true,
      hasLocalFile: false,
      localLibraryIds: [],
    },
    {
      id: 2,
      platform: 'pornhub',
      platformVideoId: 'mock-2',
      title: 'Rooftop at dusk, long take',
      url: 'https://example.invalid/view/2',
      thumbUrl: null,
      previewUrl: null,
      durationSec: 1904,
      publishedAt: new Date(Date.now() - 9e6).toISOString(),
      viewCount: 52100,
      rating: 81,
      tags: ['outdoor'],
      performers: [{ id: 2, name: 'Lena Kova', url: null }],
      isNew: false,
      hasLocalFile: false,
      localLibraryIds: [],
    },
    {
      id: 3,
      platform: 'pornhub',
      platformVideoId: 'mock-3',
      title: 'Studio session, three cameras',
      url: 'https://example.invalid/view/3',
      thumbUrl: null,
      previewUrl: null,
      durationSec: 2310,
      publishedAt: new Date(Date.now() - 26e6).toISOString(),
      viewCount: 88700,
      rating: 90,
      tags: ['studio'],
      performers: [{ id: 3, name: 'Aya Sato', url: null }],
      isNew: true,
      hasLocalFile: false,
      localLibraryIds: [],
    },
    {
      id: 4,
      platform: 'pornhub',
      platformVideoId: 'mock-4',
      title: 'Quiet hours',
      url: 'https://example.invalid/view/4',
      thumbUrl: null,
      previewUrl: null,
      durationSec: 611,
      publishedAt: new Date(Date.now() - 5e7).toISOString(),
      viewCount: 20300,
      rating: 74,
      tags: [],
      performers: [{ id: 1, name: 'Mini Diva', url: null }],
      isNew: false,
      hasLocalFile: false,
      localLibraryIds: [],
    },
    {
      id: 5,
      platform: 'pornhub',
      platformVideoId: 'mock-5',
      title: 'Two of us, one afternoon',
      url: 'https://example.invalid/view/5',
      thumbUrl: null,
      previewUrl: null,
      durationSec: 1488,
      publishedAt: new Date(Date.now() - 9e7).toISOString(),
      viewCount: 312000,
      rating: 86,
      tags: ['duo'],
      performers: [
        { id: 1, name: 'Mini Diva', url: null },
        { id: 2, name: 'Lena Kova', url: null },
      ],
      isNew: false,
      hasLocalFile: false,
      localLibraryIds: [],
    },
    {
      id: 6,
      platform: 'pornhub',
      platformVideoId: 'mock-6',
      title: 'Night shift',
      url: 'https://example.invalid/view/6',
      thumbUrl: null,
      previewUrl: null,
      durationSec: 965,
      publishedAt: new Date(Date.now() - 1.7e8).toISOString(),
      viewCount: 45500,
      rating: 79,
      tags: [],
      performers: [{ id: 3, name: 'Aya Sato', url: null }],
      isNew: false,
      hasLocalFile: false,
      localLibraryIds: [],
    },
  ];

  let newOnly = $state(false);
  let newCount = $state(0);
  let newCountMore = $state(false);
  let reloadKey = $state(0);
  let mockNote = $state('');
  let lastBump = 0;

  /**
   * 只筛某位艺人：唯一真源是地址栏 `?performer=<id>`（和 B 站页的 `?up=`、
   * YouTube 页的 `?channel=` 同一个套路）。放地址栏的理由也一样——点开影片是整页跳转，
   * 退回来本组件会重新创建，状态只活在内存里就没了；顺带刷新不丢、链接能复制。
   */
  function readPerformer(): number | null {
    const pid = Number.parseInt(router.query.get('performer') ?? '', 10);
    return Number.isFinite(pid) && pid > 0 ? pid : null;
  }

  let performerFilter = $state<number | null>(readPerformer());
  /** 头像条拉回的订阅列表：只用来把 id 翻成名字（筛选胶囊 / 说明栏要写「只看谁」） */
  let railItems = $state<Performer[]>([]);
  const picked = $derived(railItems.find((p) => p.id === performerFilter) ?? null);

  // 地址栏变了（前进/后退、贴链接、点头像）→ 跟上并重拉；相等就什么都不做，不会自己打转。
  $effect(() => {
    const next = readPerformer();
    if (next === performerFilter) return;
    performerFilter = next;
    reloadKey += 1;
  });

  function setPerformer(id: number | null): void {
    if (id === performerFilter) return;
    patchQuery({ performer: id === null ? null : String(id) });
  }

  async function loadPage(cursor: string | null): Promise<{ items: Video[]; nextCursor: string | null }> {
    try {
      if (newOnly) {
        return await api.newVideos({ cursor, limit: LIMIT, performerId: performerFilter });
      }
      return await api.videos({ cursor, limit: LIMIT, performerId: performerFilter });
    } catch (err) {
      if (import.meta.env.DEV && cursor === null) {
        mockNote = messageFor(err);
        const items = MOCK.filter(
          (v) =>
            (!newOnly || v.isNew) &&
            (performerFilter === null || v.performers.some((p) => p.id === performerFilter)),
        );
        return { items, nextCursor: null };
      }
      throw err;
    }
  }

  async function refreshNewCount(): Promise<void> {
    try {
      const res = await api.newVideos({ limit: 30, performerId: performerFilter });
      newCount = res.items.length;
      newCountMore = res.nextCursor !== null;
    } catch {
      if (import.meta.env.DEV) {
        newCount = MOCK.filter(
          (v) => v.isNew && (performerFilter === null || v.performers.some((p) => p.id === performerFilter)),
        ).length;
        newCountMore = false;
      }
    }
  }

  function toggleNewOnly(): void {
    newOnly = !newOnly;
    reloadKey += 1;
  }

  $effect(() => {
    const version = live.feedVersion;
    if (version === lastBump) return;
    lastBump = version;
    reloadKey += 1;
    void refreshNewCount();
  });

  $effect(() => {
    void refreshNewCount();
  });
</script>

<PerformerRail selected={performerFilter} onSelect={setPerformer} onLoad={(items) => (railItems = items)} />

<!-- 筛选胶囊的样式对齐 YouTube 页的「全部频道」：无底色、选中才吃 bg-raise，未看数走一个小角标。
     第一颗胶囊**永远是「全部」**（用户要求）：按艺人筛的时候不再在前面插一颗带 ✕ 的名字胶囊。
     取消筛选＝再点一次那颗头像（白环亮着的那颗），页面右侧那行「只看 X 的片」是说明、不是按钮。 -->
<div class="mb-3 flex items-center gap-1.5 overflow-x-auto pb-1 lg:mb-5">
  <button
    type="button"
    class="shrink-0 rounded-full px-3 py-1.5 text-meta transition-colors {newOnly
      ? 'text-muted hover:bg-panel hover:text-fg'
      : 'bg-raise text-fg'}"
    aria-pressed={!newOnly}
    onclick={() => { if (newOnly) toggleNewOnly(); }}
  >
    全部
  </button>
  <button
    type="button"
    class="flex shrink-0 items-center gap-1.5 rounded-full px-3 py-1.5 text-meta transition-colors {newOnly
      ? 'bg-raise text-fg'
      : 'text-muted hover:bg-panel hover:text-fg'}"
    aria-pressed={newOnly}
    onclick={() => { if (!newOnly) toggleNewOnly(); }}
  >
    新片
    {#if newCount > 0}
      <span class="rounded-full bg-hi px-1 text-[0.625rem] font-semibold leading-4 text-ink tabular-nums">
        {newCount > 99 ? '99+' : `${String(newCount)}${newCountMore ? '+' : ''}`}
      </span>
    {/if}
  </button>

  <p class="ml-auto hidden shrink-0 text-meta text-dim sm:block">
    {#if performerFilter !== null}
      只看 {picked?.name ?? '这位艺人'}{newOnly ? ' 的未看新片' : ' 的片'}
    {:else}
      {newOnly ? '只看未看的新片' : '按发布时间倒序'}
    {/if}
  </p>
</div>

{#if mockNote}
  <p class="mb-3 rounded-xl bg-panel px-3 py-2 text-meta text-dim">
    离线预览：正在使用内置示例数据（后端未连接：{mockNote}）
  </p>
{/if}

<InfiniteList load={loadPage} keyOf={(v: Video) => v.id} {reloadKey}>
  {#snippet children(video)}
    <VideoCard video={video} />
  {/snippet}

  {#snippet placeholder()}
    <div class="aspect-video rounded-xl bg-panel"></div>
    <div class="mt-2.5 flex items-start gap-2.5">
      <div class="h-9 w-9 shrink-0 rounded-full bg-panel"></div>
      <div class="min-w-0 flex-1">
        <div class="h-3 w-4/5 rounded-full bg-panel"></div>
        <div class="mt-2 h-2.5 w-1/2 rounded-full bg-panel"></div>
      </div>
    </div>
  {/snippet}

  {#snippet empty()}
    {#if performerFilter !== null && newOnly}
      <EmptyState
        title="这位艺人没有未看的新片"
        hint={picked ? `${picked.name} 的片都看过了。` : '这位艺人的片都看过了。'}
        icon="film"
      >
        {#snippet actions()}
          <button type="button" class="btn" onclick={toggleNewOnly}>看他的全部影片</button>
          <button type="button" class="btn" onclick={() => setPerformer(null)}>看所有艺人</button>
        {/snippet}
      </EmptyState>
    {:else if performerFilter !== null}
      <EmptyState
        title="这位艺人这里还没有影片"
        hint="可能是新订阅，历史影片还在回填；换一位艺人，或者看全部影片。"
        icon="film"
      >
        {#snippet actions()}
          <button type="button" class="btn btn-primary" onclick={() => setPerformer(null)}>查看全部影片</button>
        {/snippet}
      </EmptyState>
    {:else if newOnly}
      <EmptyState
        title="没有未看的新片"
        hint="订阅的艺人都已经看完，等下一次同步即可。"
        icon="film"
      >
        {#snippet actions()}
          <button type="button" class="btn" onclick={toggleNewOnly}>查看全部影片</button>
        {/snippet}
      </EmptyState>
    {:else}
      <EmptyState
        title="这里还没有影片"
        hint="先订阅一个艺人，同步回来的影片会按时间出现在这里。"
        icon="film"
      >
        {#snippet actions()}
          <a data-link href="/discover" class="btn btn-primary">发现艺人</a>
        {/snippet}
      </EmptyState>
    {/if}
  {/snippet}
</InfiniteList>
