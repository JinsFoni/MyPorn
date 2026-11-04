<script lang="ts">
  /**
   * krx18：tag 订阅 + 影片墙（M8）。
   *
   * 和 B 站（/bilibili）、YouTube（/youtube）是同一个位置的一页：都是「订阅一个内容源」，
   * 所以都不混进「艺人」和「关注」流——那两处追的是艺人，语义不同。
   *
   * **这一页的订阅单位是 tag 归档页**（`/tag/<女优名>/`）。krx18 上没有艺人主页
   * （实测整站首页只有 /movies、/genre、/release 三种链接），但每部片子都打了女优 tag，
   * 所以一位女优 = 一个 tag = 一条订阅。站上翻到一部片子、点下面那排 tag、
   * 把 tag 页地址贴进来，就是这个人的全部片子（实测 do-hee 有 3 页 92 部）。
   *
   * 数据边界：这里**没有自己的表**。订阅源是 performers 的一行、影片是 videos 的一行
   * （platform='krx18'），播放走 /api/videos/{id}/playback（krx18 的清单是服务端现做的，
   * 见 docs/M8-KRX18.md）。下载这条路还没通（上游 m3u8 是 POST 回来的正文、不是地址），
   * 所以这一页只读、不排下载。
   */
  import { onMount } from 'svelte';
  import InfiniteList, { POSTER_GRID } from '../components/InfiniteList.svelte';
  import EmptyState from '../components/EmptyState.svelte';
  import AvatarRail from '../components/AvatarRail.svelte';
  import VideoCard from '../components/VideoCard.svelte';
  import PlatformIcon from '../components/PlatformIcon.svelte';
  import { api, messageFor } from '../lib/api';
  import { imageUrl } from '../lib/img';
  import { live } from '../lib/live.svelte';
  import { patchQuery, router } from '../lib/router.svelte';
  import { formatCount, relativeFuture } from '../lib/format';
  import type { KrxChannel, ResolvedPerformer, Video } from '../lib/types';

  type Mode = 'all' | 'unseen';

  const MODES: readonly (readonly [Mode, string])[] = [
    ['all', '全部'],
    ['unseen', '未看'],
  ];

  // ---- 订阅源（tag）----
  let channels = $state<KrxChannel[]>([]);
  let channelsLoaded = $state(false);
  let channelsError = $state('');

  // ---- 筛选（唯一真源是地址栏：?tag=<id>&mode=unseen）----
  /**
   * 和 B 站同一套理由：点开影片是整页跳转（`/video/:id`），退回时本组件会重新创建，
   * 状态只活在内存里就会丢；写在地址栏里退回来还是原来那个 tag、原来那个筛选，
   * 刷新不丢，链接发出去看到的也是同一个视角。
   */
  function readQuery(): { tag: number | null; mode: Mode } {
    const q = router.query;
    const tag = Number.parseInt(q.get('tag') ?? '', 10);
    return {
      tag: Number.isFinite(tag) && tag > 0 ? tag : null,
      mode: q.get('mode') === 'unseen' ? 'unseen' : 'all',
    };
  }

  const initial = readQuery();
  let filterChannel = $state<number | null>(initial.tag);
  let mode = $state<Mode>(initial.mode);
  /** 换筛选条件时 +1，让 InfiniteList 重头拉 */
  let wallKey = $state(0);

  $effect(() => {
    const next = readQuery();
    if (next.tag === filterChannel && next.mode === mode) return;
    filterChannel = next.tag;
    mode = next.mode;
    wallKey += 1;
  });

  // ---- 添加订阅 ----
  let addOpen = $state(false);
  let addUrl = $state('');
  let addBusy = $state(false);
  let addError = $state('');
  let preview = $state<ResolvedPerformer | null>(null);
  let addInput = $state<HTMLInputElement | null>(null);

  const unseenTotal = $derived(channels.reduce((sum, c) => sum + c.unseenCount, 0));
  const failing = $derived(channels.filter((c) => c.lastError !== ''));

  /** 头像条：只画头像和名字（和关注页同口径），计数放进 title。 */
  const railItems = $derived(
    channels.map((c) => ({
      id: c.id,
      name: c.name,
      avatarUrl: c.avatarUrl,
      dimmed: !c.enabled,
      busy: live.isSyncing(c.id),
      title: `${c.name} · 已收录 ${String(c.videoCount)} 部（站点共 ${String(c.siteCount)}）${
        c.unseenCount > 0 ? ` · 未看 ${String(c.unseenCount)}` : ''
      }`,
    })),
  );

  async function loadChannels(): Promise<void> {
    channelsError = '';
    try {
      const res = await api.krxChannels();
      channels = res.items;
      live.setKrxUnseen(res.stats.unseen);
    } catch (err) {
      channelsError = messageFor(err);
    } finally {
      channelsLoaded = true;
    }
  }

  function loadPage(cursor: string | null): Promise<{ items: Video[]; nextCursor: string | null }> {
    return api.krxVideos({
      cursor,
      channelId: filterChannel,
      unseen: mode === 'unseen',
      limit: 36,
    });
  }

  function setChannel(id: number | null): void {
    patchQuery({ tag: id === null ? null : String(id) });
  }

  function setMode(next: Mode): void {
    patchQuery({ mode: next === 'all' ? null : next });
  }

  onMount(() => {
    void loadChannels();
  });

  // 后台同步抓到新片时刷墙（只刷第一页：墙按发布时间倒序，新的都在最前面）。
  // 事件里带 platform，因为「关注」流也会发 video.new——把别的平台的也当 krx18 刷就白拉了。
  let seenVersion = 0;
  $effect(() => {
    const v = live.krxVersion;
    if (v === seenVersion) return;
    seenVersion = v;
    wallKey += 1;
    void loadChannels();
  });

  // ---- 已看 ----

  async function markAllSeen(): Promise<void> {
    try {
      const res = await api.krxSeen(filterChannel === null ? {} : { channelId: filterChannel });
      live.setKrxUnseen(res.unseen);
      await loadChannels();
      wallKey += 1;
      live.toast(filterChannel === null ? 'krx18 影片已全部标为已看' : '这个 tag 的影片已标为已看', 'success');
    } catch (err) {
      live.toast(messageFor(err), 'error');
    }
  }

  // ---- 添加订阅 ----

  function openAdd(): void {
    addOpen = true;
    addUrl = '';
    addError = '';
    preview = null;
    queueMicrotask(() => addInput?.focus());
  }

  function closeAdd(): void {
    if (addBusy) return;
    addOpen = false;
  }

  async function resolve(): Promise<void> {
    const raw = addUrl.trim();
    if (raw === '' || addBusy) return;
    addBusy = true;
    addError = '';
    preview = null;
    try {
      const res = await api.resolvePerformer(raw);
      if (res.platform !== 'krx18') {
        addError = '这是一条非 krx18 的链接。这里订阅的是 tag 归档页，形如 https://krx18.com/tag/<女优名>/。';
        return;
      }
      preview = res;
    } catch (err) {
      addError = messageFor(err);
    } finally {
      addBusy = false;
    }
  }

  async function subscribe(): Promise<void> {
    const raw = addUrl.trim();
    if (raw === '' || addBusy) return;
    addBusy = true;
    addError = '';
    try {
      const res = await api.subscribe({ url: raw });
      const already = channels.some((c) => c.id === res.performer.id);
      live.toast(
        already ? `${res.performer.name} 已经订阅过了` : `已订阅 ${res.performer.name}，正在回填这个 tag 的影片`,
        'success',
      );
      addOpen = false;
      await loadChannels();
      wallKey += 1;
    } catch (err) {
      addError = messageFor(err);
    } finally {
      addBusy = false;
    }
  }

  function onKeydown(event: KeyboardEvent): void {
    if (event.key !== 'Escape') return;
    if (addOpen) {
      event.preventDefault();
      closeAdd();
    }
  }
</script>

<svelte:window onkeydown={onKeydown} />

<!-- 页面上不写标题：顶部直接是 tag 头像条（和关注页/B 站页同款，横排圆头像，点谁筛谁）。
     页面叫什么由侧边栏高亮和浏览器标题负责。 -->
<AvatarRail
  items={railItems}
  label="按女优（tag）筛选"
  selected={filterChannel}
  onSelect={setChannel}
  loading={!channelsLoaded}
/>

<div class="flex flex-col gap-4">
  {#if failing.length > 0}
    <div class="flex items-start gap-2 rounded-xl bg-panel px-3 py-2 text-meta text-brick">
      <span class="mt-0.5">●</span>
      <p class="min-w-0">
        {failing.length} 个订阅上次同步失败：{failing[0]?.lastError}
        {#if failing[0]?.nextSyncAt}
          <span class="text-dim">（{relativeFuture(failing[0].nextSyncAt)}再试）</span>
        {/if}
      </p>
    </div>
  {/if}

  {#if channelsError}
    <p class="rounded-xl bg-panel p-3 text-ui text-brick" role="alert">{channelsError}</p>
  {/if}

  {#if channelsLoaded && channels.length > 0}
    <div class="flex flex-wrap items-center gap-2">
      <div class="flex shrink-0 items-center gap-0.5 rounded-full bg-panel p-0.5 text-meta">
        {#each MODES as item (item[0])}
          <button
            type="button"
            onclick={() => setMode(item[0])}
            class="rounded-full px-2.5 py-1 transition-colors {mode === item[0]
              ? 'bg-raise text-fg'
              : 'text-muted hover:text-fg'}"
          >
            {item[1]}
          </button>
        {/each}
      </div>

      <div class="ml-auto flex shrink-0 items-center gap-2">
        {#if unseenTotal > 0}
          <button
            type="button"
            class="rounded-full px-3 py-1.5 text-meta text-muted transition-colors hover:bg-panel hover:text-fg"
            onclick={() => void markAllSeen()}
          >
            {filterChannel === null ? '全部标为已看' : '这个标为已看'}
          </button>
        {/if}
        <button
          type="button"
          class="rounded-full bg-hi px-3.5 py-1.5 text-meta font-medium text-ink transition-opacity hover:opacity-90"
          onclick={openAdd}
        >
          添加订阅
        </button>
      </div>
    </div>
  {/if}

  {#if channelsLoaded && channels.length === 0}
    <EmptyState
      title="还没有订阅任何 tag"
      hint="krx18 上没有艺人主页，能订阅的是 tag 归档页：站上随便点开一部片子，正文下面那一排女优 tag 点进去，把那个地址（形如 /tag/<名字>/）贴进来就行。订阅后会立刻回填这个 tag 下的全部影片。"
      icon="film"
    >
      {#snippet actions()}
        <button type="button" class="rounded-full bg-hi px-4 py-2 text-ui font-medium text-ink" onclick={openAdd}>
          添加订阅
        </button>
      {/snippet}
    </EmptyState>
  {:else}
    <InfiniteList
      load={loadPage}
      keyOf={(video: Video) => video.id}
      reloadKey={wallKey}
      maxLoadedPages={6}
      gridClass={POSTER_GRID}
    >
      {#snippet placeholder()}
        <div class="flex flex-col gap-2">
          <div class="aspect-[5/7] w-full rounded-xl bg-panel"></div>
          <div class="h-3 w-4/5 rounded bg-panel"></div>
          <div class="h-3 w-2/5 rounded bg-panel"></div>
        </div>
      {/snippet}

      {#snippet empty()}
        <div class="col-span-full">
          <EmptyState
            title={mode === 'unseen' ? '没有未看的影片' : '这里还没有影片'}
            hint={mode === 'all'
              ? '回填是一次一页往后翻的，第一次可能只进来一两页。稍等一会儿会自动接着翻。'
              : '换个筛选条件看看。'}
            icon="film"
          />
        </div>
      {/snippet}

      {#snippet children(video: Video)}
        <VideoCard {video} portrait />
      {/snippet}
    </InfiniteList>
  {/if}
</div>

<!-- 添加订阅（贴 tag 归档页地址） -->
{#if addOpen}
  <div class="fixed inset-0 z-50 flex items-end justify-center sm:items-center">
    <button type="button" class="absolute inset-0 bg-ink/85 backdrop-blur-sm" aria-label="关闭" onclick={closeAdd}
    ></button>
    <div
      class="relative z-10 w-full max-w-lg rounded-t-2xl bg-sheet p-4 sm:rounded-2xl sm:p-5"
      role="dialog"
      aria-label="添加订阅"
    >
      <h2 class="flex items-center gap-2 text-title font-medium text-fg">
        <PlatformIcon platform="krx18" label="krx18" size="xs" />
        添加订阅
      </h2>
      <p class="mt-1 text-meta text-dim">
        贴一条 tag 归档页地址（https://krx18.com/tag/&lt;女优名&gt;/）。站上每部片子下面都有女优 tag，
        点进去就是那个 tag 的全部影片。
      </p>

      <form
        class="mt-3 flex gap-2"
        onsubmit={(event) => {
          event.preventDefault();
          void resolve();
        }}
      >
        <input
          bind:this={addInput}
          bind:value={addUrl}
          class="field flex-1"
          type="text"
          placeholder="https://krx18.com/tag/do-hee-도희/"
          spellcheck="false"
          autocomplete="off"
          disabled={addBusy}
        />
        <button
          type="submit"
          class="rounded-full bg-raise px-3.5 py-2 text-meta text-fg transition-colors hover:bg-panel disabled:opacity-60"
          disabled={addBusy || addUrl.trim() === ''}
        >
          {addBusy ? '解析中…' : '解析'}
        </button>
      </form>

      {#if addError}
        <p class="mt-3 rounded-xl bg-panel px-3 py-2 text-meta text-brick" role="alert">{addError}</p>
      {/if}

      {#if preview}
        <div class="mt-3 flex items-center gap-3 rounded-xl bg-panel p-3">
          {#if preview.avatarUrl}
            <img src={imageUrl(preview.avatarUrl, 88) ?? ''} alt="" class="h-11 w-11 rounded-full object-cover" />
          {:else}
            <span class="grid h-11 w-11 place-items-center rounded-full bg-raise text-ui text-dim">
              {preview.name.slice(0, 1).toUpperCase()}
            </span>
          {/if}
          <div class="min-w-0 flex-1">
            <p class="truncate text-ui font-medium text-fg">{preview.name}</p>
            <p class="truncate text-meta text-dim">
              {#if preview.videoCount}
                站点共 {formatCount(preview.videoCount)} 部
              {:else}
                tag {preview.performerId}
              {/if}
            </p>
          </div>
        </div>
      {/if}

      <p class="mt-3 text-meta text-dim">
        订阅后会一页一页回填这个 tag 的影片（每页 39 部）。影片只在线播放，这一站暂不支持下载
        （上游给的是清单正文、不是可下载的地址）。
      </p>

      <div class="mt-4 flex items-center justify-end gap-2">
        <button type="button" class="rounded-full px-3 py-1.5 text-meta text-muted hover:text-fg" onclick={closeAdd}>
          取消
        </button>
        <button
          type="button"
          class="rounded-full bg-hi px-3.5 py-1.5 text-meta font-medium text-ink transition-opacity hover:opacity-90 disabled:opacity-60"
          onclick={() => void subscribe()}
          disabled={addBusy || addUrl.trim() === ''}
        >
          {addBusy ? '添加中…' : '订阅'}
        </button>
      </div>
    </div>
  </div>
{/if}
