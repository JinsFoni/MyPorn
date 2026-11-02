<script lang="ts">
  import type { Snippet } from 'svelte';

  /**
   * 播放页标题下面那一行「谁发的 + 什么时候」。
   *
   * 样式照 YouTube 原站量出来的（2026-11-02，youtube.com/watch 的 `#owner`）：
   * 40px 圆头像，右边站两行——频道名 `500 16px/22px`，下面一行订阅数 `400 12px/18px` 灰字。
   * 我们下面那行放的是发布时间（顺手把「查一下发布时间」这类小按钮也塞进去）。
   *
   * 关注流 / B 站 / YouTube 三个入口都走这一个组件：以前 `/video/:id` 是「头像 20px（还没有）
   * + 名字 14px + · + 时间」一行灰字，`/youtube/:id` 是「20px 头像 + 14px 名字」，
   * 同一部片从哪个入口进来都长得不一样。
   */
  interface Props {
    /** 已经过代理的头像地址；没有就不画（不留空圆） */
    avatarUrl?: string | null;
    /** 第一行：名字（可以是多个链接拼起来的） */
    primary: Snippet;
    /** 第二行：时间、以及顺带放进去的小按钮 */
    secondary: Snippet;
  }

  let { avatarUrl = null, primary, secondary }: Props = $props();
</script>

<div class="mt-3 flex items-start gap-3">
  {#if avatarUrl}
    <img src={avatarUrl} alt="" class="h-10 w-10 shrink-0 rounded-full object-cover" />
  {/if}
  <div class="min-w-0 flex-1">
    <!-- 16px/22px —— 跟 YouTube 的频道名同号同重（--text-title 就是这个尺寸） -->
    <p class="flex flex-wrap items-center gap-x-2 gap-y-1 text-title font-medium text-fg">
      {@render primary()}
    </p>
    <!-- 12px/18px：YouTube 的订阅数那行就是 400 12px/18px，我们 --text-meta 的行高是 16px，
         这里单独钉 18 才和它同高 -->
    <p class="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-meta leading-[1.125rem] text-dim">
      {@render secondary()}
    </p>
  </div>
</div>
