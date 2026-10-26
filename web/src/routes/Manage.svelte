<script lang="ts">
  import { onMount } from 'svelte';
  import BarList from '../components/BarList.svelte';
  import EmptyState from '../components/EmptyState.svelte';
  import PlatformIcon from '../components/PlatformIcon.svelte';
  import StatTile from '../components/StatTile.svelte';
  import SubscriptionRow from '../components/SubscriptionRow.svelte';
  import { api, messageFor } from '../lib/api';
  import { formatBytes, formatCount, formatDate, relativeTime } from '../lib/format';
  import { imageUrl } from '../lib/img';
  import { live } from '../lib/live.svelte';
  import { platformLabel } from '../lib/platform';
  import { navigate } from '../lib/router.svelte';
  import type { ManageCount, ManageResponse, ManageSubscription } from '../lib/types';

  /**
   * 管理页：统计 + 关注管理。
   *
   * 参考 peach 的做法——这一页是「数据库此刻的样子」，整页只打一个请求（/api/manage），
   * 上面那排读数、下面的分项和下面的关注列表都是同一份快照，不会这块新那块旧。
   * 页签不走地址栏：两个页签看的是同一份数据，只是展开方式不同，刷新回到「统计」即可。
   */

  type Tab = 'stats' | 'subs';

  let tab = $state<Tab>('stats');
  let data = $state<ManageResponse | null>(null);
  let loading = $state(true);
  let error = $state('');
  let query = $state('');
  let sort = $state<'recent' | 'videos' | 'new' | 'name'>('recent');
  let busyId = $state<number | null>(null);
  let bulkBusy = $state(false);
  let reloadSeq = 0;

  const stats = $derived(data?.stats ?? null);
  const subs = $derived(data?.subscriptions ?? []);

  async function load(silent = false): Promise<void> {
    const seq = (reloadSeq += 1);
    if (!silent) loading = data === null;
    error = '';
    try {
      const res = await api.manage();
      // 只认最后一次请求：连点刷新时别让早回来的旧数覆盖新数
      if (seq !== reloadSeq) return;
      data = res;
    } catch (err) {
      if (seq !== reloadSeq) return;
      error = messageFor(err);
    } finally {
      if (seq === reloadSeq) loading = false;
    }
  }

  onMount(() => {
    void load();
  });

  // 同步完成 / 有新片入库：重取一次快照（feedVersion 由 AppShell 的 SSE 统一 +1）
  let seenVersion = 0;
  $effect(() => {
    const version = live.feedVersion;
    if (version === seenVersion) return;
    seenVersion = version;
    if (data !== null) void load(true);
  });

  // ---------- 读数 ----------

  const tileHint = $derived.by(() => {
    if (!stats) return { performers: '', videos: '', files: '', space: '', downloads: '', failed: '' };
    return {
      performers: `启用 ${formatCount(stats.performersEnabled)} / ${formatCount(stats.performers)}`,
      videos: stats.videosNew > 0 ? `未看 ${formatCount(stats.videosNew)}` : '全部已看',
      files: `覆盖 ${formatCount(stats.videosWithFile)} 部影片`,
      space: `${formatCount(stats.byLibrary.length)} 个本地库`,
      downloads: stats.downloads.failed > 0 ? `失败 ${formatCount(stats.downloads.failed)}` : '无失败',
      failed: stats.performersFailed > 0 ? '需要处理' : '一切正常',
    };
  });

  // ---------- 分档的名字与固定顺序 ----------

  const DURATION: Record<string, string> = {
    'lt5': '< 5 分钟',
    '5to15': '5–15 分钟',
    '15to40': '15–40 分钟',
    gt40: '> 40 分钟',
    unknown: '未取得时长',
  };
  const DURATION_ORDER = ['lt5', '5to15', '15to40', 'gt40', 'unknown'];

  const QUALITY: Record<string, string> = {
    '2160p': '4K',
    '1440p': '2K',
    '1080p': '1080p',
    '720p': '720p',
    '480p': '480p',
    sd: '标清',
    none: '未解析',
    unknown: '未知',
  };
  const QUALITY_ORDER = ['2160p', '1440p', '1080p', '720p', '480p', 'sd', 'none', 'unknown'];

  /** 按固定顺序排列的分档；0 的档不画（缺一档就是 0，列出来只是噪声） */
  function bucketRows(rows: ManageCount[], labels: Record<string, string>, order: string[]): { name: string; value: number; note?: string }[] {
    const byKey = new Map(rows.map((row) => [row.key, row.n]));
    return order
      .filter((key) => key in labels && (byKey.get(key) ?? 0) > 0)
      .map((key) => ({ name: labels[key] ?? key, value: byKey.get(key) ?? 0 }));
  }

  const platformRows = $derived(
    (stats?.byPlatform ?? []).map((row) => ({
      name: platformLabel(row.key),
      value: row.n,
      note: row.bytes > 0 ? `已落盘 ${formatBytes(row.bytes)}` : '还没有本地副本',
    })),
  );

  const libraryRows = $derived(
    (stats?.byLibrary ?? []).map((row) => ({
      name: row.name,
      value: row.videos,
      note: row.bytes > 0 ? formatBytes(row.bytes) : '空库',
    })),
  );

  const durationRows = $derived(bucketRows(stats?.byDuration ?? [], DURATION, DURATION_ORDER));
  const qualityRows = $derived(bucketRows(stats?.byQuality ?? [], QUALITY, QUALITY_ORDER));
  // 年份分布：按数据里实际出现的年份排（不写死区间），「未知」永远垫底
  const yearRows = $derived.by(() => {
    const rows = stats?.byYear ?? [];
    return rows
      .filter((row) => row.key !== 'unknown')
      .map((row) => ({ name: `${row.key} 年`, value: row.n }))
      .concat(
        rows.filter((row) => row.key === 'unknown').map((row) => ({ name: '未知发布时间', value: row.n })),
      );
  });

  const ingest = $derived(stats?.ingest ?? []);
  const ingestMax = $derived(Math.max(1, ...ingest.map((row) => row.n)));
  const tags = $derived(stats?.tags ?? []);

  // ---------- 关注管理 ----------

  const filtered = $derived.by(() => {
    const keyword = query.trim().toLowerCase();
    const rows = keyword
      ? subs.filter(
          (item) =>
            item.name.toLowerCase().includes(keyword) ||
            platformLabel(item.platform).toLowerCase().includes(keyword),
        )
      : [...subs];
    const byNew = (a: ManageSubscription, b: ManageSubscription): number => b.newVideoCount - a.newVideoCount;
    const byVideos = (a: ManageSubscription, b: ManageSubscription): number => b.localVideoCount - a.localVideoCount;
    if (sort === 'videos') rows.sort(byVideos);
    else if (sort === 'new') rows.sort(byNew);
    else if (sort === 'name') rows.sort((a, b) => a.name.localeCompare(b.name, 'zh-Hans-CN'));
    else
      rows.sort((a, b) => {
        const at = a.lastSyncAt ? Date.parse(a.lastSyncAt) : 0;
        const bt = b.lastSyncAt ? Date.parse(b.lastSyncAt) : 0;
        return bt - at;
      });
    return rows;
  });

  const enabledCount = $derived(subs.filter((item) => item.enabled).length);
  const failedCount = $derived(subs.filter((item) => item.lastError).length);

  const SORT_OPTIONS = [
    { key: 'recent', label: '最近同步' },
    { key: 'new', label: '未看' },
    { key: 'videos', label: '影片数' },
    { key: 'name', label: '名字' },
  ] as const;

  async function toggle(item: ManageSubscription, enabled: boolean): Promise<void> {
    if (busyId !== null || item.subscriptionId === null) return;
    busyId = item.id;
    try {
      await api.updateSubscription(item.subscriptionId, { enabled });
      // 本地先改，不等整表重取；重取只为对齐 nextSyncAt
      data = {
        ...data!,
        subscriptions: (data?.subscriptions ?? []).map((row) =>
          row.id === item.id ? { ...row, enabled } : row,
        ),
      };
      live.toast(enabled ? `已恢复 ${item.name} 的同步` : `已暂停 ${item.name} 的同步`, 'success');
      void load(true);
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      busyId = null;
    }
  }

  async function sync(item: ManageSubscription): Promise<void> {
    if (busyId !== null || item.subscriptionId === null) return;
    busyId = item.id;
    try {
      await api.syncSubscription(item.subscriptionId, 'incremental');
      live.toast(`已排入 ${item.name} 的同步任务`, 'success');
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      busyId = null;
    }
  }

  async function unsubscribe(item: ManageSubscription): Promise<void> {
    if (busyId !== null || item.subscriptionId === null) return;
    busyId = item.id;
    try {
      await api.unsubscribe(item.subscriptionId);
      live.toast(`已取消关注 ${item.name}`, 'success');
      void load(true);
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      busyId = null;
    }
  }

  /** 一键同步：只提交启用的、最近没在同步的那些，别把队列塞满 */
  async function syncAll(): Promise<void> {
    if (bulkBusy) return;
    const targets = subs.filter(
      (item) => item.enabled && item.subscriptionId !== null && !live.isSyncing(item.id),
    );
    if (targets.length === 0) {
      live.toast('没有需要同步的艺人', 'info');
      return;
    }
    bulkBusy = true;
    let ok = 0;
    try {
      for (const item of targets) {
        try {
          await api.syncSubscription(item.subscriptionId as number, 'incremental');
          ok += 1;
        } catch {
          // 单个失败不打断整批，最后统一报数
        }
      }
      live.toast(`已排入 ${String(ok)} / ${String(targets.length)} 个同步任务`, ok > 0 ? 'success' : 'error');
    } finally {
      bulkBusy = false;
    }
  }

  const tabClass = (active: boolean): string =>
    `h-8 rounded-full px-3.5 text-ui transition-colors ${
      active ? 'bg-hi font-medium text-ink' : 'text-muted hover:bg-panel hover:text-fg'
    }`;
</script>

<div class="mx-auto w-full max-w-5xl">
  {#if error && data === null}
    <p class="mt-6 text-ui text-brick" role="alert">{error}</p>
  {:else if loading || !data || !stats}
    <div class="mt-2 space-y-2">
      <div class="grid grid-cols-2 gap-2 sm:grid-cols-3 xl:grid-cols-6">
        {#each Array.from({ length: 6 }, (_, i) => i) as i (i)}
          <div class="h-18 animate-pulse rounded-xl bg-panel"></div>
        {/each}
      </div>
      <div class="h-64 animate-pulse rounded-xl bg-panel"></div>
    </div>
  {:else}
    <!-- 读数条：六个读数就是这一页的摘要，明细在下面按页签展开 -->
    <div class="grid grid-cols-2 gap-2 sm:grid-cols-3 xl:grid-cols-6">
      <StatTile label="关注艺人" value={formatCount(stats.performers)} unit="位" hint={tileHint.performers} />
      <StatTile label="已收录影片" value={formatCount(stats.videos)} unit="部" hint={tileHint.videos} />
      <StatTile label="已落盘" value={formatCount(stats.files)} unit="个" hint={tileHint.files} />
      <StatTile label="占用空间" value={formatBytes(stats.bytes)} hint={tileHint.space} />
      <StatTile
        label="下载完成"
        value={formatCount(stats.downloads.done)}
        unit="个"
        hint={tileHint.downloads}
      />
      <StatTile
        label="同步异常"
        value={formatCount(stats.performersFailed)}
        unit="位"
        hint={tileHint.failed}
        tone={stats.performersFailed > 0 ? 'warn' : 'default'}
      />
    </div>

    <!-- 页签 + 刷新 -->
    <div class="mt-5 flex flex-wrap items-center justify-between gap-2">
      <div class="flex items-center gap-1" role="tablist" aria-label="管理视图">
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'stats'}
          class={tabClass(tab === 'stats')}
          onclick={() => (tab = 'stats')}
        >
          统计
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'subs'}
          class={tabClass(tab === 'subs')}
          onclick={() => (tab = 'subs')}
        >
          关注管理
          <span class="ml-1 tabular-nums text-dim">{formatCount(subs.length)}</span>
        </button>
      </div>
      <div class="flex items-center gap-2">
        <span class="hidden text-meta text-dim sm:inline">
          快照于 {relativeTime(data.generatedAt)}
        </span>
        {#if error}
          <span class="text-meta text-brick">{error}</span>
        {/if}
        <button type="button" class="btn btn-ghost h-8" onclick={() => void load(true)}>刷新</button>
      </div>
    </div>

    {#if tab === 'stats'}
      <div class="mt-3 flex flex-col gap-2">
        <div class="grid items-start gap-2 md:grid-cols-2">
          <BarList
            title="平台分布"
            rows={platformRows}
            unit="部"
            empty="还没有收录任何影片。去「发现」里关注一位艺人试试。"
          />
          <BarList
            title="本地库占用"
            rows={libraryRows}
            unit="部"
            empty="还没有本地库。在设置 → 本地库里添加一个目录。"
          />
        </div>

        <div class="grid items-start gap-2 md:grid-cols-3">
          <BarList title="时长分布" rows={durationRows} unit="部" />
          <BarList title="最高清晰度" rows={qualityRows} unit="部" />
          <BarList title="发布年份" rows={yearRows} unit="部" />
        </div>

        <div class="grid items-start gap-2 md:grid-cols-3">
          <!-- 入库趋势：柱子高度就是当月新增的影片数 -->
          <section class="panel flex flex-col p-4 md:col-span-2">
            <h3 class="text-ui font-medium text-fg">
              入库趋势
              {#if ingest.length > 1}
                <span class="text-meta text-dim">最近 {ingest.length} 个月</span>
              {/if}
            </h3>
            {#if ingest.length === 0}
              <p class="mt-3 text-meta text-dim">还没有入库记录</p>
            {:else}
              <div class="mt-3 flex h-32 items-stretch gap-1.5">
                {#each ingest as row (row.key)}
                  <div class="flex h-full min-w-0 flex-1 flex-col items-center gap-1">
                    <span class="text-meta tabular-nums text-dim">{row.n}</span>
                    <div class="flex w-full flex-1 items-end justify-center">
                      <div
                        class="w-full max-w-10 rounded-t-[3px] bg-hi/75"
                        style={`height:${Math.max(2, (row.n / ingestMax) * 100).toFixed(1)}%`}
                        title={`${row.key} · ${String(row.n)} 部`}
                      ></div>
                    </div>
                  </div>
                {/each}
              </div>
              <div class="mt-1 flex gap-1.5">
                {#each ingest as row (row.key)}
                  <span class="min-w-0 flex-1 truncate text-center text-meta tabular-nums text-dim">
                    {row.key.slice(5)}月
                  </span>
                {/each}
              </div>
            {/if}
          </section>

          <section class="panel flex flex-col gap-2.5 p-4">
            <h3 class="text-ui font-medium text-fg">下载任务</h3>
            <div class="flex items-baseline justify-between gap-2 text-meta">
              <span class="text-muted">已完成</span>
              <b class="tabular-nums text-fg">{formatCount(stats.downloads.done)}</b>
            </div>
            <div class="flex items-baseline justify-between gap-2 text-meta">
              <span class="text-muted">进行中</span>
              <b class="tabular-nums text-fg">{formatCount(stats.downloads.active)}</b>
            </div>
            <div class="flex items-baseline justify-between gap-2 text-meta">
              <span class="text-muted">失败</span>
              <b class="tabular-nums {stats.downloads.failed > 0 ? 'text-brick' : 'text-fg'}">
                {formatCount(stats.downloads.failed)}
              </b>
            </div>
            <div class="mt-1 flex items-baseline justify-between gap-2 border-t border-line pt-2 text-meta">
              <span class="text-muted">已落盘影片</span>
              <b class="tabular-nums text-fg">
                {formatCount(stats.videosWithFile)}<span class="ml-1 text-dim">/ {formatCount(stats.videos)}</span>
              </b>
            </div>
            <button type="button" class="btn btn-ghost mt-auto h-8" onclick={() => navigate('/downloads')}>
              打开下载页
            </button>
          </section>
        </div>

        {#if tags.length > 0}
          <section class="panel p-4">
            <h3 class="text-ui font-medium text-fg">
              热门标签 <span class="text-meta text-dim">出现最多的 {tags.length} 个</span>
            </h3>
            <ul class="mt-3 flex flex-wrap gap-1.5">
              {#each tags as tag (tag.key)}
                <li class="chip">
                  {tag.key}
                  <span class="tabular-nums text-dim">{formatCount(tag.n)}</span>
                </li>
              {/each}
            </ul>
          </section>
        {/if}

        <section class="panel p-4">
          <h3 class="text-ui font-medium text-fg">最近入库</h3>
          {#if stats.recent.length === 0}
            <p class="mt-3 text-meta text-dim">还没有影片</p>
          {:else}
            <ul class="mt-3 flex flex-col divide-y divide-line">
              {#each stats.recent as row (row.id)}
                <li class="flex items-center gap-3 py-2 first:pt-0 last:pb-0">
                  <a data-link href={`/video/${String(row.id)}`} class="flex min-w-0 flex-1 items-center gap-3">
                    {#if row.thumbUrl}
                      <img
                        src={imageUrl(row.thumbUrl, 240) ?? ''}
                        alt=""
                        class="h-11 w-20 shrink-0 rounded-lg object-cover"
                        loading="lazy"
                        decoding="async"
                      />
                    {:else}
                      <div class="h-11 w-20 shrink-0 rounded-lg bg-raise"></div>
                    {/if}
                    <span class="min-w-0 flex-1">
                      <span class="block truncate text-ui text-fg/90">{row.title}</span>
                      <span class="mt-0.5 flex items-center gap-1.5 text-meta text-dim">
                        <PlatformIcon platform={row.platform} label={platformLabel(row.platform)} size="sm" />
                        <span>{row.publishedAt ? formatDate(row.publishedAt) : '无发布时间'}</span>
                        {#if row.firstSeenAt}
                          <span aria-hidden="true">·</span>
                          <span>收录 {relativeTime(row.firstSeenAt)}</span>
                        {/if}
                      </span>
                    </span>
                  </a>
                </li>
              {/each}
            </ul>
          {/if}
        </section>
      </div>
    {:else}
      <!-- 关注管理 -->
      <div class="mt-3 flex flex-col gap-2">
        <div class="flex flex-wrap items-center gap-2">
          <input
            type="search"
            class="field h-9 max-w-64 flex-1"
            placeholder="搜索艺人"
            aria-label="搜索关注艺人"
            bind:value={query}
          />
          <div class="flex items-center gap-1" role="group" aria-label="排序">
            {#each SORT_OPTIONS as option (option.key)}
              <button
                type="button"
                class="h-8 rounded-full px-3 text-meta transition-colors {sort === option.key
                  ? 'bg-raise text-fg'
                  : 'text-dim hover:text-fg'}"
                aria-pressed={sort === option.key}
                onclick={() => (sort = option.key)}
              >
                {option.label}
              </button>
            {/each}
          </div>
          <div class="ml-auto flex items-center gap-2">
            <span class="text-meta text-dim">
              共 {formatCount(subs.length)} 位 · 启用 {formatCount(enabledCount)}
              {#if failedCount > 0}
                · <span class="text-brick">异常 {formatCount(failedCount)}</span>
              {/if}
            </span>
            <button type="button" class="btn h-8" disabled={bulkBusy} onclick={() => void syncAll()}>
              {bulkBusy ? '提交中…' : '全部同步'}
            </button>
          </div>
        </div>

        {#if subs.length === 0}
          <EmptyState
            title="还没有关注任何艺人"
            hint="在「发现」里搜索艺人并关注，或者打开艺人的主页点关注。"
            icon="user"
          >
            {#snippet actions()}
              <button type="button" class="btn btn-primary" onclick={() => navigate('/discover')}>
                去发现艺人
              </button>
            {/snippet}
          </EmptyState>
        {:else if filtered.length === 0}
          <p class="panel px-4 py-6 text-center text-ui text-dim">没有匹配「{query}」的艺人</p>
        {:else}
          <ul class="panel divide-y divide-line">
            {#each filtered as item (item.id)}
              <SubscriptionRow
                {item}
                busy={busyId === item.id}
                syncing={live.isSyncing(item.id)}
                onSync={(row) => void sync(row)}
                onToggle={(row, enabled) => void toggle(row, enabled)}
                onUnsubscribe={(row) => void unsubscribe(row)}
              />
            {/each}
          </ul>
        {/if}
      </div>
    {/if}
  {/if}
</div>
