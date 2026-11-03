<script lang="ts">
  import { onMount } from 'svelte';
  import BarList from '../components/BarList.svelte';
  import EmptyState from '../components/EmptyState.svelte';
  import PlatformIcon from '../components/PlatformIcon.svelte';
  import StatTile from '../components/StatTile.svelte';
  import SubscriptionRow from '../components/SubscriptionRow.svelte';
  import { api, messageFor } from '../lib/api';
  import BiliLoginDialog from '../components/BiliLoginDialog.svelte';
  import { formatBytes, formatCount, formatDate, formatDateTime, relativeFuture, relativeTime } from '../lib/format';
  import { imageUrl } from '../lib/img';
  import { live } from '../lib/live.svelte';
  import { confirmDialog } from '../lib/confirm.svelte';
  import { platformLabel } from '../lib/platform';
  import { navigate, router } from '../lib/router.svelte';
  import { ui } from '../lib/ui.svelte';
  import type {
    BiliChannel,
    BiliHealth,
    BiliManageResponse,
    ManageCount,
    ManageResponse,
    ManageSubscription,
    MomentAccount,
    MomentsManageResponse,
    YtChannel,
    YtHealth,
    YtManageResponse,
  } from '../lib/types';

  /**
   * 管理页：统计 + 关注管理。
   *
   * 参考 peach 的做法——这一页是「数据库此刻的样子」，整页只打一个请求（/api/manage），
   * 上面那排读数、下面的分项和下面的关注列表都是同一份快照，不会这块新那块旧。
   * 页签不走地址栏：两个页签看的是同一份数据，只是展开方式不同，刷新回到「统计」即可。
   */

  type Tab = 'stats' | 'subs' | 'moments' | 'youtube' | 'bilibili';

  let tab = $state<Tab>('stats');
  let data = $state<ManageResponse | null>(null);
  let loading = $state(true);
  let error = $state('');
  let query = $state('');
  let sort = $state<'recent' | 'videos' | 'new' | 'name'>('recent');
  let busyId = $state<number | null>(null);
  let bulkBusy = $state(false);
  let reloadSeq = 0;

  // ---- YouTube 管理（M6）：同样是独立接口，切到页签才拉 ----
  let yt = $state<YtManageResponse | null>(null);
  let ytLoading = $state(false);
  let ytError = $state('');
  let ytHealth = $state<YtHealth | null>(null);
  let ytBusyId = $state<number | null>(null);
  let ytAddUrl = $state('');
  let ytAddBusy = $state(false);

  // ---- B 站管理（M7）：和 YouTube 同构，同样是切到页签才拉 ----
  let bili = $state<BiliManageResponse | null>(null);
  let biliLoading = $state(false);
  let biliError = $state('');
  let biliHealth = $state<BiliHealth | null>(null);
  // 扫码登录弹窗（手填 Cookie 拿不到 HttpOnly 的 SESSDATA，也没有 refresh_token）
  let biliLoginOpen = $state(false);
  let biliBusyId = $state<number | null>(null);
  let biliAddUrl = $state('');
  let biliAddBusy = $state(false);

  // ---- 动态管理（M5）：单独一个接口，切到这个页签才去拉 ----
  let moments = $state<MomentsManageResponse | null>(null);
  let momentsLoading = $state(false);
  let momentsError = $state('');
  let momentBusyId = $state<number | null>(null);

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

  async function loadYt(): Promise<void> {
    ytLoading = yt === null;
    ytError = '';
    try {
      yt = await api.manageYt();
    } catch (err) {
      ytError = messageFor(err);
    } finally {
      ytLoading = false;
    }
    if (ytHealth === null) {
      try {
        ytHealth = await api.ytHealth();
      } catch {
        ytHealth = null;
      }
    }
  }

  async function syncYt(channel: YtChannel, deep: boolean): Promise<void> {
    ytBusyId = channel.id;
    try {
      const res = await api.ytSync(channel.id, deep);
      live.toast(
        res.jobId > 0
          ? `已投递「${channel.title}」${deep ? '深度回填' : '增量同步'}`
          : `「${channel.title}」已经在队列里了`,
        'info',
      );
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      ytBusyId = null;
    }
  }

  async function toggleYt(channel: YtChannel): Promise<void> {
    ytBusyId = channel.id;
    try {
      await api.ytPatch(channel.id, { enabled: !channel.enabled });
      await loadYt();
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      ytBusyId = null;
    }
  }

  async function removeYt(channel: YtChannel): Promise<void> {
    const ok = await confirmDialog.ask({
      title: `取消订阅「${channel.title}」？`,
      body: '只删记录，已下载到本地库的视频不会动。',
      confirmText: '取消订阅',
    });
    if (!ok) return;
    ytBusyId = channel.id;
    try {
      await api.ytDelete(channel.id);
      live.toast('已取消订阅', 'success');
      await loadYt();
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      ytBusyId = null;
    }
  }

  async function downloadYtUnseen(channel: YtChannel): Promise<void> {
    ytBusyId = channel.id;
    try {
      const res = await api.ytDownloadUnseen(channel.id);
      live.toast(res.queued > 0 ? `已排队 ${String(res.queued)} 条视频` : '没有未看的视频可下载', 'info');
      live.refreshDownloads(true);
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      ytBusyId = null;
    }
  }

  async function addYtChannel(): Promise<void> {
    const raw = ytAddUrl.trim();
    if (raw === '' || ytAddBusy) return;
    ytAddBusy = true;
    try {
      const res = await api.ytAdd(raw);
      live.toast(res.created ? `已订阅 ${res.channel.title}` : `${res.channel.title} 已经订阅过了`, 'success');
      ytAddUrl = '';
      await loadYt();
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      ytAddBusy = false;
    }
  }

  async function loadBili(): Promise<void> {
    biliLoading = bili === null;
    biliError = '';
    try {
      bili = await api.manageBili();
    } catch (err) {
      biliError = messageFor(err);
    } finally {
      biliLoading = false;
    }
    if (biliHealth === null) {
      try {
        biliHealth = await api.biliHealth();
      } catch {
        biliHealth = null;
      }
    }
  }

  /**
   * 同步一位 UP 主。deep=false 走增量（只拉第 1 页，App 接口那一路）；
   * deep=true 从断点继续往回翻历史（web 接口那一路，限流最紧）。
   */
  async function syncBili(channel: BiliChannel, deep: boolean): Promise<void> {
    biliBusyId = channel.id;
    try {
      const res = await api.syncSubscription(channel.subscriptionId, deep ? 'full' : 'incremental');
      live.toast(
        res.jobId > 0
          ? `已投递「${channel.name}」${deep ? '回填续翻' : '增量同步'}`
          : `「${channel.name}」已经在队列里了`,
        'info',
      );
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      biliBusyId = null;
    }
  }

  async function toggleBili(channel: BiliChannel): Promise<void> {
    biliBusyId = channel.id;
    try {
      await api.updateSubscription(channel.subscriptionId, { enabled: !channel.enabled });
      await loadBili();
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      biliBusyId = null;
    }
  }

  async function removeBili(channel: BiliChannel): Promise<void> {
    const ok = await confirmDialog.ask({
      title: `取消订阅「${channel.name}」？`,
      body: '它的投稿会一起从 B 站墙上撤下（重新订阅就能恢复），已下载到本地库的文件不会动。',
      confirmText: '取消订阅',
    });
    if (!ok) return;
    biliBusyId = channel.id;
    try {
      await api.unsubscribe(channel.subscriptionId);
      live.toast('已取消订阅', 'success');
      await loadBili();
      // 角标是「还在订阅里的 UP 主的未看投稿」，删掉一位就得跟着降，不然点进去是空的。
      void live.refreshBiliUnseen();
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      biliBusyId = null;
    }
  }

  async function downloadBiliUnseen(channel: BiliChannel): Promise<void> {
    biliBusyId = channel.id;
    try {
      const res = await api.biliDownloadUnseen(channel.id);
      live.toast(
        res.queued > 0 ? `已排队 ${String(res.queued)} 条投稿（下完进本地库）` : '这位 UP 主没有未看的投稿',
        'info',
      );
      live.refreshDownloads(true);
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      biliBusyId = null;
    }
  }

  async function addBiliChannel(): Promise<void> {
    const raw = biliAddUrl.trim();
    if (raw === '' || biliAddBusy) return;
    biliAddBusy = true;
    try {
      const res = await api.subscribe({ url: raw });
      live.toast(`已订阅 ${res.performer.name}，正在回填历史投稿`, 'success');
      biliAddUrl = '';
      await loadBili();
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      biliAddBusy = false;
    }
  }

  async function loadMoments(): Promise<void> {
    momentsLoading = moments === null;
    momentsError = '';
    try {
      moments = await api.manageMoments();
    } catch (err) {
      momentsError = messageFor(err);
    } finally {
      momentsLoading = false;
    }
  }

  async function syncMoment(account: MomentAccount): Promise<void> {
    momentBusyId = account.id;
    try {
      const res = await api.syncMomentAccount(account.id, account.backfillDone ? 'auto' : 'full');
      live.toast(res.queued ? `已投递 @${account.handle} 的同步` : '这个博主已经在队列里了', 'info');
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      momentBusyId = null;
    }
  }

  async function refillMoment(account: MomentAccount): Promise<void> {
    momentBusyId = account.id;
    try {
      await api.patchMomentAccount(account.id, { mode: 'full' });
      const res = await api.syncMomentAccount(account.id, 'full');
      live.toast(res.queued ? `已从头回填 @${account.handle}` : '@' + account.handle + ' 已在队列里', 'success');
      await loadMoments();
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      momentBusyId = null;
    }
  }

  async function unlinkMoment(account: MomentAccount): Promise<void> {
    momentBusyId = account.id;
    try {
      await api.patchMomentAccount(account.id, { performerId: 0 });
      live.toast(`已解除 @${account.handle} 与艺人的关联`, 'info');
      await loadMoments();
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      momentBusyId = null;
    }
  }

  async function toggleMoment(account: MomentAccount): Promise<void> {
    momentBusyId = account.id;
    try {
      await api.patchMomentAccount(account.id, { enabled: !account.enabled });
      await loadMoments();
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      momentBusyId = null;
    }
  }

  async function removeMoment(account: MomentAccount): Promise<void> {
    const ok = await confirmDialog.ask({
      title: `取消订阅 @${account.handle}？`,
      body: '已存进本地库的照片不会删除。',
      confirmText: '取消订阅',
    });
    if (!ok) return;
    momentBusyId = account.id;
    try {
      await api.deleteMomentAccount(account.id);
      live.toast(`已取消 @${account.handle}`, 'info');
      await loadMoments();
      live.bumpMoments();
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      momentBusyId = null;
    }
  }

  onMount(() => {
    void load();
    // /manage?tab=bilibili 这样的深链接（B 站墙上的「去管理」按钮就发这个地址）。
    const want = router.query.get('tab');
    if (want === 'youtube' || want === 'bilibili' || want === 'moments' || want === 'subs') {
      tab = want;
      if (want === 'youtube') void loadYt();
      else if (want === 'bilibili') void loadBili();
      else if (want === 'moments') void loadMoments();
    }
  });

  // 切到「动态管理」时按需拉一次
  let momentsLoadedOnce = false;
  $effect(() => {
    if (tab !== 'moments' || momentsLoadedOnce) return;
    momentsLoadedOnce = true;
    void loadMoments();
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
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'moments'}
          class={tabClass(tab === 'moments')}
          onclick={() => (tab = 'moments')}
        >
          动态管理
          {#if moments}
            <span class="ml-1 tabular-nums text-dim">{formatCount(moments.stats.accounts)}</span>
          {/if}
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'youtube'}
          class={tabClass(tab === 'youtube')}
          onclick={() => {
            tab = 'youtube';
            if (yt === null && !ytLoading) void loadYt();
          }}
        >
          YouTube
          {#if yt}
            <span class="ml-1 tabular-nums text-dim">{formatCount(yt.stats.channels)}</span>
          {/if}
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'bilibili'}
          class={tabClass(tab === 'bilibili')}
          onclick={() => {
            tab = 'bilibili';
            if (bili === null && !biliLoading) void loadBili();
          }}
        >
          B 站
          {#if bili}
            <span class="ml-1 tabular-nums text-dim">{formatCount(bili.stats.channels)}</span>
          {/if}
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
                    <span class="h-11 w-20 shrink-0 overflow-hidden rounded-lg">
                      {#if row.thumbUrl}
                        <img
                          data-media
                          src={imageUrl(row.thumbUrl, 240) ?? ''}
                          alt=""
                          class="h-full w-full object-cover"
                          loading="lazy"
                          decoding="async"
                        />
                      {:else}
                        <span class="block h-full w-full bg-raise"></span>
                      {/if}
                    </span>
                    <span class="min-w-0 flex-1">
                      <span class="block truncate text-ui text-fg/90">{row.title}</span>
                      <span class="mt-0.5 flex items-center gap-1.5 text-meta text-dim">
                        <PlatformIcon platform={row.platform} label={platformLabel(row.platform)} size="xs" />
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
    {:else if tab === 'subs'}
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
    {:else if tab === 'moments'}
      <!-- 动态管理（M5）：博主列表 + 每人的抓取状态 -->
      <div class="mt-3 flex flex-col gap-2">
        <div class="flex flex-wrap items-center gap-2">
          <span class="text-meta text-dim">
            {#if moments}
              共 {formatCount(moments.stats.accounts)} 位博主 · {formatCount(moments.stats.posts)} 帖 ·
              {formatCount(moments.stats.photos)} 张照片
              {#if moments.stats.errors > 0}
                · <span class="text-brick">异常 {formatCount(moments.stats.errors)} 位</span>
              {/if}
            {:else}
              正在载入…
            {/if}
          </span>
          <div class="ml-auto flex items-center gap-2">
            {#if moments && !moments.hasInstagramCookie}
              <span class="text-meta text-dim">Instagram 未配置 Cookie，加不了 IG 博主</span>
            {/if}
            <button type="button" class="btn btn-ghost h-8" onclick={() => void loadMoments()}>刷新</button>
            <button type="button" class="btn h-8" onclick={() => navigate('/moments')}>打开照片墙</button>
          </div>
        </div>

        {#if momentsError}
          <p class="panel px-4 py-3 text-ui text-brick" role="alert">{momentsError}</p>
        {:else if momentsLoading || moments === null}
          <div class="panel px-4 py-10 text-center text-ui text-dim">正在载入…</div>
        {:else if moments.items.length === 0}
          <EmptyState
            title="还没有订阅任何博主"
            hint="在「动态」里贴一个 X 或 Instagram 的主页链接就能开始抓图。"
            icon="user"
          >
            {#snippet actions()}
              <button type="button" class="btn btn-primary" onclick={() => navigate('/moments')}>
                去动态添加
              </button>
            {/snippet}
          </EmptyState>
        {:else}
          <ul class="panel divide-y divide-line">
            {#each moments.items as account (account.id)}
              <li class="flex flex-wrap items-center gap-3 px-3 py-2.5">
                {#if account.avatarUrl}
                  <img
                    src={imageUrl(account.avatarUrl, 80) ?? ''}
                    alt=""
                    class="h-9 w-9 shrink-0 rounded-full object-cover"
                    loading="lazy"
                    decoding="async"
                  />
                {:else}
                  <span
                    class="grid h-9 w-9 shrink-0 place-items-center rounded-full bg-raise text-meta text-dim"
                  >
                    {account.handle.slice(0, 1).toUpperCase()}
                  </span>
                {/if}

                <div class="min-w-0 flex-1">
                  <div class="flex items-center gap-1.5">
                    <span class="truncate text-ui text-fg">{account.displayName || account.handle}</span>
                    <span class="shrink-0 text-meta text-dim">{account.platform === 'x' ? 'X' : 'Instagram'}</span>
                    {#if !account.enabled}
                      <span class="shrink-0 rounded-full bg-raise px-1.5 text-meta text-dim">已停用</span>
                    {/if}
                  </div>
                  <div class="mt-0.5 flex flex-wrap items-center gap-x-1.5 text-meta text-dim">
                    <span class="truncate">@{account.handle}</span>
                    <span aria-hidden="true">·</span>
                    <span class="tabular-nums">{formatCount(account.postCount)} 帖 / {formatCount(account.photoCount)} 张</span>
                    {#if account.performerName}
                      <span aria-hidden="true">·</span>
                      <a
                        data-link
                        href={`/performer/${String(account.performerId)}`}
                        class="truncate text-muted hover:text-fg"
                        >艺人 {account.performerName}</a
                      >
                      <button
                        type="button"
                        class="shrink-0 text-dim hover:text-brick"
                        disabled={momentBusyId === account.id}
                        onclick={() => void unlinkMoment(account)}
                        title="解除与艺人的关联（只影响关联，不删任何东西）"
                      >
                        解除
                      </button>
                    {:else if account.performerId === 0}
                      <span aria-hidden="true">·</span>
                      <span class="text-dim">未关联艺人</span>
                    {/if}
                    {#if account.lastSyncedAt}
                      <span aria-hidden="true">·</span>
                      <span>{relativeTime(account.lastSyncedAt)}同步</span>
                    {/if}
                    {#if account.backfillDone}
                      <span aria-hidden="true">·</span>
                      <span>历史已回填</span>
                    {/if}
                  </div>
                  {#if account.lastError}
                    <p class="mt-1 line-clamp-2 text-meta text-brick">{account.lastError}</p>
                  {/if}
                </div>

                <div class="flex shrink-0 items-center gap-1">
                  <button
                    type="button"
                    class="h-8 rounded-full px-2.5 text-meta text-dim transition-colors hover:bg-raise hover:text-fg"
                    disabled={momentBusyId === account.id}
                    onclick={() => void syncMoment(account)}
                  >
                    同步
                  </button>
                  <button
                    type="button"
                    class="h-8 rounded-full px-2.5 text-meta text-dim transition-colors hover:bg-raise hover:text-fg"
                    disabled={momentBusyId === account.id}
                    onclick={() => void refillMoment(account)}
                    title="清掉回填断点，从头再抓一遍"
                  >
                    重填
                  </button>
                  <button
                    type="button"
                    class="h-8 rounded-full px-2.5 text-meta text-dim transition-colors hover:bg-raise hover:text-fg"
                    disabled={momentBusyId === account.id}
                    onclick={() => void toggleMoment(account)}
                  >
                    {account.enabled ? '停用' : '启用'}
                  </button>
                  <button
                    type="button"
                    class="h-8 rounded-full px-2.5 text-meta text-brick/80 transition-colors hover:bg-raise hover:text-brick"
                    disabled={momentBusyId === account.id}
                    onclick={() => void removeMoment(account)}
                  >
                    删除
                  </button>
                </div>
              </li>
            {/each}
          </ul>
          <p class="text-meta text-dim">
            删除博主只清记录，已经存进本地库的照片不会被删。
          </p>
        {/if}
      </div>
    {:else if tab === 'youtube'}
      <!-- YouTube 管理（M6）：频道列表 + yt-dlp 健康度 -->
      <div class="mt-3 flex flex-col gap-2">
        <div class="flex flex-wrap items-center gap-2">
          <span class="text-meta text-dim">
            {#if yt}
              共 {formatCount(yt.stats.channels)} 个频道 · {formatCount(yt.stats.videos)} 条视频 ·
              {formatCount(yt.stats.unseen)} 条未看
              {#if yt.stats.errors > 0}
                · <span class="text-brick">异常 {formatCount(yt.stats.errors)} 个</span>
              {/if}
            {:else}
              正在载入…
            {/if}
          </span>
          <div class="ml-auto flex items-center gap-2">
            <button type="button" class="btn btn-ghost h-8" onclick={() => void loadYt()}>刷新</button>
            <button type="button" class="btn h-8" onclick={() => navigate('/youtube')}>打开视频墙</button>
          </div>
        </div>

        <!-- yt-dlp 健康度：这一格是「为什么下载总是失败」的答案，放在最上面 -->
        {#if ytHealth}
          <div class="panel px-4 py-3">
            {#if !ytHealth.found}
              <p class="text-ui text-brick">没找到 yt-dlp：只能抓 RSS（没有时长），也不能下载。</p>
              {#if ytHealth.error}
                <p class="mt-1 break-all font-mono text-[0.6875rem] text-brick">{ytHealth.error}</p>
              {/if}
              <p class="mt-1 text-meta text-dim">
                装一个或在设置里填绝对路径（pipx / brew / 官方二进制都行），然后回来点「重新检查」。
              </p>
            {:else}
              <div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-meta">
                <span class="text-fg">yt-dlp {ytHealth.version}</span>
                <span class={ytHealth.stale ? 'text-brick' : 'text-sage'}>
                  {ytHealth.stale ? `已 ${String(ytHealth.ageDays)} 天未更新，下载很可能 403` : '版本新鲜'}
                </span>
                <span class={ytHealth.ffmpegOk ? 'text-dim' : 'text-brick'}>
                  {ytHealth.ffmpegOk ? 'ffmpeg 可用' : 'ffmpeg 缺失，合轨会失败'}
                </span>
                <span class="truncate font-mono text-[0.6875rem] text-dim" title={ytHealth.path}>{ytHealth.path}</span>
              </div>
            {/if}
            <div class="mt-2 flex items-center gap-2">
              <button
                type="button"
                class="rounded-full bg-raise px-2.5 py-1 text-meta text-fg transition-colors hover:bg-panel"
                onclick={() => {
                  ytHealth = null;
                  void loadYt();
                }}
              >
                重新检查
              </button>
              <button type="button" class="text-meta text-dim hover:text-fg" onclick={() => ui.openSettings('youtube')}>
                打开 YouTube 设置
              </button>
            </div>
          </div>
        {/if}

        <!-- 直接在这里加频道：管理页是「一次性配好」的地方，不必跳回视频墙 -->
        <form
          class="flex flex-wrap items-center gap-2"
          onsubmit={(event) => {
            event.preventDefault();
            void addYtChannel();
          }}
        >
          <input
            bind:value={ytAddUrl}
            class="field h-9 min-w-0 flex-1"
            type="text"
            placeholder="频道链接、@名 或 UC 开头的频道 id"
            spellcheck="false"
            autocomplete="off"
            disabled={ytAddBusy}
          />
          <button type="submit" class="btn h-9" disabled={ytAddBusy || ytAddUrl.trim() === ''}>
            {ytAddBusy ? '添加中…' : '添加频道'}
          </button>
        </form>

        {#if ytError}
          <p class="panel px-4 py-3 text-ui text-brick" role="alert">{ytError}</p>
        {:else if ytLoading || yt === null}
          <div class="panel px-4 py-10 text-center text-ui text-dim">正在载入…</div>
        {:else if yt.channels.length === 0}
          <EmptyState
            title="还没有订阅任何 YouTube 频道"
            hint="在上面贴一个频道链接就能订阅；首次添加会用 yt-dlp 回填最近的历史视频。"
            icon="film"
          >
            {#snippet actions()}
              <button type="button" class="btn btn-primary" onclick={() => navigate('/youtube')}>去视频墙添加</button>
            {/snippet}
          </EmptyState>
        {:else}
          <ul class="panel divide-y divide-line">
            {#each yt.channels as channel (channel.id)}
              <li class="flex flex-wrap items-center gap-3 px-3 py-2.5">
                {#if channel.avatarUrl}
                  <img
                    src={imageUrl(channel.avatarUrl, 80) ?? ''}
                    alt=""
                    class="h-9 w-9 shrink-0 rounded-full object-cover"
                    loading="lazy"
                    decoding="async"
                  />
                {:else}
                  <span class="grid h-9 w-9 shrink-0 place-items-center rounded-full bg-raise text-meta text-dim">
                    {channel.title.slice(0, 1).toUpperCase()}
                  </span>
                {/if}

                <div class="min-w-0 flex-1">
                  <div class="flex items-center gap-1.5">
                    <span class="truncate text-ui text-fg">{channel.title}</span>
                    {#if !channel.enabled}
                      <span class="shrink-0 rounded-full bg-raise px-1.5 text-meta text-dim">已停用</span>
                    {/if}
                  </div>
                  <div class="mt-0.5 flex flex-wrap items-center gap-x-1.5 text-meta text-dim">
                    {#if channel.handle}
                      <span class="truncate">@{channel.handle}</span>
                      <span aria-hidden="true">·</span>
                    {/if}
                    <span class="tabular-nums">
                      {formatCount(channel.videoCount)} 条
                      {#if channel.unseenCount > 0}
                        · <span class="text-fg">{formatCount(channel.unseenCount)} 未看</span>
                      {/if}
                    </span>
                    {#if channel.lastSyncedAt}
                      <span aria-hidden="true">·</span>
                      <span>{relativeTime(channel.lastSyncedAt)}同步</span>
                    {/if}
                    {#if channel.backfilledAt}
                      <span aria-hidden="true">·</span>
                      <span>历史已回填</span>
                    {/if}
                  </div>
                  {#if channel.lastError}
                    <p class="mt-1 line-clamp-2 text-meta text-brick">{channel.lastError}</p>
                  {/if}
                </div>

                <div class="flex shrink-0 items-center gap-1">
                  <button
                    type="button"
                    class="h-8 rounded-full px-2.5 text-meta text-dim transition-colors hover:bg-raise hover:text-fg"
                    disabled={ytBusyId === channel.id}
                    onclick={() => void syncYt(channel, false)}
                  >
                    同步
                  </button>
                  <button
                    type="button"
                    class="h-8 rounded-full px-2.5 text-meta text-dim transition-colors hover:bg-raise hover:text-fg"
                    disabled={ytBusyId === channel.id}
                    title="用 yt-dlp 再列一遍历史（补时长、补漏掉的条目）"
                    onclick={() => void syncYt(channel, true)}
                  >
                    深度回填
                  </button>
                  {#if channel.unseenCount > 0}
                    <button
                      type="button"
                      class="h-8 rounded-full px-2.5 text-meta text-dim transition-colors hover:bg-raise hover:text-fg"
                      disabled={ytBusyId === channel.id}
                      title="把这个频道所有未看视频排进下载队列"
                      onclick={() => void downloadYtUnseen(channel)}
                    >
                      下载未看
                    </button>
                  {/if}
                  <button
                    type="button"
                    class="h-8 rounded-full px-2.5 text-meta text-dim transition-colors hover:bg-raise hover:text-fg"
                    disabled={ytBusyId === channel.id}
                    onclick={() => void toggleYt(channel)}
                  >
                    {channel.enabled ? '停用' : '启用'}
                  </button>
                  <button
                    type="button"
                    class="h-8 rounded-full px-2.5 text-meta text-brick/80 transition-colors hover:bg-raise hover:text-brick"
                    disabled={ytBusyId === channel.id}
                    onclick={() => void removeYt(channel)}
                  >
                    删除
                  </button>
                </div>
              </li>
            {/each}
          </ul>
          <p class="text-meta text-dim">
            下载是手动的：视频墙点「下载」，或这里点「下载未看」。下完就是本地库里一部普通影片（带 nfo，Emby 能刮到）。
          </p>
        {/if}
      </div>
    {:else}
      <!-- B 站管理（M7）：UP 主列表 + Cookie / ffmpeg 状态。
           和 YouTube 那一栏同构，区别是「健康度」换成了 B 站自己的两件事：
           有没有 SESSDATA（决定能不能超过 720P）、有没有 ffmpeg（分轨档位靠它合流）。 -->
      <div class="mt-3 flex flex-col gap-2">
        <div class="flex flex-wrap items-center gap-2">
          <span class="text-meta text-dim">
            {#if bili}
              共 {formatCount(bili.stats.channels)} 位 UP 主 · {formatCount(bili.stats.videos)} 条投稿 ·
              {formatCount(bili.stats.unseen)} 条未看 · {formatCount(bili.stats.downloaded)} 条已落盘
              {#if bili.stats.errors > 0}
                · <span class="text-brick">异常 {formatCount(bili.stats.errors)} 位</span>
              {/if}
            {:else}
              正在载入…
            {/if}
          </span>
          <div class="ml-auto flex items-center gap-2">
            <button type="button" class="btn btn-ghost h-8" onclick={() => void loadBili()}>刷新</button>
            <button type="button" class="btn h-8" onclick={() => navigate('/bilibili')}>打开视频墙</button>
          </div>
        </div>

        {#if biliHealth}
          <div class="panel px-4 py-3">
            <div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-meta">
              <span class={biliHealth.loggedIn ? 'text-sage' : 'text-brick'}>
                {biliHealth.loggedIn
                  ? `已登录 ${biliHealth.uname}（1080P 可用）`
                  : biliHealth.navError
                    ? '查不出 B 站登录状态（网络/风控），不代表 Cookie 失效'
                    : biliHealth.cookieBroken
                      ? 'Cookie 在库里但解不开（服务端密钥变了，重填之前先修密钥）'
                      : biliHealth.hasCookie
                        ? biliHealth.lastLoginOk
                          ? `B 站作废了这条登录态（上次确认有效 ${formatDateTime(biliHealth.lastLoginOk)}，最高 720P）`
                          : 'Cookie 存了但 B 站说未登录（最高 720P，可能已作废）'
                        : '未填 Cookie（最高 720P）'}
              </span>
              <span class={biliHealth.ffmpegOk ? 'text-sage' : 'text-brick'}>
                {biliHealth.ffmpegOk ? 'ffmpeg 可用（分轨档位能合流）' : 'ffmpeg 缺失，480P 及以上放不了'}
              </span>
              <span class="text-dim">{biliHealth.hasProxy ? '走代理' : '直连'}</span>
              <button
                type="button"
                class="rounded-full bg-raise px-2.5 py-1 text-meta text-fg transition-colors hover:bg-panel"
                onclick={() => (biliLoginOpen = true)}
              >
                {biliHealth.loggedIn ? '换一个账号扫码登录' : '扫码登录'}
              </button>
            </div>
            <div class="mt-2 flex items-center gap-2">
              <button
                type="button"
                class="rounded-full bg-raise px-2.5 py-1 text-meta text-fg transition-colors hover:bg-panel"
                onclick={() => {
                  biliHealth = null;
                  void loadBili();
                }}
              >
                重新检查
              </button>
              <button type="button" class="text-meta text-dim hover:text-fg" onclick={() => ui.openSettings('bilibili')}>
                打开 B 站设置
              </button>
            </div>
          </div>
        {/if}

        <!-- 管理页是「一次性配好」的地方，加 UP 主不必跳回视频墙 -->
        <form
          class="flex flex-wrap items-center gap-2"
          onsubmit={(event) => {
            event.preventDefault();
            void addBiliChannel();
          }}
        >
          <input
            bind:value={biliAddUrl}
            class="field h-9 min-w-0 max-w-md flex-1"
            type="text"
            placeholder="UP 主主页链接（space.bilibili.com/<mid>）"
            spellcheck="false"
            autocomplete="off"
            disabled={biliAddBusy}
          />
          <button type="submit" class="btn h-9" disabled={biliAddBusy || biliAddUrl.trim() === ''}>
            {biliAddBusy ? '添加中…' : '添加 UP 主'}
          </button>
        </form>

        {#if biliError}
          <p class="panel px-4 py-3 text-ui text-brick" role="alert">{biliError}</p>
        {:else if biliLoading || bili === null}
          <div class="panel px-4 py-10 text-center text-ui text-dim">正在载入…</div>
        {:else if bili.channels.length === 0}
          <EmptyState
            title="还没有订阅任何 UP 主"
            hint="在上面贴一个 UP 主主页链接就能订阅；第一次会回填历史投稿。B 站限流很严，被限流会停在这一页、几小时后接着翻。"
            icon="film"
          >
            {#snippet actions()}
              <button type="button" class="btn btn-primary" onclick={() => navigate('/bilibili')}>去视频墙添加</button>
            {/snippet}
          </EmptyState>
        {:else}
          <ul class="panel divide-y divide-line">
            {#each bili.channels as channel (channel.id)}
              <li class="flex flex-wrap items-center gap-3 px-3 py-2.5">
                {#if channel.avatarUrl}
                  <img
                    src={imageUrl(channel.avatarUrl, 80) ?? ''}
                    alt=""
                    class="h-9 w-9 shrink-0 rounded-full object-cover"
                    loading="lazy"
                    decoding="async"
                  />
                {:else}
                  <span class="grid h-9 w-9 shrink-0 place-items-center rounded-full bg-raise text-meta text-dim">
                    {channel.name.slice(0, 1).toUpperCase()}
                  </span>
                {/if}

                <div class="min-w-0 flex-1">
                  <div class="flex items-center gap-1.5">
                    <span class="truncate text-ui text-fg">{channel.name}</span>
                    {#if !channel.enabled}
                      <span class="shrink-0 rounded-full bg-raise px-1.5 text-meta text-dim">已停用</span>
                    {/if}
                  </div>
                  <div class="mt-0.5 flex flex-wrap items-center gap-x-1.5 text-meta text-dim">
                    <a
                      class="truncate font-mono text-[0.6875rem] hover:text-fg"
                      href={channel.url}
                      target="_blank"
                      rel="noreferrer">mid {channel.mid}</a
                    >
                    <span aria-hidden="true">·</span>
                    <span class="tabular-nums">
                      {formatCount(channel.videoCount)} 条
                      {#if channel.unseenCount > 0}
                        · <span class="text-fg">{formatCount(channel.unseenCount)} 未看</span>
                      {/if}
                      {#if channel.downloaded > 0}
                        · {formatCount(channel.downloaded)} 已落盘
                      {/if}
                    </span>
                    {#if channel.lastSyncAt}
                      <span aria-hidden="true">·</span>
                      <span>{relativeTime(channel.lastSyncAt)}同步</span>
                    {/if}
                    <span aria-hidden="true">·</span>
                    <span class="tabular-nums">
                      回填 {channel.backfillPage}{#if channel.totalPages}/{channel.totalPages}{/if} 页
                      {#if channel.backfillDone}（已完）{/if}
                    </span>
                  </div>
                  {#if channel.lastError}
                    <p class="mt-1 line-clamp-2 text-meta text-brick">
                      {channel.lastError}
                      {#if channel.nextSyncAt}
                        <span class="text-dim">（{relativeFuture(channel.nextSyncAt)}再试）</span>
                      {/if}
                    </p>
                  {/if}
                </div>

                <div class="flex shrink-0 items-center gap-1">
                  <button
                    type="button"
                    class="h-8 rounded-full px-2.5 text-meta text-dim transition-colors hover:bg-raise hover:text-fg"
                    disabled={biliBusyId === channel.id}
                    title="拉一遍最新一页投稿（走 App 接口，限流最松）"
                    onclick={() => void syncBili(channel, false)}
                  >
                    同步
                  </button>
                  <button
                    type="button"
                    class="h-8 rounded-full px-2.5 text-meta text-dim transition-colors hover:bg-raise hover:text-fg"
                    disabled={biliBusyId === channel.id}
                    title="从断点继续往前翻历史投稿（每页 20 条，限流最紧）"
                    onclick={() => void syncBili(channel, true)}
                  >
                    回填续翻
                  </button>
                  {#if channel.unseenCount > 0}
                    <button
                      type="button"
                      class="h-8 rounded-full px-2.5 text-meta text-dim transition-colors hover:bg-raise hover:text-fg"
                      disabled={biliBusyId === channel.id}
                      title="把这位 UP 主未看的投稿排进下载队列"
                      onclick={() => void downloadBiliUnseen(channel)}
                    >
                      下载未看
                    </button>
                  {/if}
                  <button
                    type="button"
                    class="h-8 rounded-full px-2.5 text-meta text-dim transition-colors hover:bg-raise hover:text-fg"
                    disabled={biliBusyId === channel.id}
                    onclick={() => void toggleBili(channel)}
                  >
                    {channel.enabled ? '停用' : '启用'}
                  </button>
                  <button
                    type="button"
                    class="h-8 rounded-full px-2.5 text-meta text-brick/80 transition-colors hover:bg-raise hover:text-brick"
                    disabled={biliBusyId === channel.id}
                    onclick={() => void removeBili(channel)}
                  >
                    删除
                  </button>
                </div>
              </li>
            {/each}
          </ul>
          <p class="text-meta text-dim">
            B 站的空间投稿接口是全站风控最严的那个：翻页途中被限流会自动停下，断点保留，3 小时后自动接着翻。
            被限流时「同步」也会报同样的错，属于正常现象。
          </p>
        {/if}
      </div>
    {/if}
  {/if}
</div>

<!-- 扫码登录弹窗：服务端写库，这里刷新一次健康行即可。 -->
<BiliLoginDialog
  open={biliLoginOpen}
  onClose={() => (biliLoginOpen = false)}
  onSuccess={() => {
    biliLoginOpen = false;
    void loadBili();
  }}
/>
