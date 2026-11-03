<script lang="ts">
  /**
   * 动态：订阅的 X / Instagram 博主的照片墙。
   *
   * 三件事：看照片（瀑布流 + 灯箱）、管博主（添加 / 启停 / 立即同步）、
   * 删掉不要的（勾几张 → 删除）。
   *
   * 2026-11-04 用户要求：照片抓下来就**自动**下载进图片库（按 {名字}/{年}/{月} 分目录，
   * 根目录在设置里填），墙上原来那个「多选 → 存进本地库」因此改成「多选 → 删除」——
   * 存档不再需要人动手，多选只剩清理这一个用途。
   *
   * 页面顶部**没有标题**（2026-11-02 用户要求去掉左上角那个「动态」）：直接是博主头像条，
   * 和关注页 / B 站页 / YouTube 页同一套 `AvatarRail`——72px 圆头像、名字居中，点谁筛谁、再点一次取消。
   * 下面那行工具栏只剩「全部」+ 平台筛选 + 「添加博主」。
   *
   * 契约见 docs/M5-SOCIAL.md §10/§11。
   */
  import { onMount, untrack } from 'svelte';
  import AvatarRail from '../components/AvatarRail.svelte';
  import SocialBrandIcon from '../components/SocialBrandIcon.svelte';
  import InfiniteList from '../components/InfiniteList.svelte';
  import EmptyState from '../components/EmptyState.svelte';
  import { ApiError, api, messageFor } from '../lib/api';
  import { blurredImageUrl, imageUrl } from '../lib/img';
  import { live } from '../lib/live.svelte';
  import { confirmDialog } from '../lib/confirm.svelte';
  import { patchQuery, router } from '../lib/router.svelte';
  import { ui } from '../lib/ui.svelte';
  import { formatDateTime } from '../lib/format';
  import { socialBrandOfPlatform } from '../lib/socialBrand';
  import type { RailItem } from '../lib/rail';
  import type {
    MomentAccount,
    MomentPhoto,
    MomentResolve,
    SocialPlatformInfo,
  } from '../lib/types';

  // ---- 博主列表 ----
  let accounts = $state<MomentAccount[]>([]);
  let platforms = $state<SocialPlatformInfo[]>([]);
  let accountsLoaded = $state(false);
  let accountsError = $state('');

  // ---- 筛选 ----
  let filterAccount = $state<number | null>(null);
  let filterPlatform = $state<string | null>(null);
  /** 换筛选条件时 +1，让 InfiniteList 重头拉 */
  let wallKey = $state(0);

  // ---- 墙上的照片（InfiniteList 报上来的，灯箱要在里面前后翻） ----
  let photos = $state<MomentPhoto[]>([]);
  /** 刚被删掉的照片 id：交给 InfiniteList 立刻从版面上抹掉（换筛选时清空） */
  let droppedPhotos = $state<Set<number>>(new Set());

  // ---- 选择与删除 ----
  let selection = $state<Set<number>>(new Set());
  let deleting = $state(false);

  // ---- 灯箱 ----
  let lightboxAt = $state<number | null>(null);
  const current = $derived(lightboxAt === null ? null : (photos[lightboxAt] ?? null));

  // ---- 添加博主 ----
  let addOpen = $state(false);
  let addUrl = $state('');
  let addBusy = $state(false);
  let addError = $state('');
  /** 添加失败是因为「缺登录态」（IG 没配 Cookie）时，要顺手给出配置入口 */
  let addNeedsAuth = $state(false);
  /** 上一次失败是「冷却中」时为 true：给一个「现在就试」的按钮，不用干等到点 */
  let addCooling = $state(false);
  /** 上一次失败是「被风控拦截」时为 true：换 Cookie 的入口 */
  let addBlocked = $state(false);
  let preview = $state<MomentResolve | null>(null);

  /**
   * 照片上写谁的名字：博主自己写的 displayName，空着才回退 @handle。
   *
   * 和头像条（`Moments.svelte` 里那排 rail 项的 `name`）同一条口径，2026-11-03 用户要求
   * 头像下显示名字、2026-11-04 又要求悬停浮层也别写 ID，两处必须一致，
   * 不然同一个博主在图上是名字、在浮层里又变回 `@kiho_kanematsu`。
   */
  function photoName(photo: MomentPhoto): string {
    const name = photo.displayName.trim();
    return name === '' ? `@${photo.handle}` : name;
  }

  const selectionCount = $derived(selection.size);

  const activeAccounts = $derived(accounts.filter((a) => a.enabled));

  /**
   * 头像条的条目：一位博主一颗。名字用平台的**显示名**（`displayName`），空着才退回 `@handle`。
   *
   * 2026-11-03 用户要求「头像下面不要显示 ID，显示名称」：原来这排写的是 `@handle`，
   * @ 后面那串就是账号 ID，三位博主里只有一位认得出来。改显示名后实测是
   * `香水じゅん` / `金松季歩(kanematsu kiho)` / `mysaaat | ONLYFANS`。
   * 同一天用户又要求「名字限制一行」：80px 一列按 12px 字号一行只装得下 6 个汉字或
   * 13 个西文，所以后两位会截断成 `金松季歩(kanem…` / `mysaaat | O…`——完整名字在
   * `title` 里（和 `@handle`、照片数一样，悬停与读屏拿得到）。**不再折两行**：
   * 折行会把这条头像条的整体高度撑高 16px，三排头像也不再一条水平线。
   */
  const railItems = $derived<RailItem[]>(
    accounts.map((a) => ({
      id: a.id,
      name: a.displayName.trim() || `@${a.handle}`,
      avatarUrl: a.avatarUrl || null,
      title: `${a.displayName.trim() || `@${a.handle}`} · @${a.handle} · ${String(a.photoCount)} 张`,
      dimmed: !a.enabled,
    })),
  );

  async function loadAccounts(): Promise<void> {
    accountsError = '';
    try {
      const res = await api.momentAccounts();
      accounts = res.items;
      platforms = res.platforms;
      live.setMomentsUnseen(res.unseen);
    } catch (err) {
      accountsError = messageFor(err);
    } finally {
      accountsLoaded = true;
    }
  }

  /** InfiniteList 的取数：这里只关心照片；平台/博主筛选都在服务端做 */
  async function loadPage(cursor: string | null): Promise<{ items: MomentPhoto[]; nextCursor: string | null }> {
    const res = await api.moments({
      cursor,
      accountId: filterAccount,
      platform: filterPlatform,
      limit: 36,
    });
    return { items: res.items, nextCursor: res.nextCursor ?? null };
  }

  /**
   * 换筛选。**地址栏是唯一真源**，和关注页 / B 站页 / YouTube 页那三条头像条一个规矩：
   * 点谁筛谁就写 `?account=`，取消就删掉这个参数——刷新、复制给别人、后退回来都还是同一面墙。
   * （平台筛选仍只在内存里：它是这一页的第二维，没有深链需求。）
   */
  function setFilter(accountId: number | null, platform: string | null): void {
    filterAccount = accountId;
    filterPlatform = platform;
    selection = new Set();
    droppedPhotos = new Set();
    wallKey += 1;
    patchQuery({ account: accountId === null ? null : String(accountId) });
  }

  // 后台抓到新照片时自动刷新（只刷第一页：照片墙按时间倒序，新的都在最前面）
  let seenVersion = 0;
  $effect(() => {
    const v = live.momentsVersion;
    if (v === seenVersion) return;
    seenVersion = v;
    wallKey += 1;
  });

  /** 深链：/moments?account=3（艺人页「社交动态」点进来就只看这位博主） */
  function syncQuery(): void {
    const id = Number.parseInt(router.query.get('account') ?? '', 10);
    if (Number.isFinite(id) && id > 0 && id !== filterAccount) {
      filterAccount = id;
      filterPlatform = null;
      selection = new Set();
      wallKey += 1;
    }
  }

  // 地址栏里的 account 变了（比如在艺人页点了另一个人的图）就跟着换筛选
  $effect(() => {
    void router.search;
    // untrack：syncQuery 会读 filterAccount，让它进依赖会自己触发自己
    untrack(() => syncQuery());
  });

  onMount(() => {
    syncQuery();
    void loadAccounts();
    // 进来就算「看过了」：红点只在没进来的时候有意义
    void api
      .markMomentsSeen()
      .then(() => live.setMomentsUnseen(0))
      .catch(() => {});
  });

  // ---- 选择 ----
  function toggleSelect(id: number): void {
    const next = new Set(selection);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    selection = next;
  }

  /**
   * 批量删掉选中的照片（底部操作条那个「删除」）。
   *
   * 服务端是软删：留一行 `hidden = 1` 当墓碑，所以「以后同步也不会再抓回来」是真的
   * （`social_photos` 上 `UNIQUE (platform, url)` 会挡住重插）。硬盘上已经落到图片库里的
   * 文件不动——那是用户的资产，删墙不等于删盘，确认框里得说清这一点。
   */
  async function deleteSelected(): Promise<void> {
    if (selectionCount === 0 || deleting) return;
    const count = selectionCount;
    const ok = await confirmDialog.ask({
      title: `从照片墙删掉这 ${count} 张？`,
      body: '以后同步也不会再抓回来。已经下载到图片库里的文件不会被删除。',
      confirmText: '删除',
    });
    if (!ok) return;
    deleting = true;
    const ids = [...selection];
    try {
      const res = await api.deleteMomentPhotos(ids);
      dropLocally(ids);
      live.toast(res.deleted > 0 ? `已从照片墙删除 ${res.deleted} 张` : '这些照片已经不在墙上了', 'success');
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      deleting = false;
    }
  }

  /** 从版面上抹掉这些 id，并把它们从选择里摘掉（不重拉整条墙，免得把人弹回第一屏）。 */
  function dropLocally(ids: number[]): void {
    const dropped = new Set(droppedPhotos);
    for (const id of ids) dropped.add(id);
    droppedPhotos = dropped;
    if (ids.some((id) => selection.has(id))) {
      const next = new Set(selection);
      for (const id of ids) next.delete(id);
      selection = next;
    }
    // 头像条上那位博主的「N 张」跟着变，重拉一次博主列表（几个账号，很便宜）
    void loadAccounts();
  }

  /**
   * 删掉一张照片（格子右下角的垃圾桶、灯箱里的「删除这张」）。
   *
   * 和批量删除同一套语义（软删、不动硬盘上的文件），只是确认框写得更具体一点。
   * `closeLightbox` 为 true 时删完顺手关掉灯箱——留着会停在一张已经不在墙上的图上。
   */
  async function removePhoto(photo: MomentPhoto, closeLightbox = false): Promise<void> {
    const ok = await confirmDialog.ask({
      title: '从照片墙删掉这张？',
      body: '以后同步也不会再抓回来。已经下载到图片库里的文件不会被删除。',
      confirmText: '删除',
    });
    if (!ok) return;
    try {
      await api.deleteMomentPhotos([photo.id]);
    } catch (err) {
      live.toast(messageFor(err), 'error');
      return;
    }
    if (closeLightbox) lightboxAt = null;
    dropLocally([photo.id]);
    live.toast('已从照片墙删除', 'success');
  }

  // ---- 灯箱键盘 ----
  function onKeydown(event: KeyboardEvent): void {
    if (lightboxAt === null) return;
    if (event.key === 'Escape') {
      event.preventDefault();
      lightboxAt = null;
    } else if (event.key === 'ArrowRight') {
      event.preventDefault();
      step(1);
    } else if (event.key === 'ArrowLeft') {
      event.preventDefault();
      step(-1);
    }
  }

  function step(delta: number): void {
    if (lightboxAt === null) return;
    // 同一条帖子的几张图挨在一起（服务端就是这么排的），所以「前后」天然是先走完同帖。
    const next = lightboxAt + delta;
    if (next < 0) {
      if (delta < 0) wallLoadMoreHint();
      return;
    }
    if (next >= photos.length) {
      wallLoadMoreHint();
      return;
    }
    lightboxAt = next;
  }

  /** 翻到已加载的边界时提示滚动加载（不自动替他滚，免得灯箱位置乱跳） */
  let hintAt = 0;
  function wallLoadMoreHint(): void {
    if (Date.now() - hintAt < 3000) return;
    hintAt = Date.now();
    live.toast('到已加载的尽头了，关掉大图后继续往下滚', 'info');
  }

  // ---- 添加博主 ----
  function openAdd(): void {
    addOpen = true;
    addUrl = '';
    addError = '';
    addNeedsAuth = false;
    addCooling = false;
    addBlocked = false;
    preview = null;
  }

  /** 把接口错误翻译成「用户接下来该干什么」 */
  function noteAddError(err: unknown): void {
    addError = messageFor(err);
    addNeedsAuth = err instanceof ApiError && err.code === 'needs_auth';
    addCooling = err instanceof ApiError && err.code === 'rate_limited';
    addBlocked = err instanceof ApiError && err.code === 'blocked';
  }

  /** 手动清掉本地冷却再重试一次（Cookie / IP 换过之后，旧的冷却时间已经没有意义）。 */
  async function retryAfterCooldown(): Promise<void> {
    try {
      await api.clearMomentCooldown('instagram');
    } catch {
      // 清不掉就照常重试，最坏情况还是那句「冷却中」
    }
    addCooling = false;
    await resolve();
  }

  async function resolve(): Promise<void> {
    const url = addUrl.trim();
    if (url === '' || addBusy) return;
    addBusy = true;
    addError = '';
    preview = null;
    try {
      preview = await api.resolveMoment(url);
    } catch (err) {
      noteAddError(err);
    } finally {
      addBusy = false;
    }
  }

  async function confirmAdd(): Promise<void> {
    if (addBusy || !preview) return;
    addBusy = true;
    addError = '';
    try {
      const res = await api.addMoment(addUrl.trim());
      live.toast(`已添加 @${res.account.handle}，正在回填历史帖子`, 'success');
      addOpen = false;
      preview = null;
      await loadAccounts();
      wallKey += 1;
    } catch (err) {
      noteAddError(err);
    } finally {
      addBusy = false;
    }
  }

  async function syncNow(account: MomentAccount): Promise<void> {
    try {
      const res = await api.syncMomentAccount(account.id, account.backfillDone ? 'auto' : 'full');
      live.toast(res.queued ? `已投递 @${account.handle} 的同步` : '这个博主已经在队列里了', 'info');
    } catch (err) {
      live.toast(messageFor(err), 'error');
    }
  }

  function platformName(key: string): string {
    return platforms.find((p) => p.key === key)?.name ?? key;
  }

  // 格子比例固定成 3:4（2026-11-03 按用户要求改）：以前每格跟着照片自己的比例走，
  // 同一行里矮的那几张底下会露出一块深色空档（行高按最高的那张算）。现在格子统一，
  // 照片按原比例 object-contain 居中，比例对不上的地方用同一张图的糊版垫满，
  // 见格子里那层 blurredImageUrl。
  const TILE_RATIO = 3 / 4;

  /**
   * 只有比例和格子对不上的照片才铺糊影垫底：比例正好是 3:4 的那张本来就填满整格，
   * 垫了也看不见，白白多请求一张小图（实测每张 6–12 KB，一页 24 格）。
   * 拿不到宽高时按「需要」处理——宁可多一层糊影，也别露出一条死板的面板色。
   */
  function needsBackdrop(photo: MomentPhoto): boolean {
    if (!(photo.width > 0 && photo.height > 0)) return true;
    return Math.abs(photo.width / photo.height - TILE_RATIO) > 0.02;
  }
</script>

<svelte:window onkeydown={onKeydown} />

<!-- 这一页不再挂「动态 / YouTube / B 站」那排分段控件：三个入口已经都在导航里
     （桌面侧边栏、手机底栏），页面顶部只有这条头像条 + 一行工具栏。 -->
<!-- 博主头像条：和关注页 / B 站页 / YouTube 页同一个组件（2026-11-02 用户要求）——
     顶部不再有「动态」这两个字，谁可以筛、现在筛的是谁，全在这条上。
     点谁筛谁，再点一次同一颗＝取消（「全部」那颗胶囊仍然留着，语义一样）。 -->
<AvatarRail
  items={railItems}
  label="按博主筛选"
  selected={filterAccount}
  onSelect={(id) => setFilter(id, null)}
  loading={!accountsLoaded}
/>

<div class="flex flex-col gap-4">
  <!-- 工具栏：左边只有「全部」，右边是平台筛选 + 唯一的主动作 -->
  <div class="flex flex-wrap items-center gap-2">
    <button
      type="button"
      onclick={() => setFilter(null, null)}
      class="shrink-0 rounded-full px-3 py-1.5 text-meta transition-colors {filterAccount === null &&
      filterPlatform === null
        ? 'bg-raise text-fg'
        : 'text-muted hover:bg-panel hover:text-fg'}"
    >
      全部
    </button>

    <!-- 平台筛选紧贴「全部」右边，用和艺人主页同一个品牌图标（`SocialBrandIcon`）：
         32px 圆底座 + 22/26px 原色标，选中时底座亮起来。比原来那两颗文字胶囊省一半宽度，
         所以手机上也不用再藏了。 -->
    {#each platforms as p (p.key)}
      <button
        type="button"
        onclick={() => setFilter(null, filterPlatform === p.key ? null : p.key)}
        title={p.name}
        aria-label={p.name}
        aria-pressed={filterPlatform === p.key}
        class="grid h-8 w-8 shrink-0 place-items-center rounded-full transition-colors {filterPlatform === p.key
          ? 'bg-raise ring-1 ring-fg/15'
          : 'hover:bg-panel'}"
      >
        <SocialBrandIcon kind={socialBrandOfPlatform(p.key)} />
      </button>
    {/each}

    <div class="ml-auto flex shrink-0 items-center gap-2">
      <button
        type="button"
        onclick={openAdd}
        class="rounded-full bg-hi px-3.5 py-1.5 text-meta font-medium text-ink transition-opacity hover:opacity-90"
      >
        添加博主
      </button>
    </div>
  </div>

  {#if accountsError}
    <p class="rounded-xl bg-panel p-3 text-ui text-brick" role="alert">{accountsError}</p>
  {/if}

  <!-- 报错先亮出来：needs_auth / rate_limited 都得让用户看见，不能装作没事 -->
  {#each accounts.filter((a) => a.lastError) as account (account.id)}
    <div class="flex items-start gap-2 rounded-xl bg-panel px-3 py-2 text-meta text-brick">
      <span class="font-medium">@{account.handle}</span>
      <span class="min-w-0 flex-1 text-muted">{account.lastError}</span>
      <button type="button" class="shrink-0 text-dim hover:text-fg" onclick={() => void syncNow(account)}>
        重试
      </button>
    </div>
  {/each}

  {#if accountsLoaded && accounts.length === 0}
    <EmptyState
      title="还没有订阅任何博主"
      hint="贴一个 X 或 Instagram 的博主主页链接，抓来的照片会铺满这面墙。Instagram 需要先在设置里配 Cookie。"
      icon="user"
    >
      {#snippet actions()}
        <button
          type="button"
          onclick={openAdd}
          class="rounded-full bg-hi px-4 py-2 text-ui font-medium text-ink transition-opacity hover:opacity-90"
        >
          添加博主
        </button>
      {/snippet}
    </EmptyState>
  {:else}
    <InfiniteList
      load={loadPage}
      keyOf={(photo: MomentPhoto) => photo.id}
      reloadKey={wallKey}
      maxLoadedPages={8}
      gridClass="grid grid-cols-2 gap-3 sm:grid-cols-3 xl:grid-cols-4 2xl:grid-cols-5"
      droppedKeys={droppedPhotos}
      onItems={(items: MomentPhoto[]) => {
        photos = items;
      }}
    >
      {#snippet placeholder()}
        <div class="aspect-3/4 rounded-lg bg-panel"></div>
      {/snippet}

      {#snippet empty()}
        <div class="col-span-full">
          <EmptyState
            title="还没有抓到照片"
            hint={activeAccounts.length === 0
              ? '先把博主启用一下，或者点「添加博主」再加一个。'
              : '抓取任务可能还没跑完，也可以点上面博主右边的「同步」立刻试一次。'}
            icon="film"
          />
        </div>
      {/snippet}

      {#snippet children(photo: MomentPhoto)}
        <div class="group relative aspect-3/4 overflow-hidden rounded-lg bg-panel">
          <button
            type="button"
            class="absolute inset-0 block cursor-zoom-in"
            onclick={() => {
              lightboxAt = photos.findIndex((p) => p.id === photo.id);
            }}
            aria-label={`查看 ${photoName(photo)} 的照片`}
          >
            <!-- 悬停放大 2%，和视频卡（VideoCard / YtCard / LibraryCard 的 `CoverArt`
                 `group-hover:scale-[1.02]`）同一个手感：那三面墙的卡片都是
                 `transition-transform duration-300` + 1.02，这里照抄，别自己发明一套曲线。
                 兜底层、压暗层、主体三块一起缩放——只缩主体的话，糊影和照片之间会裂开一道缝。 -->
            <span class="block absolute inset-0 transition-transform duration-300 group-hover:scale-[1.02]">
              <!-- 垫底：同一张图的糊版小图（后端缩到一百多像素再糊，6–12 KB）铺满格子， -->
              <!-- 留白处不是一块死板的面板色，而是这张照片自己的虚影。 -->
              <!-- 用 object-fill 而不是 object-cover：糊版已经看不出形变，cover 会把小图中间的 -->
              <!-- 一小块放大四五倍（横图铺进 3:4 格子就是这样），糊到最后只剩一块纯色。 -->
              {#if needsBackdrop(photo)}
                <img
                  src={blurredImageUrl(photo.thumbUrl, 480) ?? ''}
                  alt=""
                  aria-hidden="true"
                  class="absolute inset-0 h-full w-full object-fill"
                  loading="lazy"
                  decoding="async"
                />
              {/if}
              <span class="pointer-events-none absolute inset-0 bg-ink/10"></span>
              <!-- 主体：原比例居中，比例不合的留白正好落在上面那层糊影里 -->
              <img
                src={imageUrl(photo.thumbUrl, 480) ?? ''}
                alt={photo.caption ? photo.caption.slice(0, 80) : `${photoName(photo)} 的照片`}
                class="relative h-full w-full object-contain"
                loading="lazy"
                decoding="async"
              />
            </span>
          </button>

          <!-- 勾选：右上角，只在悬停/已选时出现，避免墙上到处是控件 -->
          <button
            type="button"
            onclick={() => toggleSelect(photo.id)}
            aria-pressed={selection.has(photo.id)}
            aria-label={selection.has(photo.id) ? '取消选择' : '选择这张'}
            class="absolute right-1.5 top-1.5 grid h-6 w-6 place-items-center rounded-full border transition-all {selection.has(
              photo.id,
            )
              ? 'border-transparent bg-hi text-ink'
              : 'border-white/40 bg-ink/45 text-white/90 opacity-0 group-hover:opacity-100 focus-visible:opacity-100'}"
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" class="h-3.5 w-3.5">
              <path d="m5 12.5 4.5 4.5L19 7" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
          </button>

          <!-- 照片不画任何「已存 / 待存」角标（2026-11-04 用户要求「照片不要显示已存标识」）。
               理由也站得住：抓完自动落盘之后，落没落盘是后台的事，墙上每格都挂一枚状态
               只是噪音；真要看进度有「下载」页的任务列表。 -->

          <!-- 悬停浮层写博主名字，不写 @handle（2026-11-04 用户要求：「悬浮时左下角显示名字，
               不要显示 X 的 ID」）。和头像条同一条口径：displayName 空着才回退 @handle；
               号码本身还在 title 里，想知道 ID 悬停一下就有，读屏也念得到。

               只留字，不要那层从下往上压的黑晕（2026-11-04 用户要求：「不要显示图片下面的那层
               黑晕」——原来这里是 `bg-linear-to-t from-ink/80 to-transparent` + `pt-6`，一整条
               渐变横在照片下沿）。字压在白底照片上也认得出，靠的是文字自己的投影，不是一块底。 -->
          <div
            class="pointer-events-none absolute inset-x-0 bottom-0 px-2 pb-1.5 text-[0.6875rem] text-white/90 [text-shadow:0_1px_3px_rgb(0_0_0_/_0.95),0_0_2px_rgb(0_0_0_/_0.7)] opacity-0 transition-opacity group-hover:opacity-100"
            title={`${photoName(photo)} · @${photo.handle}`}
          >
            <span class="block truncate">{photoName(photo)}</span>
          </div>

          <!-- 删除：右下角。桌面端跟着悬停出现（不给版面添常驻控件），
               触屏上（没有 hover）常显——不然手机上没有入口。放在最后画，
               盖住底下那条 byline 渐变；byline 是 pointer-events-none，不会抢点击。 -->
          <button
            type="button"
            onclick={() => void removePhoto(photo)}
            aria-label="删除这张照片"
            title="删除这张照片"
            class="absolute bottom-1.5 right-1.5 z-10 grid h-6 w-6 place-items-center rounded-full bg-ink/55 text-white/90 opacity-100 transition-all hover:bg-brick hover:text-white focus-visible:opacity-100 sm:opacity-0 sm:group-hover:opacity-100"
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" class="h-3.5 w-3.5">
              <path
                d="M4.5 7h15M9.5 7V5.2h5V7M6.6 7l.9 12.3h9l.9-12.3"
                stroke-linecap="round"
                stroke-linejoin="round"
              />
            </svg>
          </button>
        </div>
      {/snippet}
    </InfiniteList>
  {/if}
</div>

<!-- 选中操作条：固定在底部，有选中才出现。
     2026-11-04 用户要求：多选从「存进本地库」改成「删除」——照片抓完就自动落盘，
     多选剩下唯一的用途是把墙上不要的批量清掉。 -->
{#if selectionCount > 0}
  <div
    class="fixed inset-x-0 bottom-[calc(3.75rem+env(safe-area-inset-bottom))] z-30 mx-auto flex w-fit max-w-[calc(100vw-1.5rem)] items-center gap-2 rounded-full bg-panel/95 px-3 py-2 text-meta shadow-lg backdrop-blur-xl lg:bottom-6"
  >
    <span class="pl-1 text-fg">已选 {selectionCount} 张</span>
    <button type="button" onclick={() => (selection = new Set())} class="rounded-full px-2 py-1 text-dim hover:text-fg">
      取消
    </button>
    <button
      type="button"
      onclick={() => void deleteSelected()}
      disabled={deleting}
      class="rounded-full bg-brick px-3 py-1 font-medium text-white transition-opacity hover:opacity-90 disabled:opacity-50"
    >
      {deleting ? '删除中…' : '删除'}
    </button>
  </div>
{/if}

<!-- 灯箱 -->
{#if current}
  <div
    class="fixed inset-0 z-50 flex flex-col bg-ink/94 backdrop-blur-sm"
    role="dialog"
    aria-modal="true"
    aria-label="照片大图"
  >
    <div class="flex items-center justify-between px-3 py-2 text-meta text-dim">
      <span class="tabular-nums">{lightboxAt !== null ? lightboxAt + 1 : 0} / {photos.length}</span>
      <button type="button" onclick={() => (lightboxAt = null)} class="rounded-full p-2 hover:text-fg" aria-label="关闭">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" class="h-5 w-5">
          <path d="M6 6l12 12M18 6 6 18" stroke-linecap="round" />
        </svg>
      </button>
    </div>

    <button
      type="button"
      class="relative flex min-h-0 flex-1 items-center justify-center bg-transparent px-2"
      onclick={(event) => {
        // 点图两侧（左 30% / 右 30%）翻页，中间关闭
        const rect = event.currentTarget.getBoundingClientRect();
        const x = (event.clientX - rect.left) / rect.width;
        if (x < 0.3) step(-1);
        else if (x > 0.7) step(1);
        else lightboxAt = null;
      }}
      aria-label="点击左侧上一张、右侧下一张、中间关闭"
    >
      <img
        src={imageUrl(current.url, 1600) ?? current.url}
        alt={current.caption || `@${current.handle} 的照片`}
        class="max-h-full max-w-full object-contain"
        decoding="async"
      />
    </button>

    <div class="flex items-start gap-3 px-3 pb-[calc(0.75rem+env(safe-area-inset-bottom))] pt-2">
      <div class="min-w-0 flex-1">
        <!-- 灯箱里同一件事也写名字（同一张照片，没理由换个说法）；@handle 进 title -->
        <p class="truncate text-ui text-fg" title={photoName(current) + ' · @' + current.handle}>
          <span class="text-dim">{platformName(current.platform)}</span> · {photoName(current)}
        </p>
        <!-- 不显示原帖正文（2026-11-04 用户要求「不要显示原贴内容」）：灯箱是看图的地方，
             正文一多就把图挤小；要读原文点右下角「原帖」过去看。caption 仍留着——给 <img> 的
             alt 用（读屏和「图片加载失败」时的文字），只是不上屏。 -->
        {#if current.postedAt}
          <p class="mt-1 text-meta text-dim">{formatDateTime(current.postedAt)}</p>
        {/if}
      </div>
      <div class="flex shrink-0 items-center gap-1.5">
        {#if current.postUrl}
          <a
            href={current.postUrl}
            target="_blank"
            rel="noreferrer noopener"
            class="rounded-full px-2.5 py-1.5 text-meta text-dim hover:bg-panel hover:text-fg"
          >
            原帖
          </a>
        {/if}
        <button
          type="button"
          onclick={() => void removePhoto(current, true)}
          class="rounded-full bg-raise px-3 py-1.5 text-meta font-medium text-fg transition-colors hover:bg-brick hover:text-white"
        >
          删除这张
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- 添加博主 -->
{#if addOpen}
  <div
    class="fixed inset-0 z-50 flex items-end justify-center bg-ink/70 p-3 backdrop-blur-sm sm:items-center"
    role="dialog"
    aria-modal="true"
    aria-label="添加博主"
  >
    <div class="w-full max-w-md rounded-2xl bg-panel p-4">
      <div class="flex items-center justify-between">
        <h2 class="text-title font-medium text-fg">添加博主</h2>
        <button
          type="button"
          onclick={() => (addOpen = false)}
          class="rounded-full p-1.5 text-dim hover:text-fg"
          aria-label="关闭"
        >
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" class="h-5 w-5">
            <path d="M6 6l12 12M18 6 6 18" stroke-linecap="round" />
          </svg>
        </button>
      </div>

      <p class="mt-1 text-meta text-muted">粘贴 X 或 Instagram 的主页链接，例如 x.com/某人 或 instagram.com/某人。</p>

      <div class="mt-3 flex gap-2">
        <input
          bind:value={addUrl}
          onkeydown={(event) => {
            if (event.key === 'Enter') void resolve();
          }}
          placeholder="https://x.com/…"
          class="min-w-0 flex-1 rounded-xl bg-raise px-3 py-2 text-ui text-fg outline-none placeholder:text-dim"
          spellcheck="false"
          autocomplete="off"
        />
        <button
          type="button"
          onclick={() => void resolve()}
          disabled={addBusy || addUrl.trim() === ''}
          class="shrink-0 rounded-xl bg-raise px-3 py-2 text-ui text-fg transition-opacity hover:opacity-90 disabled:opacity-50"
        >
          {addBusy ? '探测中…' : '探测'}
        </button>
      </div>

      {#if addError}
        <div class="mt-3 rounded-xl bg-raise p-2.5" role="alert">
          <p class="text-meta text-brick">{addError}</p>
          {#if addNeedsAuth || addBlocked}
            <a
              data-link
              href="/settings"
              class="mt-1.5 inline-block text-meta text-fg underline decoration-line underline-offset-2 hover:decoration-fg"
              onclick={() => ui.openSettings('social')}
              >{addBlocked ? '去设置里换一份 Cookie' : '去设置 → 动态里粘贴 Instagram Cookie'}</a
            >
          {/if}
          {#if addCooling}
            <button
              type="button"
              class="mt-1.5 text-meta text-fg underline decoration-line underline-offset-2 hover:decoration-fg"
              onclick={() => void retryAfterCooldown()}
              >换过 Cookie 了？清除冷却并重试</button
            >
          {/if}
        </div>
      {/if}

      {#if preview}
        <div class="mt-3 flex items-center gap-3 rounded-xl bg-raise p-3">
          {#if preview.avatarUrl}
            <img src={preview.avatarUrl} alt="" class="h-11 w-11 rounded-full object-cover" />
          {:else}
            <span class="grid h-11 w-11 place-items-center rounded-full bg-panel text-ui text-dim">
              {preview.handle.slice(0, 1).toUpperCase()}
            </span>
          {/if}
          <div class="min-w-0">
            <p class="truncate text-ui text-fg">{preview.displayName}</p>
            <p class="truncate text-meta text-dim">
              {preview.platformName} · @{preview.handle}
              {#if preview.postCount > 0}· 已有 {preview.postCount} 帖{/if}
            </p>
          </div>
        </div>
        {#if preview.performerName}
          <p class="mt-2 text-meta text-muted">会自动关联到艺人「{preview.performerName}」</p>
        {/if}
        {#if preview.warning}
          <p class="mt-1 text-meta text-muted">{preview.warning}</p>
        {/if}
        <button
          type="button"
          onclick={() => void confirmAdd()}
          disabled={addBusy}
          class="mt-3 w-full rounded-xl bg-hi py-2 text-ui font-medium text-ink transition-opacity hover:opacity-90 disabled:opacity-50"
        >
          {preview.existingId > 0 ? '重新回填这个博主' : '添加并开始回填'}
        </button>
      {/if}
    </div>
  </div>
{/if}
