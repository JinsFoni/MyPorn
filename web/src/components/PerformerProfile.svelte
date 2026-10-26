<script lang="ts">
  import type { ProfileField } from '../lib/types';

  interface Props {
    /** 资料行（性别、身高、出身地……），顺序就是平台页面上的顺序 */
    profile: ProfileField[];
  }

  let { profile }: Props = $props();
</script>

<!-- 资料行：页头右侧那一块（窄屏时跨整行掉到下面，见 Performer.svelte 里的 grid）。
     列数按这一栏自己的宽度算，不看窗口——`xl` 分栏后它只剩 ~500px，窄屏跨整行时又有 700px 以上，
     同一份代码两边都得排得好看。 -->
{#if profile.length}
  <!-- @container 必须套在外层：容器查询只看祖先容器，写在自己的 dl 上永远匹配不到。 -->
  <div class="@container">
    <dl class="grid grid-cols-1 gap-x-6 gap-y-2 @sm:grid-cols-2 @3xl:grid-cols-3">
      {#each profile as field (field.label)}
        <div class="grid grid-cols-[5rem_minmax(0,1fr)] items-baseline gap-2">
          <dt class="text-ui text-dim">{field.label}</dt>
          <dd class="min-w-0 break-words text-ui text-fg">{field.value}</dd>
        </div>
      {/each}
    </dl>
  </div>
{/if}
