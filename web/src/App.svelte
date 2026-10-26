<script lang="ts">
  import { onMount } from 'svelte';
  import AppShell from './components/AppShell.svelte';
  import Login from './routes/Login.svelte';
  import Feed from './routes/Feed.svelte';
  import Performers from './routes/Performers.svelte';
  import Discover from './routes/Discover.svelte';
  import Settings from './routes/Settings.svelte';
  import Downloads from './routes/Downloads.svelte';
  import NotFound from './routes/NotFound.svelte';
  import { api } from './lib/api';
  import { navigate, router } from './lib/router.svelte';
  import type { RouteName } from './lib/router.svelte';
  import type { Component } from 'svelte';

  /**
   * 详情页懒加载：首屏只发 shell + 影片流，把播放器/艺人详情挤到独立 chunk，
   * 保首屏包体（gzip ≤45KB）。chunk 命中后 promise 复用，不重复请求。
   */
  type IdPage = Component<{ id: string }>;

  const ID_PAGE_LOADERS: Record<'video' | 'performer', () => Promise<{ default: IdPage }>> = {
    video: () => import('./routes/Video.svelte') as Promise<{ default: IdPage }>,
    performer: () => import('./routes/Performer.svelte') as Promise<{ default: IdPage }>,
  };

  const idPageCache = new Map<string, Promise<{ default: IdPage }>>();

  function loadIdPage(name: 'video' | 'performer'): Promise<{ default: IdPage }> {
    const cached = idPageCache.get(name);
    if (cached) return cached;
    const pending = ID_PAGE_LOADERS[name]();
    idPageCache.set(name, pending);
    return pending;
  }

  let ready = $state(false);

  const match = $derived(router.match);

  const DEFAULT_TITLE: Record<RouteName | 'not-found', string> = {
    feed: '',
    performers: '艺人',
    performer: '',
    discover: '',
    downloads: '',
    video: '',
    settings: '',
    login: '登录',
    'not-found': '',
  };

  const shellTitle = $derived(DEFAULT_TITLE[match.name]);

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
    {#if match.name === 'feed'}
      <Feed />
    {:else if match.name === 'performers'}
      <Performers />
    {:else if match.name === 'performer'}
      {#await loadIdPage('performer')}
        <p class="py-16 text-center text-ui text-dim">正在载入…</p>
      {:then mod}
        {@const PerformerPage = mod.default}
        <PerformerPage id={match.params.id} />
      {:catch}
        <p class="py-16 text-center text-ui text-brick" role="alert">页面载入失败，刷新再试。</p>
      {/await}
    {:else if match.name === 'discover'}
      <Discover />
    {:else if match.name === 'video'}
      {#await loadIdPage('video')}
        <p class="py-16 text-center text-ui text-dim">正在载入…</p>
      {:then mod}
        {@const VideoPage = mod.default}
        <VideoPage id={match.params.id} />
      {:catch}
        <p class="py-16 text-center text-ui text-brick" role="alert">页面载入失败，刷新再试。</p>
      {/await}
    {:else if match.name === 'downloads'}
      <Downloads />
    {:else if match.name === 'settings'}
      <Settings />
    {:else}
      <NotFound />
    {/if}
  </AppShell>
{/if}
