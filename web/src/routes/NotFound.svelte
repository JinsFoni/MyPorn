<script lang="ts">
  import EmptyState from '../components/EmptyState.svelte';
  import { router } from '../lib/router.svelte';
</script>

<EmptyState
  title="这个页面不存在"
  hint={`没有找到 ${router.path}，可能链接写错了。`}
  icon="search"
>
  {#snippet actions()}
    <a data-link href="/" class="btn btn-primary">回到关注</a>
  {/snippet}
</EmptyState>
