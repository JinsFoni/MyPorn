<script lang="ts">
  import ProfileIcon from './ProfileIcon.svelte';
  import { displayProfile } from '../lib/profile';
  import type { ProfileField } from '../lib/types';

  interface Props {
    /** 资料行（性别、身高、出身地……），顺序就是平台页面上的顺序 */
    profile: ProfileField[];
  }

  let { profile }: Props = $props();

  /** 站上原文 → 页面口径（标签改名、是/否本地化、体重只留公斤），见 lib/profile.ts */
  const rows = $derived(displayProfile(profile));

  /** 按列铺开：先算一列能放几行，再让 grid 竖着填满一列才换列。
   *  这一块最多两列（`@sm` 起才分列），所以一列的行数＝总数除二向上取整。 */
  const rowsPerColumn = $derived(Math.ceil(rows.length / 2));
</script>

<!-- 资料行：页头右侧那一块（窄屏时跨整行掉到下面，见 Performer.svelte 里的 grid）。
     列数按这一栏自己的宽度算，不看窗口——`xl` 分栏后它只剩 ~500px，窄屏跨整行时又有 700px 以上，
     同一份代码两边都得排得好看。 -->
{#if rows.length}
  <!-- @container 必须套在外层：容器查询只看祖先容器，写在自己的 dl 上永远匹配不到。 -->
  <div class="@container">
    <!-- 按列读：`grid-flow-col` + `grid-template-rows` 让前 N 项竖着填满第一列再换列，
         顺序才是「往下数」而不是一行一行横着数。窄到一列时退回默认的行优先，跟站点顺序一致。 -->
    <dl
      class="grid grid-cols-1 gap-x-6 gap-y-2 @sm:auto-cols-fr @sm:grid-flow-col @sm:grid-cols-none @sm:grid-rows-[repeat(var(--rows),auto)]"
      style="--rows: {rowsPerColumn}"
    >
      {#each rows as field (field.label)}
        <!-- 标签前挂一个 16px 字形（见 ProfileIcon），标签列因此留 5rem：
             图标 16 + 间距 6 + 最长的「出身地」42 ≈ 64px，四个字的标签也放得下。 -->
        <div class="grid grid-cols-[5rem_minmax(0,1fr)] items-baseline gap-2">
          <dt class="text-ui text-dim"><ProfileIcon label={field.label} />{field.label}</dt>
          <dd class="min-w-0 break-words text-ui text-fg">{field.value}</dd>
        </div>
      {/each}
    </dl>
  </div>
{/if}
