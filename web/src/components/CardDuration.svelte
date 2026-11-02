<script lang="ts">
  /**
   * 卡片封面右下角的时长。
   *
   * 不带黑色药丸底（按用户要求去掉），只给一层文字阴影，亮封面上也读得清：1px 偏移的实阴影
   * 定轮廓、12px 的大晕铺底、3px 的近距晕补字缝。三条阴影的浓度是逐条减淡过的
   * （先 1/0.95/1，再减到 0.8/0.7/0.8），太黑会在浅色封面上糊出一块脏底。
   *
   * 三条墙（关注流 / B 站 / YouTube）和媒体库共用这一个组件：以前 YtCard 和 VideoCard 各抄了一份
   * `text-shadow`，一份 0.8/0.7/0.8、一份 1/0.95/1，位置也各在一角（右上被下载按钮占了，
   * 左上被未看小圆点占了），同一块屏幕上一换页时长的黑度和位置都跟着变。现在只有这一处。
   *
   * `pointer-events-none`：压着封面，别挡住卡片的点击。
   */
  import { formatDuration } from '../lib/format';

  interface Props {
    /** 0 或负（没抓到时长 / 直播）就整块不画，别显示 00:00 */
    seconds: number;
  }

  let { seconds }: Props = $props();
</script>

{#if seconds > 0}
  <span
    class="pointer-events-none absolute bottom-2 right-2 text-meta font-medium tabular-nums text-fg [text-shadow:0_1px_5px_rgba(0,0,0,0.8),0_0_12px_rgba(0,0,0,0.7),0_0_3px_rgba(0,0,0,0.8)]"
  >
    {formatDuration(seconds)}
  </span>
{/if}
