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
  import TopBar from '../components/TopBar.svelte';
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
  import {
    formatBandwidth,
    formatCount,
    formatDateTime,
    relativeTime,
  } from '../lib/format';
  import { HLS_TYPE, codecFamily, type QualityOption } from '../lib/player';
  import type {
    YtChannel,
    YtHealth,
    YtResolve,
    YtStreamInfo,
    YtStreamQuality,
    YtVideo,
  } from '../lib/types';

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
   * 上一次从播放器报上来的进度。换画质会重建播放器（两条路的清单地址不同），
   * 换完把进度接回去，不然用户看到的是「切个画质又被扔回片头」。
   */
  let lastPos = $state(0);
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

  /**
   * 弹窗里的「上一个 / 下一个」：就在墙上这一份列表里前后挪一格。
   *
   * 用 `videos`（墙上已经加载出来的条目）而不是再问后端：这页的列表本来就是分页加载的，
   * 用户看到的顺序就是它，切歌跟着这份顺序走最不容易出意外。深链接进来的片子可能不在
   * 墙上（`findIndex` 为 -1），那时两个按钮都按下去——没有上下文就不猜。
   */
  const detailIndex = $derived(detail === null ? -1 : videos.findIndex((v) => v.videoId === detail?.videoId));
  const prevDetail = $derived(detailIndex > 0 ? videos[detailIndex - 1] : null);
  const nextDetail = $derived(detailIndex >= 0 && detailIndex < videos.length - 1 ? videos[detailIndex + 1] : null);

  function openDetail(video: YtVideo): void {
    if (router.query.get('v') === video.videoId) return;
    // push 一条历史：后退＝关弹窗。频道 / 筛选参数一路带着（patchQuery 只改 v）。
    // keepScroll：弹窗是「同一页上的浮层」，地址栏只多了个 `?v=`。
    // 不带这个参数时路由会把页面滚回顶部——开一次弹窗滚一次顶，关掉人就在顶部了。
    patchQuery({ v: video.videoId }, { replace: false, keepScroll: true });
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
        patchQuery({ v: null }, { keepScroll: true });
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
    lastPos = 0;
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

  /** 取某一档（null = 默认最高）。 */
  function qualityAt(info: YtStreamInfo | null, height: number | null): YtStreamQuality | null {
    if (!info || info.qualities.length === 0) return null;
    const h = height ?? info.default;
    return info.qualities.find((q) => q.height === h) ?? null;
  }

  /** 当前这一档（决定走不走原生播放）。 */
  const activeQuality = $derived(qualityAt(stream, streamHeight));
  /** 这一档要不要走「单文件 HLS」（AV1 分片 mp4 按 sidx 切分片，目前只有 2160p）。 */
  const usesDash = $derived(activeQuality?.dash === true);

  /** HLS 地址：切画质就是换 ?q=。 */
  const streamSrc = $derived(
    stream ? (streamHeight ? `${stream.src}?q=${streamHeight}` : stream.src) : '',
  );
  /**
   * 4K 的地址：`/dash.m3u8` 把 AV1 分片 mp4 按 sidx 切成 HLS 分片发，起播只要第一段。
   *
   * 建不起来（视频没有 AV1 直链 / 不是分片 mp4）时后端会 302 回 VP9 那份清单，
   * 所以这里不用额外判断——能播就播，播不了 hls.js 会正常报错并给出重试。
   */
  const dashSrc = $derived(
    stream && stream.dash && activeQuality
      ? `${stream.dash}?q=${String(activeQuality.height)}`
      : '',
  );
  const playbackSrc = $derived(usesDash ? dashSrc || streamSrc : streamSrc);
  const playbackType = $derived(HLS_TYPE);
  /**
   * 这会儿画面里有没有播放器（`<video-skin>`）。
   *
   * 关闭键必须**画在播放器里面**，不能当它的兄弟：播放器一旦发现指针离开了自己，
   * 立刻把控制条收起来，而关闭键的显隐是跟着控制条走的——于是「鼠标移到关闭键上」
   * 会被播放器当成「指针离开了」，控制条 + 关闭键当场淡出、`pointer-events` 变 none，
   * 指针落到画面上（`cursor:none`），光标就消失了，点一下还会变成播放/暂停。
   * 实测（1920×1080 窗口，HAVC 片段播放中）：唤醒控制条后把指针移到关闭键上，
   * +0ms `data-controls-visible` 就没了、+200ms 关闭键透明度归零。
   * 插进播放器的浮层里（`.overlay-layer`）之后，同一操作 controls 一直为真、光标保持 default。 */
  const hasPlayer = $derived(!useEmbed && !!stream && !!playbackSrc);
  const playbackEngine = $derived<'hls' | 'native'>('hls');

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
    const next = Number.isFinite(height) && height > 0 ? height : null;
    streamHeight = next;
    selectedQualityId = id;
    playError = '';
    // 换档就重建播放器（{#key} 里带着 streamAttempt），进度靠 startAt 接回来
    streamAttempt += 1;
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
      // 同理不能滚：关掉之后要留在原来那个格子附近。
      patchQuery({ v: null }, { keepScroll: true });
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
      // 补上一条，顶上那行「N 条没有发布时间」的数字也要跟着减
      if (fresh.dateKnown) void loadHealth();
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
  <!-- 关闭键：跟播放器底部控制条（.video-controls）同一套——白色 10% 底 + 16px 背景模糊 +
       99px 圆角 + 一圈 1px 暗环。形状跟着控制条那根药丸走：比高更宽（42×28，左右各 14 的内距把
       14px 图标裹在中间），不是一颗圆点；高度比控制条里的按钮（36×36）矮一号，不带文字。
       位置在画面右上角；窄屏（< 32rem）要往下让一截，原因见下面 .yt-close 的注释。
       有播放器时这颗键画在播放器浮层里（理由见 hasPlayer 的注释），所以这里的定位是相对画面框，
       两种画法位置一样（播放器浮层就是画面框那么大）。 -->
  {#snippet closeBtn()}
    <button
      type="button"
      class="yt-close absolute right-3 top-3 z-30 flex h-7 items-center justify-center rounded-full px-3.5 text-fg"
      onclick={(e) => {
        // 这颗键现在画在播放器里，点下去会顺着组合路径冒泡到播发器的「点画面=播放/暂停」
        e.stopPropagation();
        closeDetail();
      }}
      aria-label="关闭"
    >
      <svg viewBox="0 0 24 24" class="h-3.5 w-3.5" fill="none" stroke="currentColor" stroke-width="2.2">
        <path d="m6 6 12 12M18 6 6 18" stroke-linecap="round" />
      </svg>
    </button>
  {/snippet}
  <div class="fixed inset-0 z-50 flex items-end justify-center sm:items-center">
    <button type="button" class="absolute inset-0 bg-ink/85 backdrop-blur-sm" aria-label="关闭" onclick={closeDetail}
    ></button>
    <div
      class="yt-sheet relative z-10 max-h-[92dvh] w-full overflow-y-auto rounded-t-2xl bg-sheet p-4 sm:max-w-4xl sm:rounded-2xl md:max-w-5xl"
      role="dialog"
      aria-modal="true"
      aria-label={v.title}
    >
      <!-- 画面（解析中 / 失败那张兜底也算）和右上角的关闭键在同一个容器里：
           关闭键平时不显示，跟着播放器底部控制条一起进出（有播放器时看 video-skin 上的
           data-controls-visible，没有播放器时才按 hover）。 -->
      <div class="yt-media relative">
        <!-- 播放器：默认走服务端出的 HLS（画质到 4K、无广告、隐私模式下也能看）。
             内嵌 iframe 只在解析失败时当兜底，隐私模式下连这个兜底也不给。 -->
        {#if useEmbed}
          <iframe
            class="aspect-video w-full rounded-xl bg-ink"
            src={v.embedUrl}
            title={v.title}
            allow="accelerometer; encrypted-media; gyroscope; picture-in-picture; fullscreen"
            allowfullscreen
          ></iframe>
        {:else if stream && playbackSrc}
          <!-- 换档要重建播放器：2160p 和别的档是两个不同的清单地址，不能指望 hls.js 原地换
               （进度靠 onTime/startAt 接回来） -->
          {#key `${String(streamAttempt)}-${playbackEngine}-${v.videoId}`}
            <div class="overflow-hidden rounded-xl bg-black">
              <VideoPlayer
                src={playbackSrc}
                type={playbackType}
                engine={playbackEngine}
                startAt={lastPos}
                onTime={(t) => (lastPos = t)}
                poster={v.posterUrl || v.thumbUrl}
                posterFallback={v.thumbUrl}
                title={v.title}
                videoKey={v.id}
                qualities={qualityOptions}
                selectedQuality={selectedQualityId}
                onSelectQuality={applyQuality}
                prevTitle={prevDetail?.title ?? null}
                nextTitle={nextDetail?.title ?? null}
                onPrev={() => { if (prevDetail) openDetail(prevDetail); }}
                onNext={() => { if (nextDetail) openDetail(nextDetail); }}
                onEnded={() => { if (nextDetail) openDetail(nextDetail); }}
                onFailed={onStreamFailed}
                hideIdleCursor={false}
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
                  {@render closeBtn()}
                {/snippet}
              </VideoPlayer>
            </div>
          {/key}
        {:else}
          <div class="relative aspect-video w-full overflow-hidden rounded-xl bg-black">
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

        <!-- 没有播放器时（解析中 / 解析失败 / 兜底 iframe）关闭键只能当画面的兄弟，按 hover 显隐。 -->
        {#if !hasPlayer}
          {@render closeBtn()}
        {/if}
      </div>

      <!-- 标题在播放器之后：弹窗一眼先看到画面，标题紧挨着频道/播放量这一行。
           原来的「下载 / 收藏 / 复制链接 / 在 YouTube 打开」是标题下面一排带字的药丸，
           四条文字在描述之前又占一行、跟正文连成一片；收成四颗图标跟在标题右侧，
           原文字挪进 title，鼠标停上去仍读得到。 -->
      <div class="mt-3 flex items-start gap-3">
        <h2 class="min-w-0 flex-1 text-title font-medium leading-snug text-fg">{v.title}</h2>

        <div class="flex shrink-0 items-center gap-1">
          <button
            type="button"
            class="yt-act"
            data-emphasis="primary"
            title={downloading.has(v.id) ? '正在排队下载' : v.downloaded ? '已在本地库，再下一次' : '下载到本地库'}
            aria-label={downloading.has(v.id) ? '正在排队下载' : '下载到本地库'}
            onclick={() => void download(v)}
            disabled={downloading.has(v.id)}
          >
            {#if downloading.has(v.id)}
              <svg viewBox="0 0 24 24" class="h-4 w-4 animate-spin" fill="none" stroke="currentColor" stroke-width="2">
                <circle cx="12" cy="12" r="8" stroke-opacity="0.25" />
                <path d="M20 12a8 8 0 0 0-8-8" />
              </svg>
            {:else}
              <svg
                viewBox="0 0 24 24"
                class="h-4 w-4"
                fill="none"
                stroke="currentColor"
                stroke-width="1.8"
                stroke-linecap="round"
              >
                <path d="M12 4v10" />
                <path d="m8 10.5 4 4 4-4" />
                <path d="M4.5 18.5h15" />
              </svg>
            {/if}
          </button>

          <button
            type="button"
            class="yt-act"
            data-saved={v.saved ? '' : undefined}
            title={v.saved ? '取消收藏' : '收藏'}
            aria-label={v.saved ? '取消收藏' : '收藏'}
            onclick={() => void toggleSave(v)}
          >
            <svg
              viewBox="0 0 24 24"
              class="h-4 w-4"
              fill={v.saved ? 'currentColor' : 'none'}
              stroke="currentColor"
              stroke-width="1.8"
              stroke-linejoin="round"
            >
              <path d="m12 3.6 2.6 5.3 5.9.9-4.3 4.1 1 5.8-5.2-2.7-5.2 2.7 1-5.8L3.5 9.8l5.9-.9z" />
            </svg>
          </button>

          <button
            type="button"
            class="yt-act"
            title="复制播放链接"
            aria-label="复制播放链接"
            onclick={() => void copyLink()}
          >
            <svg viewBox="0 0 24 24" class="h-4 w-4" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round">
              <path d="M10.2 13.4a4.5 4.5 0 0 0 6.7.4l1.9-1.9a4.5 4.5 0 0 0-6.4-6.4l-1.1 1.1" />
              <path d="M13.8 10.6a4.5 4.5 0 0 0-6.7-.4l-1.9 1.9a4.5 4.5 0 0 0 6.4 6.4l1.1-1.1" />
            </svg>
          </button>

          <a
            href={v.url}
            target="_blank"
            rel="noreferrer"
            class="yt-act"
            title="在 YouTube 打开"
            aria-label="在 YouTube 打开"
          >
            <svg viewBox="0 0 24 24" class="h-4 w-4" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round">
              <path d="M18 13.5V18a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h4.5" />
              <path d="M15 3.5h5.5V9" />
              <path d="M20.2 3.8 11 13" />
            </svg>
          </a>
        </div>
      </div>

      <div class="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-meta text-dim">
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

<style>
  /* 弹窗宽度不再只看视口宽：标题挪到播放器下面之后，视频顶到弹窗底下就把标题挤出了可视区
     （1440×900 实测：视频 745 高、标题落在 y=831，弹窗底 864，标题只露半行）。
     这里再叠一条「按高度反算宽度」的上限，让「顶栏 + 视频 + 标题 + 频道行」在不滚动时正好放得下：
     92dvh 减去 7.5rem（上下内距 2rem + 关闭键 28px + 间距 + 标题 + 频道行）。 */
  @media (min-width: 64rem) {
    .yt-sheet {
      max-width: min(92vw, 84rem, calc((92dvh - 7.5rem) * 16 / 9 + 2rem));
    }
  }

  /* 标题行右上角那四颗图标键：32×32 圆点，图标 16px。
     和画面上的关闭键不是一套——那是压在视频上、要半透明才不挡画面；
     这几颗在弹窗正文里，默认不铺底，hover / 键盘聚焦才浮一层 panel（冷白 5%）。
     下载那颗用 data-emphasis="primary" 提亮到 fg：它是这四个里唯一的「写」操作。 */
  .yt-act {
    display: grid;
    width: 2rem;
    height: 2rem;
    place-items: center;
    border-radius: 9999px;
    color: var(--color-muted);
    transition:
      background-color 140ms ease,
      color 140ms ease;
  }

  .yt-act:hover:not(:disabled),
  .yt-act:focus-visible {
    background-color: var(--color-panel);
    color: var(--color-fg);
  }

  .yt-act[data-emphasis='primary'] {
    color: var(--color-fg);
  }

  /* 已收藏：星星填实并提亮，和列表卡片右上角那颗保持同一个信号 */
  .yt-act[data-saved] {
    color: var(--color-hi);
  }

  /* hover 那条（0,3,0）比 [data-saved]（0,2,0）高一级，不补这条的话鼠标一停到星星上
     它会从白掉回 fg，看着像「取消收藏已经生效了」 */
  .yt-act[data-saved]:hover:not(:disabled),
  .yt-act[data-saved]:focus-visible {
    color: var(--color-hi);
  }

  .yt-act:disabled {
    opacity: 0.45;
    pointer-events: none;
  }

  /* 关闭键跟播放器底部控制条是同一套视觉：量出来那根药丸是 background oklch(1 0 0 / .1)、
     border-radius 99px、backdrop-filter blur(16px) saturate(1.1)、外加 0 0 0 1px 的暗环，
     里面每个按钮 36×36、图标 16px。这里照抄，所以它站在画面上像控制条里多出来的一颗，
     而不是「贴了张黑胶布」——旧样式是 bg-ink/70 的深色圆点，黑画面里看不清边界。
     尺寸比那几颗按钮矮一号（高 28、图标 14），但宽出两截内距（42×28）——圆角 99px 把两头收成半圆，
     所以看着是控制条那根药丸的缩小版，而不是一颗圆点。 */
  .yt-close {
    background: oklch(1 0 0 / 0.1);
    -webkit-backdrop-filter: blur(16px) saturate(1.1);
    backdrop-filter: blur(16px) saturate(1.1);
    box-shadow:
      0 0 0 1px oklch(0 0 0 / 0.1),
      0 1px 3px 0 oklch(0 0 0 / 0.15),
      0 1px 2px -1px oklch(0 0 0 / 0.15);
    /* 常驻会跟画面抢注意力（底部控制条自己几秒就淡出），所以跟着控制条走。
       藏起来的时候连指针一起关掉：否则画面上有块看不见的 42×28 会吃掉点击（点一下片子就没了），
       控制条隐藏时也是点不到的，行为对齐。 */
    opacity: 0;
    pointer-events: none;
    transition:
      opacity 160ms ease,
      background-color 120ms ease;
  }

  /* 没有播放器时（解析中 / 解析失败 / 兜底 iframe 都没有 video-skin）没有控制条可言，退回 hover。 */
  .yt-media:not(:has(video-skin)):hover .yt-close,
  .yt-close:focus-visible {
    opacity: 1;
    pointer-events: auto;
  }

  /* 有播放器时：显隐跟着播放器控制条走——皮肤把「控制条这会儿显示着没有」反射到
     `video-skin[data-controls-visible]`（见 VideoPlayer.svelte 的 watchControls）。原来只按
     `.yt-media:hover`，鼠标停在画面上不动、控制条几秒后自己收起来了，这颗键还亮着（2026-11-01 用户报的）。
     外层必须写成 `:global()`：`video-skin` 在 VideoPlayer 里，这个组件的模板里看不见它，
     svelte 会把整条选择器当成「没用到」直接从产物里删掉（实测构建后 `.yt-media:has(...)` 那条确实消失了）。 */
  :global(.yt-media:has(video-skin[data-controls-visible])) .yt-close {
    opacity: 1;
    pointer-events: auto;
  }

  .yt-close:hover {
    background: oklch(1 0 0 / 0.2);
  }

  /* 有播放器时关闭键画在播放器里面，播放器一进全屏它就跟着进去。
     全屏里退出靠 ESC / 控制条，画面上不该留一颗「关掉整个弹窗」的键。
     判断用宿主上的 `data-fullscreen`（VideoPlayer 的 watchFullscreen 写的），
     不用 CSS 的 `:fullscreen`：无头浏览器里 `matches(':fullscreen')` 恒为 false，那条路验不了。 */
  :global(.yt-media:has(video-skin[data-fullscreen])) .yt-close {
    display: none;
  }

  /* 窄屏往右下让一截：皮肤在 < 32rem 时会把投屏/画中画/全屏收进画面右上角那颗浮动药丸，
     390×844 实测它占画面 y=8~48（那三颗按钮 x=254 / 291 / 328，画面右边 374），
     正好压在右上角。关闭键挪到 top:3.5rem(56px) 就落在它下面，两者不叠；
     桌面端那组按钮仍在底部控制条里，右上角是空的，所以只在窄屏加这一条。 */
  @media (max-width: 32rem) {
    .yt-close {
      top: 3.5rem;
    }
  }

  /* 触屏没有 hover：滑进画面也触发不了，一直显示，否则手机上关不掉（点背景也能关，但不能指望） */
  @media (hover: none) {
    .yt-close {
      opacity: 1;
      pointer-events: auto;
    }
  }
</style>
