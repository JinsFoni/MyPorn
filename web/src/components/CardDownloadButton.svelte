<script lang="ts">
  /**
   * 卡片封面右上角那颗「下载到本地库」按钮（悬停/聚焦才出现）。
   *
   * 三条墙共用：以前 YouTube 卡是 `bg-ink/70 + p-1.5`（28×28）、影片卡是 `h-7 w-7 + bg-ink/80`，
   * 图标一个 16px 一个 14px，描边也差 0.1——同一块屏幕上一换页按钮就换了个样子。
   *
   * 常显断点取 `max-lg`（≤1023px 一直可见）而不是模板里的 `max-sm`：平板（768~1023px）
   * 没有 hover，`max-sm` 那版在 iPad 尺寸上 opacity 一直是 0，按钮根本点不到。
   *
   * 按钮挂在 `<article>` 上（不是挂在卡片链接里面）——所以自带 `z-10`，别被链接内容盖住。
   */
  interface Props {
    /** 读屏用：「下载 xxx」 */
    label: string;
    /** 悬停提示：下载到本地库 / 已在本地库，再下一次 */
    title: string;
    /** 正在下载：图标换成转圈并保持在原位（此时点不动） */
    downloading?: boolean;
    onclick: (event: MouseEvent) => void;
  }

  let { label, title, downloading = false, onclick }: Props = $props();
</script>

<button
  type="button"
  class="absolute right-2 top-2 z-10 rounded-full bg-ink/70 p-1.5 text-fg opacity-0 transition-opacity
    hover:bg-ink focus-visible:opacity-100 group-hover:opacity-100 group-focus-within:opacity-100
    max-lg:opacity-100 {downloading ? 'pointer-events-none opacity-100' : ''}"
  aria-label={label}
  {title}
  {onclick}
>
  {#if downloading}
    <svg viewBox="0 0 24 24" class="h-4 w-4 animate-spin" fill="none" stroke="currentColor" stroke-width="2">
      <circle cx="12" cy="12" r="8" stroke-opacity="0.25" />
      <path d="M20 12a8 8 0 0 0-8-8" />
    </svg>
  {:else}
    <svg viewBox="0 0 24 24" class="h-4 w-4" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round">
      <path d="M12 4v10" />
      <path d="m8 10.5 4 4 4-4" />
      <path d="M4.5 18.5h15" />
    </svg>
  {/if}
</button>
