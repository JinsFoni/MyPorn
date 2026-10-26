<script lang="ts">
  import { formatCount } from '../lib/format';

  /**
   * 一块「横向条形排行」：每行一个名字 + 一个数 + 一条占比进度。
   * 条形不描边、不用彩色，长度就是读数——颜色只用白色，宽度按本组最大值归一。
   */
  interface Row {
    name: string;
    value: number;
    /** 行下的小字（体积、占比说明等） */
    note?: string;
  }

  interface Props {
    title: string;
    rows: Row[];
    unit?: string;
    empty?: string;
  }

  let { title, rows, unit = '', empty = '还没有数据' }: Props = $props();

  // 归一化用本组的最大值；全是 0 时也别除以 0。
  const max = $derived(Math.max(1, ...rows.map((row) => row.value)));
</script>

<section class="panel flex flex-col p-4">
  <h3 class="text-ui font-medium text-fg">{title}</h3>
  {#if rows.length === 0}
    <p class="mt-3 text-meta text-dim">{empty}</p>
  {:else}
    <ul class="mt-3 flex flex-col gap-2.5">
      {#each rows as row (row.name)}
        <li>
          <div class="flex items-baseline justify-between gap-3">
            <span class="min-w-0 truncate text-meta text-muted" title={row.name}>{row.name}</span>
            <span class="shrink-0 text-meta tabular-nums text-fg/90">
              {formatCount(row.value)}<span class="ml-1 text-dim">{unit}</span>
            </span>
          </div>
          <div class="mt-1 h-1 w-full overflow-hidden rounded-full bg-raise" aria-hidden="true">
            <div
              class="h-full rounded-full bg-hi/80"
              style={`width:${((row.value / max) * 100).toFixed(1)}%`}
            ></div>
          </div>
          {#if row.note}
            <p class="mt-1 truncate text-meta text-dim">{row.note}</p>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</section>
