<script lang="ts">
  import PlatformIcon from './PlatformIcon.svelte';
  import { formatCount, formatBytes, relativeFuture, relativeTime } from '../lib/format';
  import { imageUrl } from '../lib/img';
  import { live } from '../lib/live.svelte';
  import { platformLabel } from '../lib/platform';
  import type { ManageSubscription } from '../lib/types';

  /**
   * 关注管理的一行：艺人 + 本地收录量 + 同步状态 + 三个动作（同步 / 启用开关 / 取消关注）。
   *
   * 取消关注是删除动作，点第一次只把按钮换成「确认取消」，再点一次才真的取消；
   * 5 秒没动就自己退回——比弹窗轻，也不会误删。
   */
  interface Props {
    item: ManageSubscription;
    /** 正在跑同步任务 */
    syncing?: boolean;
    /** 有请求在飞：三个动作一起禁用，避免连点 */
    busy?: boolean;
    onSync: (item: ManageSubscription) => void;
    onToggle: (item: ManageSubscription, enabled: boolean) => void;
    onUnsubscribe: (item: ManageSubscription) => void;
  }

  let { item, syncing = false, busy = false, onSync, onToggle, onUnsubscribe }: Props = $props();

  let confirming = $state(false);
  let confirmTimer = 0;

  function startConfirm(): void {
    confirming = true;
    window.clearTimeout(confirmTimer);
    confirmTimer = window.setTimeout(() => {
      confirming = false;
    }, 5000);
  }

  function cancelConfirm(): void {
    confirming = false;
    window.clearTimeout(confirmTimer);
  }

  $effect(() => () => window.clearTimeout(confirmTimer));

  const avatar = $derived(imageUrl(item.avatarUrl, 96));
  // 回填还没结束时显示页数进度（历史影片是一页页往前翻的）
  const backfill = $derived(item.backfillProgress);

  /** 正在跑同步时，这一行就地显示进度条（艺人页已经不放同步信息了，进度统一看这里） */
  const sync = $derived(live.syncState(item.id));
  const percent = $derived(
    sync && sync.totalPages && sync.totalPages > 0
      ? Math.min(100, Math.round((sync.page / sync.totalPages) * 100))
      : null,
  );
</script>

<li class="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3">
  <!-- 移动端：艺人独占一行，时间与动作并排第二行；桌面端三者一行 -->
  <a
    data-link
    href={`/performer/${String(item.id)}`}
    class="flex min-w-0 basis-full items-center gap-3 sm:basis-0 sm:flex-1"
    title={item.name}
  >
    <span class="h-10 w-10 shrink-0 overflow-hidden rounded-full bg-raise">
      {#if avatar}
        <img
          src={avatar}
          alt=""
          class="h-full w-full object-cover"
          loading="lazy"
          decoding="async"
        />
      {:else}
        <span class="grid h-full w-full place-items-center text-meta text-dim">
          {item.name.slice(0, 1)}
        </span>
      {/if}
    </span>

    <span class="min-w-0 flex-1">
      <span class="flex min-w-0 items-center gap-1.5">
        <span class="min-w-0 truncate text-ui {item.enabled ? 'text-fg' : 'text-muted'}">
          {item.name}
        </span>
        <PlatformIcon platform={item.platform} label={platformLabel(item.platform)} size="sm" />
      </span>
      <span class="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-meta text-dim">
        <span class="tabular-nums">本地 {formatCount(item.localVideoCount)} 部</span>
        {#if item.localFileCount > 0}
          <span aria-hidden="true">·</span>
          <span class="tabular-nums">已落盘 {formatCount(item.localFileCount)}（{formatBytes(item.localBytes)}）</span>
        {/if}
        {#if item.newVideoCount > 0}
          <span aria-hidden="true">·</span>
          <span class="tabular-nums text-fg/80">未看 {formatCount(item.newVideoCount)}</span>
        {/if}
      </span>
    </span>
  </a>

  <div class="min-w-0 flex-1 text-meta text-dim sm:flex-none sm:basis-44">
    {#if sync}
      <p class="flex items-baseline justify-between gap-2">
        <span class="min-w-0 truncate text-fg/85">
          {sync.phase === 'backfill' ? '回填历史' : '增量同步'}{#if sync.totalPages} · 第 {sync.page}/{sync.totalPages} 页{/if}
        </span>
        {#if percent !== null}
          <span class="shrink-0 tabular-nums">{percent}%</span>
        {/if}
      </p>
      <div class="mt-1.5 h-1 overflow-hidden rounded-full bg-raise">
        {#if percent !== null}
          <div class="h-full rounded-full bg-hi transition-[width] duration-500" style="width: {percent}%"></div>
        {:else}
          <div class="h-full w-1/3 animate-pulse rounded-full bg-hi/70"></div>
        {/if}
      </div>
      {#if sync.videosFound > 0}
        <p class="mt-1 tabular-nums">已入库 {formatCount(sync.videosFound)} 部</p>
      {/if}
    {:else if backfill}
      <p class="tabular-nums">
        回填历史 {backfill.donePages}{#if backfill.totalPages}/{backfill.totalPages}{/if} 页
      </p>
    {:else if item.lastSyncAt}
      <p>上次同步 {relativeTime(item.lastSyncAt)}</p>
    {:else}
      <p>还没同步过</p>
    {/if}
    {#if sync}
      <!-- 正在同步时不再报「下次同步」：这一格里已经全是进度，多一行只会让行变高 -->
    {:else if item.enabled && item.nextSyncAt}
      <p class="tabular-nums">下次 {relativeFuture(item.nextSyncAt)}</p>
    {:else if !item.enabled}
      <p>已停用，不参与定时同步</p>
    {/if}
    {#if item.lastError}
      <p class="line-clamp-2 text-brick" title={item.lastError}>{item.lastError}</p>
    {/if}
  </div>

  <div class="flex shrink-0 items-center gap-1.5">
    <button
      type="button"
      class="btn btn-ghost h-8"
      disabled={busy || syncing || !item.enabled}
      onclick={() => onSync(item)}
    >
      {syncing ? '同步中…' : '同步'}
    </button>

    <!-- 启用开关：药丸轨道 + 白色滑块，开=白轨黑点，关=灰轨 -->
    <button
      type="button"
      role="switch"
      aria-checked={item.enabled}
      aria-label={item.enabled ? `暂停同步 ${item.name}` : `恢复同步 ${item.name}`}
      title={item.enabled ? '暂停定时同步' : '恢复定时同步'}
      disabled={busy}
      onclick={() => onToggle(item, !item.enabled)}
      class="relative h-6 w-11 shrink-0 rounded-full transition-colors {item.enabled
        ? 'bg-hi'
        : 'bg-raise'}"
    >
      <span
        class="absolute top-1 h-4 w-4 rounded-full transition-all {item.enabled
          ? 'left-6 bg-ink'
          : 'left-1 bg-fg/70'}"
      ></span>
    </button>

    {#if confirming}
      <button
        type="button"
        class="btn btn-danger h-8"
        disabled={busy}
        onclick={() => {
          cancelConfirm();
          onUnsubscribe(item);
        }}
      >
        确认取消
      </button>
      <button type="button" class="btn btn-ghost h-8" onclick={cancelConfirm}>算了</button>
    {:else}
      <button type="button" class="btn btn-ghost h-8" disabled={busy} onclick={startConfirm}>
        取消关注
      </button>
    {/if}
  </div>
</li>
