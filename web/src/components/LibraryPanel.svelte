<script lang="ts">
  import { api, messageFor } from '../lib/api';
  import { formatBytes } from '../lib/format';
  import { live } from '../lib/live.svelte';
  import { confirmDialog } from '../lib/confirm.svelte';
  import type { Library, PathValidation, UploadPolicy } from '../lib/types';

  let libraries = $state<Library[]>([]);
  let suggestedRoot = $state('./media');
  let loading = $state(true);
  let error = $state('');

  let editingId = $state<number | null>(null);
  let name = $state('');
  let rootPath = $state('');
  let namingTemplate = $state('');
  let uploadPolicy = $state<UploadPolicy>('local');
  let isDefault = $state(false);

  let checking = $state(false);
  let check = $state<PathValidation | null>(null);
  let saving = $state(false);
  let busyId = $state<number | null>(null);

  const POLICY_LABEL: Record<UploadPolicy, string> = {
    local: '仅本地',
    local_remote: '本地保留 + 上传',
    strm: '上传后转 strm',
  };

  function resetForm(): void {
    editingId = null;
    name = '';
    rootPath = suggestedRoot;
    namingTemplate = '';
    uploadPolicy = 'local';
    isDefault = libraries.length === 0;
    check = null;
    error = '';
  }

  function startEdit(lib: Library): void {
    editingId = lib.id;
    name = lib.name;
    rootPath = lib.rootPath;
    namingTemplate = lib.namingTemplate;
    uploadPolicy = lib.uploadPolicy;
    isDefault = lib.isDefault;
    check = null;
    error = '';
  }

  async function load(): Promise<void> {
    loading = true;
    error = '';
    try {
      const res = await api.libraries();
      libraries = res.items;
      suggestedRoot = res.suggestedRootPath || './media';
      if (editingId === null && rootPath === '') rootPath = suggestedRoot;
      // 第一个库必须成为默认库，否则后面投递下载会没有默认目标
      if (editingId === null && res.items.length === 0) isDefault = true;
    } catch (err) {
      error = messageFor(err);
    } finally {
      loading = false;
    }
  }

  async function validate(): Promise<void> {
    if (checking || rootPath.trim() === '') return;
    checking = true;
    check = null;
    try {
      check = await api.validateLibraryPath(rootPath.trim());
    } catch (err) {
      error = messageFor(err);
    } finally {
      checking = false;
    }
  }

  async function save(): Promise<void> {
    if (saving) return;
    if (rootPath.trim() === '') {
      error = '库目录不能为空';
      return;
    }
    saving = true;
    error = '';
    const payload = {
      name: name.trim() || undefined,
      rootPath: rootPath.trim(),
      namingTemplate: namingTemplate.trim(),
      uploadPolicy,
      isDefault: isDefault || libraries.length === 0,
    };
    try {
      if (editingId === null) await api.createLibrary(payload);
      else await api.patchLibrary(editingId, payload);
      live.toast(editingId === null ? '本地库已创建' : '本地库已更新', 'success');
      await load();
      resetForm();
    } catch (err) {
      const message = messageFor(err);
      error = message;
      live.toast(message, 'error');
    } finally {
      saving = false;
    }
  }

  async function makeDefault(lib: Library): Promise<void> {
    busyId = lib.id;
    try {
      await api.patchLibrary(lib.id, { isDefault: true });
      await load();
      live.toast(`已把「${lib.name}」设为默认库`, 'success');
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      busyId = null;
    }
  }

  async function toggleEnabled(lib: Library): Promise<void> {
    busyId = lib.id;
    try {
      await api.patchLibrary(lib.id, { enabled: !lib.enabled });
      await load();
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      busyId = null;
    }
  }

  async function remove(lib: Library): Promise<void> {
    if (busyId !== null) return;
    const ok = await confirmDialog.ask({
      title: `删除本地库「${lib.name}」？`,
      body: '只删索引，磁盘上的文件不会被删除。',
      confirmText: '删除',
    });
    if (!ok) return;
    busyId = lib.id;
    try {
      await api.deleteLibrary(lib.id);
      await load();
      if (editingId === lib.id) resetForm();
      live.toast('本地库已删除（磁盘文件保留）', 'info');
    } catch (err) {
      live.toast(messageFor(err), 'error');
    } finally {
      busyId = null;
    }
  }

  $effect(() => {
    void load();
  });
</script>

<!-- 标题由设置弹窗的分类栏给出，这里不再重复一遍 -->
<section class="pb-1">
  <div class="flex items-start justify-between gap-3">
    <p class="text-meta text-dim">
      每部影片独立一个文件夹，内含原片 + nfo + poster / thumb / fanart，Emby 可直接刮削。
    </p>
    <button type="button" class="btn btn-ghost h-8 shrink-0" onclick={() => void load()}>刷新空间</button>
  </div>

  {#if loading && libraries.length === 0}
    <div class="mt-3 h-20 animate-pulse rounded-xl bg-panel"></div>
  {:else if libraries.length === 0}
    <p class="mt-3 rounded-xl bg-panel px-3.5 py-3 text-ui text-muted">
      还没有本地库。在下面指定一个可写目录，创建后就能开始下载。
    </p>
  {:else}
    <ul class="mt-3 space-y-2">
      {#each libraries as lib (lib.id)}
        <li class="panel p-3.5">
          <div class="flex flex-wrap items-start justify-between gap-2">
            <div class="min-w-0">
              <p class="flex items-center gap-2 text-ui font-medium text-fg/90">
                <span class="truncate">{lib.name}</span>
                {#if lib.isDefault}
                  <span class="rounded bg-hi-soft px-1.5 py-0.5 text-meta font-medium text-hi">默认</span>
                {/if}
                {#if !lib.enabled}
                  <span class="rounded bg-raise px-1.5 py-0.5 text-meta text-dim">已停用</span>
                {/if}
              </p>
              <p class="mt-0.5 truncate text-meta text-dim" title={lib.rootPath}>{lib.rootPath}</p>
              <p class="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-meta tabular-nums text-dim">
                <span>剩余 {formatBytes(lib.freeBytes)} / {formatBytes(lib.totalBytes)}</span>
                <span>{lib.fileCount} 部 · {formatBytes(lib.sizeBytes)}</span>
                <span class="text-muted">{POLICY_LABEL[lib.uploadPolicy]}</span>
                {#if !lib.writable}
                  <span class="text-brick">{lib.spaceMessage}</span>
                {/if}
              </p>
            </div>
            <div class="flex shrink-0 items-center gap-1">
              {#if !lib.isDefault}
                <button type="button" class="btn btn-ghost h-8" onclick={() => void makeDefault(lib)} disabled={busyId === lib.id}>
                  设为默认
                </button>
              {/if}
              <button type="button" class="btn btn-ghost h-8" onclick={() => startEdit(lib)} disabled={busyId === lib.id}>
                编辑
              </button>
              <button type="button" class="btn btn-ghost h-8" onclick={() => void toggleEnabled(lib)} disabled={busyId === lib.id}>
                {lib.enabled ? '停用' : '启用'}
              </button>
              <button type="button" class="btn btn-danger h-8" onclick={() => void remove(lib)} disabled={busyId === lib.id}>
                删除
              </button>
            </div>
          </div>
        </li>
      {/each}
    </ul>
  {/if}

  <form
    class="panel mt-3 space-y-3 p-3.5"
    onsubmit={(event) => {
      event.preventDefault();
      void save();
    }}
  >
    <div class="flex items-baseline justify-between gap-2">
      <h3 class="text-ui font-medium text-fg/90">{editingId === null ? '新建本地库' : '编辑本地库'}</h3>
      {#if editingId !== null}
        <button type="button" class="btn btn-ghost h-8" onclick={resetForm}>取消编辑</button>
      {/if}
    </div>

    <div class="grid gap-3 sm:grid-cols-2">
      <div>
        <label for="lib-name" class="label-meta mb-1.5">名称</label>
        <input id="lib-name" class="field" type="text" placeholder="主库" bind:value={name} disabled={saving} />
      </div>
      <div>
        <label for="lib-template" class="label-meta mb-1.5">命名模板（留空跟随全局）</label>
        <input
          id="lib-template"
          class="field"
          type="text"
          placeholder={'{标题}'}
          bind:value={namingTemplate}
          disabled={saving}
        />
      </div>
    </div>

    <div>
      <label for="lib-root" class="label-meta mb-1.5">目录（服务器上的绝对路径，或相对工作目录）</label>
      <div class="flex gap-2">
        <input
          id="lib-root"
          class="field"
          type="text"
          placeholder={suggestedRoot}
          spellcheck="false"
          autocomplete="off"
          bind:value={rootPath}
          disabled={saving}
        />
        <button type="button" class="btn shrink-0" onclick={() => void validate()} disabled={checking || saving}>
          {checking ? '检查中…' : '检查'}
        </button>
      </div>
      {#if check}
        <p class="mt-1 text-meta {check.ok ? 'text-sage' : 'text-brick'}">
          {check.message}{#if check.ok} · 剩余 {formatBytes(check.freeBytes)}{/if}
        </p>
      {/if}
    </div>

    <div class="grid gap-3 sm:grid-cols-2">
      <div>
        <label for="lib-policy" class="label-meta mb-1.5">上传策略</label>
        <select id="lib-policy" class="field" bind:value={uploadPolicy} disabled={saving}>
          <option value="local">仅本地</option>
          <option value="local_remote">本地保留 + 上传（M3）</option>
          <option value="strm">上传后转 strm（M3）</option>
        </select>
        <p class="mt-1 text-meta text-dim">M2 只会保存到本地；上传与 strm 在 M3 生效。</p>
      </div>
      <label class="flex items-end gap-2 pb-2 text-meta text-muted">
        <input type="checkbox" class="accent-hi" bind:checked={isDefault} disabled={saving} />
        设为默认库
      </label>
    </div>

    {#if error}
      <p class="text-meta text-brick" role="alert">{error}</p>
    {/if}

    <div class="flex justify-end">
      <button type="submit" class="btn btn-primary" disabled={saving}>
        {saving ? '保存中…' : editingId === null ? '创建本地库' : '保存修改'}
      </button>
    </div>
  </form>
</section>
