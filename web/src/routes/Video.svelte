<script lang="ts">
  import DownloadDialog from '../components/DownloadDialog.svelte';
  import VideoCard from '../components/VideoCard.svelte';
  import VideoByline from '../components/VideoByline.svelte';
  import EmptyState from '../components/EmptyState.svelte';
  import Skeleton from '../components/Skeleton.svelte';
  import VideoPlayer from '../components/VideoPlayer.svelte';
  import VideoLoading from '../components/VideoLoading.svelte';
  import { api, messageFor, type AdjacentScope } from '../lib/api';
  import {
    formatBandwidth,
    formatBytes,
    formatCount,
    formatDate,
    formatDuration,
    relativeTime,
  } from '../lib/format';
  import { imageUrl } from '../lib/img';
  import { live } from '../lib/live.svelte';
  import { platformLabel } from '../lib/platform';
  import { privacy } from '../lib/privacy.svelte';
  import {
    MP4_TYPE,
    codecTag,
    needsHlsEngine,
    needsRemuxPrep,
    pickVariant,
    rememberHeight,
    rememberedHeight,
    sortVariantsDesc,
    sourceTypeFor,
    variantLabel,
    type QualityOption,
  } from '../lib/player';
  import { canPlayDash, mseCodec, type DashSpec } from '../lib/dashMse';
  import { navigate, router } from '../lib/router.svelte';
  import type {
    PlaybackInfo,
    PlaybackVariant,
    RemuxStatus,
    Video,
    VideoDetail,
    VideoNeighbors,
  } from '../lib/types';

  interface Props {
    id: string;
  }

  let { id }: Props = $props();

  /** 仅 DEV：后端未就绪时的示例数据（不请求任何外部地址） */
  const MOCK_VIDEO: VideoDetail = {
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
    description: '示例数据：后端未连接，播放器不会有真实片源。',
    related: [],
  };

  const videoId = $derived(Number.parseInt(id, 10));

  let video = $state<VideoDetail | null>(null);
  let playback = $state<PlaybackInfo | null>(null);
  let loadingVideo = $state(true);
  let loadingPlayback = $state(true);
  let pageError = $state('');
  let playError = $state('');
  let proxyAvailable = $state(false);
  let usingProxy = $state(false);
  let selectedVariantId = $state<string | null>(null);
  /**
   * 记忆里那一档的高度（px），null = 没记忆 → 默认最高。
   * 单独存一份是为了让清晰度菜单能把它标出来：记忆造成的降档必须是看得见的选择，
   * 不能让 4K 片子悄悄地按记忆里的 1080 起播、用户却不知道是谁决定的。
   */
  let rememberedPx = $state<number | null>(rememberedHeight());
  let mockNote = $state('');
  let downloadOpen = $state(false);
  /**
   * 「正在准备这一档」的进度：只有分轨档位（B 站 1080P+/4K）会用到。
   * 这些档位要先在服务端把音画两条流合成一份 mp4，4K 一档 554 MB~1.77 GB，
   * 头一次点要等 1~3 分钟。以前这段时间是一个不回头的 HTTP 请求，用户只看到转圈，
   * 最后当成「放不了」——现在换成轮询进度 + 明确百分比。
   */
  let preparing = $state<{
    variantId: string;
    label: string;
    percent: number;
    bytes: number;
    elapsedSec: number;
  } | null>(null);
  /** 换档位/离开页面时 +1，让还在跑的轮询自己退出 */
  let prepToken = 0;
  /** 「重新解析」时 +1，用来重建播放器（地址没变也要重新挂） */
  let attempt = $state(0);
  /** 标签默认只露这么多，剩下的折在「+N」里 */
  const TAG_PREVIEW = 8;
  let tagsExpanded = $state(false);
  let seenSent = false;
  /**
   * 播放器「上一个 / 下一个」的两个邻居（列表口径见 neighborScope）。
   * 到列表头/尾那一侧是 null，按钮据此按下去。
   */
  let neighbors = $state<VideoNeighbors>({ prev: null, next: null });
  /** 换影片/换口径时 +1，让还在路上的那次请求自己作废（连点两下切歌时很常见） */
  let neighborToken = 0;

  /**
   * 作者行那颗 40px 头像：取第一个带头像的艺人（B 站就是 UP 主本人；xHamster 之类常常没有，
   * 那就干脆不画，不留一个空圆）。
   */
  const performers = $derived(video?.performers ?? []);
  const publishedAt = $derived(video?.publishedAt ?? null);
  const authorAvatar = $derived(imageUrl(performers.find((p) => p.avatarUrl)?.avatarUrl ?? null, 80));
  const variants = $derived(playback ? sortVariantsDesc(playback.variants) : []);
  const selectedVariant = $derived(variants.find((v) => v.id === selectedVariantId) ?? null);
  /**
   * 这些档位试过 MSE、拼不起来（编码不支持、上游不给 Range……）：记下原因，退回服务端合流。
   * 记在 id 上而不是全局一个开关：换一档（比如从 AV1 换到 H.264）常常就好了。
   */
  let dashBlocked = $state<Record<string, string>>({});
  /** B 站音轨都是 AAC-LC（30216/30232/30280），杜比/无损我们在后端就没接 */
  const DASH_AUDIO_CODEC = 'mp4a.40.2';

  /** 这一档能不能自己拼流（MSE）而不是等服务端合流 */
  function dashUsable(variant: PlaybackVariant | null): boolean {
    if (!variant || variant.dash !== true || !variant.audioUrl) return false;
    if (usingProxy) return false; // 代理播放是「整条文件」的降级通道，不拼流
    if (dashBlocked[variant.id] !== undefined) return false;
    return canPlayDash(variant.codec, DASH_AUDIO_CODEC);
  }

  /** dash 的两条轨：视频轨随清晰度变，音频轨不变（换档只重建视频那一个 SourceBuffer） */
  const dashSpec = $derived.by((): DashSpec | null => {
    const variant = selectedVariant;
    if (!variant || !dashUsable(variant)) return null;
    return {
      video: { url: variant.url, mime: `video/mp4; codecs="${mseCodec(variant.codec)}"` },
      audio: { url: variant.audioUrl ?? '', mime: `audio/mp4; codecs="${DASH_AUDIO_CODEC}"` },
      durationSec: video?.durationSec ?? 0,
    };
  });
  /**
   * 播放器的 poster（就是封面）。隐私模式下干脆不给：这个封面是 <video> 自己的属性，
   * CSS 没法只糊它而不糊画面。宁可在解析片源的这一两秒里黑着 + 转环，
   * 也不把一张高清封面亮在屏幕正中央——这页本来就是为了看片，一闪而过的封面没有信息价值。
   */
  const poster = $derived(video && !privacy.on ? imageUrl(video.thumbUrl, 1280) : null);
  /** 准备中的那一行字：`正在准备 4K AV1 · 62%` */
  const prepHeadline = $derived(
    preparing ? `${preparing.label} · ${String(Math.round(preparing.percent))}%` : '',
  );
  /**
   * 副行：体积 + 已经等了多久 + 「只等这一次」。
   * 最后这半句很重要——不说清楚，用户会以为每次点 4K 都要等这么久。
   */
  const prepDetail = $derived.by(() => {
    if (!preparing) return '';
    const size = preparing.bytes > 0 ? `约 ${formatBytes(preparing.bytes)} · ` : '';
    return `${size}已等 ${String(preparing.elapsedSec)} 秒 · 合好之后同一档秒开`;
  });
  const prepVeilDetail = $derived(preparing ? `${prepHeadline} · ${prepDetail}` : null);

  /**
   * 引擎三选一：能拼流就拼流（dash，MSE），否则这批片源里只要有一条 HLS 就整个用 hls.js
   * （它也能放 mp4），再不然原生 <video>。
   */
  const engine = $derived.by((): 'hls' | 'native' | 'dash' => {
    if (dashSpec !== null) return 'dash';
    return needsHlsEngine(playback?.variants ?? []) ? 'hls' : 'native';
  });
  const source = $derived.by((): { src: string; type: string } | null => {
    const info = playback;
    const variant = selectedVariant;
    if (!info || !variant) return null;
    // 拼流那条路：src 就是视频轨。换档时它变，播放器据此「只重建视频轨」（见 dashMse.ts）
    if (dashSpec !== null) return { src: dashSpec.video.url, type: MP4_TYPE };
    const isHls = usingProxy ? true : variant.hls;
    const fallback = variant.remuxUrl ?? variant.url;
    const src = usingProxy && info.proxyPlaybackUrl ? info.proxyPlaybackUrl : fallback;
    return { src, type: sourceTypeFor(isHls) };
  });

  async function loadVideo(): Promise<void> {
    loadingVideo = true;
    pageError = '';
    try {
      const detail = await api.video(videoId);
      video = detail;
      mockNote = '';
    } catch (err) {
      if (import.meta.env.DEV) {
        video = MOCK_VIDEO;
        mockNote = messageFor(err);
      } else {
        pageError = messageFor(err);
      }
    } finally {
      loadingVideo = false;
    }
  }

  async function loadPlayback(): Promise<void> {
    loadingPlayback = true;
    playError = '';
    // 换了一部片子（或重新解析）：上一次的准备进度、还在跑的轮询都作废。
    preparing = null;
    prepToken += 1;
    dashBlocked = {};
    try {
      const info = await api.playback(videoId);
      // HLS 引擎是单独一个 chunk（含 hls.js）：只有真要放 HLS 时才下载
      if (needsHlsEngine(info.variants)) await import('@videojs/html/media/hlsjs-video');
      playback = info;
      // 标签只在详情页里有：解析播放地址时一起带回来，别让用户看到空标签或刷新才出现。
      if (info.tags?.length && video && video.tags.length === 0) {
        video = { ...video, tags: info.tags };
      }
      // 每次解析都重读一遍记忆：别的标签页可能刚改过，旧键也要在这里被清掉
      rememberedPx = rememberedHeight();
      const preferred = pickVariant(info, rememberedPx);
      // 本地文件本来就是同源直发，"用代理播放"对它是空操作。
      const chosen = preferred ?? info.variants.find((v) => v.id === info.defaultVariantId) ?? null;
      proxyAvailable = info.proxyPlaybackUrl !== null && chosen?.local !== true;
      if (chosen && needsRemuxPrep(chosen)) {
        // 分轨档位（4K/1080P+）要先在服务端合流：地址先压着，进度到 ready 再交给播放器。
        // 注意**不**传 remember：这一档是代码按默认选出来的，不是用户点的，不能写进偏好。
        selectedVariantId = null;
        void activateVariant(chosen);
      } else {
        selectedVariantId = chosen?.id ?? info.defaultVariantId;
      }
    } catch (err) {
      playError = messageFor(err);
    } finally {
      loadingPlayback = false;
    }
  }

  function onFailed(detail: string): void {
    if (playback?.proxyPlaybackUrl && !usingProxy) {
      proxyAvailable = true;
      playError = '直链播放失败，通常是 Referer/UA 或跨域限制。可以试试代理播放。';
      return;
    }
    playError = detail === '' ? '播放失败，请重试。' : `播放失败（${detail}）`;
  }

  /**
   * 拼流试过但没成（浏览器不支持这个编码的 MSE、上游不给 Range、append 被拒……）。
   * 把这一档记成「别拼了」，退回服务端合流那条老路——用户要的是看片，不是看我在这里硬撑。
   */
  function onDashFailed(message: string): void {
    const variant = selectedVariant;
    if (!variant) return;
    dashBlocked = { ...dashBlocked, [variant.id]: message };
    live.toast('这一档没法直接拼流，改用服务端合流', 'info');
    void activateVariant(variant);
  }

  function retryPlayback(): void {
    playError = '';
    preparing = null;
    prepToken += 1;
    attempt += 1;
    void loadPlayback();
  }

  function applyVariant(variantId: string): void {
    const variant = playback?.variants.find((v) => v.id === variantId);
    if (!variant) return;
    // 全站只有这条路是「用户自己点的档位」——偏好的记忆也只在这里写
    void activateVariant(variant, { remember: true });
  }

  /**
   * 切到某一档。
   *
   * 分轨档位（B 站 1080P+ / 4K）要先等服务端把音画两轨合成一份 mp4——4K 一档
   * 554 MB~1.77 GB，头一次要等 1~3 分钟。所以这里先轮询进度、把百分比画在画面上，
   * 合好才改 selectedVariantId（这时 <video> 拿到的是一个已经就绪的文件，秒开）。
   *
   * `remember` 只有清晰度菜单传 true。起播时自动选中合流档位也走这个函数
   * （B 站的默认档就是 1080P，必须合流），但那是代码选的、不是用户选的：
   * 以前这里无条件 rememberHeight，结果打开任何一部 B 站片子就把偏好写成 1080，
   * 之后所有平台的 4K 片都从 1080p 起播。
   */
  async function activateVariant(
    variant: PlaybackVariant,
    opts: { remember?: boolean } = {},
  ): Promise<void> {
    const token = ++prepToken;
    playError = '';
    // 能自己拼流的档位根本不用等合流：分片一到就出画面，换档只换视频轨
    if (!dashUsable(variant) && needsRemuxPrep(variant)) {
      const ready = await waitForRemux(variant, token);
      if (!ready || token !== prepToken) return;
    }
    selectedVariantId = variant.id;
    if (opts.remember === true) {
      rememberHeight(variant.height);
      rememberedPx = variant.height;
    }
  }

  /** 轮询合流进度，ready 时返回 true；失败/取消返回 false（原因写进 playError）。 */
  async function waitForRemux(variant: PlaybackVariant, token: number): Promise<boolean> {
    const codec = codecTag(variant);
    const label = `正在准备 ${variantLabel(variant)}${codec === null ? '' : ` ${codec}`}`;
    // 体积按码率 × 时长估：菜单上只有 Mbps，用户看到「约 554 MB」才知道自己在等什么。
    const bytes =
      variant.bandwidth > 0 && video !== null ? (variant.bandwidth / 8) * video.durationSec : 0;
    for (;;) {
      let status: RemuxStatus;
      try {
        status = await api.remuxStatus(videoId, variant.id);
      } catch (err) {
        if (token === prepToken) {
          preparing = null;
          playError = messageFor(err);
        }
        return false;
      }
      if (token !== prepToken) return false;
      if (status.state === 'ready') {
        preparing = null;
        return true;
      }
      if (status.state === 'failed') {
        preparing = null;
        playError = `这一档准备失败：${status.error ?? '未知原因'}。换个档位，或点「重新解析」再来一次。`;
        return false;
      }
      preparing = {
        variantId: variant.id,
        label,
        percent: status.percent,
        bytes,
        elapsedSec: status.elapsedSec,
      };
      await new Promise((resolve) => setTimeout(resolve, 1200));
    }
  }

  function switchToProxy(): void {
    if (!playback?.proxyPlaybackUrl) return;
    usingProxy = true;
    proxyAvailable = false;
    playError = '';
    live.toast('已切换到代理播放', 'info');
  }

  function onStarted(): void {
    if (seenSent) return;
    seenSent = true;
    void api
      .markSeen(videoId)
      .then(() => {
        if (video) video.isNew = false;
      })
      .catch(() => {
        /* 上报失败不影响播放 */
      });
  }

  /**
   * 记忆真正会命中的那一条（同高度可能有多个编码，取排序第一的，与 pickVariant 同口径）。
   * 记忆存的是高度，不是变体 id，所以标的时候得先按同样的规则算出「到底是哪一条」，
   * 否则 1080p 的 H.264 和 AV1 两条会一起被标上「上次选的」。
   */
  const rememberedVariantId = $derived<string | null>(
    rememberedPx === null || rememberedPx >= (variants[0]?.height ?? 0)
      ? null
      : (variants.find((v) => v.local !== true && v.height === rememberedPx)?.id ?? null),
  );

  /**
   * 一个分辨率只留一条（`variants` 已按 sortVariantsDesc 排好：分辨率优先、
   * 同分辨率内 AV1 > HEVC > VP9 > H.264、解不了的编码排最后，所以每组的第一条就是该用的那条）。
   *
   * 为什么菜单只按分辨率列：B 站一条片子上来就有 1080p 的 AV1 / HEVC / H.264 三版，
   * 加上普通版和高码率版能到 13 条——菜单被挤成需要滚动不说，里面还有两条 ID 相同的
   * 「1080p AV1」（点第二条等于点第一条，看着像点了没反应）。用户要选的是「多清楚」，
   * 编码是同一清晰度内部的事，写在副文案里就够了。
   */
  const qualityByHeight = $derived.by(() => {
    const map = new Map<number, PlaybackVariant>();
    for (const variant of variants) {
      if (!map.has(variant.height)) map.set(variant.height, variant);
    }
    return map;
  });

  /**
   * 播放器左上角清晰度菜单的条目（顺序即 sortVariantsDesc 的顺序，最高在最上）。
   *
   * 「上次选的」标在记忆那一档上——但只在它确实比最高档低时才标：
   * 那种情况下「为什么这部 4K 片在放 1080p」必须能一眼看出来是记忆造成的，
   * 而不是让人以为片源只有 1080p。
   */
  const qualities = $derived<QualityOption[]>(
    [...qualityByHeight.values()].map((variant) => ({
      id: variant.id,
      label: variantLabel(variant),
      hint: [
        codecTag(variant),
        formatBandwidth(variant.bandwidth),
        variant.local === true ? '本地文件' : null,
        variant.local !== true && variant.id === playback?.defaultVariantId ? '最高' : null,
        variant.id === rememberedVariantId ? '上次选的' : null,
      ]
        .filter((part) => part !== null && part !== '')
        .join(' · '),
    })),
  );

  /**
   * 菜单里该打勾的那一条：按**分辨率**对应，不是按 id。
   * 选中项可能是 1080p 的 HEVC 而菜单这一行代表的是 1080p 的 AV1，按 id 比对就打不上勾了。
   */
  const selectedQualityId = $derived<string | null>(
    selectedVariant === null ? null : (qualityByHeight.get(selectedVariant.height)?.id ?? null),
  );

  const specs = $derived.by((): { label: string; value: string }[] => {
    const v = video;
    if (!v) return [];
    const list = [
      { label: '时长', value: formatDuration(v.durationSec) },
      {
        label: '清晰度',
        value: selectedVariant
          ? `${variantLabel(selectedVariant)} · ${formatBandwidth(selectedVariant.bandwidth)}`
          : '—',
      },
      { label: '发布', value: formatDate(v.publishedAt) },
      { label: '播放量', value: formatCount(v.viewCount) },
      { label: '平台', value: platformLabel(v.platform) },
    ];
    return list;
  });

  $effect(() => {
    void loadVideo();
    void loadPlayback();
  });

  // 离开这一页就把轮询停掉：prepToken 一变，waitForRemux 下一轮自己退出。
  $effect(() => () => {
    prepToken += 1;
  });

  // 先把封面塞进浏览器缓存：播放器挂载时会立刻要这张图，否则会闪一下它自带的占位插画
  $effect(() => {
    const url = poster;
    if (!url) return;
    const img = new Image();
    img.decoding = 'async';
    img.src = url;
  });
  /**
   * `?back=`：点进来的是哪一页（卡片链接自带）。只认站内绝对路径（以单个 / 开头），
   * 别的一律忽略，免得被带去外站。
   *
   * 页面上**不再画「返回 XX」按钮**（原来标题下方和「相关影片」标题右边各一颗）：
   * 侧边栏/顶栏本来就在，播放页上再放一颗「返回关注」「返回 B 站」是把导航写了两遍，
   * 还占掉「相关影片」那一行的右端。`backTo` 现在只剩一个用途——决定上一个/下一个切哪份列表
   * （见下面 neighborScope），所以别把这个 $derived 一起删了。
   */
  const backTo = $derived.by(() => {
    const raw = router.query.get('back') ?? '';
    return raw.startsWith('/') && !raw.startsWith('//') ? raw : '/';
  });

  /**
   * 播放器「上一个 / 下一个」按哪份列表算：跟着 `?back=` 走——它记着用户是从哪一页点进来的。
   *
   * 三处口径必须和那边的列表接口**完全一致**，否则切着切着会冒出列表里根本没出现过的片子
   * （后端用的就是同一套 FeedQuery 条件，见 store.AdjacentVideos）：
   *   · 艺人页     → 只要这位艺人的片；
   *   · B 站墙     → platform=bilibili（可再按 UP 主 `?up=`、未看 `?mode=unseen` 过滤，
   *                 并且取消订阅的 UP 主不在墙上 → subscribedOnly）；
   *   · 关注流/其它 → 和 /api/videos 一样排除 B 站（它有自己的页面）。
   * 从下载页、动态点进来的没有对应的「影片列表」，也按关注流那套算——总比跳到别的平台强。
   */
  const neighborScope = $derived.by((): AdjacentScope => {
    const url = new URL(backTo, window.location.origin);
    if (url.pathname.startsWith('/performer/')) {
      const pid = Number.parseInt(url.pathname.slice('/performer/'.length), 10);
      if (Number.isFinite(pid) && pid > 0) return { performerId: pid };
    }
    if (url.pathname === '/bilibili') {
      const up = Number.parseInt(url.searchParams.get('up') ?? '', 10);
      return {
        platform: 'bilibili',
        performerId: Number.isFinite(up) && up > 0 ? up : undefined,
        onlyNew: url.searchParams.get('mode') === 'unseen',
        subscribedOnly: true,
      };
    }
    if (video?.platform === 'bilibili') return { platform: 'bilibili', subscribedOnly: true };
    return { excludePlatform: 'bilibili' };
  });

  async function loadNeighbors(): Promise<void> {
    const token = ++neighborToken;
    try {
      const found = await api.adjacent(videoId, neighborScope);
      if (token === neighborToken) neighbors = found;
    } catch {
      // 拿不到邻居不该让播放器变红：按钮置灰就是了（下一部照样能手动点卡片进去）
      if (token === neighborToken) neighbors = { prev: null, next: null };
    }
  }

  // 换了影片、或者换了一份列表口径，都重新问一次「上下家是谁」
  $effect(() => {
    void videoId;
    void neighborScope;
    void loadNeighbors();
  });

  /**
   * 切到邻居。`?back=` 原样带着走——「上一个 / 下一个」按哪份列表算就是看它，
   * 连切几部之后仍要留在同一份列表里（B 站墙点进来的第二部，不能突然按关注流算）。
   */
  function goToNeighbor(target: Video | null): void {
    if (!target) return;
    navigate(`/video/${String(target.id)}${router.search}`);
  }

  /**
   * 换影片（手动连点、或播完自动切换）时把「这一部」的状态清干净。
   * Video.svelte 的实例在参数变化时是复用的，不清就会串味：下一部一进来顶着上一部的
   * 播放错误、被当成「已上报已看」而不再标记已读、代理开关还开着。
   */
  $effect(() => {
    void videoId;
    seenSent = false;
    tagsExpanded = false;
    usingProxy = false;
    proxyAvailable = false;
    playError = '';
    preparing = null;
    prepToken += 1;
    selectedVariantId = null;
    neighbors = { prev: null, next: null };
  });
</script>

{#if pageError}
  <EmptyState title="打不开这个影片" hint={pageError} icon="error">
    {#snippet actions()}
      <button type="button" class="btn" onclick={() => void loadVideo()}>重试</button>
    {/snippet}
  </EmptyState>
{:else if loadingVideo && !video}
  <div class="mx-auto max-w-[72rem] space-y-4">
    <Skeleton class="aspect-video w-full" rounded="rounded-xl" />
    <Skeleton class="h-6 w-2/3" />
    <Skeleton class="h-3.5 w-1/3" />
  </div>
{:else if video}
  <article>
    {#if mockNote}
      <p class="mb-3 rounded-xl bg-panel px-3 py-2 text-meta text-dim">
        离线预览：正在使用内置示例数据（后端未连接：{mockNote}）
      </p>
    {/if}

    {#if source}
      <!-- 换影片也要重建播放器：同一条 src 之外的东西（进度、ended）都是「上一部」的，
           不重建就会接着上一部的位置往下一部里 seek。attempt 是「重新解析」，videoId 是换片。 -->
      {#key `${String(attempt)}:${String(videoId)}`}
        <VideoPlayer
          src={source.src}
          type={source.type}
          engine={engine}
          poster={poster}
          title={video.title}
          videoKey={videoId}
          {qualities}
          dash={dashSpec}
          onDashFailed={onDashFailed}
          selectedQuality={selectedQualityId}
          onSelectQuality={applyVariant}
          prevTitle={neighbors.prev?.title ?? null}
          nextTitle={neighbors.next?.title ?? null}
          onPrev={() => goToNeighbor(neighbors.prev)}
          onNext={() => goToNeighbor(neighbors.next)}
          onEnded={() => goToNeighbor(neighbors.next)}
          onStarted={onStarted}
          onFailed={onFailed}
        >
          {#snippet overlay()}
            {#if preparing}
              <div
                class="absolute inset-0 flex flex-col items-center justify-center gap-2 bg-ink/80 px-6 text-center"
              >
                <span
                  class="h-6 w-6 animate-spin rounded-full border-2 border-fg/25 border-t-fg"
                  aria-hidden="true"
                ></span>
                <p class="text-ui tabular-nums text-fg/90">{prepHeadline}</p>
                <p class="text-meta text-dim">{prepDetail}</p>
              </div>
            {:else if playError}
              <div class="pointer-events-auto absolute inset-0 flex flex-col items-center justify-center gap-3 bg-ink/85 px-6 text-center">
                <p class="max-w-md text-ui text-fg/90">{playError}</p>
                <div class="flex flex-wrap items-center justify-center gap-2">
                  <button type="button" class="btn" onclick={retryPlayback}>重新解析</button>
                  {#if proxyAvailable && !usingProxy}
                    <button type="button" class="btn btn-primary" onclick={switchToProxy}>用代理播放</button>
                  {/if}
                </div>
              </div>
            {/if}
          {/snippet}
        </VideoPlayer>
      {/key}
    {:else}
      <div class="relative aspect-video w-full overflow-hidden rounded-xl bg-black">
        {#if loadingPlayback || preparing}
          <VideoLoading
            {poster}
            kind="poster"
            label={preparing ? prepHeadline : '正在解析播放地址'}
            detail={prepVeilDetail}
          />
        {:else if playError}
          <div class="absolute inset-0 flex flex-col items-center justify-center gap-3 px-6 text-center">
            <p class="max-w-md text-ui text-fg/90">{playError}</p>
            <button type="button" class="btn" onclick={retryPlayback}>重新解析</button>
          </div>
        {/if}
      </div>
    {/if}

    <!-- 右侧信息列 17rem → 14rem：全站最短的几条「标签 — 值」，17rem 让每行的标签和值之间空出
         一百多像素，看着像两列互不相干的东西；收窄之后标签贴着值、标题栏也多出 48px。
         `lg:mt-0`：首行资料的上沿要和标题首行（也就是那两颗图标键）同一条线，原来 `mt-4` + `lg:pt-1`
         把它整体压低了 20px，标题和资料表之间凭空错开半行（窄屏是单列，那一行间距要留着）。 -->
    <div class="mt-5 grid gap-6 lg:grid-cols-[minmax(0,1fr)_14rem]">
      <div class="min-w-0">
        <!-- 下载 / 在平台打开跟着标题走（和 YouTube 弹窗那排同一套 `icon-act`）。
             原来是侧栏两颗整宽按钮 + 窄屏标题下再来一颗「下载到本地库」：同一个动作在
             两个位置各写一遍，窄屏那颗还把标题和标签之间撑出一行。现在收成标题行右端两颗。 -->
        <div class="flex items-start gap-3">
          <h2 class="min-w-0 flex-1 text-film-title font-bold text-fg lg:text-film-title-lg">
            {video.title}
          </h2>

          <!-- 这一行的高度钉成标题首行的行高（18/26 与 20/28，见 app.css 的 --text-film-title），
               36px 的按钮从上下各溢出几像素，圆心正好压在首行中线上；
               不钉的话 items-start 会把 36px 的方框顶对齐到行框上，看着偏低。 -->
          <div class="flex h-[var(--text-film-title--line-height)] shrink-0 items-center gap-1 lg:h-[var(--text-film-title-lg--line-height)]">
            <button
              type="button"
              class="icon-act"
              data-emphasis="primary"
              title={video.hasLocalFile ? '再下载一份' : '下载到本地库'}
              aria-label={video.hasLocalFile ? '再下载一份' : '下载到本地库'}
              onclick={() => (downloadOpen = true)}
              disabled={playback === null && loadingPlayback}
            >
              <svg
                viewBox="0 0 24 24"
                class="h-5 w-5"
                fill="none"
                stroke="currentColor"
                stroke-width="1.8"
                stroke-linecap="round"
              >
                <path d="M12 4v10" />
                <path d="m8 10.5 4 4 4-4" />
                <path d="M4.5 18.5h15" />
              </svg>
            </button>

            <a
              href={video.url}
              target="_blank"
              rel="noreferrer noopener"
              class="icon-act"
              title={`在 ${platformLabel(video.platform)} 打开`}
              aria-label={`在 ${platformLabel(video.platform)} 打开原始页面`}
            >
              <svg viewBox="0 0 24 24" class="h-5 w-5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round">
                <path d="M18 13.5V18a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h4.5" />
                <path d="M15 3.5h5.5V9" />
                <path d="M20.2 3.8 11 13" />
              </svg>
            </a>
          </div>
        </div>

        <!-- 作者行：样式与 `/youtube/:videoId` 同一套（40px 头像 + 名字 16px/500 + 下面一行时间），
             见 components/VideoByline.svelte。 -->
        <VideoByline avatarUrl={authorAvatar}>
          {#snippet primary()}
            {#each performers as person, index (person.name + String(index))}
              {#if person.id !== null}
                <a
                  data-link
                  href={`/performer/${String(person.id)}`}
                  class="underline-offset-4 transition-colors hover:text-hi hover:underline"
                >
                  {person.name}
                </a>
              {:else}
                <span>{person.name}</span>
              {/if}
              {#if index < performers.length - 1}
                <span class="text-dim" aria-hidden="true">/</span>
              {/if}
            {/each}
            {#if performers.length === 0}
              <span class="text-muted">未知艺人</span>
            {/if}
          {/snippet}
          {#snippet secondary()}
            <span>{relativeTime(publishedAt)}</span>
          {/snippet}
        </VideoByline>

        {#if video.tags.length > 0}
          <ul class="mt-3 flex flex-wrap items-center gap-1.5" aria-label="标签">
            {#each tagsExpanded ? video.tags : video.tags.slice(0, TAG_PREVIEW) as tag (tag)}
              <li class="chip">{tag}</li>
            {/each}
            {#if video.tags.length > TAG_PREVIEW}
              <li>
                <button
                  type="button"
                  class="chip text-fg transition-colors hover:bg-raise"
                  aria-expanded={tagsExpanded}
                  onclick={() => (tagsExpanded = !tagsExpanded)}
                >
                  {tagsExpanded ? '收起' : `+${String(video.tags.length - TAG_PREVIEW)}`}
                </button>
              </li>
            {/if}
          </ul>
        {/if}

        {#if video.description}
          <p class="mt-4 max-w-[68ch] whitespace-pre-line text-ui leading-relaxed text-muted">{video.description}</p>
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

        <!-- 下载 / 在平台打开已经挪到标题行右端（见上），这里只留一句状态说明 -->
        {#if video.hasLocalFile}
          <p class="mt-2 text-meta text-sage">已在本地 {video.localLibraryIds.length} 个库中</p>
        {/if}
      </aside>
    </div>

    {#if video.related.length > 0}
      <section class="mt-10">
        <h3 class="mb-3 text-title font-medium text-fg">相关影片</h3>
        <div class="grid grid-cols-2 gap-x-3 gap-y-6 sm:grid-cols-3 xl:grid-cols-4">
          {#each video.related.slice(0, 8) as related (related.id)}
            <VideoCard video={related} />
          {/each}
        </div>
      </section>
    {/if}
  </article>
{/if}

{#if video}
  <DownloadDialog videoId={video.id} open={downloadOpen} onClose={() => (downloadOpen = false)} />
{/if}
