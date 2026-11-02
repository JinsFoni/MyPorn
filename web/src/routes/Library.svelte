<script lang="ts">
  /**
   * 媒体库：**磁盘上已经下载下来的**影片。
   *
   * 和「下载」页的分工（用户 2026-11-03 要求加这一页）：
   * 下载页是任务队列——排队中/下载中/失败的/历史记录，关心的是「还没下完的东西」；
   * 这一页只认 `library_files` 里那些**文件真的躺在硬盘上**的片子，关心的是「我有什么」。
   * 数据来自 `/api/library/videos`（见 internal/store/library_videos.go）。
   *
   * 三个要点：
   * - 筛选/排序/搜索都写在地址栏上（`?lib=&sort=&q=&performer=&channel=`），刷新、后退、贴链接都能还原；
   * - 卡片封面优先用下载时落盘的 thumb.jpg（`/api/videos/{id}/art`），平台封面地址过期也不影响；
   * - 点开就是 `/video/{id}`——播放器本来就优先播本地文件（handlePlayback 里 local 排第一），
   *   所以这里不需要另起一套播放器。
   */
  import { onMount } from 'svelte';
  import AvatarRail from '../components/AvatarRail.svelte';
  import InfiniteList from '../components/InfiniteList.svelte';
  import EmptyState from '../components/EmptyState.svelte';
  import LibraryCard from '../components/LibraryCard.svelte';
  import { api } from '../lib/api';
  import { formatBytes } from '../lib/format';
  import { live } from '../lib/live.svelte';
  import { patchQuery, router } from '../lib/router.svelte';
  import { ui } from '../lib/ui.svelte';
  import type { LibrarySort, LibraryVideo, LibraryVideosPage } from '../lib/types';
  import type { RailItem } from '../lib/rail';

  const PAGE_SIZE = 24;

  const SORTS: readonly (readonly [LibrarySort, string])[] = [
    ['recent', '最近下载'],
    ['size', '体积'],
    ['duration', '时长'],
    ['title', '文件名'],
  ];

  interface Filters {
    lib: number | null;
    sort: LibrarySort;
    q: string;
    /** 顶部头像条选中的「来源」：平台影片的艺人 / YouTube 影片的频道，二者互斥 */
    performer: number | null;
    channel: number | null;
  }

  function readQuery(): Filters {
    const q = router.query;
    const lib = Number.parseInt(q.get('lib') ?? '', 10);
    const performer = Number.parseInt(q.get('performer') ?? '', 10);
    const channel = Number.parseInt(q.get('channel') ?? '', 10);
    const sort = q.get('sort');
    return {
      lib: Number.isFinite(lib) && lib > 0 ? lib : null,
      sort:
        sort === 'size' || sort === 'duration' || sort === 'title'
          ? (sort as LibrarySort)
          : ('recent' as LibrarySort),
      q: (q.get('q') ?? '').trim(),
      performer: Number.isFinite(performer) && performer > 0 ? performer : null,
      channel: Number.isFinite(channel) && channel > 0 ? channel : null,
    };
  }

  const initial = readQuery();
  let lib = $state<number | null>(initial.lib);
  let sort = $state<LibrarySort>(initial.sort);
  let q = $state(initial.q);
  let performer = $state<number | null>(initial.performer);
  let channel = $state<number | null>(initial.channel);
  let searchInput = $state(initial.q);
  /** 换筛选条件时 +1，让 InfiniteList 重头拉 */
  let wallKey = $state(0);

  /** 每页响应都带汇总（总数 / 总占用 / 各库计数），只认「当前筛选条件」那份的回包 */
  let summary = $state<LibraryVideosPage | null>(null);

  const signature = $derived(`${String(lib)}|${sort}|${q}|${String(performer)}|${String(channel)}`);

  async function loadPage(cursor: string | null): Promise<{ items: LibraryVideo[]; nextCursor: string | null }> {
    const sig = signature;
    const res = await api.libraryVideos({
      cursor,
      libraryId: lib,
      q: q === '' ? null : q,
      sort,
      performer,
      channel,
      limit: PAGE_SIZE,
    });
    // 回包可能比筛选变化慢（快速连点排序）：只让「还是当前条件」的那份覆盖汇总
    if (sig === signature) summary = res;
    return { items: res.items, nextCursor: res.nextCursor };
  }

  function refresh(): void {
    wallKey += 1;
  }

  /**
   * 顶部头像条的 id 空间：**艺人用正数**（就是 performers.id），**频道用负数**（-yt_channels.id）。
   *
   * 为什么要有这一层：这一页的来源有两种（平台影片的艺人 / YouTube 影片的频道），而
   * `AvatarRail` 只认一个数字 id（三条墙各自的来源都只有一种，所以它们不用操心）。
   * 负数段位是空的，不会和艺人 id 撞；地址栏里仍然是两个说人话的参数（`?performer=38` /
   * `?channel=9`），railId 只是这页内部把「一颗头像」翻译成「哪个参数」的那一步。
   */
  function railIdOf(kind: 'performer' | 'channel', id: number): number {
    return kind === 'channel' ? -id : id;
  }

  const railItems = $derived<RailItem[]>(
    (summary?.sources ?? []).map((src) => ({
      id: railIdOf(src.kind, src.id),
      name: src.name,
      avatarUrl: src.avatarUrl,
      title: `${src.name} · 本地库 ${String(src.fileCount)} 部`,
    })),
  );

  const railSelected = $derived(
    performer !== null ? performer : channel !== null ? -channel : null,
  );

  function setSource(railId: number | null): void {
    if (railId === null) {
      patchQuery({ performer: null, channel: null });
      return;
    }
    // 一次只留一个来源：选频道就把艺人参数摘掉，反之亦然
    if (railId < 0) patchQuery({ channel: String(-railId), performer: null });
    else patchQuery({ performer: String(railId), channel: null });
  }

  function setLib(id: number | null): void {
    patchQuery({ lib: id === null ? null : String(id) });
  }

  function setSort(next: LibrarySort): void {
    patchQuery({ sort: next === 'recent' ? null : next });
  }

  let searchTimer = 0;
  function onSearch(value: string): void {
    searchInput = value;
    window.clearTimeout(searchTimer);
    // 每敲一个字都发请求会把「搜索」变成打点器：停手 260ms 再发
    searchTimer = window.setTimeout(() => {
      patchQuery({ q: value.trim() === '' ? null : value.trim() });
    }, 260);
  }

  // 地址栏变了（后退/前进、贴链接）→ 状态跟上并重拉；相等就跳过，不会和 patchQuery 打转
  $effect(() => {
    const next = readQuery();
    if (
      next.lib === lib &&
      next.sort === sort &&
      next.q === q &&
      next.performer === performer &&
      next.channel === channel
    )
      return;
    lib = next.lib;
    sort = next.sort;
    q = next.q;
    performer = next.performer;
    channel = next.channel;
    if (searchInput.trim() !== next.q) searchInput = next.q;
    wallKey += 1;
  });

  // 下载任务落定 → 库里有新片了。数「已完成」那条数变多就刷新一次，
  // 不用等用户手动刷（AppShell 那边下完会 refreshDownloads）。
  const doneCount = $derived(live.downloads.filter((d) => d.status === 'done').length);
  let seenDone = -1;
  $effect(() => {
    const n = doneCount;
    if (n === seenDone) return;
    const firstTime = seenDone === -1;
    seenDone = n;
    if (firstTime) return;
    wallKey += 1;
  });

  /**
   * 库胶囊的名单来自 /api/libraries（**不是**汇总里的 byLibrary）：
   * byLibrary 是「当前筛选下每个库有几部」，搜索没有结果时它整个是空的，
   * 胶囊会跟着消失——「全部库」那颗也没了，用户只能靠「清空筛选」按钮回去。
   * 计数仍用 byLibrary（带搜索词时降为 0 是实话）。
   */
  let libs = $state<{ id: number; name: string }[]>([]);
  onMount(() => {
    void api
      .libraries()
      .then((res) => {
        libs = res.items.filter((l) => l.enabled).map((l) => ({ id: l.id, name: l.name }));
      })
      .catch(() => {
        /* 拿不到库名就不画胶囊，不影响这一页的其它功能 */
      });
  });
  const counts = $derived(new Map((summary?.byLibrary ?? []).map((x) => [x.libraryId, x.fileCount])));
  const chips = $derived(libs.map((l) => ({ id: l.id, name: l.name, count: counts.get(l.id) ?? 0 })));
  const total = $derived(summary?.total ?? 0);
  const totalBytes = $derived(summary?.totalSizeBytes ?? 0);
  const filtered = $derived(lib !== null || q !== '' || performer !== null || channel !== null);
</script>

<!-- 页面顶部**不画标题**（2026-11-03 用户要求）：和关注 / B 站 / YouTube 三条墙一样，
     顶部直接是头像条——点谁只看谁的下载。页面叫什么由侧边栏高亮和浏览器标题负责。
     来源有两种（艺人 / 频道），railIdOf 里那套负数段位负责把它们塞进同一个 id 空间。 -->
<AvatarRail
  items={railItems}
  label="按来源筛选"
  selected={railSelected}
  onSelect={setSource}
  loading={summary === null}
/>

<div class="flex flex-col gap-4">
  <!-- 汇总行：一共多少部、占多大，右边是「有了新片要刷新 / 去管库」两个出口 -->
  <div class="flex flex-wrap items-center gap-x-3 gap-y-2 text-ui text-muted">
    <p>
      {#if summary === null}
        <span class="text-dim">正在数…</span>
      {:else}
        共 <span class="tabular-nums text-fg/90">{total}</span> 部 ·
        占用 <span class="tabular-nums text-fg/90">{formatBytes(totalBytes)}</span>
      {/if}
    </p>
    <div class="ml-auto flex shrink-0 items-center gap-1">
      <button
        type="button"
        class="rounded-full px-3 py-1.5 text-meta text-muted transition-colors hover:bg-panel hover:text-fg"
        onclick={() => ui.openSettings('libraries')}
      >
        管理本地库
      </button>
      <button
        type="button"
        class="rounded-full px-3 py-1.5 text-meta text-muted transition-colors hover:bg-panel hover:text-fg"
        onclick={refresh}
      >
        刷新
      </button>
    </div>
  </div>

  <!-- 筛选行：库胶囊（多于一个库才画）+ 排序 + 搜索。手机上会折成两行 -->
  <div class="flex flex-wrap items-center gap-2">
    {#if chips.length > 1}
      <div class="flex flex-wrap items-center gap-1">
        <button
          type="button"
          class="rounded-full px-3 py-1 text-meta transition-colors {lib === null
            ? 'bg-raise text-fg'
            : 'text-muted hover:bg-panel hover:text-fg'}"
          onclick={() => setLib(null)}
        >
          全部库
        </button>
        {#each chips as item (item.id)}
          <button
            type="button"
            class="rounded-full px-3 py-1 text-meta transition-colors {lib === item.id
              ? 'bg-raise text-fg'
              : 'text-muted hover:bg-panel hover:text-fg'}"
            title={`只看 ${item.name}（当前筛选下 ${String(item.count)} 部）`}
            onclick={() => setLib(item.id)}
          >
            {item.name} <span class="tabular-nums text-dim">{item.count}</span>
          </button>
        {/each}
      </div>
    {/if}

    <div class="flex shrink-0 items-center gap-0.5 rounded-full bg-panel p-0.5 text-meta">
      {#each SORTS as item (item[0])}
        <button
          type="button"
          class="rounded-full px-2.5 py-1 transition-colors {sort === item[0]
            ? 'bg-raise text-fg'
            : 'text-muted hover:text-fg'}"
          onclick={() => setSort(item[0])}
        >
          {item[1]}
        </button>
      {/each}
    </div>

    <label class="ml-auto flex min-w-0 items-center gap-2 rounded-full bg-panel px-3 py-1.5">
      <span class="sr-only">搜索标题或文件名</span>
      <svg viewBox="0 0 24 24" class="h-4 w-4 shrink-0 text-dim" fill="none" stroke="currentColor" stroke-width="1.7">
        <circle cx="10.75" cy="10.75" r="5.75" />
        <path d="m15.5 15.5 4 4" stroke-linecap="round" />
      </svg>
      <input
        value={searchInput}
        oninput={(event) => onSearch(event.currentTarget.value)}
        type="search"
        placeholder="搜索标题或文件名"
        class="w-40 min-w-0 bg-transparent text-meta text-fg placeholder:text-dim focus:outline-none sm:w-52"
        spellcheck="false"
        autocomplete="off"
      />
    </label>
  </div>

  <InfiniteList
    load={loadPage}
    keyOf={(item: LibraryVideo) => `${String(item.videoId)}-${String(item.libraryId)}`}
    reloadKey={wallKey}
    maxLoadedPages={6}
  >
    {#snippet placeholder()}
      <div class="flex flex-col gap-2">
        <div class="aspect-video w-full rounded-xl bg-panel"></div>
        <div class="h-3 w-4/5 rounded bg-panel"></div>
        <div class="h-3 w-2/5 rounded bg-panel"></div>
      </div>
    {/snippet}

    {#snippet empty()}
      <div class="col-span-full">
        {#if filtered}
          <EmptyState
            title="没有匹配的影片"
            hint="换个搜索词或者切回「全部库」看看。"
            icon="search"
          >
            {#snippet actions()}
              <button
                type="button"
                class="rounded-full bg-raise px-4 py-2 text-ui font-medium text-fg"
                onclick={() => patchQuery({ lib: null, q: null, performer: null, channel: null })}
              >
                清空筛选
              </button>
            {/snippet}
          </EmptyState>
        {:else}
          <EmptyState
            title="媒体库还是空的"
            hint="在关注流、YouTube 或 B 站页点下载，选好本地库，下完的片子就会出现在这里。"
            icon="film"
          >
            {#snippet actions()}
              <a data-link href="/" class="rounded-full bg-hi px-4 py-2 text-ui font-medium text-ink">
                去关注看看
              </a>
              <a data-link href="/downloads" class="rounded-full bg-raise px-4 py-2 text-ui text-fg">
                下载队列
              </a>
            {/snippet}
          </EmptyState>
        {/if}
      </div>
    {/snippet}

    {#snippet children(item: LibraryVideo)}
      <LibraryCard {item} showLibrary={libs.length > 1} />
    {/snippet}
  </InfiniteList>
</div>
