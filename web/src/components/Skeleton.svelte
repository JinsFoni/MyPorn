<script lang="ts">
  interface Props {
    /** 尺寸/形状工具类，例如 'aspect-video w-full' */
    class?: string;
    rounded?: string;
    pulse?: boolean;
  }

  let { class: cls = 'h-4 w-full', rounded = 'rounded-xl', pulse = true }: Props = $props();
</script>

<div class="{cls} {rounded} bg-panel {pulse ? 'animate-pulse' : ''}" aria-hidden="true"></div>
