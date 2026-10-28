<script lang="ts">
  /**
   * B 站：UP 主订阅 + 投稿墙（M7）。
   *
   * 和 YouTube（/youtube）是同一个位置的两页：都是「订阅一个内容源」，
   * 所以都不混进「艺人」和「关注」流——那两处追的是艺人，语义不同。
   *
   * 三件事：看投稿（封面墙 + 点进 /video/:id 播放）、管 UP 主（添加 / 筛选 / 全部标已看）、
   * 下载（B 站的 480P 档位是音画分轨，下载与播放都由服务端合流，见 docs/M7-BILIBILI.md）。
   *
   * 数据边界：这里**没有自己的表**。UP 主是 performers 的一行、投稿是 videos 的一行
   * （platform='bilibili'），本页只是把它们按平台重新组织；播放/下载/合流复用
   * /api/videos/{id}/* 那套现成接口（理由见 internal/store/bilibili.go 顶部）。
   *
   * 最容易被误解的一点：**「只有 720P」是未登录时的正常结果**，不是解析失败。
   * 登录（填 SESSDATA）之后才谈得上 1080P 以上；ffmpeg 缺失则分轨档位根本放不了。
   * 这两件事都摆在页面上，不藏在设置里。
   */
  import { onMount } from 'svelte';
  import InfiniteList from '../components/InfiniteList.svelte';
  import EmptyState from '../components/EmptyState.svelte';
  import VideoCard from '../components/VideoCard.svelte';
  import PlatformIcon from '../components/PlatformIcon.svelte';
  import { api, messageFor } from '../lib/api';
  import { imageUrl } from '../lib/img';
  import { live } from '../lib/live.svelte';
  import { ui } from '../lib/ui.svelte';
  import { navigate } from '../lib/router.svelte';
  import { formatCount, relativeFuture } from '../lib/format';
  import type { BiliChannel, BiliHealth, ResolvedPerformer, Video } from '../lib/types';

  type Mode = 'all' | 'unseen';

  const MODES: readonly (readonly [Mode, string])[] = [
    ['all', '全部'],
    ['unseen', '未看'],
  ];

  // ---- UP 主 ----
  let channels = $state<BiliChannel[]>([]);
  let channelsLoaded = $state(false);
  let channelsError = $state('');
  let health = $state<BiliHealth | null>(null);

  // ---- 筛选 ----
  let filterChannel = $state<number | null>(null);
  let mode = $state<Mode>('all');
  /** 换筛选条件时 +1，让 InfiniteList 重头拉 */
  let wallKey = $state(0);

  // ---- 添加 UP 主 ----
  let addOpen = $state(false);
  let addUrl = $state('');
  let addBusy = $state(false);
  let addError = $state('');
  let preview = $state<ResolvedPerformer | null>(null);
  let addInput = $state<HTMLInputElement | null>(null);

  const unseenTotal = $derived(channels.reduce((sum, c) => sum + c.unseenCount, 0));
  const failing = $derived(channels.filter((c) => c.lastError !== ''));

  async function loadChannels(): Promise<void> {
    channelsError = '';
    try {
      const res = await api.biliChannels();
      channels = res.items;
      live.setBiliUnseen(res.stats.unseen);
    } catch (err) {
      channelsError = messageFor(err);
    } finally {
      channelsLoaded = true;
    }
  }

  async function loadHealth(): Promise<void> {
    try {
      health = await api.biliHealth();
    } catch {
      health = null;
    }
  }

  function loadPage(cursor: string | null): Promise<{ items: Video[]; nextCursor: string | null }> {
    return api.biliVideos({
      cursor,
      channelId: filterChannel,
      unseen: mode === 'unseen',
      limit: 36,
    });
  }

  function setChannel(id: number | null): void {
    filterChannel = id;
    wallKey += 1;
  }

  function setMode(next: Mode): void {
    mode = next;
    wallKey += 1;
  }

  onMount(() => {
    void loadChannels();
    void loadHealth();
  });

  // 后台抓到新投稿时刷墙（只刷第一页：墙按发布时间倒序，新的都在最前面）
  let seenVersion = 0;
  $effect(() => {
    const v = live.biliVersion;
    if (v === seenVersion) return;
    seenVersion = v;
    wallKey += 1;
    void loadChannels();
  });

  // ---- 已看 ----

  async function markAllSeen(): Promise<void> {
    try {
      const res = await api.biliSeen(filterChannel === null ? {} : { channelId: filterChannel });
      live.setBiliUnseen(res.unseen);
      await loadChannels();
      wallKey += 1;
      live.toast(filterChannel === null ? 'B 站投稿已全部标为已看' : '这位 UP 主的投稿已标为已看', 'success');
    } catch (err) {
      live.toast(messageFor(err), 'error');
    }
  }

  // ---- 添加 UP 主 ----

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
      const res = await api.resolvePerformer(raw);
      // 解析是通用的（任何已注册平台都能解析），这里只要 B 站：
      // 贴了 pornhub 的链接还照单全收，就会在 B 站墙上多出一个「艺人」。
      if (res.platform !== 'bilibili') {
        addError = '这是一条非 B 站的链接。这里只订阅 UP 主，请贴 space.bilibili.com/<mid> 或用户主页。';
        return;
      }
      preview = res;
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
      const res = await api.subscribe({ url: raw });
      const already = channels.some((c) => c.id === res.performer.id);
      live.toast(
        already ? `${res.performer.name} 已经订阅过了` : `已订阅 ${res.performer.name}，正在回填历史投稿`,
        'success',
      );
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
    }
  }
</script>

<svelte:window onkeydown={onKeydown} />

<div class="flex flex-col gap-4">
  <!-- 顶部：只有主动作。和 YouTube 一样，不在页内再放「动态 / YouTube / B 站」的切换：
       那是 /moments 这个入口的事（桌面走侧边栏，手机上走底部第 2 格）。 -->
  <div class="flex flex-wrap items-center justify-end gap-3">
    <div class="flex items-center gap-2">
      {#if unseenTotal > 0}
        <button
          type="button"
          class="rounded-full px-3 py-1.5 text-meta text-muted transition-colors hover:bg-panel hover:text-fg"
          onclick={() => void markAllSeen()}
        >
          {filterChannel === null ? '全部标为已看' : '这位标为已看'}
        </button>
      {/if}
      <button
        type="button"
        class="rounded-full bg-hi px-3.5 py-1.5 text-meta font-medium text-ink transition-opacity hover:opacity-90"
        onclick={openAdd}
      >
        添加 UP 主
      </button>
    </div>
  </div>

  <!-- 未登录：这不是故障，是 B 站的规则（未登录只给 720P / 480P）。 -->
  {#if health && !health.hasCookie}
    <div class="flex items-start gap-2 rounded-xl bg-panel px-3 py-2 text-meta text-muted">
      <span class="mt-0.5 text-dim">●</span>
      <p>
        没填 B 站 Cookie：最高只能看 720P、下载也只有 720P/480P。
        <button type="button" class="underline underline-offset-2" onclick={() => ui.openSettings('bilibili')}>
          填一个 Cookie
        </button>
        就能拿到 1080P 以上。
      </p>
    </div>
  {/if}

  <!-- 配了 Cookie 却没登录上：不是 B 站的问题，是存进去的那串不对（实测踩过：
       浏览器导出的 cookies.txt 被整理成两百多个假 SESSDATA，hasCookie 照样 true）。 -->
  {#if health && health.hasCookie && !health.loggedIn}
    <div class="flex items-start gap-2 rounded-xl bg-panel px-3 py-2 text-meta text-muted">
      <span class="mt-0.5 text-brick">●</span>
      <p>
        Cookie 存了但没登录成功（B 站说未登录），所以画质仍是 720P。
        整段 Cookie 头、只贴 SESSDATA 的值、浏览器导出的 cookies.txt 都能认，重新
        <button type="button" class="underline underline-offset-2" onclick={() => ui.openSettings('bilibili')}>
          填一次
        </button>
        试试。
      </p>
    </div>
  {/if}

  <!-- ffmpeg 缺失只影响「音画分轨」的档位：480P/1080P 都是两条流，要靠它合成。 -->
  {#if health && !health.ffmpegOk}
    <div class="flex items-start gap-2 rounded-xl bg-panel px-3 py-2 text-meta text-brick">
      <span class="mt-0.5">●</span>
      <p>
        没找到 ffmpeg：480P 及以上是音画分轨，合流要它——这些档位现在点开会失败。
        <button type="button" class="underline underline-offset-2" onclick={() => ui.openSettings('sync')}>
          去设置里配置
        </button>
      </p>
    </div>
  {/if}

  <!-- 限流是这个功能最常见的失败原因（B 站空间投稿接口全站风控最严的那个），
       把「下次什么时候再试」直接写出来，比只记一句错误有用。 -->
  {#if failing.length > 0}
    <div class="flex items-start gap-2 rounded-xl bg-panel px-3 py-2 text-meta text-brick">
      <span class="mt-0.5">●</span>
      <p class="min-w-0">
        {failing.length} 位 UP 主上次同步失败：{failing[0]?.lastError}
        {#if failing[0]?.nextSyncAt}
          <span class="text-dim">（{relativeFuture(failing[0].nextSyncAt)}再试）</span>
        {/if}
        <button
          type="button"
          class="ml-1 underline underline-offset-2"
          onclick={() => navigate('/manage?tab=bilibili')}
        >
          去管理
        </button>
      </p>
    </div>
  {/if}

  {#if channelsError}
    <p class="rounded-xl bg-panel p-3 text-ui text-brick" role="alert">{channelsError}</p>
  {/if}

  {#if channelsLoaded && channels.length > 0}
    <!-- 筛选：UP 主胶囊 + 右侧 全部/未看 -->
    <div class="flex flex-wrap items-center gap-2">
      <div class="-mx-1 flex min-w-0 flex-1 items-center gap-1.5 overflow-x-auto px-1 pb-1">
        <button
          type="button"
          onclick={() => setChannel(null)}
          class="shrink-0 rounded-full px-3 py-1.5 text-meta transition-colors {filterChannel === null
            ? 'bg-raise text-fg'
            : 'text-muted hover:bg-panel hover:text-fg'}"
        >
          全部 UP 主
        </button>
        {#each channels as channel (channel.id)}
          <button
            type="button"
            onclick={() => setChannel(channel.id)}
            title={`${channel.name} · 已收录 ${String(channel.videoCount)} 条`}
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
                {channel.name.slice(0, 1).toUpperCase()}
              </span>
            {/if}
            <span class="max-w-[9rem] truncate">{channel.name}</span>
            {#if live.isSyncing(channel.id)}
              <span class="h-1.5 w-1.5 shrink-0 animate-pulse rounded-full bg-hi" title="正在同步"></span>
            {:else if channel.unseenCount > 0}
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
      title="还没有订阅任何 UP 主"
      hint="贴一个 UP 主主页链接就能订阅。添加后会立刻回填历史投稿——B 站风控很严，被限流会自动停下来，过几小时再接着翻，已收的部分不会丢。"
      icon="film"
    >
      {#snippet actions()}
        <button type="button" class="rounded-full bg-hi px-4 py-2 text-ui font-medium text-ink" onclick={openAdd}>
          添加 UP 主
        </button>
      {/snippet}
    </EmptyState>
  {:else}
    <InfiniteList
      load={loadPage}
      keyOf={(video: Video) => video.id}
      reloadKey={wallKey}
      maxLoadedPages={6}
      gridClass="grid grid-cols-1 gap-x-4 gap-y-6 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4"
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
            title={mode === 'unseen' ? '没有未看的投稿' : '这里还没有投稿'}
            hint={mode === 'all'
              ? 'B 站的投稿列表接口限流很紧，第一次同步可能只进来一两页。稍后会自动接着翻；也可以去 管理 → B 站 手动点一次同步。'
              : '换个筛选条件看看。'}
            icon="film"
          />
        </div>
      {/snippet}

      {#snippet children(video: Video)}
        <VideoCard {video} />
      {/snippet}
    </InfiniteList>
  {/if}
</div>

<!-- 添加 UP 主 -->
{#if addOpen}
  <div class="fixed inset-0 z-50 flex items-end justify-center sm:items-center">
    <button type="button" class="absolute inset-0 bg-ink/85 backdrop-blur-sm" aria-label="关闭" onclick={closeAdd}
    ></button>
    <div
      class="relative z-10 w-full max-w-lg rounded-t-2xl bg-sheet p-4 sm:rounded-2xl sm:p-5"
      role="dialog"
      aria-label="添加 UP 主"
    >
      <h2 class="flex items-center gap-2 text-title font-medium text-fg">
        <PlatformIcon platform="bilibili" label="bilibili" size="xs" />
        添加 UP 主
      </h2>
      <p class="mt-1 text-meta text-dim">
        支持 UP 主主页（space.bilibili.com/&lt;mid&gt;）或贴一条视频链接。B 站不支持按名字搜人，
        所以这里只能贴链接。
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
          placeholder="https://space.bilibili.com/946974"
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
              {preview.name.slice(0, 1).toUpperCase()}
            </span>
          {/if}
          <div class="min-w-0 flex-1">
            <p class="truncate text-ui font-medium text-fg">{preview.name}</p>
            <p class="truncate text-meta text-dim">
              {#if preview.subscriberCount}
                {formatCount(preview.subscriberCount)} 粉丝
              {/if}
              {#if preview.videoCount}
                {#if preview.subscriberCount}<span aria-hidden="true">·</span>{/if}
                投稿 {formatCount(preview.videoCount)}
              {/if}
              {#if !preview.subscriberCount && !preview.videoCount}
                mid {preview.performerId}
              {/if}
            </p>
          </div>
        </div>
      {/if}

      <p class="mt-3 text-meta text-dim">
        订阅后会立刻回填历史投稿（每页 20 条）。B 站对空间列表接口限流很严，被限流时会自动停下、几小时后接着翻。
      </p>

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
