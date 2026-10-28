<script lang="ts">
  import type { Snippet } from 'svelte';

  interface Props {
    title: string;
    /** 标题右侧的动作按钮（页内状态的东西：未读总数、弹窗开关…） */
    actions?: Snippet;
  }

  let { title, actions }: Props = $props();
</script>

<!-- 列表页的页头：不做粘性吸顶，跟着内容一起滚，让封面占满视野。
     右侧留一个动作位：那些按钮的状态（未读总数、弹窗开关）住在页面里，
     所以由页面把 snippet 传进来，而不是让这里去猜。 -->
<div class="flex items-center gap-3 pb-3 pt-2 lg:pb-5 lg:pt-3">
  <h1 class="min-w-0 flex-1 truncate text-display font-normal tracking-[-0.01em] text-fg lg:text-hero">
    {title}
  </h1>
  {#if actions}
    <div class="flex shrink-0 items-center gap-2">
      {@render actions()}
    </div>
  {/if}
</div>
