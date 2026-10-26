<script lang="ts">
  import LibraryPanel from './LibraryPanel.svelte';
  import { api, messageFor } from '../lib/api';
  import { live } from '../lib/live.svelte';
  import { navigate } from '../lib/router.svelte';
  import { ui, type SettingsPane } from '../lib/ui.svelte';
  import type { Settings, SettingsPatch } from '../lib/types';

  /** 仅 DEV：后端未就绪时的示例数据 */
  const MOCK: Settings = {
    syncIntervalHours: 6,
    globalProxy: '',
    platformProxy: { pornhub: 'socks5://127.0.0.1:1080' },
    namingTemplate: '{标题}',
    downloadConcurrency: 2,
    downloadRateKb: 0,
    autoDownloadNew: false,
    defaultLibraryId: 0,
    libraryRoot: './media',
    ffmpegAvailable: true,
    openlist: { baseUrl: 'http://192.168.0.108:5244', root: '/123云盘/资源目录/MyPorn', hasToken: true },
  };

  /** 侧边栏分类：左侧栏在弹窗里切换，右侧只渲染当前这一块 */
  const PANES: readonly { id: SettingsPane; label: string; hint: string }[] = [
    { id: 'sync', label: '同步', hint: '同步间隔与文件命名' },
    { id: 'network', label: '网络与代理', hint: '全局与按平台代理' },
    { id: 'upload', label: '上传', hint: 'OpenList 上传目标' },
    { id: 'download', label: '下载', hint: '并发、限速与自动下载' },
    { id: 'libraries', label: '本地库', hint: '落盘目录与空间' },
    { id: 'account', label: '账号', hint: '会话与退出' },
  ];

  /** 这四个分类共用一个表单，保存按钮在页脚（用 form 属性挂回来） */
  const FORM_PANES: readonly SettingsPane[] = ['sync', 'network', 'upload', 'download'];

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
  let namingTemplate = $state('{标题}');
  let downloadConcurrency = $state(2);
  let downloadRateKb = $state(0);
  let autoDownloadNew = $state(false);
  let openlistBaseUrl = $state('');
  let openlistRoot = $state('');
  let openlistToken = $state('');

  const current = $derived(PANES.find((p) => p.id === ui.settingsPane) ?? PANES[0]);
  const isFormPane = $derived(FORM_PANES.includes(ui.settingsPane));

  function fill(value: Settings): void {
    settings = value;
    syncIntervalHours = value.syncIntervalHours;
    globalProxy = value.globalProxy;
    pornhubProxy = value.platformProxy.pornhub;
    namingTemplate = value.namingTemplate;
    downloadConcurrency = value.downloadConcurrency;
    downloadRateKb = value.downloadRateKb;
    autoDownloadNew = value.autoDownloadNew;
    openlistBaseUrl = value.openlist.baseUrl;
    openlistRoot = value.openlist.root;
    openlistToken = '';
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
      platformProxy: { pornhub: pornhubProxy },
      namingTemplate,
      downloadConcurrency,
      downloadRateKb,
      autoDownloadNew,
      openlist: { baseUrl: openlistBaseUrl, root: openlistRoot },
    };
    const token = openlistToken.trim();
    if (token.length > 0) patch.openlist = { ...patch.openlist, token };
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
                {:else}
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
