<script lang="ts">
  import LibraryPanel from './LibraryPanel.svelte';
  import { api, messageFor } from '../lib/api';
  import { live } from '../lib/live.svelte';
  import { navigate } from '../lib/router.svelte';
  import { ui, type SettingsPane } from '../lib/ui.svelte';
  import type { Settings, SettingsPatch, YtHealth } from '../lib/types';

  /** 仅 DEV：后端未就绪时的示例数据 */
  const MOCK: Settings = {
    syncIntervalHours: 6,
    globalProxy: '',
    platformProxy: {
      pornhub: 'socks5://127.0.0.1:1080',
      xhamster: '',
      beeg: '',
      instagram: '',
      x: '',
      youtube: '',
      bilibili: '',
    },
    namingTemplate: '{标题}',
    downloadConcurrency: 2,
    downloadRateKb: 0,
    autoDownloadNew: false,
    defaultLibraryId: 0,
    libraryRoot: './media',
    ffmpegAvailable: true,
    openlist: { baseUrl: 'http://192.168.0.108:5244', root: '/123云盘/资源目录/MyPorn', hasToken: true },
    social: { syncHours: 6, hasInstagramCookie: false },
    youtube: { syncHours: 6, backfillLimit: 200, format: '', ytdlpPath: '', hasCookies: false },
    bilibili: { hasCookie: false },
  };

  /** 侧边栏分类：左侧栏在弹窗里切换，右侧只渲染当前这一块 */
  const PANES: readonly { id: SettingsPane; label: string; hint: string }[] = [
    { id: 'sync', label: '同步', hint: '同步间隔与文件命名' },
    { id: 'network', label: '网络与代理', hint: '全局与按平台代理' },
    { id: 'upload', label: '上传', hint: 'OpenList 上传目标' },
    { id: 'download', label: '下载', hint: '并发、限速与自动下载' },
    { id: 'social', label: '动态', hint: '博主抓取频率与 Instagram 登录态' },
    { id: 'youtube', label: 'YouTube', hint: 'yt-dlp、画质与登录态' },
    { id: 'bilibili', label: 'B 站', hint: '登录态与画质' },
    { id: 'libraries', label: '本地库', hint: '落盘目录与空间' },
    { id: 'account', label: '账号', hint: '会话与退出' },
  ];

  /** 这四个分类共用一个表单，保存按钮在页脚（用 form 属性挂回来） */
  const FORM_PANES: readonly SettingsPane[] = [
    'sync',
    'network',
    'upload',
    'download',
    'social',
    'youtube',
    'bilibili',
  ];

  let settings = $state<Settings | null>(null);
  let loading = $state(true);
  let error = $state('');
  let saving = $state(false);
  let mockNote = $state('');
  let loggingOut = $state(false);
  let dialogEl = $state<HTMLDivElement | null>(null);

  let syncIntervalHours = $state(6);
  let globalProxy = $state('');
  let pornhubProxy = $state('');
  let xhamsterProxy = $state('');
  let beegProxy = $state('');
  let instagramProxy = $state('');
  let xProxy = $state('');
  let namingTemplate = $state('{标题}');
  let downloadConcurrency = $state(2);
  let downloadRateKb = $state(0);
  let autoDownloadNew = $state(false);
  let openlistBaseUrl = $state('');
  let openlistRoot = $state('');
  let openlistToken = $state('');
  let socialSyncHours = $state(6);
  let instagramCookie = $state('');
  let youtubeProxy = $state('');
  let ytSyncHours = $state(6);
  let ytBackfillLimit = $state(200);
  let ytFormat = $state('');
  let ytdlpPath = $state('');
  let ytCookies = $state('');
  let biliProxy = $state('');
  let biliCookie = $state('');
  /** yt-dlp 健康度：进 YouTube 那一节时才拉（要 fork 一个进程，不值得每次开设置都跑） */
  let ytHealth = $state<YtHealth | null>(null);
  let ytHealthLoading = $state(false);

  const current = $derived(PANES.find((p) => p.id === ui.settingsPane) ?? PANES[0]);
  const isFormPane = $derived(FORM_PANES.includes(ui.settingsPane));

  function fill(value: Settings): void {
    settings = value;
    syncIntervalHours = value.syncIntervalHours;
    globalProxy = value.globalProxy;
    pornhubProxy = value.platformProxy.pornhub;
    xhamsterProxy = value.platformProxy.xhamster;
    beegProxy = value.platformProxy.beeg;
    instagramProxy = value.platformProxy.instagram ?? '';
    xProxy = value.platformProxy.x ?? '';
    namingTemplate = value.namingTemplate;
    downloadConcurrency = value.downloadConcurrency;
    downloadRateKb = value.downloadRateKb;
    autoDownloadNew = value.autoDownloadNew;
    openlistBaseUrl = value.openlist.baseUrl;
    openlistRoot = value.openlist.root;
    openlistToken = '';
    socialSyncHours = value.social.syncHours;
    instagramCookie = '';
    youtubeProxy = value.platformProxy.youtube ?? '';
    ytSyncHours = value.youtube.syncHours;
    ytBackfillLimit = value.youtube.backfillLimit;
    ytFormat = value.youtube.format;
    ytdlpPath = value.youtube.ytdlpPath;
    ytCookies = '';
    biliProxy = value.platformProxy.bilibili ?? '';
    biliCookie = '';
  }

  async function loadYtHealth(): Promise<void> {
    if (ytHealthLoading) return;
    ytHealthLoading = true;
    try {
      ytHealth = await api.ytHealth();
    } catch {
      ytHealth = null;
    } finally {
      ytHealthLoading = false;
    }
  }

  async function load(): Promise<void> {
    loading = true;
    error = '';
    try {
      fill(await api.settings());
      mockNote = '';
    } catch (err) {
      if (import.meta.env.DEV) {
        fill(MOCK);
        mockNote = messageFor(err);
      } else {
        error = messageFor(err);
      }
    } finally {
      loading = false;
    }
  }

  async function save(): Promise<void> {
    if (saving) return;
    saving = true;
    error = '';
    const patch: SettingsPatch = {
      syncIntervalHours,
      globalProxy,
      platformProxy: {
        pornhub: pornhubProxy,
        xhamster: xhamsterProxy,
        beeg: beegProxy,
        instagram: instagramProxy,
        x: xProxy,
        youtube: youtubeProxy,
        bilibili: biliProxy,
      },
      namingTemplate,
      downloadConcurrency,
      downloadRateKb,
      autoDownloadNew,
      openlist: { baseUrl: openlistBaseUrl, root: openlistRoot },
    };
    const token = openlistToken.trim();
    if (token.length > 0) patch.openlist = { ...patch.openlist, token };
    patch.social = { syncHours: socialSyncHours };
    // Cookie 只在用户真的粘了新内容时才提交：留空 = 不动（服务端把它当"清除"，
    // 所以这里必须"没填就不带这个字段"）。
    const cookie = instagramCookie.trim();
    if (cookie.length > 0) patch.social = { ...patch.social, instagramCookie: cookie };

    patch.youtube = {
      syncHours: ytSyncHours,
      backfillLimit: ytBackfillLimit,
      format: ytFormat.trim(),
      ytdlpPath: ytdlpPath.trim(),
    };
    // 同上：Cookie 留空 = 不动
    const ytCookie = ytCookies.trim();
    if (ytCookie.length > 0) patch.youtube = { ...patch.youtube, cookies: ytCookie };
    // B 站同理：留空 = 不动（服务端把空串当清除）
    const biliCk = biliCookie.trim();
    if (biliCk.length > 0) patch.bilibili = { cookie: biliCk };
    try {
      await api.updateSettings(patch);
      // 重新拉一次：token 永不回显，hasToken 等状态以服务端为准
      fill(await api.settings());
      live.toast('设置已保存', 'success');
    } catch (err) {
      const message = messageFor(err);
      error = message;
      live.toast(message, 'error');
    } finally {
      saving = false;
    }
  }

  async function logout(): Promise<void> {
    loggingOut = true;
    try {
      await api.logout();
    } catch {
      /* 会话可能已失效，直接回登录页 */
    } finally {
      loggingOut = false;
      ui.closeSettings();
      navigate('/login', { replace: true });
    }
  }

  function close(): void {
    if (saving) return;
    ui.closeSettings();
  }

  function onKeydown(event: KeyboardEvent): void {
    if (!ui.settingsOpen) return;
    if (event.key === 'Escape') {
      event.preventDefault();
      close();
    }
  }

  // 进到 YouTube 那一节才去问 yt-dlp 的版本：读版本要 fork 一个进程，
  // 没必要每次开设置都跑一次（而这个信息只在那一节有用）。
  $effect(() => {
    if (!ui.settingsOpen || ui.settingsPane !== 'youtube') return;
    if (ytHealth !== null || ytHealthLoading) return;
    void loadYtHealth();
  });

  // 每次打开都重新拉一遍：别的标签页可能改过，也可能刚在下载对话框里建了库
  $effect(() => {
    if (!ui.settingsOpen) return;
    loading = true;
    void load();
    queueMicrotask(() => dialogEl?.focus());
    document.body.style.overflow = 'hidden';
    return () => {
      document.body.style.overflow = '';
    };
  });
</script>

{#snippet paneIcon(id: SettingsPane)}
  <svg
    viewBox="0 0 24 24"
    class="h-[1.125rem] w-[1.125rem] shrink-0"
    fill="none"
    stroke="currentColor"
    stroke-width="1.5"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
  >
    {#if id === 'sync'}
      <path d="M20 12a8 8 0 0 1-13.4 5.9M4 12a8 8 0 0 1 13.4-5.9" />
      <path d="M17.4 2.6v3.9h-3.9M6.6 21.4v-3.9h3.9" />
    {:else if id === 'network'}
      <circle cx="12" cy="12" r="8.5" />
      <path d="M3.6 12h16.8" />
      <path d="M12 3.5c2.1 2.3 3.3 5.3 3.3 8.5s-1.2 6.2-3.3 8.5c-2.1-2.3-3.3-5.3-3.3-8.5S9.9 5.8 12 3.5z" />
    {:else if id === 'upload'}
      <path d="M7 16.5a4 4 0 0 1-.4-8 5.6 5.6 0 0 1 10.8.9 3.4 3.4 0 0 1-.4 6.7" />
      <path d="M12 21v-7" />
      <path d="m9 17 3-3 3 3" />
    {:else if id === 'download'}
      <path d="M12 4v10" />
      <path d="m8 10.5 4 4 4-4" />
      <path d="M4.5 17.5h15" />
    {:else if id === 'social'}
      <rect x="3" y="3.5" width="7.5" height="9" rx="2" />
      <rect x="13.5" y="3.5" width="7.5" height="6" rx="2" />
      <rect x="13.5" y="12.5" width="7.5" height="8" rx="2" />
      <rect x="3" y="15.5" width="7.5" height="5" rx="2" />
    {:else if id === 'youtube'}
      <rect x="2.5" y="5.5" width="19" height="13" rx="4" />
      <path d="M10.4 9.4v5.2l4.4-2.6z" fill="currentColor" stroke="none" />
    {:else if id === 'libraries'}
      <path d="M3.5 7.6a2 2 0 0 1 2-2h3.1l2 2.4h7.9a2 2 0 0 1 2 2v8.4a2 2 0 0 1-2 2H5.5a2 2 0 0 1-2-2z" />
    {:else}
      <circle cx="12" cy="9" r="3.2" />
      <path d="M5.5 19.5a6.5 6.5 0 0 1 13 0" />
    {/if}
  </svg>
{/snippet}

<svelte:window onkeydown={onKeydown} />

{#if ui.settingsOpen}
  <div class="fixed inset-0 z-50 flex items-center justify-center p-3 sm:p-6">
    <button
      type="button"
      class="absolute inset-0 bg-ink/80 backdrop-blur-sm"
      aria-label="关闭设置"
      onclick={close}
    ></button>

    <!-- 弹窗本体：左边分类栏，右边滚动内容；没有描边和投影，靠 ink 底压出层次。
         高度固定（不跟内容变），切分类时不会跳来跳去，内容多了在右侧内部滚 -->
    <div
      bind:this={dialogEl}
      role="dialog"
      aria-modal="true"
      aria-labelledby="settings-pane-title"
      tabindex="-1"
      class="relative flex h-[min(80dvh,38rem)] w-full max-w-4xl overflow-hidden rounded-2xl bg-sheet outline-none"
    >
      <nav class="hidden w-52 shrink-0 flex-col gap-0.5 bg-ink/50 p-2.5 sm:flex" aria-label="设置分类">
        <p class="px-3 pb-2.5 pt-1 text-ui font-medium tracking-[-0.01em] text-fg">设置</p>
        {#each PANES as pane (pane.id)}
          <button
            type="button"
            class="flex h-9 items-center gap-2.5 rounded-xl px-3 text-left text-ui transition-colors {ui.settingsPane ===
            pane.id
              ? 'bg-raise text-fg'
              : 'text-muted hover:bg-panel hover:text-fg'}"
            aria-current={ui.settingsPane === pane.id ? 'true' : undefined}
            onclick={() => ui.setPane(pane.id)}
          >
            {@render paneIcon(pane.id)}
            <span class="min-w-0 flex-1 truncate">{pane.label}</span>
          </button>
        {/each}
      </nav>

      <div class="flex min-w-0 flex-1 flex-col">
        <header class="flex items-start justify-between gap-3 px-4 pb-2 pt-3.5 sm:px-5">
          <div class="min-w-0">
            <p class="text-meta text-dim sm:hidden">设置</p>
            <h2 id="settings-pane-title" class="truncate text-title font-medium text-fg">{current.label}</h2>
            <p class="mt-0.5 truncate text-meta text-dim">{current.hint}</p>
          </div>
          <button
            type="button"
            class="grid h-9 w-9 shrink-0 place-items-center rounded-full text-muted transition-colors hover:bg-panel hover:text-fg"
            aria-label="关闭设置"
            onclick={close}
          >
            <svg
              viewBox="0 0 20 20"
              class="h-4 w-4"
              fill="none"
              stroke="currentColor"
              stroke-width="1.6"
              aria-hidden="true"
            >
              <path d="m5.5 5.5 9 9M14.5 5.5l-9 9" />
            </svg>
          </button>
        </header>

        <!-- 移动端没有左侧栏，用一条可横滑的分类条代替 -->
        <div class="flex gap-1.5 overflow-x-auto px-4 pb-2 sm:hidden" role="tablist" aria-label="设置分类">
          {#each PANES as pane (pane.id)}
            <button
              type="button"
              class="chip h-8 shrink-0 px-3 text-ui {ui.settingsPane === pane.id
                ? 'bg-hi text-ink'
                : 'hover:bg-panel'}"
              role="tab"
              aria-selected={ui.settingsPane === pane.id}
              onclick={() => ui.setPane(pane.id)}
            >
              {pane.label}
            </button>
          {/each}
        </div>

        <div class="min-h-0 flex-1 overflow-y-auto overscroll-contain px-4 pb-4 pt-1 sm:px-5">
          {#if loading && !settings}
            <div class="space-y-3">
              {#each Array.from({ length: 3 }, (_, i) => i) as i (i)}
                <div class="h-20 rounded-xl bg-panel"></div>
              {/each}
            </div>
          {:else if error && !settings}
            <div class="flex flex-col items-center gap-3 py-12 text-center" role="alert">
              <p class="text-ui text-muted">{error}</p>
              <button type="button" class="btn" onclick={() => void load()}>重试</button>
            </div>
          {:else if settings}
            {#if mockNote}
              <p class="mb-3 rounded-xl bg-panel px-3 py-2 text-meta text-dim">
                离线预览：正在使用内置示例数据（后端未连接：{mockNote}）
              </p>
            {/if}

            {#if ui.settingsPane === 'libraries'}
              <LibraryPanel />
            {:else if ui.settingsPane === 'account'}
              <div class="space-y-3 py-1">
                <p class="text-ui text-fg/85">
                  当前浏览器已用访问密码登录；退出后需要重新输入密码才能进入。
                </p>
                <p class="text-meta text-dim">
                  会话只存在这个站点，退出不会影响订阅、库文件和下载任务。
                </p>
                <button
                  type="button"
                  class="btn btn-ghost mt-1"
                  onclick={() => void logout()}
                  disabled={loggingOut}
                >
                  {loggingOut ? '退出中…' : '退出登录'}
                </button>
              </div>
            {:else}
              <form
                id="settings-form"
                class="space-y-6 pb-1"
                onsubmit={(event) => {
                  event.preventDefault();
                  void save();
                }}
              >
                {#if ui.settingsPane === 'sync'}
                  <section>
                    <p class="text-meta text-dim">增量同步只抓第一页，遇到已入库的影片即停止。</p>
                    <div class="mt-3 grid gap-3 sm:grid-cols-2">
                      <div>
                        <label for="sync-interval" class="label-meta mb-1.5">同步间隔（小时）</label>
                        <input
                          id="sync-interval"
                          class="field tabular-nums"
                          type="number"
                          min="1"
                          max="168"
                          step="1"
                          bind:value={syncIntervalHours}
                          disabled={saving}
                        />
                      </div>
                      <div>
                        <label for="naming" class="label-meta mb-1.5">命名模板</label>
                        <input
                          id="naming"
                          class="field"
                          type="text"
                          bind:value={namingTemplate}
                          disabled={saving}
                        />
                      </div>
                    </div>
                    <p class="mt-1.5 text-meta text-dim">
                      可用变量：{'{标题}'} {'{发布日期}'} {'{艺人}'} {'{平台}'} {'{id}'} {'{分辨率}'}
                    </p>
                  </section>
                {:else if ui.settingsPane === 'network'}
                  <section>
                    <p class="text-meta text-dim">支持 http:// 与 socks5://，留空表示直连。</p>
                    <div class="mt-3 space-y-3">
                      <div>
                        <label for="global-proxy" class="label-meta mb-1.5">全局代理</label>
                        <input
                          id="global-proxy"
                          class="field"
                          type="text"
                          placeholder="socks5://127.0.0.1:1080"
                          spellcheck="false"
                          autocomplete="off"
                          bind:value={globalProxy}
                          disabled={saving}
                        />
                      </div>
                      <div>
                        <label for="pornhub-proxy" class="label-meta mb-1.5">Pornhub 专用代理</label>
                        <input
                          id="pornhub-proxy"
                          class="field"
                          type="text"
                          placeholder="留空则跟随全局代理"
                          spellcheck="false"
                          autocomplete="off"
                          bind:value={pornhubProxy}
                          disabled={saving}
                        />
                      </div>
                      <div>
                        <label for="xhamster-proxy" class="label-meta mb-1.5">xHamster 专用代理</label>
                        <input
                          id="xhamster-proxy"
                          class="field"
                          type="text"
                          placeholder="留空则跟随全局代理"
                          spellcheck="false"
                          autocomplete="off"
                          bind:value={xhamsterProxy}
                          disabled={saving}
                        />
                      </div>
                      <div>
                        <label for="beeg-proxy" class="label-meta mb-1.5">beeg 专用代理</label>
                        <input
                          id="beeg-proxy"
                          class="field"
                          type="text"
                          placeholder="留空则跟随全局代理"
                          spellcheck="false"
                          autocomplete="off"
                          bind:value={beegProxy}
                          disabled={saving}
                        />
                      </div>
                      <div>
                        <label for="instagram-proxy" class="label-meta mb-1.5">Instagram 专用代理</label>
                        <input
                          id="instagram-proxy"
                          class="field"
                          type="text"
                          placeholder="留空则跟随全局代理"
                          spellcheck="false"
                          autocomplete="off"
                          bind:value={instagramProxy}
                          disabled={saving}
                        />
                        <p class="mt-1 text-meta text-dim">
                          抓动态时的出口 IP 直接决定 IG 会不会拦你。改完保存即刻生效，不用重启。
                        </p>
                      </div>
                      <div>
                        <label for="x-proxy" class="label-meta mb-1.5">X 专用代理</label>
                        <input
                          id="x-proxy"
                          class="field"
                          type="text"
                          placeholder="留空则跟随全局代理"
                          spellcheck="false"
                          autocomplete="off"
                          bind:value={xProxy}
                          disabled={saving}
                        />
                      </div>
                      <div>
                        <label for="youtube-proxy" class="label-meta mb-1.5">YouTube 专用代理</label>
                        <input
                          id="youtube-proxy"
                          class="field"
                          type="text"
                          placeholder="留空则跟随全局代理"
                          spellcheck="false"
                          autocomplete="off"
                          bind:value={youtubeProxy}
                          disabled={saving}
                        />
                        <p class="mt-1 text-meta text-dim">
                          抓 RSS 与下载都会走这个出口。碰上「要求人机验证」就换一个，改完即刻生效。
                        </p>
                      </div>
                      <div>
                        <label for="bilibili-proxy" class="label-meta mb-1.5">B 站专用代理</label>
                        <input
                          id="bilibili-proxy"
                          class="field"
                          type="text"
                          placeholder="留空则跟随全局代理"
                          spellcheck="false"
                          autocomplete="off"
                          bind:value={biliProxy}
                          disabled={saving}
                        />
                        <p class="mt-1 text-meta text-dim">
                          投稿列表接口限流极严（隔 10 秒打三次就封 IP 十几分钟）。国内直连即可；走海外出口时注意 B 站会限制部分区域的画质。
                        </p>
                      </div>
                    </div>
                  </section>
                {:else if ui.settingsPane === 'upload'}
                  <section>
                    <p class="text-meta text-dim">上传目标；token 只写不回显，保存后仅显示是否已配置。</p>
                    <div class="mt-3 space-y-3">
                      <div>
                        <label for="openlist-base" class="label-meta mb-1.5">地址</label>
                        <input
                          id="openlist-base"
                          class="field"
                          type="text"
                          placeholder="http://192.168.0.108:5244"
                          spellcheck="false"
                          autocomplete="off"
                          bind:value={openlistBaseUrl}
                          disabled={saving}
                        />
                      </div>
                      <div>
                        <label for="openlist-root" class="label-meta mb-1.5">根路径</label>
                        <input
                          id="openlist-root"
                          class="field"
                          type="text"
                          placeholder="/123云盘/资源目录/MyPorn"
                          spellcheck="false"
                          autocomplete="off"
                          bind:value={openlistRoot}
                          disabled={saving}
                        />
                      </div>
                      <div>
                        <label for="openlist-token" class="label-meta mb-1.5">Token</label>
                        <input
                          id="openlist-token"
                          class="field"
                          type="password"
                          placeholder={settings.openlist.hasToken ? '已保存 · 留空表示不修改' : '尚未配置'}
                          autocomplete="new-password"
                          spellcheck="false"
                          bind:value={openlistToken}
                          disabled={saving}
                        />
                      </div>
                    </div>
                  </section>
                {:else if ui.settingsPane === 'youtube'}
                  <section>
                    <p class="text-meta text-dim">
                      YouTube 的视频由 yt-dlp 下载：它跟着 YouTube 的接口天天变，
                      <span class="text-muted">版本过期是最常见的失败原因</span>（旧版会直接 403）。
                    </p>

                    <!-- yt-dlp 健康度：这一块存在的意义就是「别让人对着 403 猜」 -->
                    <div class="mt-3 rounded-xl bg-panel px-3 py-2.5">
                      {#if ytHealthLoading}
                        <p class="text-meta text-dim">正在检查 yt-dlp…</p>
                      {:else if !ytHealth || !ytHealth.found}
                        <p class="text-meta text-brick">
                          没找到 yt-dlp。没有它只能抓 RSS（最近 15 条、没有时长），也不能下载。
                        </p>
                        <p class="mt-1 text-meta text-dim">
                          装一个（<code class="font-mono">pipx install yt-dlp</code> 或
                          <code class="font-mono">brew install yt-dlp</code>），或在下面填绝对路径。
                        </p>
                      {:else}
                        <div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-meta">
                          <span class="text-fg">yt-dlp {ytHealth.version}</span>
                          <span class={ytHealth.stale ? 'text-brick' : 'text-sage'}>
                            {ytHealth.stale
                              ? `已 ${String(ytHealth.ageDays)} 天未更新，下载很可能失败`
                              : '版本新鲜'}
                          </span>
                          <span class={ytHealth.ffmpegOk ? 'text-dim' : 'text-brick'}>
                            {ytHealth.ffmpegOk ? 'ffmpeg 可用' : 'ffmpeg 缺失，音视频合轨会失败'}
                          </span>
                        </div>
                        <p class="mt-1 truncate font-mono text-[0.6875rem] text-dim" title={ytHealth.path}>
                          {ytHealth.path}
                        </p>
                        {#if ytHealth.stale}
                          <p class="mt-1.5 text-meta text-dim">
                            升级：<code class="font-mono">yt-dlp -U</code>（或
                            <code class="font-mono">brew upgrade yt-dlp</code> / 重装 pipx 包），改完回来点一下「重新检查」。
                          </p>
                        {/if}
                        <button
                          type="button"
                          class="mt-2 rounded-full bg-raise px-2.5 py-1 text-meta text-fg transition-colors hover:bg-panel"
                          onclick={() => {
                            ytHealth = null;
                            void loadYtHealth();
                          }}
                        >
                          重新检查
                        </button>
                      {/if}
                    </div>

                    <div class="mt-4 grid gap-3 sm:grid-cols-2">
                      <div>
                        <label for="yt-hours" class="label-meta mb-1.5">同步间隔（小时）</label>
                        <input
                          id="yt-hours"
                          class="field tabular-nums"
                          type="number"
                          min="1"
                          max="48"
                          step="1"
                          bind:value={ytSyncHours}
                          disabled={saving}
                        />
                        <p class="mt-1.5 text-meta text-dim">每轮抓一次 RSS（1 次请求，最多 15 条新视频）。</p>
                      </div>
                      <div>
                        <label for="yt-backfill" class="label-meta mb-1.5">深度回填条数</label>
                        <input
                          id="yt-backfill"
                          class="field tabular-nums"
                          type="number"
                          min="10"
                          max="2000"
                          step="10"
                          bind:value={ytBackfillLimit}
                          disabled={saving}
                        />
                        <p class="mt-1.5 text-meta text-dim">
                          添加频道时用 yt-dlp 一次列这么多条历史（200 条约 6 秒）。
                        </p>
                      </div>
                    </div>

                    <div class="mt-4">
                      <label for="yt-format" class="label-meta mb-1.5">下载格式串（yt-dlp -f）</label>
                      <input
                        id="yt-format"
                        class="field font-mono text-meta"
                        type="text"
                        placeholder="留空 = AV1 最高画质 + 最高音质（bv*[vcodec^=av01]+ba/bv*+ba/b）"
                        spellcheck="false"
                        autocomplete="off"
                        bind:value={ytFormat}
                        disabled={saving}
                      />
                      <p class="mt-1.5 text-meta text-dim">
                        默认取 AV1 视频轨 + 最高音质音轨，再用 ffmpeg <code class="font-mono">-c copy</code>
                        合成 mp4（只换容器，不转码）。YouTube 不提供 AV1+音轨的现成文件，这一步躲不掉。
                        个别播放器不认 mp4 里的 Opus 音轨时，改成
                        <code class="font-mono">bv*+ba[ext=m4a]/bv*+ba</code>。
                      </p>
                    </div>

                    <div class="mt-4">
                      <label for="yt-path" class="label-meta mb-1.5">yt-dlp 路径</label>
                      <input
                        id="yt-path"
                        class="field font-mono text-meta"
                        type="text"
                        placeholder="留空 = 从 PATH 里找 yt-dlp"
                        spellcheck="false"
                        autocomplete="off"
                        bind:value={ytdlpPath}
                        disabled={saving}
                      />
                      <p class="mt-1.5 text-meta text-dim">
                        想固定用最新版（不动系统里的那个）时可以填绝对路径，例如
                        <code class="font-mono">/opt/ytvenv/bin/yt-dlp</code>。
                      </p>
                    </div>

                    <div class="mt-5">
                      <div class="flex items-center justify-between">
                        <label for="yt-cookie" class="label-meta">Cookie（可选）</label>
                        <span class="text-meta {settings.youtube.hasCookies ? 'text-sage' : 'text-dim'}">
                          {settings.youtube.hasCookies ? '已配置' : '未配置'}
                        </span>
                      </div>
                      <p class="mt-1 text-meta text-dim">
                        只有「需要登录才能看」的内容才用得上（会员、年龄限制）。贴
                        <span class="text-muted">cookies.txt</span>（Netscape 格式，整份粘进来即可）或请求头里那整段 Cookie。
                        Cookie 加密存库，保存后不再回显。
                      </p>
                      <textarea
                        id="yt-cookie"
                        class="field mt-2 h-28 resize-y font-mono text-meta"
                        placeholder={settings.youtube.hasCookies
                          ? '已保存 · 留空表示不修改'
                          : '# Netscape HTTP Cookie File —— 一整份 cookies.txt，或 SID=…; HSID=…'}
                        spellcheck="false"
                        autocomplete="off"
                        bind:value={ytCookies}
                        disabled={saving}
                      ></textarea>
                    </div>
                  </section>
                {:else if ui.settingsPane === 'bilibili'}
                  <section>
                    <p class="text-meta text-dim">
                      订阅 UP 主时贴主页地址 <code class="font-mono">space.bilibili.com/&lt;mid&gt;</code> 即可。B
                      站不支持按名字搜索（搜索接口要浏览器指纹签名），所以搜索框里敲名字会直接提示贴链接。
                    </p>

                    <div class="mt-5">
                      <div class="flex items-center justify-between">
                        <label for="bili-cookie" class="label-meta">Cookie（SESSDATA）</label>
                        <span class="text-meta {settings.bilibili.hasCookie ? 'text-sage' : 'text-dim'}">
                          {settings.bilibili.hasCookie ? '已配置' : '未配置'}
                        </span>
                      </div>
                      <p class="mt-1 text-meta text-dim">
                        不填也能看，但最高只有 720P（未登录时 B 站只给 480P 的 DASH 和 720P 的单文件）。
                        1080P / 4K 必须登录：从已登录的浏览器里复制整串 Cookie（至少含
                        <span class="text-muted">SESSDATA</span>）粘进来即可。Cookie 加密存库，保存后不再回显。
                      </p>
                      <textarea
                        id="bili-cookie"
                        class="field mt-2 h-24 resize-y font-mono text-meta"
                        placeholder={settings.bilibili.hasCookie
                          ? '已保存 · 留空表示不修改'
                          : 'SESSDATA=xxxxxx%2Cyyyyyy%2C…; bili_jct=…; DedeUserID=…'}
                        spellcheck="false"
                        autocomplete="off"
                        bind:value={biliCookie}
                        disabled={saving}
                      ></textarea>
                    </div>

                    <p class="mt-5 text-meta text-dim">
                      下载与在线播放都会用到这份登录态；1080P 以上的片源是音画分轨，服务端会先用 ffmpeg
                      合成一份再播（第一次点开要等几秒，之后直接读缓存）。
                    </p>
                  </section>
                {:else if ui.settingsPane === 'download'}
                  <section>
                    <p class="text-meta text-dim">并发数改完立即生效；限速只作用于单部影片的分片下载。</p>
                    <div class="mt-3 grid gap-3 sm:grid-cols-2">
                      <div>
                        <label for="dl-concurrency" class="label-meta mb-1.5">最大并发下载数</label>
                        <input
                          id="dl-concurrency"
                          class="field tabular-nums"
                          type="number"
                          min="1"
                          max="8"
                          step="1"
                          bind:value={downloadConcurrency}
                          disabled={saving}
                        />
                      </div>
                      <div>
                        <label for="dl-rate" class="label-meta mb-1.5">单任务限速（KB/s，0 = 不限）</label>
                        <input
                          id="dl-rate"
                          class="field tabular-nums"
                          type="number"
                          min="0"
                          max="1000000"
                          step="128"
                          bind:value={downloadRateKb}
                          disabled={saving}
                        />
                      </div>
                    </div>
                    <label
                      class="mt-4 flex cursor-pointer items-start gap-3 rounded-xl bg-ink/35 p-3.5"
                      for="dl-auto"
                    >
                      <input
                        id="dl-auto"
                        type="checkbox"
                        class="mt-0.5 size-4 accent-hi"
                        bind:checked={autoDownloadNew}
                        disabled={saving}
                      />
                      <span class="min-w-0">
                        <span class="block text-ui text-fg">同步到新片后自动下载</span>
                        <span class="mt-0.5 block text-meta text-dim">
                          默认关闭：下载只在你点「下载到本地库」时发生。打开后，定时同步发现的新片会自动存进默认库；
                          订阅后的首次全量回填永远不自动下载。
                        </span>
                      </span>
                    </label>
                    <p class="mt-2 text-meta {settings.ffmpegAvailable ? 'text-dim' : 'text-brick'}">
                      {settings.ffmpegAvailable
                        ? 'ffmpeg 可用：下载完成后以 -c copy 封装为 mp4，不转码。'
                        : '服务端没有可用的 ffmpeg，下载会在封装步骤失败，请检查镜像或 FFMPEG_PATH。'}
                    </p>
                  </section>
                {:else}
                  <section>
                    <p class="text-meta text-dim">
                      两个站的抓取都是按 IP 限流的，所以同一时刻只跑一个同步任务，撞到限流会直接停下来等冷却。
                    </p>
                    <div class="mt-3 grid gap-3 sm:grid-cols-2">
                      <div>
                        <label for="social-hours" class="label-meta mb-1.5">抓取间隔（小时）</label>
                        <input
                          id="social-hours"
                          class="field tabular-nums"
                          type="number"
                          min="1"
                          max="48"
                          step="1"
                          bind:value={socialSyncHours}
                          disabled={saving}
                        />
                        <p class="mt-1.5 text-meta text-dim">每轮抓一次；X 免登录一次只有 20 帖，靠多轮慢慢累积。</p>
                      </div>
                      <div>
                        <p class="label-meta mb-1.5">X</p>
                        <p class="rounded-xl bg-ink/35 px-3 py-2 text-meta text-dim">
                          不需要登录。免费配额是每 15 分钟 30 次请求（按 IP 计），一次抓取只用 1–2 次。
                        </p>
                      </div>
                    </div>

                    <div class="mt-5">
                      <div class="flex items-center justify-between">
                        <label for="ig-cookie" class="label-meta">Instagram Cookie</label>
                        <span class="text-meta {settings.social.hasInstagramCookie ? 'text-sage' : 'text-dim'}">
                          {settings.social.hasInstagramCookie ? '已配置' : '未配置'}
                        </span>
                      </div>
                      <p class="mt-1 text-meta text-dim">
                        Instagram 已经不能免登录抓取，必须贴一段登录态。两种都认：
                        <span class="text-muted">① 浏览器扩展导出的 cookies.txt</span>（Netscape 格式，
                        <code class="font-mono"># Netscape HTTP Cookie File</code> 那种，整份粘进来即可，
                        别的站的 Cookie 会被丢掉）；
                        <span class="text-muted">② 开发者工具 → Network → 任选一个请求</span>，
                        把请求头里的 Cookie 整段复制过来。Cookie 加密存库，保存后不再回显。
                      </p>
                      <textarea
                        id="ig-cookie"
                        class="field mt-2 h-32 resize-y font-mono text-meta"
                        placeholder={settings.social.hasInstagramCookie
                          ? '已保存 · 留空表示不修改'
                          : '# Netscape HTTP Cookie File —— 整份 cookies.txt 粘进来，或 sessionid=…; csrftoken=…'}
                        spellcheck="false"
                        autocomplete="off"
                        bind:value={instagramCookie}
                        disabled={saving}
                      ></textarea>
                    </div>
                  </section>
                {/if}

                {#if error}
                  <p class="text-ui text-brick" role="alert">{error}</p>
                {/if}
              </form>
            {/if}
          {/if}
        </div>

        {#if isFormPane && settings}
          <footer class="flex items-center justify-end gap-2 px-4 py-3 sm:px-5">
            <button type="button" class="btn btn-ghost" onclick={close}>关闭</button>
            <button type="submit" form="settings-form" class="btn btn-primary" disabled={saving}>
              {saving ? '保存中…' : '保存设置'}
            </button>
          </footer>
        {/if}
      </div>
    </div>
  </div>
{/if}
