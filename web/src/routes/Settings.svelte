<script lang="ts">
  import LibraryPanel from '../components/LibraryPanel.svelte';
  import TopBar from '../components/TopBar.svelte';
  import { api, messageFor } from '../lib/api';
  import { live } from '../lib/live.svelte';
  import { navigate } from '../lib/router.svelte';
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

  let settings = $state<Settings | null>(null);
  let loading = $state(true);
  let error = $state('');
  let saving = $state(false);
  let mockNote = $state('');
  let loggingOut = $state(false);

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
      navigate('/login', { replace: true });
    }
  }

  $effect(() => {
    void load();
  });

  // 从下载页/对话框跳过来时直接滚到「本地库」区块
  $effect(() => {
    if (!settings) return;
    if (window.location.hash === '#libraries') {
      queueMicrotask(() => document.getElementById('libraries')?.scrollIntoView({ block: 'start' }));
    }
  });
</script>

<div class="mx-auto w-full max-w-3xl">
  <TopBar title="设置" />

  {#if loading && !settings}
    <div class="space-y-3">
      {#each Array.from({ length: 4 }, (_, i) => i) as i (i)}
        <div class="h-20 rounded-xl bg-panel"></div>
      {/each}
    </div>
  {:else if error && !settings}
    <div class="flex flex-col items-center gap-3 py-16 text-center" role="alert">
      <p class="text-ui text-muted">{error}</p>
      <button type="button" class="btn" onclick={() => void load()}>重试</button>
    </div>
  {:else if settings}
    <div class="space-y-8">
    <form class="space-y-8" onsubmit={(event) => { event.preventDefault(); void save(); }}>
      {#if mockNote}
        <p class="rounded-xl bg-panel px-3 py-2 text-meta text-dim">
          离线预览：正在使用内置示例数据（后端未连接：{mockNote}）
        </p>
      {/if}

      <section>
        <h2 class="text-title font-medium text-fg">同步</h2>
        <p class="mt-1 text-meta text-dim">增量同步只抓第一页，遇到已入库的影片即停止。</p>
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
            <input id="naming" class="field" type="text" bind:value={namingTemplate} disabled={saving} />
            <p class="mt-1 text-meta text-dim">可用变量：{'{标题}'} {'{发布日期}'} {'{艺人}'} {'{平台}'} {'{id}'} {'{分辨率}'}</p>
          </div>
        </div>
      </section>

      <section>
        <h2 class="text-title font-medium text-fg">网络与代理</h2>
        <p class="mt-1 text-meta text-dim">支持 http:// 与 socks5://，留空表示直连。</p>
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

      <section>
        <h2 class="text-title font-medium text-fg">OpenList</h2>
        <p class="mt-1 text-meta text-dim">上传目标；token 只写不回显，保存后仅显示是否已配置。</p>
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

      <section>
        <h2 class="text-title font-medium text-fg">下载</h2>
        <p class="mt-1 text-meta text-dim">并发数改完立即生效；限速只作用于单部影片的分片下载。</p>
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
          class="mt-4 flex cursor-pointer items-start gap-3 rounded-xl bg-raise/50 p-3.5"
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

      {#if error}
        <p class="text-ui text-brick" role="alert">{error}</p>
      {/if}

      <div class="flex items-center justify-between gap-3 pt-2">
        <button type="submit" class="btn btn-primary" disabled={saving}>
          {saving ? '保存中…' : '保存设置'}
        </button>
        <button type="button" class="btn btn-ghost" onclick={() => void logout()} disabled={loggingOut}>
          {loggingOut ? '退出中…' : '退出登录'}
        </button>
      </div>
    </form>

    <LibraryPanel />
    </div>
  {/if}
</div>
