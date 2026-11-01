<script lang="ts">
  /**
   * YouTube：订阅频道的视频墙。
   *
   * 两件事：翻视频墙（16:9 封面墙 + 频道/未看/收藏筛选）、管频道（添加 / 全部标已看）、
   * 顺手下个载（交给后台的 yt_download 任务，下完就是本地库里一部普通影片）。
   *
   * **点卡片不再是弹窗**：整页跳到 `/youtube/:videoId`（见 YoutubeVideo.svelte），
   * 和关注流、B 站点开影片是同一种形态。这一页只负责「挑」。
   * 旧链接 `/youtube?v=<videoId>` 仍在地址栏里出现过（「复制链接」以前复制的是它），
   * 进页面时会被 replace 成新的整页地址，见下面 legacyVideoId 的注释。
   *
   * 契约见 docs/M6-YOUTUBE.md §9；两条抓取通道的差异见 §11。
   * 这里最容易被误解的一点：**「时间未知」是正常态**——深度回填走 yt-dlp flat，
   * 实测 200 条里没有一个时间戳。点开条目时再单独补那一条（4 秒），不批量拉。
   */
  import { onMount } from 'svelte';
  import InfiniteList from '../components/InfiniteList.svelte';
  import TopBar from '../components/TopBar.svelte';
  import EmptyState from '../components/EmptyState.svelte';
  import YtCard from '../components/YtCard.svelte';
  import { api, messageFor } from '../lib/api';
  import { imageUrl } from '../lib/img';
  import { live } from '../lib/live.svelte';
  import { ui } from '../lib/ui.svelte';
  import { navigate, patchQuery, router } from '../lib/router.svelte';
  import type { YtChannel, YtHealth, YtResolve, YtVideo } from '../lib/types';

  type Mode = 'all' | 'unseen' | 'saved';

  const MODES: readonly (readonly [Mode, string])[] = [
    ['all', '全部'],
    ['unseen', '未看'],
    ['saved', '收藏'],
  ];

  // ---- 频道 ----
  let channels = $state<YtChannel[]>([]);
  let channelsLoaded = $state(false);
  let channelsError = $state('');
  let health = $state<YtHealth | null>(null);
  /** 补发布时间正在排队/执行（按钮转圈，避免连点排一串任务） */
  let filling = $state(false);

  // ---- 筛选（唯一真源是地址栏：?channel=<id>&mode=unseen|saved） ----
  /**
   * 选中的频道与「全部 / 未看 / 收藏」都写在 URL 上：点开一条（整页跳走）、刷新、后退
   * 都靠地址栏还原——播放页是另一个组件，回来时这一页是重建的，内存里的状态留不住。
   */
  function readQuery(): { channel: number | null; mode: Mode } {
    const q = router.query;
    const ch = Number.parseInt(q.get('channel') ?? '', 10);
    const m = q.get('mode');
    return {
      channel: Number.isFinite(ch) && ch > 0 ? ch : null,
      mode: m === 'unseen' || m === 'saved' ? m : 'all',
    };
  }

  const initialQuery = readQuery();
  let filterChannel = $state<number | null>(initialQuery.channel);
  let mode = $state<Mode>(initialQuery.mode);
  /** 换筛选条件时 +1，让 InfiniteList 重头拉 */
  let wallKey = $state(0);


  // ---- 添加频道 ----
  let addOpen = $state(false);
  let addUrl = $state('');
  let addBusy = $state(false);
  let addError = $state('');
  let preview = $state<YtResolve | null>(null);

  /**
   * 悬停预热的去重表。预热本身还留着（点击要等 yt-dlp 4~5 秒，提前发一次请求，
   * 服务端把解析结果缓存半小时，翻到播放页时就是热的），但它不再需要在本页存结果：
   * 播放已经搬到 `/youtube/:videoId`，那份结果只对服务端缓存有用。
   */
  const warming = new Set<number>();

  // ---- 正在下载的条目（按钮转圈） ----
  let downloading = $state<Set<number>>(new Set());
  let addInput = $state<HTMLInputElement | null>(null);

  const unseenTotal = $derived(channels.reduce((sum, c) => sum + c.unseenCount, 0));

  async function loadChannels(): Promise<void> {
    channelsError = '';
    try {
      const res = await api.ytChannels();
      channels = res.items;
      live.setYtUnseen(res.stats.unseen);
    } catch (err) {
      channelsError = messageFor(err);
    } finally {
      channelsLoaded = true;
    }
  }

  async function loadHealth(): Promise<void> {
    try {
      health = await api.ytHealth();
    } catch {
      health = null;
    }
  }

  /**
   * 补齐「时间未知」：排一个后台任务，一条一个普通 GET（观看页），跑完 SSE 会把墙重拉一遍。
   * 为什么需要：深度回填走 yt-dlp 的 flat 列表，那个通道一个时间戳都不给，老条目全是未知。
   */
  async function fillDates(): Promise<void> {
    if (filling) return;
    filling = true;
    try {
      const res = await api.ytFillDates();
      if (res.total === 0) {
        live.toast('已经没有缺发布时间的视频了', 'success');
        await loadHealth();
        return;
      }
      live.toast(`正在补齐 ${String(res.total)} 条发布时间`, 'success');
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      filling = false;
    }
  }

  async function loadPage(cursor: string | null): Promise<{ items: YtVideo[]; nextCursor: string | null }> {
    const res = await api.ytVideos({
      cursor,
      channelId: filterChannel,
      unseen: mode === 'unseen',
      saved: mode === 'saved',
      limit: 36,
    });
    return { items: res.items, nextCursor: res.nextCursor ?? null };
  }

  function setChannel(id: number | null): void {
    patchQuery({ channel: id === null ? null : String(id) });
  }

  function setMode(next: Mode): void {
    patchQuery({ mode: next === 'all' ? null : next });
  }

  // 地址栏变了（后退/前进、贴链接）→ 状态跟上并重拉；相等就跳过，不会和 patchQuery 打转。
  $effect(() => {
    const next = readQuery();
    if (next.channel === filterChannel && next.mode === mode) return;
    filterChannel = next.channel;
    mode = next.mode;
    wallKey += 1;
  });

  onMount(() => {
    void loadChannels();
    void loadHealth();
  });

  // 后台抓到新视频时刷新（只刷第一页：墙按发布时间倒序，新的都在最前面）
  let seenVersion = 0;
  $effect(() => {
    const v = live.ytVersion;
    if (v === seenVersion) return;
    seenVersion = v;
    wallKey += 1;
    void loadChannels();
    // 补发布时间的任务跑完也走这条路（AppShell 里 yt_meta done → bumpYt），
    // 顺手把「还缺多少条」重新数一遍。
    void loadHealth();
  });

  // ---- 全部标为已看 ----

  async function markAllSeen(): Promise<void> {
    try {
      let unseen = 0;
      for (const ch of channels) {
        if (ch.unseenCount <= 0) continue;
        const res = await api.ytSeen({ channelId: ch.id });
        unseen = res.unseen;
      }
      live.setYtUnseen(unseen);
      await loadChannels();
      wallKey += 1;
      live.toast('已全部标为已看', 'success');
    } catch (err) {
      live.toast(messageFor(err), 'error');
    }
  }

  // ---- 点开一条 ----

  /**
   * 点卡片＝整页跳到 `/youtube/:videoId`（和关注流、B 站点开影片同一个形态）。
   *
   * 为什么不再用弹窗：弹窗里那块画面最宽只有 1024px、标题小一号、资料挤在标题下面一行，
   * 同一部片在 YouTube 和 B 站/关注流上长得不一样。播放页那套（整页播放器 + 标题行图标 +
   * 右侧资料列）已经是站点统一的看片形态，这里就只保留「挑片」。
   *
   * 频道与「全部 / 未看 / 收藏」都写在地址栏上（`?channel=&mode=`），跳走再后退由路由还原，
   * 所以链接里不用再带 `?back=`：播放页的「上一个 / 下一个」按视频所属频道算，不依赖来源筛选。
   */
  function openVideo(video: YtVideo): void {
    navigate(`/youtube/${encodeURIComponent(video.videoId)}`);
  }

  /**
   * 旧地址：`/youtube?v=<videoId>`（「复制链接」以前复制的是这个，弹窗时代留下的）。
   * 进页面就把它 replace 成整页地址，频道 / 筛选一并带过去——旧链接照样能落到同一条视频上，
   * 而且不会在历史里留一条「一进来就跳走」的记录。
   */
  const legacyVideoId = $derived(router.query.get('v') ?? '');

  $effect(() => {
    const videoId = legacyVideoId;
    if (videoId === '') return;
    const q = new URLSearchParams(router.search);
    q.delete('v');
    const rest = q.toString();
    navigate(`/youtube/${encodeURIComponent(videoId)}${rest === '' ? '' : `?${rest}`}`, {
      replace: true,
    });
  });

  /** 悬停/聚焦预热：把 yt-dlp 那次解析（4~5 秒）挪到用户真正点下之前。 */
  function warmStream(video: YtVideo): void {
    if (warming.has(video.id)) return;
    warming.add(video.id);
    void api
      .ytStream(video.id)
      .catch(() => {})
      .finally(() => warming.delete(video.id));
  }

  // ---- 下载 ----

  async function download(video: YtVideo): Promise<void> {
    if (downloading.has(video.id)) return;
    downloading = new Set(downloading).add(video.id);
    try {
      await api.ytDownload(video.id);
      live.toast('已加入下载队列，下完会进本地库', 'success');
      live.refreshDownloads(true);
    } catch (err) {
      downloading = new Set([...downloading].filter((id) => id !== video.id));
      live.toast(messageFor(err), 'error');
    }
  }

  // ---- 添加频道 ----

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
      preview = await api.ytResolve(raw);
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
      const res = await api.ytAdd(raw);
      live.toast(res.created ? `已订阅 ${res.channel.title}` : `${res.channel.title} 已经订阅过了`, 'success');
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
    if (!addOpen) return;
    event.preventDefault();
    closeAdd();
  }
</script>

<svelte:window onkeydown={onKeydown} />

<!-- 页头自己画：「全部标为已看 / 添加频道」要待在标题那一行，而不是在页内单独占一行
     （那一行原来只在右边飘着两个按钮，左边空着，看着像没画完）。
     这两个按钮的状态（未读总数、添加弹窗）都住在本页，所以标题由本页渲染，见 App.svelte 的 DEFAULT_TITLE。 -->
<TopBar title="YouTube">
  {#snippet actions()}
    {#if unseenTotal > 0}
      <button
        type="button"
        class="rounded-full px-3 py-1.5 text-meta text-muted transition-colors hover:bg-panel hover:text-fg"
        onclick={() => void markAllSeen()}
      >
        全部标为已看
      </button>
    {/if}
    <button
      type="button"
      class="rounded-full bg-hi px-3.5 py-1.5 text-meta font-medium text-ink transition-opacity hover:opacity-90"
      onclick={openAdd}
    >
      添加频道
    </button>
  {/snippet}
</TopBar>

<div class="flex flex-col gap-4">

  <!-- yt-dlp 缺失/过期是这个功能最常见的故障，直接摆在页面上，不藏在设置里 -->
  {#if health && !health.found}
    <div class="flex items-start gap-2 rounded-xl bg-panel px-3 py-2 text-meta text-brick">
      <span class="mt-0.5">●</span>
      <p>
        没找到 yt-dlp：只能抓 RSS（最近 15 条、没有时长），也下载不了。
        <button type="button" class="underline underline-offset-2" onclick={() => ui.openSettings('youtube')}>
          去设置里配置
        </button>
      </p>
    </div>
  {:else if health?.stale}
    <div class="flex items-start gap-2 rounded-xl bg-panel px-3 py-2 text-meta text-brick">
      <span class="mt-0.5">●</span>
      <p>
        yt-dlp {health.version} 已经是 {String(health.ageDays)} 天前的版本，下载很可能 403。
        <button type="button" class="underline underline-offset-2" onclick={() => ui.openSettings('youtube')}>
          去升级
        </button>
      </p>
    </div>
  {/if}

  <!-- 深度回填来的条目没有发布时间（yt-dlp 的 flat 列表不给）：一条条补，别让它一直显示「时间未知」 -->
  {#if (health?.missingDates ?? 0) > 0}
    <div class="flex items-center gap-2 rounded-xl bg-panel px-3 py-2 text-meta text-muted">
      <span class="text-brick">●</span>
      <p class="min-w-0 flex-1">
        有 {String(health?.missingDates)} 条视频没有发布时间（深度回填只给了标题和时长）。
      </p>
      <button
        type="button"
        class="shrink-0 rounded-full px-3 py-1 text-meta text-muted transition-colors hover:bg-raise hover:text-fg disabled:opacity-50"
        disabled={filling}
        onclick={() => void fillDates()}
      >
        {filling ? '排队中…' : '补齐'}
      </button>
    </div>
  {/if}

  {#if channelsError}
    <p class="rounded-xl bg-panel p-3 text-ui text-brick" role="alert">{channelsError}</p>
  {/if}

  {#if channelsLoaded && channels.length > 0}
    <!-- 筛选：频道胶囊 + 右侧的 全部/未看/收藏 -->
    <div class="flex flex-wrap items-center gap-2">
      <div class="-mx-1 flex min-w-0 flex-1 items-center gap-1.5 overflow-x-auto px-1 pb-1">
        <button
          type="button"
          onclick={() => setChannel(null)}
          class="shrink-0 rounded-full px-3 py-1.5 text-meta transition-colors {filterChannel === null
            ? 'bg-raise text-fg'
            : 'text-muted hover:bg-panel hover:text-fg'}"
        >
          全部频道
        </button>
        {#each channels as channel (channel.id)}
          <button
            type="button"
            onclick={() => setChannel(channel.id)}
            title={`${channel.title} · ${String(channel.videoCount)} 条视频`}
            class="flex shrink-0 items-center gap-1.5 rounded-full py-1 pl-1 pr-2.5 text-meta transition-colors {filterChannel ===
            channel.id
              ? 'bg-raise text-fg'
              : 'text-muted hover:bg-panel hover:text-fg'} {channel.enabled ? '' : 'opacity-50'}"
          >
            {#if channel.avatarUrl}
              <img
                src={imageUrl(channel.avatarUrl, 44) ?? ''}
                alt=""
                class="h-5 w-5 rounded-full object-cover"
                loading="lazy"
                decoding="async"
              />
            {:else}
              <span class="grid h-5 w-5 place-items-center rounded-full bg-panel text-[0.625rem] text-dim">
                {channel.title.slice(0, 1).toUpperCase()}
              </span>
            {/if}
            <span class="max-w-[9rem] truncate">{channel.title}</span>
            {#if channel.unseenCount > 0}
              <span class="rounded-full bg-hi px-1 text-[0.625rem] font-semibold leading-4 text-ink tabular-nums">
                {channel.unseenCount > 99 ? '99+' : channel.unseenCount}
              </span>
            {/if}
          </button>
        {/each}
      </div>

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
    </div>
  {/if}

  {#if channelsLoaded && channels.length === 0}
    <EmptyState
      title="还没有订阅任何频道"
      hint="贴一个频道链接、@名或 UC 开头的频道 id 就能订阅；添加后会自动回填最近的历史视频。"
      icon="film"
    >
      {#snippet actions()}
        <button type="button" class="rounded-full bg-hi px-4 py-2 text-ui font-medium text-ink" onclick={openAdd}>
          添加频道
        </button>
      {/snippet}
    </EmptyState>
  {:else}
    <InfiniteList
      load={loadPage}
      keyOf={(video: YtVideo) => video.id}
      reloadKey={wallKey}
      maxLoadedPages={6}
      gridClass="grid grid-cols-1 gap-x-4 gap-y-6 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4"
      onItems={(items: YtVideo[]) => {
        // 列表一到位就把头几条的解析提前跑起来（重复调用会被 warming 挡掉）。
        // 错开 1.5 秒再发下一个：yt-dlp 解析是网络 + 一次 JS 解释，三个一起发会互相抢，
        // 反而把「刚进页面就点第一条」那个场景拖慢——那恰好是最常见的点法。
        items.slice(0, 3).forEach((v, i) => {
          if (i === 0) warmStream(v);
          else setTimeout(() => warmStream(v), i * 1500);
        });
      }}
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
          <EmptyState
            title={mode === 'unseen' ? '没有未看的视频' : mode === 'saved' ? '还没有收藏' : '这个频道还没有视频'}
            hint={mode === 'all'
              ? '抓取任务可能还没跑完，去 管理 → YouTube 点一次「深度回填」试试。'
              : '换个筛选条件看看。'}
            icon="film"
          />
        </div>
      {/snippet}

      {#snippet children(video: YtVideo)}
        <YtCard
          {video}
          onOpen={(v) => openVideo(v)}
          onWarm={(v) => warmStream(v)}
          onDownload={(v) => void download(v)}
          downloading={downloading.has(video.id)}
        />
      {/snippet}
    </InfiniteList>
  {/if}
</div>

<!-- 添加频道 -->
{#if addOpen}
  <div class="fixed inset-0 z-50 flex items-end justify-center sm:items-center">
    <button type="button" class="absolute inset-0 bg-ink/85 backdrop-blur-sm" aria-label="关闭" onclick={closeAdd}
    ></button>
    <div
      class="relative z-10 w-full max-w-lg rounded-t-2xl bg-sheet p-4 sm:rounded-2xl sm:p-5"
      role="dialog"
      aria-label="添加 YouTube 频道"
    >
      <h2 class="text-title font-medium text-fg">添加 YouTube 频道</h2>
      <p class="mt-1 text-meta text-dim">
        支持频道链接、@名 或 UC 开头的频道 id；贴一条视频链接也行（会反查它属于哪个频道）。
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
          placeholder="https://www.youtube.com/@handle"
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
              {preview.title.slice(0, 1).toUpperCase()}
            </span>
          {/if}
          <div class="min-w-0 flex-1">
            <p class="truncate text-ui font-medium text-fg">{preview.title}</p>
            <p class="truncate font-mono text-[0.6875rem] text-dim">
              {preview.handle ? `@${preview.handle} · ` : ''}{preview.channelId}
            </p>
            {#if !preview.ytdlpOk}
              <p class="mt-1 text-meta text-brick">没有 yt-dlp：只能抓最近 15 条，也没有时长与下载。</p>
            {:else if preview.subscribed}
              <p class="mt-1 text-meta text-dim">已经订阅过了，再加一次不会重复。</p>
            {/if}
          </div>
        </div>
      {/if}

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

