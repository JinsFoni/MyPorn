<script lang="ts">
  import { imageUrl } from '../lib/img';
  import { formatCount, relativeTime } from '../lib/format';
  import type { Performer } from '../lib/types';

  interface Props {
    performer: Performer;
    eager?: boolean;
  }

  let { performer, eager = false }: Props = $props();

  const avatar = $derived(imageUrl(performer.avatarUrl, 320));
  let avatarFailed = $state(false);
  const monogram = $derived(performer.name.trim().charAt(0).toUpperCase());

  type SyncTone = 'error' | 'paused' | 'idle' | 'ok';

  const sync = $derived.by((): { tone: SyncTone; label: string; detail: string | undefined } => {
    if (!performer.enabled) return { tone: 'paused', label: '已暂停', detail: '暂停后不再自动同步' };
    if (performer.lastError) return { tone: 'error', label: '同步出错', detail: performer.lastError };
    if (!performer.lastSyncAt) return { tone: 'idle', label: '等待首次同步', detail: undefined };
    return { tone: 'ok', label: `${relativeTime(performer.lastSyncAt)}同步`, detail: undefined };
  });

  const dotTone: Record<SyncTone, string> = {
    error: 'bg-brick',
    paused: 'bg-dim',
    idle: 'bg-hi',
    ok: 'bg-sage',
  };
</script>

<a
  href={`/performer/${String(performer.id)}`}
  data-link
  class="group flex min-w-0 flex-col text-center"
  aria-label={`${performer.name}${performer.newVideoCount > 0 ? `，${String(performer.newVideoCount)} 部新片` : ''}`}
>
  <!-- 头像宽度由栅格轨道固定（88/96px），所以左边缘和页面标题对齐；间距只由 gap 决定 -->
  <div class="aspect-square w-full">
    <div class="h-full w-full overflow-hidden rounded-full bg-panel">
      {#if avatar && !avatarFailed}
        <img
          src={avatar}
          onerror={() => (avatarFailed = true)}
          alt=""
          width="320"
          height="320"
          loading={eager ? 'eager' : 'lazy'}
          decoding="async"
          class="h-full w-full object-cover opacity-90 transition duration-300 group-hover:opacity-100"
        />
      {:else}
        <div class="flex h-full w-full items-center justify-center text-dim">
          {#if monogram}
            <span class="text-hero font-normal text-dim">{monogram}</span>
          {:else}
            <svg viewBox="0 0 24 24" class="h-7 w-7" fill="none" stroke="currentColor" stroke-width="1.4" aria-hidden="true">
              <circle cx="12" cy="9" r="3.25" />
              <path d="M5.5 19.5a6.5 6.5 0 0 1 13 0" />
            </svg>
          {/if}
        </div>
      {/if}
    </div>

  </div>

  <!-- 名字最多两行（格子只有 96px 宽，单行会被截得只剩半句）；状态行用 mt-auto 贴底，同一行卡片仍对齐 -->
  <h3 class="mt-2.5 line-clamp-2 text-ui leading-snug text-fg/95 transition-colors group-hover:text-fg">{performer.name}</h3>
  <p class="mt-auto flex items-center justify-center gap-1.5 pt-1 text-meta text-dim">
    <span
      class="h-1.5 w-1.5 shrink-0 rounded-full {dotTone[sync.tone]}"
      role="img"
      aria-label={sync.label}
      title={sync.detail ?? sync.label}
    ></span>
    <span class="truncate">{sync.label}</span>
    {#if performer.subscriberCount !== null}
      <span class="shrink-0 tabular-nums" aria-hidden="true">·</span>
      <span class="shrink-0 tabular-nums">{formatCount(performer.subscriberCount)}</span>
    {/if}
  </p>
</a>
