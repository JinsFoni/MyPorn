<script lang="ts">
  /** 管理页顶部的一格读数：一个名字、一个大数、一句它的单位/脚注。 */
  interface Props {
    label: string;
    value: string;
    unit?: string;
    /** 第二行小字：说清这个大数是怎么来的（启用 x/y、覆盖 m 部…） */
    hint?: string;
    /** warn：读数本身是「不健康」的数量（检查失败），为 0 时前端不传这个色调 */
    tone?: 'default' | 'warn';
  }

  let { label, value, unit = '', hint = '', tone = 'default' }: Props = $props();
</script>

<div class="panel flex flex-col gap-0.5 px-3 py-2.5">
  <span class="text-meta text-dim">{label}</span>
  <p class="flex items-baseline gap-1">
    <b class="text-display font-normal tabular-nums {tone === 'warn' ? 'text-brick' : 'text-fg'}">{value}</b>
    {#if unit}<span class="text-meta text-dim">{unit}</span>{/if}
  </p>
  {#if hint}
    <span class="truncate text-meta text-dim" title={hint}>{hint}</span>
  {/if}
</div>
