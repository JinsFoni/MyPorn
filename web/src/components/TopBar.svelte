<script lang="ts">
  interface Props {
    title: string;
  }

  let { title }: Props = $props();
</script>

<!-- 列表页的页头：不做粘性吸顶，跟着内容一起滚，让封面占满视野 -->
<div class="flex items-center pb-3 pt-2 lg:pb-5 lg:pt-3">
  <h1 class="min-w-0 flex-1 truncate text-display font-normal tracking-[-0.01em] text-fg lg:text-hero">
    {title}
  </h1>
</div>
