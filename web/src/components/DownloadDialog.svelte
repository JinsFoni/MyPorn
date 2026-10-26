<script lang="ts">
  import { api, messageFor } from '../lib/api';
  import { formatBytes, formatDuration } from '../lib/format';
  import { live } from '../lib/live.svelte';
  import { navigate } from '../lib/router.svelte';
  import { ui } from '../lib/ui.svelte';
  import type { DownloadOptions } from '../lib/types';

  interface Props {
    videoId: number;
    open: boolean;
    onClose: () => void;
  }

  let { videoId, open, onClose }: Props = $props();

  let options = $state<DownloadOptions | null>(null);
  let loading = $state(false);
  let submitting = $state(false);
  let error = $state('');
  let libraryId = $state<number | null>(null);
  let overwrite = $state(false);
  let dialogEl = $state<HTMLDivElement | null>(null);

  const selected = $derived(options?.libraries.find((l) => l.id === libraryId) ?? null);
  const estimate = $derived(options?.estimatedBytes ?? null);
  const notEnoughSpace = $derived(
    selected !== null && estimate !== null && selected.freeBytes > 0 && estimate > selected.freeBytes,
  );
  const alreadyHere = $derived(
    options?.localCopies.find((c) => c.libraryId === libraryId) ?? null,
  );

  async function load(): Promise<void> {
    loading = true;
    error = '';
    try {
      const res = await api.downloadOptions(videoId);
      options = res;
      libraryId = res.defaultLibraryId ?? res.libraries[0]?.id ?? null;
    } catch (err) {
      error = messageFor(err);
    } finally {
      loading = false;
    }
  }

  async function submit(): Promise<void> {
    if (submitting || libraryId === null) return;
    submitting = true;
    error = '';
    try {
      const res = await api.createDownload(videoId, { libraryId, overwrite });
      if (res.skipped) {
        live.toast(res.reason ?? '这个库中已有该影片', 'info');
      } else {
        live.toast('已加入下载队列', 'success');
        live.refreshDownloads(true);
      }
      onClose();
    } catch (err) {
      const message = messageFor(err);
      error = message;
      live.toast(message, 'error');
    } finally {
      submitting = false;
    }
  }

  function onKeydown(event: KeyboardEvent): void {
    if (!open) return;
    if (event.key === 'Escape') {
      event.preventDefault();
      onClose();
    }
  }

  $effect(() => {
    if (!open) return;
    void load();
    // 打开后把焦点交给对话框，键盘用户不会被背后的页面困住
    queueMicrotask(() => dialogEl?.focus());
    document.body.style.overflow = 'hidden';
    return () => {
      document.body.style.overflow = '';
    };
  });
</script>

<svelte:window onkeydown={onKeydown} />

{#if open}
  <div class="fixed inset-0 z-50 flex items-end justify-center sm:items-center">
    <button
      type="button"
      class="absolute inset-0 bg-ink/80 backdrop-blur-sm"
      aria-label="关闭"
      onclick={onClose}
    ></button>

    <div
      bind:this={dialogEl}
      role="dialog"
      aria-modal="true"
      aria-labelledby="download-dialog-title"
      tabindex="-1"
      class="relative w-full max-w-md rounded-t-2xl bg-raise p-4 sm:rounded-2xl mb-safe"
    >
      <div class="flex items-start justify-between gap-3">
        <div>
          <h2 id="download-dialog-title" class="text-title font-medium tracking-tight text-fg">下载到本地库</h2>
          <p class="mt-0.5 text-meta text-dim">保存最高画质原片，不转码</p>
        </div>
        <button
          type="button"
          class="btn btn-ghost -mr-1 h-8 w-8 shrink-0 p-0"
          aria-label="关闭"
          onclick={onClose}
        >
          <svg viewBox="0 0 20 20" class="h-4 w-4" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
            <path d="m5.5 5.5 9 9M14.5 5.5l-9 9" />
          </svg>
        </button>
      </div>

      {#if loading}
        <div class="mt-4 space-y-2">
          <div class="h-11 animate-pulse rounded-lg bg-raise"></div>
          <div class="h-11 animate-pulse rounded-lg bg-raise"></div>
        </div>
      {:else if error && !options}
        <p class="mt-4 text-ui text-brick" role="alert">{error}</p>
        <div class="mt-3 flex justify-end gap-2">
          <button type="button" class="btn btn-ghost" onclick={onClose}>关闭</button>
          <button type="button" class="btn" onclick={() => void load()}>重试</button>
        </div>
      {:else if options}
        {#if options.libraries.length === 0}
          <div class="mt-4 rounded-xl bg-ink/40 p-3">
            <p class="text-ui text-fg/85">还没有本地库</p>
            <p class="mt-1 text-meta text-dim">先创建一个库（指定一个可写目录），下载才有地方落盘。</p>
            <button
              type="button"
              class="btn btn-primary mt-3 w-full"
              onclick={() => {
                onClose();
                ui.openSettings('libraries');
              }}
            >
              去创建设置库
            </button>
          </div>
        {:else}
          <fieldset class="mt-4">
            <legend class="label-meta mb-1.5">保存到</legend>
            <div class="space-y-1.5">
              {#each options.libraries as lib (lib.id)}
                <label
                  class="flex cursor-pointer items-center gap-2.5 rounded-xl px-2.5 py-2 transition-colors {libraryId ===
                  lib.id
                    ? 'bg-hi-soft'
                    : 'hover:bg-raise'}"
                >
                  <input
                    type="radio"
                    name="library"
                    class="accent-hi"
                    value={lib.id}
                    checked={libraryId === lib.id}
                    onchange={() => (libraryId = lib.id)}
                    disabled={submitting}
                  />
                  <span class="min-w-0 flex-1">
                    <span class="block truncate text-ui text-fg/90">{lib.name}</span>
                    <span class="block truncate text-meta text-dim">{lib.rootPath}</span>
                  </span>
                  <span class="shrink-0 text-right text-meta tabular-nums text-dim">
                    剩 {formatBytes(lib.freeBytes)}
                  </span>
                </label>
              {/each}
            </div>
          </fieldset>

          <dl class="mt-3 space-y-1 border-t border-line pt-3 text-meta">
            <div class="flex justify-between">
              <dt class="text-dim">预计体积</dt>
              <dd class="tabular-nums text-fg/85">
                {estimate !== null ? `≈ ${formatBytes(estimate)}` : '码率未知'}
                {#if options.variantId}
                  <span class="text-dim">· {options.variantId}</span>
                {/if}
              </dd>
            </div>
            <div class="flex justify-between">
              <dt class="text-dim">时长</dt>
              <dd class="tabular-nums text-fg/85">{formatDuration(options.durationSec)}</dd>
            </div>
            <div class="flex justify-between">
              <dt class="text-dim">元数据</dt>
              <dd class="text-fg/85">nfo + poster / thumb / fanart</dd>
            </div>
          </dl>

          {#if alreadyHere}
            <p class="mt-3 rounded-xl bg-ink/40 px-3 py-2 text-meta text-dim">
              这个库里已有该影片（{alreadyHere.resolution || '未知画质'} · {formatBytes(alreadyHere.sizeBytes)}）。
              重新下载会覆盖同名文件。
            </p>
            <label class="mt-2 flex items-center gap-2 text-meta text-muted">
              <input type="checkbox" class="accent-hi" bind:checked={overwrite} disabled={submitting} />
              覆盖已有文件
            </label>
          {/if}

          {#if notEnoughSpace && selected}
            <p class="mt-3 text-meta text-brick" role="alert">
              目标库剩余空间只有 {formatBytes(selected.freeBytes)}，可能不够。
            </p>
          {/if}
          {#if selected && !selected.writable}
            <p class="mt-3 text-meta text-brick" role="alert">该库目录当前不可写：{selected.spaceMessage}</p>
          {/if}

          {#if error}
            <p class="mt-3 text-ui text-brick" role="alert">{error}</p>
          {/if}

          <div class="mt-4 flex items-center justify-between gap-2">
            <button type="button" class="btn btn-ghost" onclick={() => navigate('/downloads')}>
              查看队列
            </button>
            <div class="flex gap-2">
              <button type="button" class="btn" onclick={onClose} disabled={submitting}>取消</button>
              <button
                type="button"
                class="btn btn-primary"
                onclick={() => void submit()}
                disabled={submitting || libraryId === null || (selected !== null && !selected.writable)}
              >
                {submitting ? '投递中…' : '开始下载'}
              </button>
            </div>
          </div>
        {/if}
      {/if}
    </div>
  </div>
{/if}
