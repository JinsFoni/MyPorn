<script lang="ts">
  /**
   * 全局确认框：`confirmDialog.ask()` 的渲染端，挂在 `AppShell` 里（和 `Toasts` 并列）。
   *
   * 样式照抄页面里已有的弹窗（「添加博主」那一个）：`rounded-2xl bg-panel` 面板。
   * 桌面端居中、手机上贴底（`items-end sm:items-center`），遮罩是 `bg-ink/70` + 模糊，
   * 目的是让确认框看起来像页面自己的一部分，而不是浏览器弹出来的一个白框。
   */
  import { confirmDialog } from '../lib/confirm.svelte';

  let confirmBtn = $state<HTMLButtonElement | null>(null);

  const options = $derived(confirmDialog.options);
  const danger = $derived((options.tone ?? 'danger') === 'danger');

  // 打开即把焦点放到确认按钮上：键盘回车直接过（原生 confirm 也是聚焦默认按钮）。
  $effect(() => {
    if (confirmDialog.open) confirmBtn?.focus();
  });

  // Esc = 取消。挂在 window 上而不是容器上：点过遮罩之后焦点可能跑到 body，
  // 挂在容器上就按不动了；用捕获阶段还顺手压住底层页面的快捷键（比如灯箱的 Esc）。
  $effect(() => {
    if (!confirmDialog.open) return;
    const onKey = (event: KeyboardEvent): void => {
      if (event.key === 'Escape') {
        event.preventDefault();
        event.stopPropagation();
        confirmDialog.settle(false);
      }
    };
    window.addEventListener('keydown', onKey, true);
    return () => window.removeEventListener('keydown', onKey, true);
  });
</script>

{#if confirmDialog.open}
  <div
    class="fixed inset-0 z-[60] flex items-end justify-center p-3 sm:items-center"
    role="dialog"
    aria-modal="true"
    aria-labelledby="confirm-title"
    tabindex="-1"
  >
    <!-- 遮罩本身做成 button：点它就取消。给 div 挂 click 要再补一套键盘事件才不算
         a11y 问题，那就等于把「点空白处关闭」重写成按钮，不如直接用按钮
         （`tabindex="-1"` 让它不占 Tab 位，键盘用户走 Esc 和下面两颗按钮）。 -->
    <button
      type="button"
      tabindex="-1"
      aria-label="取消"
      class="absolute inset-0 cursor-default bg-ink/70 backdrop-blur-sm"
      onclick={() => confirmDialog.settle(false)}
    ></button>

    <div class="relative w-full max-w-sm rounded-2xl bg-panel p-4 shadow-xl">
      <h2 id="confirm-title" class="text-title font-medium text-fg">{options.title}</h2>
      {#if options.body}
        <p class="mt-1.5 whitespace-pre-line text-ui text-muted">{options.body}</p>
      {/if}

      <div class="mt-4 flex items-center justify-end gap-2">
        <button
          type="button"
          onclick={() => confirmDialog.settle(false)}
          class="rounded-full px-3 py-1.5 text-ui text-dim transition-colors hover:bg-raise hover:text-fg"
        >
          {options.cancelText ?? '取消'}
        </button>
        <button
          type="button"
          bind:this={confirmBtn}
          onclick={() => confirmDialog.settle(true)}
          class="rounded-full px-3.5 py-1.5 text-ui font-medium transition-opacity hover:opacity-90 {danger
            ? 'bg-brick text-white'
            : 'bg-hi text-ink'}"
        >
          {options.confirmText ?? '删除'}
        </button>
      </div>
    </div>
  </div>
{/if}
