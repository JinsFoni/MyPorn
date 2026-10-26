<script lang="ts">
  import InfiniteList from '../components/InfiniteList.svelte';
  import PlatformIcon from '../components/PlatformIcon.svelte';
  import PerformerProfile from '../components/PerformerProfile.svelte';
  import VideoCard from '../components/VideoCard.svelte';
  import EmptyState from '../components/EmptyState.svelte';
  import Skeleton from '../components/Skeleton.svelte';
  import { ApiError, api, messageFor } from '../lib/api';
  import { imageUrl } from '../lib/img';
  import { formatCount } from '../lib/format';
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

  const avatar = $derived(performer ? imageUrl(performer.avatarUrl, 240) : null);

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
          <!-- 图标即入口：点它直接开该艺人的平台主页（原来那个「平台主页」按钮已经去掉）。 -->
          <PlatformIcon
            platform={performer.platform}
            label={platformLabel(performer.platform)}
            href={performer.url}
          />
        </div>

        <!-- 简介不在这里：它和资料字段一起在下面的「个人资料」面板里整段显示，
             页头只留「是谁 + 有多少片」，避免同一段文字在一屏里出现两次。 -->

        <!-- 一行元信息，用中点分隔（beeg 的艺人页也是这么写的：数字直接跟着图标/词走）。
             本页不再显示任何同步信息（未看数、上次/下次同步、已暂停、同步出错、进度条），
             那些都在「管理 → 关注管理」里看：这儿只回答「这是谁、有多少片子」。 -->
        <p class="mt-2.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-meta text-dim">
          <span class="tabular-nums">{formatCount(performer.localVideoCount ?? performer.videoCount ?? 0)}部影片</span>
          {#if performer.subscriberCount !== null}
            <span aria-hidden="true">·</span>
            <span class="tabular-nums">{formatCount(performer.subscriberCount)}订阅</span>
          {/if}
        </p>
      </div>
    </div>

  </section>

  <!-- 资料与站外链接：来自艺人主页的「简介 / 资料区块 / 社交列表」。
       两项性癖偏好和平台统计行（播放量等）在解析时就被丢掉了，不会出现在这里。 -->
  <PerformerProfile profile={performer.profile} social={performer.social} bio={performer.bio} />

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
