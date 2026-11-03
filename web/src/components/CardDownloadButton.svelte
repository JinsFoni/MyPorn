<script lang="ts">
  /**
   * 卡片**封面左下角**那颗动作按钮（悬停/聚焦才出现）：三条墙是「下载到本地库」，
   * 媒体库是「复制文件路径」（已经下到盘上了，再下一遍没有意义）。
   *
   * 2026-11-04 用户要求「视频卡片鼠标悬浮时的下载按钮放在左下角」，从右上角搬过来。
   *
   * **按钮外面套一层「封面形状」的壳**：卡片 `<article>` 比封面高（下面还有头像 / 标题 /
   * 副行），按钮要是直接挂在 article 上写 `bottom-2`，它掉到副行上去了。壳用
   * `absolute inset-x-0 top-0 aspect-video`（与封面同宽同高）+ `pointer-events-none`
   * 让点击穿过去，按钮自己再 `pointer-events-auto` 收回来。
   *
   * 左下角原本有两只角标因此让位到 `left-10`（40px）：YouTube 卡的「直播」、媒体库卡的
   * 「文件已不在磁盘上」——它们和这颗按钮同时存在，不让位就叠在一起（≤1023px 触屏上
   * 按钮是常显的，遮挡是实打实的）。**右下角是时长，不动。**
   *
   * 三条墙共用：以前 YouTube 卡是 `bg-ink/70 + p-1.5`（28×28）、影片卡是 `h-7 w-7 + bg-ink/80`，
   * 图标一个 16px 一个 14px，描边也差 0.1——同一块屏幕上一换页按钮就换了个样子。
   *
   * 常显断点取 `max-lg`（≤1023px 一直可见）而不是模板里的 `max-sm`：平板（768~1023px）
   * 没有 hover，`max-sm` 那版在 iPad 尺寸上 opacity 一直是 0，按钮根本点不到。
   *
   * 按钮挂在 `<article>` 上（不是挂在卡片链接里面，免得 `<a>` 里套 `<button>`）——所以自带 `z-10`，
   * 别被链接内容盖住。
   */
  interface Props {
    /** 读屏用：「下载 xxx」 */
    label: string;
    /** 悬停提示：下载到本地库 / 已在本地库，再下一次 */
    title: string;
    /** 正在下载：图标换成转圈并保持在原位（此时点不动） */
    downloading?: boolean;
    /**
     * 画哪个图标。位置、底、悬停时机一律不动——媒体库那只「复制路径」要是自己写一份
     * `bg-ink/70 p-1.5`，两个页面上的按钮迟早会差一档黑度。
     */
    icon?: 'download' | 'copy' | 'check';
    onclick: (event: MouseEvent) => void;
  }

  let { label, title, downloading = false, icon = 'download', onclick }: Props = $props();
</script>

<!-- 壳：铺成封面那块 16:9，让按钮的 `bottom-2` 落在封面底边（见文件头注释） -->
<div class="pointer-events-none absolute inset-x-0 top-0 aspect-video">
  <button
    type="button"
    class="absolute bottom-2 left-2 z-10 rounded-full bg-ink/70 p-1.5 text-fg opacity-0 transition-opacity
      hover:bg-ink focus-visible:opacity-100 group-hover:opacity-100 group-focus-within:opacity-100
      max-lg:opacity-100 {downloading ? 'pointer-events-none opacity-100' : 'pointer-events-auto'}"
    aria-label={label}
    {title}
    {onclick}
  >
    {#if downloading}
      <svg viewBox="0 0 24 24" class="h-4 w-4 animate-spin" fill="none" stroke="currentColor" stroke-width="2">
        <circle cx="12" cy="12" r="8" stroke-opacity="0.25" />
        <path d="M20 12a8 8 0 0 0-8-8" />
      </svg>
    {:else if icon === 'copy'}
      <svg viewBox="0 0 24 24" class="h-4 w-4" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">
        <rect x="9" y="9" width="11" height="11" rx="2.5" />
        <path d="M15 5.5A2.5 2.5 0 0 0 12.5 3H6.5A3.5 3.5 0 0 0 3 6.5v6A2.5 2.5 0 0 0 5.5 15" />
      </svg>
    {:else if icon === 'check'}
      <svg viewBox="0 0 24 24" class="h-4 w-4" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round">
        <path d="m5 12.5 4.5 4.5L19 7.5" />
      </svg>
    {:else}
      <svg viewBox="0 0 24 24" class="h-4 w-4" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round">
        <path d="M12 4v10" />
        <path d="m8 10.5 4 4 4-4" />
        <path d="M4.5 18.5h15" />
      </svg>
    {/if}
  </button>
</div>
