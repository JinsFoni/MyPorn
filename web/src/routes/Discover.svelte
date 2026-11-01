<script lang="ts">
  import EmptyState from '../components/EmptyState.svelte';
  import PlatformIcon from '../components/PlatformIcon.svelte';
  import Skeleton from '../components/Skeleton.svelte';
  import TopBar from '../components/TopBar.svelte';
  import { api, messageFor } from '../lib/api';
  import { imageUrl } from '../lib/img';
  import { formatCount } from '../lib/format';
  import { live } from '../lib/live.svelte';
  import { platformLabel } from '../lib/platform';
  import type { PlatformInfo, RemotePerformer, ResolvedPerformer } from '../lib/types';

  /** 仅 DEV：后端未就绪时的示例数据 */
  const MOCK_RESULTS: RemotePerformer[] = [
    {
      platform: 'pornhub',
      performerId: 'mini-diva',
      name: 'Mini Diva',
      avatarUrl: null,
      url: 'https://example.invalid/model/mini-diva',
      videoCount: 412,
      subscriberCount: 45600,
      subscribedId: 11,
    },
    {
      platform: 'pornhub',
      performerId: 'lena-kova',
      name: 'Lena Kova',
      avatarUrl: null,
      url: 'https://example.invalid/model/lena-kova',
      videoCount: 87,
      subscriberCount: 12400,
      subscribedId: null,
    },
  ];

  const MOCK_RESOLVED: ResolvedPerformer = {
    platform: 'pornhub',
    performerId: 'aya-sato',
    name: 'Aya Sato',
    avatarUrl: null,
    url: 'https://example.invalid/model/aya-sato',
    videoCount: 233,
  };

  let q = $state('');
  let searching = $state(false);
  let searched = $state(false);
  let results = $state<RemotePerformer[]>([]);
  let resolved = $state<ResolvedPerformer | null>(null);
  let subscribingUrl = $state<string | null>(null);
  let error = $state('');
  let mockNote = $state('');
  let backfillLimit = $state(0);
  let platformHint = $state('');
  /** 搜索用的平台：三个站各有各的艺人库，名字搜起来也更准。 */
  let platform = $state('pornhub');
  let platforms = $state<PlatformInfo[]>([]);

  const isUrl = $derived(/^https?:\/\//i.test(q.trim()));

  async function submit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    const value = q.trim();
    if (value.length === 0 || searching) return;
    error = '';
    resolved = null;
    if (isUrl) {
      await resolveUrl(value);
      return;
    }
    await search(value);
  }

  async function search(keyword: string): Promise<void> {
    searching = true;
    try {
      const res = await api.searchPerformers({ q: keyword, platform });
      results = res.items;
      searched = true;
      mockNote = '';
    } catch (err) {
      if (import.meta.env.DEV) {
        results = MOCK_RESULTS;
        searched = true;
        mockNote = messageFor(err);
      } else {
        error = messageFor(err);
      }
    } finally {
      searching = false;
    }
  }

  async function resolveUrl(url: string): Promise<void> {
    searching = true;
    try {
      resolved = await api.resolvePerformer(url);
      results = [];
      searched = false;
      mockNote = '';
    } catch (err) {
      if (import.meta.env.DEV) {
        resolved = MOCK_RESOLVED;
        mockNote = messageFor(err);
      } else {
        error = messageFor(err);
      }
    } finally {
      searching = false;
    }
  }

  async function subscribe(url: string): Promise<void> {
    if (subscribingUrl !== null) return;
    subscribingUrl = url;
    error = '';
    try {
      const res = await api.subscribe({ url, backfillLimit });
      live.toast(`已订阅 ${res.performer.name}，正在回填影片`, 'success');
      live.bumpFeed();
      const hit = results.find((item) => item.url === url);
      if (hit) hit.subscribedId = res.subscriptionId;
      if (resolved && resolved.url === url) resolved = null;
    } catch (err) {
      const message = messageFor(err);
      error = message;
      live.toast(message, 'error');
    } finally {
      subscribingUrl = null;
    }
  }

  $effect(() => {
    void (async () => {
      try {
        const list = await api.platforms();
        platforms = list;
      } catch {
        /* 平台列表只是辅助，拿不到就只显示默认那几个 */
      }
    })();
  });

  // 只提示当前选中平台的**真实失败**（status='error'），别的平台出问题不在这里吓人。
  // 之前这里还会为 status='no_proxy' 挂一条「没有配置代理，抓取可能失败」——那是按「国内直连一般不通」
  // 猜的，不是观测到的结果：本机 proxy_pornhub / global_proxy 都是空的，可
  // /api/search/performers?platform=pornhub&q=lena 照样返回艺人列表（出口在系统层或路由器上走代理，
  // 应用看不见），挂在那儿只是误导。能不能抓，等真出错再说。
  $effect(() => {
    const hit = platforms.find((p) => p.key === platform);
    platformHint = hit?.status === 'error' ? `${platformLabel(hit.key)} 最近抓取出错，订阅可能失败。` : '';
  });
</script>

<div class="mx-auto w-full max-w-2xl">
  <TopBar title="发现艺人" />

  <form class="flex flex-col gap-2 sm:flex-row" onsubmit={submit}>
    <div class="min-w-0 flex-1">
      <label for="discover-q" class="sr-only">艺人名字或主页链接</label>
      <input
        id="discover-q"
        class="field"
        type="search"
        inputmode="search"
        placeholder="输入艺人名字，或粘贴主页链接"
        autocomplete="off"
        bind:value={q}
        disabled={searching}
      />
    </div>
    <div class="flex gap-2">
      <button type="submit" class="btn btn-primary flex-1 sm:flex-none" disabled={searching || q.trim().length === 0}>
        {searching ? '查询中…' : isUrl ? '解析链接' : '搜索艺人'}
      </button>
    </div>
  </form>

  <div class="mt-3 flex flex-wrap items-center gap-2" role="group" aria-label="搜索平台">
    {#each (platforms.length > 0 ? platforms : [{ key: 'pornhub' }, { key: 'xhamster' }, { key: 'beeg' }]) as p (p.key)}
      <button
        type="button"
        class="chip h-8 gap-1.5 pl-1.5 pr-3 text-meta transition-colors {platform === p.key
          ? 'bg-raise text-fg ring-1 ring-white/25 ring-inset hover:bg-raise'
          : 'hover:bg-raise'}"
        aria-pressed={platform === p.key}
        onclick={() => {
          platform = p.key;
          results = [];
          searched = false;
          resolved = null;
        }}
      >
        <PlatformIcon platform={p.key} label={platformLabel(p.key)} size="sm" />
        {platformLabel(p.key)}
      </button>
    {/each}
  </div>

  <div class="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2">
    <label class="flex items-center gap-2 text-meta text-dim">
      首次回填范围
      <select class="field h-8 w-auto py-0 pl-2 pr-6 text-meta" bind:value={backfillLimit} aria-label="首次回填范围">
        <option value={0}>全部历史</option>
        <option value={50}>最近 50 部</option>
        <option value={200}>最近 200 部</option>
      </select>
    </label>
    {#if platformHint}
      <p class="text-meta text-brick">{platformHint}</p>
    {/if}
  </div>

  {#if mockNote}
    <p class="mt-3 rounded-xl bg-panel px-3 py-2 text-meta text-dim">
      离线预览：正在使用内置示例数据（后端未连接：{mockNote}）
    </p>
  {/if}

  {#if error}
    <p class="mt-3 text-ui text-brick" role="alert">{error}</p>
  {/if}

  {#if resolved}
    <section class="panel mt-5 p-4" aria-label="待订阅艺人">
      <p class="text-meta text-dim">链接解析成功</p>
      <div class="mt-3 flex items-center gap-3">
        <div class="h-12 w-12 shrink-0 overflow-hidden rounded-full bg-raise">
          {#if resolved.avatarUrl}
            <img
              data-media
              src={imageUrl(resolved.avatarUrl, 120) ?? ''}
              alt=""
              width="120"
              height="120"
              class="h-full w-full object-cover"
            />
          {/if}
        </div>
        <div class="min-w-0 flex-1">
          <p class="truncate text-ui font-medium text-fg">{resolved.name}</p>
          <p class="mt-0.5 flex items-center gap-1.5 text-meta tabular-nums text-dim">
            {formatCount(resolved.videoCount)} 部影片
            <PlatformIcon platform={resolved.platform} label={platformLabel(resolved.platform)} size="sm" />
          </p>
        </div>
        <button
          type="button"
          class="btn btn-primary"
          onclick={() => void subscribe(resolved?.url ?? '')}
          disabled={subscribingUrl !== null}
        >
          {subscribingUrl !== null ? '订阅中…' : '订阅'}
        </button>
      </div>
    </section>
  {/if}

  {#if searching && results.length === 0 && !resolved}
    <div class="mt-6 space-y-2">
      {#each Array.from({ length: 4 }, (_, i) => i) as i (i)}
        <Skeleton class="h-16 w-full" rounded="rounded-xl" />
      {/each}
    </div>
  {:else if results.length > 0}
    <ul class="mt-5 divide-y divide-line overflow-hidden rounded-xl bg-panel" role="list">
      {#each results as item (item.performerId)}
        <li class="flex items-center gap-3 px-4 py-3">
          <div class="h-10 w-10 shrink-0 overflow-hidden rounded-full bg-raise">
            {#if item.avatarUrl}
              <img
                data-media
                src={imageUrl(item.avatarUrl, 96) ?? ''}
                alt=""
                width="96"
                height="96"
                loading="lazy"
                class="h-full w-full object-cover"
              />
            {/if}
          </div>
          <div class="min-w-0 flex-1">
            <p class="truncate text-ui font-medium text-fg/90">{item.name}</p>
            <p class="mt-0.5 text-meta tabular-nums text-dim">
              {formatCount(item.videoCount)} 部影片{#if item.subscriberCount !== null}
                · {formatCount(item.subscriberCount)} 关注{/if}
            </p>
          </div>
          {#if item.subscribedId !== null}
            <span class="chip h-9 px-3.5 text-ui">
              已订阅
            </span>
          {:else}
            <button
              type="button"
              class="btn btn-primary"
              onclick={() => void subscribe(item.url)}
              disabled={subscribingUrl !== null}
              aria-label={`订阅 ${item.name}`}
            >
              {subscribingUrl === item.url ? '订阅中…' : '订阅'}
            </button>
          {/if}
        </li>
      {/each}
    </ul>
  {:else if !searched && !resolved}
    <EmptyState
      title="搜艺人，或者直接贴链接"
      hint="输入名字按所选平台搜索（名字要写全）；粘贴艺人主页链接可直接解析并订阅。只做艺人搜索，不含影片与分类浏览。"
      icon="search"
    />
  {:else if searched}
    <EmptyState title="没有找到匹配的艺人" hint="换个更短的关键词，或直接粘贴主页链接。" icon="search" />
  {/if}
</div>
