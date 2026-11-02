<script lang="ts">
  import { onMount } from 'svelte';
  import AppShell from './components/AppShell.svelte';
  import Login from './routes/Login.svelte';
  import Feed from './routes/Feed.svelte';
  import Performers from './routes/Performers.svelte';
  import Discover from './routes/Discover.svelte';
  import SettingsDialog from './components/SettingsDialog.svelte';
  import ScrollThumb from './components/ScrollThumb.svelte';
  import Downloads from './routes/Downloads.svelte';
  import NotFound from './routes/NotFound.svelte';
  import { api } from './lib/api';
  import { navigate, router } from './lib/router.svelte';
  import { paneFromHash, ui } from './lib/ui.svelte';
  import type { RouteMatch, RouteName } from './lib/router.svelte';
  import type { Component } from 'svelte';

  /**
   * 详情页懒加载：首屏只发 shell + 影片流，把播放器/艺人详情挤到独立 chunk，
   * 保首屏包体（gzip ≤45KB）。chunk 命中后 promise 复用，不重复请求。
   */
  type IdPage = Component<{ id: string }>;
  type PlainPage = Component<Record<string, never>>;

  const ID_PAGE_LOADERS: Record<'video' | 'ytvideo' | 'performer', () => Promise<{ default: IdPage }>> = {
    video: () => import('./routes/Video.svelte') as Promise<{ default: IdPage }>,
    ytvideo: () => import('./routes/YoutubeVideo.svelte') as Promise<{ default: IdPage }>,
    performer: () => import('./routes/Performer.svelte') as Promise<{ default: IdPage }>,
  };

  const idPageCache = new Map<string, Promise<{ default: IdPage }>>();

  function loadIdPage(name: 'video' | 'ytvideo' | 'performer'): Promise<{ default: IdPage }> {
    const cached = idPageCache.get(name);
    if (cached) return cached;
    const pending = ID_PAGE_LOADERS[name]();
    idPageCache.set(name, pending);
    return pending;
  }

  /** 管理页是数据页，整块懒加载，别让它进首屏包 */
  let managePage: Promise<{ default: PlainPage }> | null = null;
  function loadManage(): Promise<{ default: PlainPage }> {
    managePage ??= import('./routes/Manage.svelte') as Promise<{ default: PlainPage }>;
    return managePage;
  }

  /** 动态照片墙同理懒加载（灯箱、瀑布流都在里面） */
  let momentsPage: Promise<{ default: PlainPage }> | null = null;
  function loadMoments(): Promise<{ default: PlainPage }> {
    momentsPage ??= import('./routes/Moments.svelte') as Promise<{ default: PlainPage }>;
    return momentsPage;
  }

  /** B 站视频墙同样是独立一页，一样懒加载 */
  let bilibiliPage: Promise<{ default: PlainPage }> | null = null;
  function loadBilibili(): Promise<{ default: PlainPage }> {
    bilibiliPage ??= import('./routes/Bilibili.svelte') as Promise<{ default: PlainPage }>;
    return bilibiliPage;
  }

  /** YouTube 视频墙同样懒加载（频道筛选、下载入口都在里面；单条播放已经搬去 `/youtube/:videoId`，
   *  那一页由 ID_PAGE_LOADERS 里的 `ytvideo` 懒加载） */
  let youtubePage: Promise<{ default: PlainPage }> | null = null;
  function loadYoutube(): Promise<{ default: PlainPage }> {
    youtubePage ??= import('./routes/Youtube.svelte') as Promise<{ default: PlainPage }>;
    return youtubePage;
  }

  let ready = $state(false);

  const match = $derived(router.match);

  const DEFAULT_TITLE: Record<RouteName | 'not-found', string> = {
    feed: '',
    performers: '艺人',
    performer: '',
    discover: '',
    downloads: '',
    // 动态页同样不画标题（2026-11-02：和 YouTube / B 站一样，顶部直接是博主头像条）
    moments: '',
    // YouTube / B 站这两页**连标题都不画**（2026-11-02：用户要求去掉左上角那个页面名）：
    // 顶部直接是和关注页同一套的头像条，那排动作按钮（「全部标为已看 / 添加频道」）
    // 跟着筛选行待在页面里。页面叫什么由侧边栏高亮 + 浏览器标题（路由表）负责。
    youtube: '',
    bilibili: '',
    manage: '管理',
    video: '',
    ytvideo: '',
    settings: '',
    login: '登录',
    'not-found': '',
  };

  // /settings 是「URL 上的弹窗」：地址栏换成 /settings 时弹窗打开，页面本身留在原地
  // （记下最后一个真实页面，弹窗盖在它上面，关掉时回到它，不重新拉数据）
  let lastPage = $state<RouteMatch | null>(null);

  $effect(() => {
    if (match.name !== 'settings') lastPage = match;
  });

  $effect(() => {
    if (match.name === 'settings') {
      ui.openFromRoute(paneFromHash(window.location.hash));
    } else if (ui.settingsOpen) {
      ui.closeFromRoute();
    }
  });

  const shown = $derived<RouteMatch>(
    match.name === 'settings'
      ? (lastPage ?? { name: 'feed', params: {}, title: '关注' })
      : match,
  );

  const shellTitle = $derived(DEFAULT_TITLE[shown.name]);

  onMount(() => {
    void (async () => {
      try {
        await api.me();
        if (router.isLogin) navigate('/', { replace: true });
      } catch {
        // 401 已由 api.ts 统一跳登录；其它错误交给页面自己提示
      } finally {
        ready = true;
      }
    })();
  });
</script>

{#if match.name === 'login'}
  <Login />
{:else if !ready}
  <div class="flex min-h-dvh items-center justify-center">
    <p class="text-ui text-dim">正在验证会话…</p>
  </div>
{:else}
  <AppShell title={shellTitle}>
    {#if shown.name === 'feed'}
      <Feed />
    {:else if shown.name === 'performers'}
      <Performers />
    {:else if shown.name === 'performer'}
      {#await loadIdPage('performer')}
        <p class="py-16 text-center text-ui text-dim">正在载入…</p>
      {:then mod}
        {@const PerformerPage = mod.default}
        <PerformerPage id={shown.params.id} />
      {:catch}
        <p class="py-16 text-center text-ui text-brick" role="alert">页面载入失败，刷新再试。</p>
      {/await}
    {:else if shown.name === 'discover'}
      <Discover />
    {:else if shown.name === 'video'}
      {#await loadIdPage('video')}
        <p class="py-16 text-center text-ui text-dim">正在载入…</p>
      {:then mod}
        {@const VideoPage = mod.default}
        <VideoPage id={shown.params.id} />
      {:catch}
        <p class="py-16 text-center text-ui text-brick" role="alert">页面载入失败，刷新再试。</p>
      {/await}
    {:else if shown.name === 'ytvideo'}
      {#await loadIdPage('ytvideo')}
        <p class="py-16 text-center text-ui text-dim">正在载入…</p>
      {:then mod}
        {@const YtVideoPage = mod.default}
        <YtVideoPage id={shown.params.videoId} />
      {:catch}
        <p class="py-16 text-center text-ui text-brick" role="alert">页面载入失败，刷新再试。</p>
      {/await}
    {:else if shown.name === 'moments'}
      {#await loadMoments()}
        <p class="py-16 text-center text-ui text-dim">正在载入…</p>
      {:then mod}
        {@const MomentsPage = mod.default}
        <MomentsPage />
      {:catch}
        <p class="py-16 text-center text-ui text-brick" role="alert">页面载入失败，刷新再试。</p>
      {/await}
    {:else if shown.name === 'youtube'}
      {#await loadYoutube()}
        <p class="py-16 text-center text-ui text-dim">正在载入…</p>
      {:then mod}
        {@const YoutubePage = mod.default}
        <YoutubePage />
      {:catch}
        <p class="py-16 text-center text-ui text-brick" role="alert">页面载入失败，刷新再试。</p>
      {/await}
    {:else if shown.name === 'bilibili'}
      {#await loadBilibili()}
        <p class="py-16 text-center text-ui text-dim">正在载入…</p>
      {:then mod}
        {@const BilibiliPage = mod.default}
        <BilibiliPage />
      {:catch}
        <p class="py-16 text-center text-ui text-brick" role="alert">页面载入失败，刷新再试。</p>
      {/await}
    {:else if shown.name === 'downloads'}
      <Downloads />
    {:else if shown.name === 'manage'}
      {#await loadManage()}
        <p class="py-16 text-center text-ui text-dim">正在载入…</p>
      {:then mod}
        {@const ManagePage = mod.default}
        <ManagePage />
      {:catch}
        <p class="py-16 text-center text-ui text-brick" role="alert">页面载入失败，刷新再试。</p>
      {/await}
    {:else}
      <NotFound />
    {/if}
  </AppShell>
  <SettingsDialog />
{/if}

<!-- 文档级滚动条（原生条已在 app.css 里关掉，见该处注释） -->
<ScrollThumb />
