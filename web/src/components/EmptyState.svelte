<script lang="ts">
  import type { Snippet } from 'svelte';

  interface Props {
    title: string;
    hint?: string;
    /** 空状态是"邀请动作"的地方，动作放进 actions snippet */
    actions?: Snippet;
    icon?: 'film' | 'user' | 'search' | 'error';
  }

  let { title, hint, actions, icon = 'film' }: Props = $props();
</script>

<div class="flex flex-col items-center justify-center px-6 py-16 text-center sm:py-24">
  <div class="flex h-14 w-14 items-center justify-center rounded-full bg-panel text-dim">
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" class="h-5 w-5" aria-hidden="true">
      {#if icon === 'film'}
        <rect x="3" y="5.5" width="18" height="13" rx="2" />
        <path d="M8 5.5v13M16 5.5v13M3 12h18" />
      {:else if icon === 'user'}
        <circle cx="12" cy="9" r="3.25" />
        <path d="M5.5 19.5a6.5 6.5 0 0 1 13 0" />
      {:else if icon === 'search'}
        <circle cx="10.75" cy="10.75" r="5.75" />
        <path d="m15.5 15.5 4 4" />
      {:else}
        <path d="M12 8v5" />
        <circle cx="12" cy="16.2" r="0.9" fill="currentColor" stroke="none" />
        <path d="M12 3.5 21 20H3z" />
      {/if}
    </svg>
  </div>
  <h2 class="mt-4 text-title font-medium text-fg">{title}</h2>
  {#if hint}
    <p class="mt-1.5 max-w-sm text-ui text-muted">{hint}</p>
  {/if}
  {#if actions}
    <div class="mt-5 flex flex-wrap items-center justify-center gap-2">
      {@render actions()}
    </div>
  {/if}
</div>
