<script lang="ts">
  import EmptyState from '../components/EmptyState.svelte';
  import TopBar from '../components/TopBar.svelte';
  import { api, messageFor } from '../lib/api';
  import { formatBytes, formatEta, formatSpeed, relativeTime } from '../lib/format';
  import { imageUrl } from '../lib/img';
  import { live } from '../lib/live.svelte';
  import { navigate } from '../lib/router.svelte';
  import type { DownloadItem, JobStatus } from '../lib/types';

  let error = $state('');
  let busyId = $state<number | null>(null);

  const items = $derived(live.downloads);
  const active = $derived(items.filter((d) => isActive(d.status)));
  const finished = $derived(items.filter((d) => !isActive(d.status)));

  const STATUS: Record<JobStatus, { label: string; tone: string }> = {
    queued: { label: '排队中', tone: 'text-muted' },
    running: { label: '下载中', tone: 'text-hi' },
    paused: { label: '已暂停', tone: 'text-muted' },
    done: { label: '已完成', tone: 'text-sage' },
    failed: { label: '失败', tone: 'text-brick' },
    canceled: { label: '已取消', tone: 'text-dim' },
  };

  function isActive(status: JobStatus): boolean {
    return status === 'queued' || status === 'running' || status === 'paused';
  }

  /** 剩余时间：优先用字节数算，未知则留空 */
  function etaOf(item: DownloadItem): string {
    if (item.status !== 'running') return '';
    if (item.bytesTotal > item.bytesDone && item.speedBps > 0) {
      // 体积预估只是估算，下载超过预估时就不再显示 ETA（避免负数）
      return formatEta((item.bytesTotal - item.bytesDone) / item.speedBps);
    }
    return '';
  }

  async function refresh(): Promise<void> {
    error = '';
    live.refreshDownloads(true);
  }

  async function act(item: DownloadItem, action: 'pause' | 'resume' | 'retry' | 'cancel' | 'delete'): Promise<void> {
    if (busyId !== null) return;
    busyId = item.id;
    error = '';
    try {
      if (action === 'pause') await api.pauseJob(item.id);
      else if (action === 'resume') await api.resumeJob(item.id);
      else if (action === 'retry') await api.retryJob(item.id);
      else if (action === 'cancel') await api.cancelJob(item.id);
      else await api.deleteJob(item.id);
      live.refreshDownloads(true);
    } catch (err) {
      const message = messageFor(err);
      error = message;
      live.toast(message, 'error');
    } finally {
      busyId = null;
    }
  }

  async function copyPath(path: string): Promise<void> {
    try {
      await navigator.clipboard.writeText(path);
      live.toast('路径已复制', 'success');
    } catch {
      live.toast('复制失败，请手动选择路径', 'error');
    }
  }

  $effect(() => {
    if (!live.downloadsLoaded) live.refreshDownloads(true);
  });
</script>

<div class="mx-auto w-full max-w-5xl">
  <TopBar title="下载" />

  <section>
    <div class="flex flex-wrap items-center justify-between gap-3">
      <p class="text-ui text-muted">
        最高画质原片 + Emby 元数据 · 当前占用 <span class="tabular-nums text-fg/90">{formatBytes(live.totalDownloadBytes)}</span>
      </p>
      <div class="flex items-center gap-2">
        <a data-link href="/settings#libraries" class="btn btn-ghost h-9">管理本地库</a>
        <button type="button" class="btn h-9" onclick={() => void refresh()}>刷新</button>
      </div>
    </div>

    {#if error}
      <p class="mt-3 text-ui text-brick" role="alert">{error}</p>
    {/if}

    {#if !live.downloadsLoaded}
      <div class="mt-6 space-y-2">
        {#each Array.from({ length: 3 }, (_, i) => i) as i (i)}
          <div class="h-16 animate-pulse rounded-xl bg-panel"></div>
        {/each}
      </div>
    {:else if items.length === 0}
      <div class="mt-6">
        <EmptyState
          title="下载队列还是空的"
          hint="在关注或详情页点下载按钮，选定本地库后就会开始拉取最高画质原片。"
          icon="film"
        >
          {#snippet actions()}
            <button type="button" class="btn btn-primary" onclick={() => navigate('/')}>去关注看看</button>
          {/snippet}
        </EmptyState>
      </div>
    {:else}
      {#if active.length > 0}
        <h2 class="mt-6 text-title font-medium text-fg">
          进行中 <span class="tabular-nums text-dim">{active.length}</span>
        </h2>
        <ul class="mt-2 space-y-2">
          {#each active as item (item.id)}
            {@const status = STATUS[item.status]}
            <li class="panel p-3">
              <div class="flex items-start gap-3">
                {#if item.thumbUrl}
                  <img
                    src={imageUrl(item.thumbUrl, 240) ?? ''}
                    alt=""
                    class="h-14 w-24 shrink-0 rounded-lg object-cover"
                    loading="lazy"
                    decoding="async"
                  />
                {:else}
                  <div class="h-14 w-24 shrink-0 rounded-lg bg-raise"></div>
                {/if}

                <div class="min-w-0 flex-1">
                  <div class="flex items-baseline justify-between gap-2">
                    <p class="truncate text-ui font-medium text-fg/90">{item.title ?? `#${String(item.targetId)}`}</p>
                    <span class="shrink-0 text-meta tabular-nums {status.tone}">{status.label}</span>
                  </div>
                  <p class="mt-0.5 truncate text-meta text-dim">
                    {item.libraryName ?? '未知库'} · {item.message || '等待开始'}
                  </p>

                  <div class="mt-2 h-1 w-full overflow-hidden rounded-full bg-raise" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow={Math.round(item.progress)}>
                    <div class="h-full rounded-full bg-hi transition-[width] duration-500" style={`width:${item.progress.toFixed(1)}%`}></div>
                  </div>

                  <div class="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-meta tabular-nums text-dim">
                    <span>{item.progress.toFixed(1)}%</span>
                    {#if item.bytesTotal > item.bytesDone && item.bytesTotal > 0}
                      <span>{formatBytes(item.bytesDone)} / {formatBytes(item.bytesTotal)}</span>
                    {:else if item.bytesDone > 0}
                      <!-- 预估偏小或缺失时只显示已下载体积 -->
                      <span>{formatBytes(item.bytesDone)}</span>
                    {/if}
                    {#if item.status === 'running' && item.speedBps > 0}
                      <span>{formatSpeed(item.speedBps)}</span>
                    {/if}
                    {#if etaOf(item)}
                      <span>剩余 {etaOf(item)}</span>
                    {/if}
                  </div>
                </div>

                <div class="flex shrink-0 items-center gap-1">
                  {#if item.status === 'running' || item.status === 'queued'}
                    <button type="button" class="btn btn-ghost h-8" onclick={() => void act(item, 'pause')} disabled={busyId === item.id}>
                      暂停
                    </button>
                  {:else if item.status === 'paused'}
                    <button type="button" class="btn h-8" onclick={() => void act(item, 'resume')} disabled={busyId === item.id}>
                      继续
                    </button>
                  {/if}
                  <button type="button" class="btn btn-ghost h-8" onclick={() => void act(item, 'cancel')} disabled={busyId === item.id}>
                    取消
                  </button>
                </div>
              </div>
            </li>
          {/each}
        </ul>
      {/if}

      {#if finished.length > 0}
        <h2 class="mt-6 text-title font-medium text-fg">
          历史 <span class="tabular-nums text-dim">{finished.length}</span>
        </h2>
        <ul class="mt-2 divide-y divide-line rounded-xl bg-panel">
          {#each finished as item (item.id)}
            {@const status = STATUS[item.status]}
            <li class="flex items-start gap-3 p-4">
              <div class="min-w-0 flex-1">
                <div class="flex items-baseline gap-2">
                  <p class="truncate text-ui text-fg/85">{item.title ?? `#${String(item.targetId)}`}</p>
                  <span class="shrink-0 text-meta {status.tone}">{status.label}</span>
                </div>
                <p class="mt-0.5 truncate text-meta text-dim">
                  {item.libraryName ?? '未知库'}
                  {#if item.sizeBytes} · {formatBytes(item.sizeBytes)}{/if}
                  {#if item.finishedAt} · {relativeTime(item.finishedAt)}{/if}
                </p>
                {#if item.error}
                  <p class="mt-1 line-clamp-2 text-meta text-brick">{item.error}</p>
                {/if}
                {#if item.localPath}
                  <button
                    type="button"
                    class="mt-1 block max-w-full truncate text-left text-meta text-dim underline decoration-line-strong underline-offset-2 hover:text-muted"
                    title="点击复制路径"
                    onclick={() => item.localPath && void copyPath(item.localPath)}
                  >
                    {item.localPath}
                  </button>
                {/if}
              </div>

              <div class="flex shrink-0 items-center gap-1">
                {#if item.status === 'failed' || item.status === 'canceled'}
                  <button type="button" class="btn h-8" onclick={() => void act(item, 'retry')} disabled={busyId === item.id}>
                    重试
                  </button>
                {/if}
                <button
                  type="button"
                  class="btn btn-ghost h-8"
                  aria-label="移除记录"
                  title="移除记录（不删文件）"
                  onclick={() => void act(item, 'delete')}
                  disabled={busyId === item.id}
                >
                  移除
                </button>
              </div>
            </li>
          {/each}
        </ul>
      {/if}
    {/if}
  </section>
</div>
