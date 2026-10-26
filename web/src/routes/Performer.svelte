<script lang="ts">
  import InfiniteList from '../components/InfiniteList.svelte';
  import VideoCard from '../components/VideoCard.svelte';
  import EmptyState from '../components/EmptyState.svelte';
  import Skeleton from '../components/Skeleton.svelte';
  import { ApiError, api, messageFor } from '../lib/api';
  import { imageUrl } from '../lib/img';
  import { formatCount, formatDate, relativeFuture, relativeTime } from '../lib/format';
  import { live } from '../lib/live.svelte';
  import { navigate } from '../lib/router.svelte';
  import type { Performer, SyncMode, Video } from '../lib/types';

  interface Props {
    id: string;
  }

  let { id }: Props = $props();

  /** 仅 DEV：后端未就绪时的示例数据 */
  const MOCK: Performer = {
    id: 1,
    platform: 'pornhub',
    performerId: 'mini-diva',
    name: 'Mini Diva',
    url: 'https://example.invalid/model/mini-diva',
    avatarUrl: null,
    bio: '示例数据：后端未连接。',
    videoCount: 412,
    subscriberCount: 45600,
    subscriptionId: 11,
    enabled: true,
    backfillDone: false,
    backfillProgress: { donePages: 3, totalPages: 18 },
    lastSyncAt: new Date(Date.now() - 36e5).toISOString(),
    nextSyncAt: new Date(Date.now() + 18e6).toISOString(),
    newVideoCount: 3,
    lastError: null,
  };

  const MOCK_VIDEOS: Video[] = [
    {
      id: 901,
      platform: 'pornhub',
      platformVideoId: 'mock-a',
      title: '示例影片 A',
      url: 'https://example.invalid/view/a',
      thumbUrl: null,
      previewUrl: null,
      durationSec: 812,
      publishedAt: new Date(Date.now() - 72e5).toISOString(),
      viewCount: 1000,
      rating: 80,
      tags: [],
      performers: [{ id: 1, name: 'Mini Diva', url: null }],
      isNew: true,
      hasLocalFile: false,
      localLibraryIds: [],
    },
    {
      id: 902,
      platform: 'pornhub',
      platformVideoId: 'mock-b',
      title: '示例影片 B',
      url: 'https://example.invalid/view/b',
      thumbUrl: null,
      previewUrl: null,
      durationSec: 1340,
      publishedAt: new Date(Date.now() - 12e7).toISOString(),
      viewCount: 2200,
      rating: 76,
      tags: [],
      performers: [{ id: 1, name: 'Mini Diva', url: null }],
      isNew: false,
      hasLocalFile: false,
      localLibraryIds: [],
    },
  ];

  interface ProgressView {
    done: number;
    total: number | null;
    found: number | null;
    phase: 'backfill' | 'incremental';
  }

  const performerId = $derived(Number.parseInt(id, 10));

  let performer = $state<Performer | null>(null);
  let loading = $state(true);
  let error = $state('');
  let mockNote = $state('');
  let syncing = $state(false);
  let togglingEnabled = $state(false);
  let listKey = $state(0);
  let lastBump = 0;
  let confirmKind = $state<'full' | 'unsubscribe' | null>(null);
  let confirmBusy = $state(false);
  let cancelBtn = $state<HTMLButtonElement | null>(null);

  const PLATFORM_LABEL: Record<string, string> = { pornhub: 'Pornhub' };

  const avatar = $derived(performer ? imageUrl(performer.avatarUrl, 240) : null);

  const progress = $derived.by((): ProgressView | null => {
    const p = performer;
    if (!p) return null;
    const s = live.syncState(p.id);
    if (s) return { done: s.page, total: s.totalPages, found: s.videosFound, phase: s.phase };
    // 回填已经完成就不要再显示「正在回填历史影片」；后端也可能给出空进度对象。
    if (!p.backfillDone) {
      const bp = p.backfillProgress;
      if (bp && (bp.donePages > 0 || (bp.totalPages ?? 0) > 0)) {
        return { done: bp.donePages, total: bp.totalPages ?? null, found: null, phase: 'backfill' };
      }
      return { done: 0, total: null, found: null, phase: 'backfill' };
    }
    return null;
  });

  const percent = $derived(
    progress && progress.total && progress.total > 0
      ? Math.min(100, Math.round((progress.done / progress.total) * 100))
      : null,
  );

  async function loadPerformer(): Promise<void> {
    error = '';
    try {
      const res = await api.performer(performerId);
      performer = res;
      mockNote = '';
    } catch (err) {
      if (import.meta.env.DEV) {
        performer = { ...MOCK, id: performerId };
        mockNote = messageFor(err);
      } else if (err instanceof ApiError && err.status === 404) {
        error = '找不到这位艺人，可能已被取消订阅。';
      } else {
        error = messageFor(err);
      }
    } finally {
      loading = false;
    }
  }

  async function loadVideos(cursor: string | null): Promise<{ items: Video[]; nextCursor: string | null }> {
    try {
      return await api.videos({ performerId, cursor, limit: 40 });
    } catch (err) {
      if (import.meta.env.DEV && cursor === null) {
        return { items: MOCK_VIDEOS, nextCursor: null };
      }
      throw err;
    }
  }

  async function startSync(mode: SyncMode): Promise<void> {
    const p = performer;
    if (!p || p.subscriptionId === null || syncing) return;
    syncing = true;
    try {
      await api.syncSubscription(p.subscriptionId, mode);
      live.toast(mode === 'full' ? '已开始全量回填' : '已开始增量同步', 'success');
      window.setTimeout(() => void loadPerformer(), 2500);
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      syncing = false;
    }
  }

  async function toggleEnabled(): Promise<void> {
    const p = performer;
    if (!p || p.subscriptionId === null || togglingEnabled) return;
    togglingEnabled = true;
    try {
      performer = await api.updateSubscription(p.subscriptionId, { enabled: !p.enabled });
      live.toast(performer.enabled ? '已启用自动同步' : '已暂停自动同步', 'success');
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      togglingEnabled = false;
    }
  }

  async function runConfirm(): Promise<void> {
    const kind = confirmKind;
    if (!kind) return;
    confirmBusy = true;
    if (kind === 'full') {
      confirmKind = null;
      await startSync('full');
    } else {
      const p = performer;
      if (p && p.subscriptionId !== null) {
        try {
          await api.unsubscribe(p.subscriptionId);
          live.toast(`已取消订阅 ${p.name}`, 'success');
          navigate('/performers', { replace: true });
        } catch (err) {
          live.toast(messageFor(err), 'error');
        }
      }
      confirmKind = null;
    }
    confirmBusy = false;
  }

  $effect(() => {
    void loadPerformer();
  });

  $effect(() => {
    const version = live.feedVersion;
    if (version === lastBump) return;
    lastBump = version;
    listKey += 1;
    void loadPerformer();
  });

  $effect(() => {
    if (confirmKind !== null) cancelBtn?.focus();
  });

</script>

<svelte:window
  onkeydown={(event) => {
    if (event.key === 'Escape' && confirmKind !== null) confirmKind = null;
  }}
/>

{#if loading && !performer}
  <section>
    <div class="flex items-center gap-4 sm:gap-5">
      <Skeleton class="h-20 w-20 shrink-0 sm:h-28 sm:w-28" rounded="rounded-full" />
      <div class="min-w-0 flex-1 space-y-2.5">
        <Skeleton class="h-7 w-52" />
        <Skeleton class="h-3.5 w-full max-w-md" />
        <Skeleton class="h-3 w-72" />
      </div>
    </div>
  </section>
{:else if error}
  <EmptyState title="打不开这位艺人" hint={error} icon="error">
    {#snippet actions()}
      <button type="button" class="btn" onclick={() => void loadPerformer()}>重试</button>
      <a data-link href="/performers" class="btn btn-ghost">返回艺人列表</a>
    {/snippet}
  </EmptyState>
{:else if performer}
  {#if mockNote}
    <p class="mb-3 rounded-xl bg-panel px-3 py-2 text-meta text-dim">
      离线预览：正在使用内置示例数据（后端未连接：{mockNote}）
    </p>
  {/if}

  <section>
    <div class="flex items-center gap-4 sm:gap-5">
      <div class="h-20 w-20 shrink-0 overflow-hidden rounded-full bg-panel sm:h-28 sm:w-28">
        {#if avatar}
          <img src={avatar} alt="" width="240" height="240" class="h-full w-full object-cover" />
        {:else}
          <div class="flex h-full w-full items-center justify-center text-dim">
            <svg viewBox="0 0 24 24" class="h-7 w-7" fill="none" stroke="currentColor" stroke-width="1.4" aria-hidden="true">
              <circle cx="12" cy="9" r="3.25" />
              <path d="M5.5 19.5a6.5 6.5 0 0 1 13 0" />
            </svg>
          </div>
        {/if}
      </div>

      <div class="min-w-0 flex-1">
        <div class="flex flex-wrap items-center gap-2">
          <h2 class="text-hero font-normal tracking-[-0.01em] text-fg">{performer.name}</h2>
          <span class="chip">{PLATFORM_LABEL[performer.platform] ?? performer.platform}</span>
          {#if !performer.enabled}
            <span class="chip">已暂停</span>
          {/if}
          {#if performer.lastError}
            <span class="chip text-brick" title={performer.lastError}>同步出错</span>
          {/if}
        </div>

        {#if performer.bio}
          <p class="mt-1.5 line-clamp-2 max-w-2xl text-ui text-muted">{performer.bio}</p>
        {/if}

        <!-- 一行元信息，用中点分隔（beeg 的艺人页也是这么写的：数字直接跟着图标/词走） -->
        <p class="mt-2.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-meta text-dim">
          <span class="tabular-nums">{formatCount(performer.videoCount)}部影片</span>
          {#if performer.subscriberCount !== null}
            <span aria-hidden="true">·</span>
            <span class="tabular-nums">{formatCount(performer.subscriberCount)}粉丝</span>
          {/if}
          {#if performer.newVideoCount > 0}
            <span aria-hidden="true">·</span>
            <span class="tabular-nums text-fg">{performer.newVideoCount}部未看</span>
          {/if}
          <span aria-hidden="true">·</span>
          <span title={performer.lastSyncAt ?? ''}>{relativeTime(performer.lastSyncAt)}同步</span>
          <span aria-hidden="true">·</span>
          <span>{performer.enabled ? `下次同步 ${relativeFuture(performer.nextSyncAt)}` : '已暂停同步'}</span>
        </p>
      </div>
    </div>

    <div class="mt-4 flex flex-wrap items-center gap-2">
      <button
        type="button"
        class="btn btn-primary"
        onclick={() => void startSync('incremental')}
        disabled={syncing || !performer.enabled || performer.subscriptionId === null}
      >
        {syncing ? '同步中…' : '立即同步'}
      </button>
      <button
        type="button"
        class="btn"
        onclick={() => (confirmKind = 'full')}
        disabled={syncing || performer.subscriptionId === null}
      >
        全量回填
      </button>
      <button
        type="button"
        class="btn"
        onclick={() => void toggleEnabled()}
        disabled={togglingEnabled || performer.subscriptionId === null}
      >
        {performer.enabled ? '暂停同步' : '启用同步'}
      </button>
      <a href={performer.url} target="_blank" rel="noreferrer noopener" class="btn btn-ghost">
        平台主页
      </a>
      <button
        type="button"
        class="btn btn-danger ml-auto"
        onclick={() => (confirmKind = 'unsubscribe')}
        disabled={performer.subscriptionId === null}
      >
        取消订阅
      </button>
    </div>

    {#if progress}
      <div class="panel mt-4 px-3.5 py-3" role="status" aria-live="polite">
        <div class="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1 text-meta">
          <span class="text-fg/85">
            {progress.phase === 'backfill' ? '正在回填历史影片' : '正在增量同步'}
            {#if progress.total}· 第 {progress.done}/{progress.total} 页{/if}
            {#if progress.found !== null}· 已入库 {progress.found} 部{/if}
          </span>
          {#if percent !== null}
            <span class="tabular-nums text-dim">{percent}%</span>
          {/if}
        </div>
        <div class="mt-2 h-1 overflow-hidden rounded-full bg-raise">
          {#if percent !== null}
            <div class="h-full rounded-full bg-hi transition-[width] duration-500" style="width: {percent}%"></div>
          {:else}
            <div class="h-full w-1/3 animate-pulse rounded-full bg-hi/70"></div>
          {/if}
        </div>
      </div>
    {/if}
  </section>

  <section class="mt-6">
    <h3 class="mb-3 text-title font-medium text-fg">影片</h3>
    <InfiniteList load={loadVideos} keyOf={(v: Video) => v.id} reloadKey={listKey}>
      {#snippet children(video)}
        <VideoCard video={video} />
      {/snippet}

      {#snippet empty()}
        <EmptyState title="还没有同步到影片" hint="点「立即同步」开始抓取；首次订阅会自动全量回填。" icon="film">
          {#snippet actions()}
            <button
              type="button"
              class="btn btn-primary"
              onclick={() => void startSync('incremental')}
              disabled={syncing || typeof performer?.subscriptionId !== 'number'}
            >
              立即同步
            </button>
          {/snippet}
        </EmptyState>
      {/snippet}
    </InfiniteList>
  </section>

  {#if confirmKind}
    <div class="fixed inset-0 z-50 flex items-end justify-center bg-ink/75 p-4 sm:items-center">
      <div class="w-full max-w-sm rounded-2xl bg-raise p-5" role="dialog" aria-modal="true" aria-labelledby="confirm-title">
        <h2 id="confirm-title" class="text-title font-medium text-fg">
          {confirmKind === 'full' ? '全量回填？' : '取消订阅？'}
        </h2>
        <p class="mt-2 text-ui text-muted">
          {#if confirmKind === 'full'}
            会从头翻页抓取该艺人的全部影片，几百部需要一两分钟；已入库的影片不会重复。
          {:else}
            取消后不再自动同步，已入库的影片会保留；想恢复可以重新订阅。
          {/if}
        </p>
        <p class="mt-3 text-meta text-dim">上次同步：{formatDate(performer.lastSyncAt)}</p>
        <div class="mt-5 flex justify-end gap-2">
          <button type="button" class="btn" bind:this={cancelBtn} onclick={() => (confirmKind = null)}>取消</button>
          <button
            type="button"
            class={confirmKind === 'full' ? 'btn btn-primary' : 'btn btn-danger'}
            onclick={() => void runConfirm()}
            disabled={confirmBusy}
          >
            {confirmKind === 'full' ? '开始回填' : '确认取消订阅'}
          </button>
        </div>
      </div>
    </div>
  {/if}
{/if}
