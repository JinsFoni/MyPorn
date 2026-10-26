<script lang="ts">
  import { live } from '../lib/live.svelte';

  const dotClass: Record<string, string> = {
    info: 'bg-muted',
    success: 'bg-sage',
    error: 'bg-brick',
  };
</script>

<div
  class="pointer-events-none fixed inset-x-0 bottom-[calc(4.25rem+env(safe-area-inset-bottom))] z-50 flex flex-col items-center gap-2 px-3 lg:bottom-6"
  aria-live="polite"
  aria-atomic="false"
>
  {#each live.toasts as toast (toast.id)}
    <div
      class="pointer-events-auto flex w-full max-w-md items-start gap-2.5 rounded-xl bg-raise/95 px-4 py-3 text-ui text-fg backdrop-blur-xl"
      role={toast.kind === 'error' ? 'alert' : 'status'}
    >
      <span class="mt-[0.4rem] h-1.5 w-1.5 shrink-0 rounded-full {dotClass[toast.kind] ?? 'bg-muted'}"></span>
      <p class="min-w-0 flex-1">{toast.message}</p>
      <button
        type="button"
        class="-mr-1 -mt-0.5 rounded-full p-1 text-dim transition-colors hover:text-fg"
        aria-label="关闭提示"
        onclick={() => live.dismiss(toast.id)}
      >
        <svg viewBox="0 0 16 16" class="h-3.5 w-3.5" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
          <path d="m4 4 8 8M12 4l-8 8" />
        </svg>
      </button>
    </div>
  {/each}
</div>
