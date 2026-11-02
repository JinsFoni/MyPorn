<script lang="ts">
  /**
   * YouTube 单条视频的播放页（`/youtube/:videoId`）。
   *
   * 骨架和 `/video/:id`（关注流、B 站）**逐项对齐**：整页播放器 → 标题行右端那排 `icon-act`
   * → 右侧 14rem 资料列 → 底下一排同频道的其它视频。以前这里是墙上盖的一层弹窗
   * （`/youtube?v=<videoId>`）：画面只有 1024px 宽、标题小一号、资料挤在标题下面一行，
   * 同一部片在弹窗和播放页上长得不一样。现在它就是一个普通页面，退回＝浏览器后退。
   *
   * 和 `/video/:id` 唯一的结构差异来自数据：YouTube 的视频在 `yt_videos` 表里，
   * 下载之后才会变成 `videos` 里的一行，所以
   *   · 取数走 `/api/yt/videos/lookup?videoId=`（按 YouTube 自己的 `videoId` 取，不是行 id）；
   *   · 播放地址走 `/api/yt/videos/{id}/stream`（后端跑一次 yt-dlp，结果在服务端缓存半小时）。
   *
   * 「上一个 / 下一个」按**本视频所属频道**的列表算（`/api/yt/videos?channelId=`，60 条）。
   * 不再沿用弹窗那套「在墙上已加载的那份列表里找前后一格」：那份列表活在墙的组件实例里，
   * 换到独立页面就没了；而墙的筛选（`?channel=` / `?mode=`）在后退时本来就还原得回来，
   * 不需要播放页替它记着。同频道列表顺带就是页面底部那排「同频道的其它视频」。
   */
  import EmptyState from '../components/EmptyState.svelte';
  import Skeleton from '../components/Skeleton.svelte';
  import VideoPlayer from '../components/VideoPlayer.svelte';
  import VideoLoading from '../components/VideoLoading.svelte';
  import VideoByline from '../components/VideoByline.svelte';
  import YtCard from '../components/YtCard.svelte';
  import { api, messageFor } from '../lib/api';
  import {
    formatBandwidth,
    formatCount,
    formatDate,
    formatDuration,
    relativeTime,
  } from '../lib/format';
  import { imageUrl } from '../lib/img';
  import { live } from '../lib/live.svelte';
  import { HLS_TYPE, codecFamily, type QualityOption } from '../lib/player';
  import { privacy } from '../lib/privacy.svelte';
  import { navigate, setPageTitle } from '../lib/router.svelte';
  import type { YtStreamInfo, YtStreamQuality, YtVideo } from '../lib/types';

  interface Props {
    /** 路由参数：YouTube 自己的视频 id（11 位那种），不是 `yt_videos` 的行 id */
    id: string;
  }

  let { id }: Props = $props();

  let video = $state<YtVideo | null>(null);
  let loadingVideo = $state(true);
  let pageError = $state('');

  // ---- 站内播放 ----
  // 不走 YouTube 的 iframe：实测一批版权音乐视频在嵌入里直接被拒播（见 handlers_ytstream.go 顶部）。
  // 这里放的是后端代理出来的 HLS，画质能到 4K，也没有广告。
  let stream = $state<YtStreamInfo | null>(null);
  let streamLoading = $state(false);
  let streamError = $state('');
  let playError = $state('');
  /** 解析失败时的兜底：改用 YouTube 内嵌播放器（隐私模式下不给这个选项） */
  let useEmbed = $state(false);
  /** 选中的画质高度；null = 最高 */
  let streamHeight = $state<number | null>(null);
  let selectedQualityId = $state<string | null>(null);
  /** 重试/换档时 +1，用来重建播放器（地址没变也要重新挂） */
  let streamAttempt = $state(0);
  /**
   * 上一次从播放器报上来的进度。换画质会重建播放器（两条路的清单地址不同），
   * 换完把进度接回去，不然用户看到的是「切个画质又被扔回片头」。
   */
  let lastPos = $state(0);

  // ---- 同频道（上一个 / 下一个 + 底部那排卡片）----
  let siblings = $state<YtVideo[]>([]);

  // ---- 标题行那几颗键 / 资料列的动作状态 ----
  let saving = $state(false);
  /**
   * 画面顺时针转多少度（0/90/180/270），存在 `yt_videos.rotate_deg` 上。
   *
   * 有些频道传的文件**像素本身就是横的**，而且没有任何 rotation 元数据可以依据
   *（实测 iEZ_quzVcOE：ffprobe 256×144、无 rotate tag、无 side_data；yt-dlp 41 个 format 的
   * `rotation` 全 null；连 YouTube 自己生成的 maxresdefault 封面都是躺着的）——也就是说
   * YouTube 自己的播放器也一样躺着放，这不是我们解码的问题，靠元数据认不出来。
   * 所以给定一颗键：点一下转 90°，转到正就停在那儿，下次进来还是这个角度。
   */
  let rotateDeg = $state(0);
  let downloading = $state(false);
  let refreshingDate = $state(false);

  /** 换片（连点「下一个」很常见）时让还在路上的请求自己作废 */
  let loadToken = 0;
  /** 这一部已经报过「已看」，别每次重渲染都再报一次 */
  let seenSent = false;

  const videoId = $derived(id.trim());

  /**
   * 播放器的 poster（就是封面）。隐私模式下不给，理由同 Video.svelte：
   * 这是 `<video>` 自己的属性，CSS 糊不了，宁可在解析那几秒里黑着。
   */
  const poster = $derived(
    video === null || privacy.on ? null : imageUrl(video.posterUrl || video.thumbUrl, 1280),
  );

  // ---- 画质 ----

  /** 画质 id：用「高度p」；null 表示最高那档。 */
  function qualityIdFor(info: YtStreamInfo | null, height: number | null): string | null {
    if (!info || info.qualities.length === 0) return null;
    return `${height ?? info.default}p`;
  }

  /** 取某一档（null = 默认最高）。 */
  function qualityAt(info: YtStreamInfo | null, height: number | null): YtStreamQuality | null {
    if (!info || info.qualities.length === 0) return null;
    const h = height ?? info.default;
    return info.qualities.find((q) => q.height === h) ?? null;
  }

  const activeQuality = $derived(qualityAt(stream, streamHeight));

  /** 这一档要不要走「单文件 HLS」（AV1 分片 mp4 按 sidx 切分片，目前只有 2160p）。 */
  const usesDash = $derived(activeQuality?.dash === true);

  /** HLS 地址：切画质就是换 ?q=。 */
  const streamSrc = $derived(
    stream ? (streamHeight ? `${stream.src}?q=${String(streamHeight)}` : stream.src) : '',
  );
  /**
   * 4K 的地址：`/dash.m3u8` 把 AV1 分片 mp4 按 sidx 切成 HLS 分片发，起播只要第一段。
   * 建不起来时后端 302 回 VP9 那份清单，所以这里不用额外判断。
   */
  const dashSrc = $derived(
    stream && stream.dash && activeQuality
      ? `${stream.dash}?q=${String(activeQuality.height)}`
      : '',
  );
  const playbackSrc = $derived(usesDash ? dashSrc || streamSrc : streamSrc);
  const playbackType = $derived(HLS_TYPE);

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

  function applyQuality(next: string): void {
    const height = Number.parseInt(next, 10);
    streamHeight = Number.isFinite(height) && height > 0 ? height : null;
    selectedQualityId = next;
    playError = '';
    // 换档就重建播放器（{#key} 里带着 streamAttempt），进度靠 startAt 接回来
    streamAttempt += 1;
  }

  // ---- 取数 ----

  /** 换片时把「这一部」的状态清干净（组件实例在参数变化时是复用的，不清就串味）。 */
  function resetState(): void {
    seenSent = false;
    stream = null;
    streamError = '';
    playError = '';
    useEmbed = false;
    streamHeight = null;
    selectedQualityId = null;
    lastPos = 0;
    siblings = [];
    saving = false;
    refreshingDate = false;
    rotateDeg = 0;
  }

  async function open(): Promise<void> {
    const token = ++loadToken;
    resetState();
    loadingVideo = true;
    pageError = '';
    let found: YtVideo | null = null;
    try {
      found = await api.ytVideoLookup(videoId);
    } catch (err) {
      if (token === loadToken) pageError = messageFor(err);
    }
    if (token !== loadToken) return;
    loadingVideo = false;
    if (found === null) return;
    video = found;
    rotateDeg = found.rotateDeg ?? 0;
    setPageTitle(found.title);
    void loadSiblings(found, token);
    void loadStream(found, token);
    if (!found.seen) void markSeen(found);
    // 深度回填来的条目没有发布时间：点开时单独补一条（4 秒），不批量拉
    if (!found.dateKnown) void fillDate(found, token);
  }

  /**
   * 同频道列表：60 条（后端上限 100）。用来定「上一个 / 下一个」和底部那排卡片。
   *
   * 不带 `?mode=` / `?saved=` 筛选：`unseen` 那条路会和「标记已看」打架——
   * 视频一进来就被标成已看，再按「未看」去查，这一条根本不在结果里，
   * 前后两格的按钮会全灰。频道足够定上下文，筛选条件留给墙自己（它写在地址栏里）。
   */
  async function loadSiblings(v: YtVideo, token: number): Promise<void> {
    try {
      const res = await api.ytVideos({ channelId: v.channel.id, limit: 60 });
      if (token === loadToken) siblings = res.items;
    } catch {
      if (token === loadToken) siblings = [];
    }
  }

  /** 解析播放地址（后端跑一次 yt-dlp，结果在服务端缓存到签名过期，切画质不会再解析）。 */
  async function loadStream(v: YtVideo, token: number): Promise<void> {
    streamLoading = true;
    streamError = '';
    playError = '';
    try {
      // hls.js 是单独一个 chunk：只有真要放视频时才下载
      await import('@videojs/html/media/hlsjs-video');
      if (token !== loadToken) return;
      const info = await api.ytStream(v.id);
      if (token !== loadToken) return;
      stream = info;
      selectedQualityId = qualityIdFor(info, null);
    } catch (err) {
      if (token === loadToken) streamError = messageFor(err);
    } finally {
      if (token === loadToken) streamLoading = false;
    }
  }

  async function markSeen(v: YtVideo): Promise<void> {
    if (seenSent) return;
    seenSent = true;
    try {
      const res = await api.ytSeen({ ids: [v.id] });
      live.setYtUnseen(res.unseen);
    } catch {
      /* 上报失败不值得弹错 */
    }
  }

  /** 补「时间未知」（走 yt-dlp 单条解析，约 4 秒）。 */
  async function fillDate(v: YtVideo, token = loadToken): Promise<void> {
    if (refreshingDate) return;
    refreshingDate = true;
    try {
      const fresh = await api.ytRefresh(v.id);
      if (token !== loadToken) return;
      video = fresh;
    } catch {
      /* 补不到就算了，页面上照旧写「时间未知」 */
    } finally {
      refreshingDate = false;
    }
  }

  // ---- 播放失败 ----

  function retryStream(): void {
    playError = '';
    // 重建播放器并重新要一份清单：上游签名过期（会话 410/分片 403）是这里最常见的失败，
    // 光重挂同一个地址没用，得让后端重新解析、重新开一个会话。
    streamAttempt += 1;
    stream = null;
    streamError = '';
    if (video) void loadStream(video, loadToken);
  }

  function switchToEmbed(): void {
    useEmbed = true;
    playError = '';
  }

  function onStreamFailed(message: string): void {
    playError = message === '' ? '站内播放失败，请重试。' : `站内播放失败（${message}）`;
  }

  // ---- 标题行那四颗键 ----

  async function download(): Promise<void> {
    if (downloading || video === null) return;
    downloading = true;
    try {
      await api.ytDownload(video.id);
      live.toast('已加入下载队列，下完会进本地库', 'success');
      live.refreshDownloads(true);
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      downloading = false;
    }
  }

  async function toggleSave(): Promise<void> {
    if (saving || video === null) return;
    saving = true;
    try {
      const res = await api.ytSave(video.id, !video.saved);
      video = { ...video, saved: res.saved };
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      saving = false;
    }
  }

  /**
   * 点一下转 90°：0 → 90 → 180 → 270 → 0。
   *
   * 先改本地（画面立刻转，不用等一个来回），再把角度写回库；写失败就退回去并报一句，
   * 免得下次进来发现「我刚才明明转正了」。
   */
  async function rotate(): Promise<void> {
    if (video === null) return;
    const prev = rotateDeg;
    const next = (rotateDeg + 90) % 360;
    rotateDeg = next;
    try {
      await api.ytRotate(video.id, next);
      video = { ...video, rotateDeg: next };
    } catch (err) {
      rotateDeg = prev;
      live.toast(messageFor(err), 'error');
    }
  }

  async function copyLink(): Promise<void> {
    const url = `${window.location.origin}/youtube/${encodeURIComponent(videoId)}`;
    try {
      await navigator.clipboard.writeText(url);
      live.toast('播放链接已复制', 'success');
    } catch {
      live.toast('复制失败，请手动复制地址栏', 'error');
    }
  }

  // ---- 上一个 / 下一个 ----

  const siblingIndex = $derived(siblings.findIndex((s) => s.videoId === videoId));
  const prevSibling = $derived(siblingIndex > 0 ? siblings[siblingIndex - 1] : null);
  const nextSibling = $derived(
    siblingIndex >= 0 && siblingIndex < siblings.length - 1 ? siblings[siblingIndex + 1] : null,
  );
  const related = $derived(siblings.filter((s) => s.videoId !== videoId).slice(0, 8));

  function goToSibling(target: YtVideo | null): void {
    if (target === null) return;
    navigate(`/youtube/${encodeURIComponent(target.videoId)}`);
  }

  // ---- 资料列 ----

  const specs = $derived.by((): { label: string; value: string }[] => {
    const v = video;
    if (v === null) return [];
    const q = activeQuality;
    return [
      // 深度回填来的条目常常没有时长（yt-dlp 的 flat 通道不给），0 秒要写「—」而不是「0:00」
      { label: '时长', value: v.durationSec > 0 ? formatDuration(v.durationSec) : '—' },
      {
        label: '清晰度',
        value: q === null ? '—' : [q.label, formatBandwidth(q.bandwidth)].filter((p) => p !== '—').join(' · '),
      },
      { label: '观看', value: v.views > 0 ? `${formatCount(v.views)} 次` : '—' },
      { label: '发布', value: v.publishedAt === null ? '未知' : formatDate(v.publishedAt) },
      { label: '频道', value: v.channel.title },
    ];
  });

  // 换片：`id` 一变就重来一遍（App.svelte 里组件实例是复用的）
  $effect(() => {
    void videoId;
    void open();
  });

  // 先把封面塞进浏览器缓存：播放器挂载时会立刻要这张图，否则会闪一下它自带的占位插画
  $effect(() => {
    const url = poster;
    if (url === null) return;
    const img = new Image();
    img.decoding = 'async';
    img.src = url;
  });
</script>

{#if pageError}
  <EmptyState title="打不开这个视频" hint={pageError} icon="error">
    {#snippet actions()}
      <button type="button" class="btn" onclick={() => void open()}>重试</button>
    {/snippet}
  </EmptyState>
{:else if loadingVideo && video === null}
  <div class="mx-auto max-w-[72rem] space-y-4">
    <Skeleton class="aspect-video w-full" rounded="rounded-xl" />
    <Skeleton class="h-6 w-2/3" />
    <Skeleton class="h-3.5 w-1/3" />
  </div>
{:else if video}
  {@const v = video}
  <article>
    {#if useEmbed}
      <!-- 兜底：站内解析失败（或版权视频被上游拒放）才用 YouTube 自己的播放器 -->
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
      {#key `${String(streamAttempt)}-${v.videoId}`}
        <VideoPlayer
          src={playbackSrc}
          type={playbackType}
          engine="hls"
          {rotateDeg}
          startAt={lastPos}
          onTime={(t) => (lastPos = t)}
          poster={poster}
          posterFallback={v.thumbUrl}
          title={v.title}
          videoKey={v.id}
          qualities={qualityOptions}
          selectedQuality={selectedQualityId}
          onSelectQuality={applyQuality}
          prevTitle={prevSibling?.title ?? null}
          nextTitle={nextSibling?.title ?? null}
          onPrev={() => goToSibling(prevSibling)}
          onNext={() => goToSibling(nextSibling)}
          onEnded={() => goToSibling(nextSibling)}
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
      {/key}
    {:else}
      <div class="relative aspect-video w-full overflow-hidden rounded-xl bg-black">
        {#if streamLoading}
          <VideoLoading {poster} posterFallback={v.thumbUrl} kind="poster" label="正在解析最高画质" />
        {:else if streamError}
          <div class="absolute inset-0 flex flex-col items-center justify-center gap-3 px-6 text-center">
            <p class="max-w-md text-ui text-fg/90">{streamError}</p>
            <div class="flex flex-wrap items-center justify-center gap-2">
              <button type="button" class="btn" onclick={retryStream}>重新解析</button>
              {#if !privacy.on}
                <button type="button" class="btn btn-primary" onclick={switchToEmbed}>改用 YouTube 播放器</button>
              {/if}
            </div>
          </div>
        {/if}
      </div>
    {/if}

    <!-- 布局与 `/video/:id` 一致（含 14rem 资料列、标题行图标键的高度钉法），
         两边改动要同步改——同一部片不该在两处长得不一样。 -->
    <div class="mt-5 grid gap-6 lg:grid-cols-[minmax(0,1fr)_14rem]">
      <div class="min-w-0">
        <div class="flex items-start gap-3">
          <h2 class="min-w-0 flex-1 text-film-title font-bold text-fg lg:text-film-title-lg">
            {v.title}
          </h2>

          <!-- 高度钉成标题首行的行高（18/26 与 20/28），36px 的按钮圆心正好压在首行中线上（同 Video.svelte） -->
          <div class="flex h-[var(--text-film-title--line-height)] shrink-0 items-center gap-1 lg:h-[var(--text-film-title-lg--line-height)]">
            <button
              type="button"
              class="icon-act"
              data-emphasis="primary"
              title={downloading ? '正在排队下载' : v.downloaded ? '已在本地库，再下一次' : '下载到本地库'}
              aria-label={downloading ? '正在排队下载' : '下载到本地库'}
              onclick={() => void download()}
              disabled={downloading}
            >
              {#if downloading}
                <svg viewBox="0 0 24 24" class="h-5 w-5 animate-spin" fill="none" stroke="currentColor" stroke-width="2">
                  <circle cx="12" cy="12" r="8" stroke-opacity="0.25" />
                  <path d="M20 12a8 8 0 0 0-8-8" />
                </svg>
              {:else}
                <svg viewBox="0 0 24 24" class="h-5 w-5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round">
                  <path d="M12 4v10" />
                  <path d="m8 10.5 4 4 4-4" />
                  <path d="M4.5 18.5h15" />
                </svg>
              {/if}
            </button>

            <button
              type="button"
              class="icon-act"
              data-saved={v.saved ? '' : undefined}
              title={v.saved ? '取消收藏' : '收藏'}
              aria-label={v.saved ? '取消收藏' : '收藏'}
              onclick={() => void toggleSave()}
            >
              <svg
                viewBox="0 0 24 24"
                class="h-5 w-5"
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
              class="icon-act"
              title="复制播放链接"
              aria-label="复制播放链接"
              onclick={() => void copyLink()}
            >
              <svg viewBox="0 0 24 24" class="h-5 w-5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round">
                <path d="M10.2 13.4a4.5 4.5 0 0 0 6.7.4l1.9-1.9a4.5 4.5 0 0 0-6.4-6.4l-1.1 1.1" />
                <path d="M13.8 10.6a4.5 4.5 0 0 0-6.7-.4l-1.9 1.9a4.5 4.5 0 0 0 6.4 6.4l1.1-1.1" />
              </svg>
            </button>

            <!-- 躺着拍的片子只能手动转：点一下转 90°，角度记在库里（见 rotate() 的注释） -->
            <button
              type="button"
              class="icon-act"
              data-rotated={rotateDeg === 0 ? undefined : ''}
              title={rotateDeg === 0 ? '旋转画面 90°（横着拍的片子用）' : `画面已转 ${rotateDeg}°，再点转 90°`}
              aria-label={rotateDeg === 0 ? '旋转画面 90°' : `画面已转 ${rotateDeg} 度，再点转 90 度`}
              onclick={() => void rotate()}
            >
              <svg viewBox="0 0 24 24" class="h-5 w-5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
                <path d="M20.5 12a8.5 8.5 0 1 1-2.6-6.1" />
                <path d="M20.6 3.6v5.2h-5.2" />
              </svg>
            </button>

            <a
              href={v.url}
              target="_blank"
              rel="noreferrer"
              class="icon-act"
              title="在 YouTube 打开"
              aria-label="在 YouTube 打开"
            >
              <svg viewBox="0 0 24 24" class="h-5 w-5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round">
                <path d="M18 13.5V18a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h4.5" />
                <path d="M15 3.5h5.5V9" />
                <path d="M20.2 3.8 11 13" />
              </svg>
            </a>
          </div>
        </div>

        <!-- 频道 + 时间：样式与 `/video/:id` 的艺人行同一套（40px 头像 + 名字 16px/500 + 下面一行时间），
             见 components/VideoByline.svelte。
             频道名链回视频墙的那个频道视角（`/youtube?channel=<行 id>`）：
             筛选写在地址栏里，所以这一跳过去墙就是「这个频道」的样子。 -->
        <VideoByline avatarUrl={imageUrl(v.channel.avatarUrl, 80)}>
          {#snippet primary()}
            <a
              data-link
              href={`/youtube?channel=${String(v.channel.id)}`}
              class="underline-offset-4 transition-colors hover:text-hi hover:underline"
            >
              {v.channel.title}
            </a>
          {/snippet}
          {#snippet secondary()}
            {#if v.publishedAt === null}
              <span>时间未知</span>
              <button
                type="button"
                class="rounded-full bg-raise px-2 py-0.5 text-meta text-fg transition-opacity hover:opacity-90 disabled:opacity-50"
                onclick={() => void fillDate(v)}
                disabled={refreshingDate}
              >
                {refreshingDate ? '查询中…' : '查一下发布时间'}
              </button>
            {:else}
              <span>{relativeTime(v.publishedAt)}</span>
            {/if}
          {/snippet}
        </VideoByline>

        {#if v.description}
          <!-- 折叠到 10 行：YouTube 的简介能到几千字，整段摊开会把那排同频道卡片推到两屏之外；
               弹窗里也是这么折的，收成页面之后没有理由放开。 -->
          <p class="mt-4 max-w-[68ch] whitespace-pre-line text-ui leading-relaxed text-muted line-clamp-[10]">
            {v.description}
          </p>
        {/if}
      </div>

      <aside>
        <!-- `lg:-mt-1`：标题行高从 34 收到 28（照 YouTube 量出来的 20px/28px）之后，
             资料首行的文字中线比标题首行中线低了 4.5px（实测 794 vs 789.5），往回提 4px 补平。 -->
        <dl class="mt-4 divide-y divide-line lg:-mt-1">
          {#each specs as spec (spec.label)}
            <div class="flex items-baseline justify-between gap-3 py-2">
              <dt class="text-meta text-dim">{spec.label}</dt>
              <dd class="text-right text-ui tabular-nums text-fg/85">{spec.value}</dd>
            </div>
          {/each}
        </dl>

        {#if v.downloaded}
          <p class="mt-2 text-meta text-sage">已在本地库</p>
        {/if}
      </aside>
    </div>

    {#if related.length > 0}
      <section class="mt-10">
        <h3 class="mb-3 text-title font-medium text-fg">同频道的其它视频</h3>
        <div class="grid grid-cols-1 gap-x-3 gap-y-6 sm:grid-cols-2 xl:grid-cols-4">
          {#each related as item (item.id)}
            <YtCard video={item} onOpen={(next) => goToSibling(next)} />
          {/each}
        </div>
      </section>
    {/if}
  </article>
{/if}
