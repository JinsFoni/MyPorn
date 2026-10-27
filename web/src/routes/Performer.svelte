<script lang="ts">
  import InfiniteList from '../components/InfiniteList.svelte';
  import PlatformIcon from '../components/PlatformIcon.svelte';
  import PerformerProfile from '../components/PerformerProfile.svelte';
  import ProfileIcon from '../components/ProfileIcon.svelte';
  import SocialLinks from '../components/SocialLinks.svelte';
  import VideoCard from '../components/VideoCard.svelte';
  import EmptyState from '../components/EmptyState.svelte';
  import Skeleton from '../components/Skeleton.svelte';
  import { ApiError, api, messageFor } from '../lib/api';
  import { imageUrl } from '../lib/img';
  import { formatBody, formatCount, formatRank } from '../lib/format';
  import { live } from '../lib/live.svelte';
  import { platformLabel } from '../lib/platform';
  import type { Performer, Video } from '../lib/types';

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
    profile: [
      { label: '性别', value: '女性' },
      { label: '出身地', value: 'Czech Republic' },
    ],
    social: [{ label: 'Twitter', url: 'https://example.invalid/mini-diva' }],
    body: { heightCm: 167, bust: 89, waist: 64, hips: 91 },
    rank: 269,
    videoCount: null,
    localVideoCount: 42,
    subscriberCount: 1430831,
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

  const performerId = $derived(Number.parseInt(id, 10));

  let performer = $state<Performer | null>(null);
  let loading = $state(true);
  let error = $state('');
  let mockNote = $state('');
  let listKey = $state(0);
  let lastBump = 0;

  const avatar = $derived(performer ? imageUrl(performer.avatarUrl, 320) : null);

  /** 「身材」那一行：后端已把身高/三围归一到厘米，这里只拼字符串，四项全空就不显示 */
  const bodyText = $derived(performer ? formatBody(performer.body) : '');
  /** 「排名」那一行：站点给的模特排名，没抓到就不显示 */
  const rankText = $derived(performer ? formatRank(performer.rank) : '');


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

  /* 本页不再提供任何写操作（同步 / 全量回填 / 启停 / 取消订阅都是「管理 → 关注管理」的事）：
     艺人主页只负责看——信息、同步进度、影片列表。 */

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

</script>

{#if loading && !performer}
  <section class="panel p-4 sm:p-5">
    <div class="flex items-start gap-4 sm:gap-5">
      <Skeleton class="h-26 w-26 shrink-0 self-center sm:h-32 sm:w-32" rounded="rounded-full" />
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

  <!-- 页头就是这一块，分三层：
       ① 头像 │ 名字 + 计数 + 身材 │ 资料行（`xl` 起并排；窄屏资料行跨整行掉到下面）
       ② 一道发丝线
       ③ 简介（左，跟在「简介」标签后面）＋ 站外链接药丸（右）
       原来贴在下面的「个人资料 / 社交媒体」两张卡取消了，页面少两块面板的高度。 -->
  <section class="panel p-4 sm:p-5">
    <div class="grid grid-cols-[auto_minmax(0,1fr)] items-start gap-x-4 gap-y-4 sm:gap-x-5">
      <!-- 头像尺寸跟艺人列表页的卡片同一口径（6.5rem / `sm` 起 8rem，正好等于列表栅格的轨道宽度）。
           `self-center`：头像在这一行里竖直居中，水平中线就落在右边那组「名字栏 + 资料表」的中线上
           （那组自己也是 `self-center`，所以资料表的中线跟着一起走）。 -->
      <div class="h-26 w-26 shrink-0 self-center overflow-hidden rounded-full bg-raise sm:h-32 sm:w-32">
        {#if avatar}
          <img data-media src={avatar} alt="" width="320" height="320" class="h-full w-full object-cover" />
        {:else}
          <div class="flex h-full w-full items-center justify-center text-dim">
            <svg viewBox="0 0 24 24" class="h-7 w-7" fill="none" stroke="currentColor" stroke-width="1.4" aria-hidden="true">
              <circle cx="12" cy="9" r="3.25" />
              <path d="M5.5 19.5a6.5 6.5 0 0 1 13 0" />
            </svg>
          </div>
        {/if}
      </div>

      <!-- 窄屏时这层是 `display: contents`：名字栏和资料表仍然是页头栅格的两个直接子项，
           资料表才跨得了两列、掉到头像下面。`xl` 起它才变成真栅格，把名字栏和资料表并排，
           并用 `items-start` 让两边的行起点贴在同一条上沿——于是名字/计数/身材/排名这四行
           就跟资料表的前四行一行对一行。整层自己 `self-center`，头像的中线仍然落在资料表的中线上。 -->
      <div
        class="contents xl:grid xl:grid-cols-[minmax(0,1fr)_minmax(0,1.35fr)] xl:items-start xl:gap-x-5 xl:self-center"
      >
        <!-- 名字这一栏按资料表的行距来排：每行 20px（资料行是 14px/1.25rem 的一行，名字用
             `leading-5` 压到同样 20px）+ `gap-y-2` 的 8px 行距，四行的总高 104px 跟四行资料表一样高。 -->
        <div class="grid min-w-0 gap-y-2">
          <div class="flex flex-wrap items-center gap-2">
            <!-- 名字 28px 但行高只有 20px：Roboto 的大写高 0.711em ≈ 19.9px，正好填满这一行，
                 字形不会把行距撑大（溢出的是上下留白，不占位）。 -->
            <h2 class="text-hero leading-5 font-normal tracking-[-0.01em] text-fg">{performer.name}</h2>
            <!-- 图标即入口：点它直接开该艺人的平台主页（原来那个「平台主页」按钮已经去掉）。
                 `sm`（24px 托底）配 28px 的名字；`-my-0.5` 抵掉托底比那一行多出的上下 2px，
                 否则 24px 会把这一行撑高到 24px，下面三行就整体错开 4px、跟资料行对不齐了。 -->
            <PlatformIcon
              platform={performer.platform}
              label={platformLabel(performer.platform)}
              href={performer.url}
              size="sm"
              class="-my-0.5"
            />
          </div>

          <!-- 计数与身材、排名一样，标签前挂一枚 16px 字形（资料行同一枚组件、同一套间距），
               于是这三行的文字都从同一条竖线起排；订阅跟在影片数后面，共用那枚胶片。 -->
          <p class="flex flex-wrap items-center gap-x-3 gap-y-1 text-ui text-dim">
            <span class="tabular-nums"
              ><ProfileIcon label="影片" />{formatCount(
                performer.localVideoCount ?? performer.videoCount ?? 0,
              )}部影片</span
            >
            {#if performer.subscriberCount !== null}
              <span aria-hidden="true">·</span>
              <span class="tabular-nums">{formatCount(performer.subscriberCount)}订阅</span>
            {/if}
          </p>

          {#if bodyText}
            <!-- 身材：站点资料行里的「身高」「三围」抓取时已归一成公制，紧跟在影片数下面，
                 所以它们在下面的资料行里不再重复出现。
                 标签前挂一枚 16px 剪影（跟资料行同一枚字形、同一套间距），左边这一栏的
                 「标签 + 读数」就都从同一条竖线起排。 -->
            <p class="flex flex-wrap items-baseline gap-x-2 text-ui">
              <span class="text-dim"><ProfileIcon label="身材" />身材</span>
              <span class="tabular-nums text-muted">{bodyText}</span>
            </p>
          {/if}

          {#if rankText}
            <!-- 排名：站点「排名信息」块里的模特排名（越小越靠前），跟身材一样是页头的读数 -->
            <p class="flex flex-wrap items-baseline gap-x-2 text-ui">
              <span class="text-dim"><ProfileIcon label="排名" />排名</span>
              <span class="tabular-nums text-muted">#{rankText}</span>
            </p>
          {/if}
        </div>

        <!-- 资料行（性别/身高/出身地/体重……）：来自艺人主页的「资料区块」。
             两项性癖偏好和平台统计行（播放量等）在解析时就被丢掉了，不会出现在这里。
             窄屏时它跨两列、整行掉到头像和名字下面。 -->
        {#if performer.profile.length}
          <div class="col-span-2 min-w-0 xl:col-span-1">
            <PerformerProfile profile={performer.profile} />
          </div>
        {/if}
      </div>
    </div>

    {#if performer.bio || performer.social.length}
      <!-- 发丝线以上是「她是谁 + 她的资料」，以下是一段自由文本和站外链接。 -->
      <div class="mt-4 flex flex-wrap items-start gap-x-6 gap-y-3 border-t border-line pt-3.5">
        {#if performer.bio}
          <!-- 简介：站点上就是一段自由文本，换行原样保留。宽度由右侧的药丸自然收窄，
               再用 text-pretty 收掉「只剩一个 emoji 单独一行」这种孤字行。 -->
          <div class="flex min-w-0 basis-full items-baseline gap-3 xl:flex-1 xl:basis-0">
            <span class="shrink-0 text-ui text-dim">简介</span>
            <p class="min-w-0 flex-1 whitespace-pre-line text-pretty text-ui text-muted">{performer.bio}</p>
          </div>
        {/if}

        <!-- 站外链接：来自艺人主页右侧的社交列表，全部新窗口打开 -->
        <SocialLinks social={performer.social} class={performer.bio ? 'xl:ml-auto' : ''} />
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
        <EmptyState
          title="还没有同步到影片"
          hint="到「管理 → 关注管理」点同步即可抓取；首次订阅会自动全量回填。"
          icon="film"
        />
      {/snippet}
    </InfiniteList>
  </section>
{/if}
