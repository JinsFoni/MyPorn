<script lang="ts">
  /**
   * YouTube：订阅频道的视频墙。
   *
   * 三件事：看视频（16:9 封面墙 + 播放弹窗）、管频道（添加 / 筛选 / 全部标已看）、
   * 下载（交给后台的 yt_download 任务，下完就是本地库里一部普通影片）。
   *
   * 契约见 docs/M6-YOUTUBE.md §9；两条抓取通道的差异见 §11。
   * 这里最容易被误解的一点：**「时间未知」是正常态**——深度回填走 yt-dlp flat，
   * 实测 200 条里没有一个时间戳。点开条目时再单独补那一条（4 秒），不批量拉。
   */
  import { onMount } from 'svelte';
  import InfiniteList from '../components/InfiniteList.svelte';
  import EmptyState from '../components/EmptyState.svelte';
  import YtCard from '../components/YtCard.svelte';
  import VideoPlayer from '../components/VideoPlayer.svelte';
  import VideoLoading from '../components/VideoLoading.svelte';
  import { api, messageFor } from '../lib/api';
  import { imageUrl } from '../lib/img';
  import { live } from '../lib/live.svelte';
  import { privacy } from '../lib/privacy.svelte';
  import { ui } from '../lib/ui.svelte';
  import { patchQuery, router, setPageTitle } from '../lib/router.svelte';
  import { formatBandwidth, formatCount, formatDateTime, relativeTime } from '../lib/format';
  import { HLS_TYPE, codecFamily, type QualityOption } from '../lib/player';
  import type { YtChannel, YtHealth, YtResolve, YtStreamInfo, YtVideo } from '../lib/types';

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

  // ---- 筛选 ----
  // ---- 筛选（唯一真源是地址栏：?channel=<id>&mode=unseen|saved） ----
  /**
   * 选中的频道与「全部 / 未看 / 收藏」都写在 URL 上：播放弹窗、刷新、后退都靠地址栏还原，
   * 打开某条影片时也只是在查询串上追加 `v=`（见 openDetail），不丢频道。
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

  // ---- 墙上的条目（弹窗要在里面找到最新的一份） ----
  let videos = $state<YtVideo[]>([]);
  let detailOpen = $state(false);
  let detail = $state<YtVideo | null>(null);

  // ---- 添加频道 ----
  let addOpen = $state(false);
  let addUrl = $state('');
  let addBusy = $state(false);
  let addError = $state('');
  let preview = $state<YtResolve | null>(null);

  // ---- 站内播放 ----
  // 不走 YouTube 的 iframe：实测一批版权音乐视频在嵌入里直接被拒播（见 handlers_ytstream.go 顶部）。
  // 这里放的是后端代理出来的 HLS，画质能到 4K，也没有广告。
  let stream = $state<YtStreamInfo | null>(null);
  let streamLoading = $state(false);
  let streamError = $state('');
  let playError = $state('');
  /** 选中的画质高度；null = 最高 */
  let streamHeight = $state<number | null>(null);
  let selectedQualityId = $state<string | null>(null);
  /** 解析失败时的兜底：改用 YouTube 内嵌播放器（隐私模式下不给这个选项） */
  let useEmbed = $state(false);
  /** 重试用：变一下就重建播放器 */
  let streamAttempt = $state(0);
  /**
   * 已经拿到的解析结果（悬停预热的那份）。一次点击原来要等 yt-dlp 4~5 秒，
   * 悬停/聚焦时就先要过来，点下去时通常已经是热缓存。
   */
  const streamCache = new Map<string, { info: YtStreamInfo; at: number }>();
  const warming = new Set<number>();

  // ---- 正在下载的条目（按钮转圈） ----
  let downloading = $state<Set<number>>(new Set());
  let refreshing = $state(false);
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
  });

  // ---- 已看 / 收藏 ----

  function patchLocal(id: number, patch: Partial<YtVideo>): void {
    videos = videos.map((v) => (v.id === id ? { ...v, ...patch } : v));
    if (detail?.id === id) detail = { ...detail, ...patch };
  }

  async function markSeen(ids: number[]): Promise<void> {
    if (ids.length === 0) return;
    try {
      const res = await api.ytSeen({ ids });
      videos = videos.map((v) => (ids.includes(v.id) ? { ...v, seen: true } : v));
      if (detail && ids.includes(detail.id)) detail = { ...detail, seen: true };
      live.setYtUnseen(res.unseen);
      // 频道胶囊上的未看数也要跟着掉，不然两处数字对不上
      void loadChannels();
    } catch {
      /* 标已看失败不值得弹错 */
    }
  }

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

  async function toggleSave(video: YtVideo): Promise<void> {
    try {
      const res = await api.ytSave(video.id, !video.saved);
      patchLocal(video.id, { saved: res.saved });
      if (mode === 'saved' && !res.saved) wallKey += 1;
    } catch (err) {
      live.toast(messageFor(err), 'error');
    }
  }

  // ---- 播放弹窗 ----

  /**
   * 弹窗的唯一开关是 URL：`/youtube?v=<videoId>`。
   * 这样刷新、后退、把链接贴到另一台机器都能落到同一条视频上，地址栏也不再永远是 /youtube。
   */
  const wantVideoId = $derived(router.query.get('v') ?? '');

  function openDetail(video: YtVideo): void {
    if (router.query.get('v') === video.videoId) return;
    // push 一条历史：后退＝关弹窗。频道 / 筛选参数一路带着（patchQuery 只改 v）。
    patchQuery({ v: video.videoId }, { replace: false });
  }

  // URL 一变就同步弹窗：点卡片、浏览器后退、刷新、贴深链接走的是同一条路。
  $effect(() => {
    const videoId = wantVideoId;
    if (videoId === '') {
      if (detailOpen) hideDetail();
      return;
    }
    if (detailOpen && detail?.videoId === videoId) return;
    void openByVideoId(videoId);
  });

  /** 墙上已经有这条就直接用；深链接进来时墙上（36 条 + 筛选）可能没有，单取一次。 */
  async function openByVideoId(videoId: string): Promise<void> {
    let video = videos.find((item) => item.videoId === videoId) ?? null;
    if (!video) {
      try {
        video = await api.ytVideoLookup(videoId);
      } catch (err) {
        live.toast(messageFor(err), 'error');
        patchQuery({ v: null });
        return;
      }
      // 等查询的这段时间用户可能已经退回去了，别再弹出来
      if (router.query.get('v') !== videoId) return;
    }
    showDetail(video);
  }

  function showDetail(video: YtVideo): void {
    detail = video;
    detailOpen = true;
    stream = null;
    streamError = '';
    playError = '';
    useEmbed = false;
    streamHeight = null;
    selectedQualityId = null;
    setPageTitle(video.title);
    if (!video.seen) void markSeen([video.id]);
    // 深度回填来的条目没有发布时间：点开时单独补一条（4 秒），不批量拉
    if (!video.dateKnown && health?.found) void fillMeta(video);
    void loadStream(video);
  }

  async function copyLink(): Promise<void> {
    const v = detail;
    if (!v) return;
    const url = `${window.location.origin}/youtube?v=${encodeURIComponent(v.videoId)}`;
    try {
      await navigator.clipboard.writeText(url);
      live.toast('播放链接已复制', 'success');
    } catch {
      live.toast('复制失败，请手动复制地址栏', 'error');
    }
  }

  /** 十秒内预热到的结果可直接用；过期就当作没预热过。 */
  function cachedStream(video: YtVideo): YtStreamInfo | null {
    const hit = streamCache.get(video.videoId);
    return hit && Date.now() - hit.at < 10 * 60_000 ? hit.info : null;
  }

  /** 悬停/聚焦预热：只是把请求提前发出去，失败就忘掉，不打扰用户。 */
  function warmStream(video: YtVideo): void {
    if (cachedStream(video) || warming.has(video.id)) return;
    warming.add(video.id);
    void api
      .ytStream(video.id)
      .then((info) => streamCache.set(video.videoId, { info, at: Date.now() }))
      .catch(() => {})
      .finally(() => warming.delete(video.id));
  }

  /** 解析播放地址（后端跑一次 yt-dlp，结果在服务端缓存到签名过期，切画质不会再解析）。 */
  async function loadStream(video: YtVideo): Promise<void> {
    streamLoading = true;
    streamError = '';
    playError = '';
    try {
      // hls.js 是单独一个 chunk：只有真要放视频时才下载
      await import('@videojs/html/media/hlsjs-video');
      stream = cachedStream(video) ?? (await api.ytStream(video.id));
      selectedQualityId = qualityIdFor(stream, null);
    } catch (err) {
      streamError = messageFor(err);
    } finally {
      streamLoading = false;
    }
  }

  /** 画质 id：用「高度p」；null 表示最高那档。 */
  function qualityIdFor(info: YtStreamInfo | null, height: number | null): string | null {
    if (!info || info.qualities.length === 0) return null;
    const h = height ?? info.default;
    return `${h}p`;
  }

  /** 当前片源地址：切画质就是换 ?q=。 */
  const streamSrc = $derived(
    stream ? (streamHeight ? `${stream.src}?q=${streamHeight}` : stream.src) : '',
  );

  const qualityOptions = $derived<QualityOption[]>(
    (stream?.qualities ?? []).map((q) => ({
      id: q.label,
      label: q.label,
      hint: [codecLabel(q.codecs), q.bandwidth > 0 ? formatBandwidth(q.bandwidth) : '']
        .filter((part) => part !== '')
        .join(' · '),
    })),
  );

  function codecLabel(codecs: string): string {
    switch (codecFamily(codecs)) {
      case 'av1':
        return 'AV1';
      case 'hevc':
        return 'HEVC';
      case 'vp9':
        return 'VP9';
      case 'avc':
        return 'H.264';
      default:
        return '';
    }
  }

  function applyQuality(id: string): void {
    const height = Number.parseInt(id, 10);
    streamHeight = Number.isFinite(height) && height > 0 ? height : null;
    selectedQualityId = id;
    playError = '';
  }

  function retryStream(): void {
    playError = '';
    // 重建播放器并重新要一份清单：上游签名过期（会话 410/分片 403）是这里最常见的失败，
    // 光重挂同一个地址没用，得让后端重新解析、重新开一个会话。
    streamAttempt += 1;
    stream = null;
    streamError = '';
    if (detail) void loadStream(detail);
  }

  function switchToEmbed(): void {
    useEmbed = true;
    playError = '';
  }

  function onStreamFailed(message: string): void {
    playError = message === '' ? '站内播放失败，请重试。' : `站内播放失败（${message}）`;
  }

  function closeDetail(): void {
    if (wantVideoId !== '') {
      // 关弹窗＝把 ?v= 去掉；用 replace，免得后退又把它弹回来。频道 / 筛选留在 URL 里。
      patchQuery({ v: null });
      return;
    }
    hideDetail();
  }

  function hideDetail(): void {
    detailOpen = false;
    playError = '';
    stream = null;
    streamError = '';
  }

  async function fillMeta(video: YtVideo): Promise<void> {
    if (refreshing) return;
    refreshing = true;
    try {
      const fresh = await api.ytRefresh(video.id);
      patchLocal(video.id, fresh);
      void loadChannels();
    } catch {
      /* 补不到就算了，墙上照旧显示「时间未知」 */
    } finally {
      refreshing = false;
    }
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
    if (addOpen) {
      event.preventDefault();
      closeAdd();
    } else if (detailOpen) {
      event.preventDefault();
      closeDetail();
    }
  }
</script>

<svelte:window onkeydown={onKeydown} />

<div class="flex flex-col gap-4">
  <!-- 顶部：只有主动作。「动态 / YouTube」的分段切换只留在这条流的入口（/moments）：
       进了 YouTube 以后，回「动态」走侧边栏（手机上走底栏第 2 格）就行，
       两处都给反而像两个页面互相抢入口。 -->
  <div class="flex flex-wrap items-center justify-end gap-3">
    <div class="flex items-center gap-2">
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
    </div>
  </div>

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
        videos = items;
        // 列表一到位就把头几条的解析提前跑起来（重复调用会被 cachedStream/warming 挡掉）。
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
          onOpen={(v) => openDetail(v)}
          onWarm={(v) => warmStream(v)}
          onDownload={(v) => void download(v)}
          downloading={downloading.has(video.id)}
        />
      {/snippet}
    </InfiniteList>
  {/if}
</div>

<!-- 播放弹窗 -->
{#if detailOpen && detail}
  {@const v = detail}
  <div class="fixed inset-0 z-50 flex items-end justify-center sm:items-center">
    <button type="button" class="absolute inset-0 bg-ink/85 backdrop-blur-sm" aria-label="关闭" onclick={closeDetail}
    ></button>
    <div
      class="relative z-10 max-h-[92dvh] w-full overflow-y-auto rounded-t-2xl bg-sheet p-4 sm:max-w-4xl sm:rounded-2xl md:max-w-5xl lg:max-w-[min(92vw,84rem)]"
    >
      <div class="flex items-start justify-between gap-3">
        <h2 class="text-title font-medium leading-snug text-fg">{v.title}</h2>
        <button
          type="button"
          class="shrink-0 rounded-full p-1.5 text-dim transition-colors hover:bg-panel hover:text-fg"
          onclick={closeDetail}
          aria-label="关闭"
        >
          <svg viewBox="0 0 24 24" class="h-4 w-4" fill="none" stroke="currentColor" stroke-width="2">
            <path d="m6 6 12 12M18 6 6 18" stroke-linecap="round" />
          </svg>
        </button>
      </div>

      <!-- 播放器：默认走服务端出的 HLS（画质到 4K、无广告、隐私模式下也能看）。
           内嵌 iframe 只在解析失败时当兜底，隐私模式下连这个兜底也不给。 -->
      {#if useEmbed}
        <iframe
          class="mt-3 aspect-video w-full rounded-xl bg-ink"
          src={v.embedUrl}
          title={v.title}
          allow="accelerometer; encrypted-media; gyroscope; picture-in-picture; fullscreen"
          allowfullscreen
        ></iframe>
      {:else if stream}
        {#key streamAttempt}
          <div class="mt-3 overflow-hidden rounded-xl bg-black">
            <VideoPlayer
              src={streamSrc}
              type={HLS_TYPE}
              engine="hls"
              poster={v.posterUrl || v.thumbUrl}
              posterFallback={v.thumbUrl}
              title={v.title}
              videoKey={v.id}
              qualities={qualityOptions}
              selectedQuality={selectedQualityId}
              onSelectQuality={applyQuality}
              onFailed={onStreamFailed}
            >
              {#snippet overlay()}
                {#if playError}
                  <div
                    class="pointer-events-auto absolute inset-0 flex flex-col items-center justify-center gap-3 bg-ink/85 px-6 text-center"
                  >
                    <p class="max-w-md text-ui text-fg/90">{playError}</p>
                    <div class="flex flex-wrap items-center justify-center gap-2">
                      <button type="button" class="btn" onclick={retryStream}>重试</button>
                      {#if !privacy.on}
                        <button type="button" class="btn btn-primary" onclick={switchToEmbed}>
                          改用 YouTube 播放器
                        </button>
                      {/if}
                    </div>
                  </div>
                {/if}
              {/snippet}
            </VideoPlayer>
          </div>
        {/key}
      {:else}
        <div class="relative mt-3 aspect-video w-full overflow-hidden rounded-xl bg-black">
          {#if streamLoading}
            <VideoLoading
              poster={v.posterUrl || v.thumbUrl}
              posterFallback={v.thumbUrl}
              kind="poster"
              label="正在解析最高画质"
            />
          {/if}
          {#if streamError}
            <div class="absolute inset-0 flex flex-col items-center justify-center gap-3 px-6 text-center">
              <p class="max-w-md text-ui text-fg/90">{streamError}</p>
              <div class="flex flex-wrap items-center justify-center gap-2">
                <button type="button" class="btn" onclick={() => void loadStream(v)}>重新解析</button>
                {#if !privacy.on}
                  <button type="button" class="btn btn-primary" onclick={switchToEmbed}>改用 YouTube 播放器</button>
                {/if}
              </div>
            </div>
          {:else if !streamLoading}
            <div class="absolute inset-0 grid place-items-center px-6 text-center">
              <p class="text-meta text-muted">没有可播的地址。</p>
            </div>
          {/if}
        </div>
      {/if}

      <div class="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 text-meta text-dim">
        {#if v.channel.avatarUrl}
          <img src={imageUrl(v.channel.avatarUrl, 32) ?? ''} alt="" class="h-5 w-5 rounded-full object-cover" />
        {/if}
        <span class="text-muted">{v.channel.title}</span>
        {#if v.views > 0}
          <span>{formatCount(v.views)} 次观看</span>
        {/if}
        {#if v.publishedAt}
          <span>{formatDateTime(v.publishedAt)}（{relativeTime(v.publishedAt)}）</span>
        {:else}
          <span class="text-dim">时间未知</span>
          {#if health?.found}
            <button
              type="button"
              class="rounded-full bg-raise px-2 py-0.5 text-meta text-fg transition-opacity hover:opacity-90 disabled:opacity-50"
              onclick={() => void fillMeta(v)}
              disabled={refreshing}
            >
              {refreshing ? '查询中…' : '查一下发布时间'}
            </button>
          {/if}
        {/if}
      </div>

      {#if v.description}
        <p class="mt-3 whitespace-pre-line text-ui leading-relaxed text-muted line-clamp-[10]">{v.description}</p>
      {/if}

      <div class="mt-4 flex flex-wrap items-center gap-2">
        <button
          type="button"
          class="rounded-full bg-hi px-3.5 py-1.5 text-meta font-medium text-ink transition-opacity hover:opacity-90 disabled:opacity-60"
          onclick={() => void download(v)}
          disabled={downloading.has(v.id)}
        >
          {downloading.has(v.id) ? '已排队' : v.downloaded ? '再下一次' : '下载到本地库'}
        </button>
        <button
          type="button"
          class="rounded-full bg-raise px-3.5 py-1.5 text-meta text-fg transition-colors hover:bg-panel"
          onclick={() => void toggleSave(v)}
        >
          {v.saved ? '取消收藏' : '收藏'}
        </button>
        <button
          type="button"
          class="rounded-full px-3 py-1.5 text-meta text-muted transition-colors hover:bg-panel hover:text-fg"
          onclick={() => void copyLink()}
        >
          复制链接
        </button>
        <a
          href={v.url}
          target="_blank"
          rel="noreferrer"
          class="rounded-full px-3 py-1.5 text-meta text-muted transition-colors hover:bg-panel hover:text-fg"
        >
          在 YouTube 打开
        </a>
      </div>
    </div>
  </div>
{/if}

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
