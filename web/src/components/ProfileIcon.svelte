<script lang="ts">
  interface Props {
    /** 资料行的标签文字（已经过 `displayProfile` 改过口径），用来挑字形 */
    label: string;
  }

  let { label }: Props = $props();

  /* 字形整套取自 https://github.com/JinsFoni/peach 的资料卡（它 `web/index.html` 里 `#i-*` 那套
     Lucide 图标），尺寸与描边也照抄它的资料表：16px、描边 2、圆头圆角。认标签里的关键词而不是
     整串相等——站点按艺人有增有减，将来冒出新说法也不至于秃；认不出的给一枚 `info`。
     顺序即优先级（先具体后笼统）。 */
  type IconKind =
    | 'heart'
    | 'venus-and-mars'
    | 'venus'
    | 'globe'
    | 'dumbbell'
    | 'footprints'
    | 'sparkles'
    | 'gem'
    | 'pencil'
    | 'notebook-pen'
    | 'info';

  function kindOf(label: string): IconKind {
    const s = label.toLowerCase();
    const has = (...keys: string[]) => keys.some((k) => s.includes(k));

    if (has('感情', '婚姻', '恋爱', '状态')) return 'heart';
    if (has('取向', '喜欢')) return 'venus-and-mars';
    if (has('性别', 'gender')) return 'venus';
    if (has('出身', '出生地', '来自', '籍贯', '地点', '城市', '国家')) return 'globe';
    if (has('体重', 'weight')) return 'dumbbell';
    if (has('种族', '民族', 'ethnic')) return 'footprints';
    if (has('发色', 'hair', '颜色', 'color', '眼睛', 'eye')) return 'sparkles';
    if (has('胸部', '假奶', '乳房', '穿环', 'pierc')) return 'gem';
    if (has('纹身', 'tattoo')) return 'pencil';
    if (has('爱好', '兴趣', 'hobb')) return 'notebook-pen';
    return 'info';
  }

  const kind = $derived(kindOf(label));
</script>

<svg
  viewBox="0 0 24 24"
  class="mr-1.5 inline-block h-4 w-4 shrink-0 align-[-0.2em]"
  aria-hidden="true"
  fill="none"
  stroke="currentColor"
  stroke-width="2"
  stroke-linecap="round"
  stroke-linejoin="round"
>
  {#if kind === 'heart'}
    <path
      d="M2 9.5a5.5 5.5 0 0 1 9.591-3.676.56.56 0 0 0 .818 0A5.49 5.49 0 0 1 22 9.5c0 2.29-1.5 4-3 5.5l-5.492 5.313a2 2 0 0 1-3 .019L5 15c-1.5-1.5-3-3.2-3-5.5"
    />
  {:else if kind === 'venus-and-mars'}
    <path d="M10 20h4" />
    <path d="M12 16v6" />
    <path d="M17 2h4v4" />
    <path d="m21 2-5.46 5.46" />
    <circle cx="12" cy="11" r="5" />
  {:else if kind === 'venus'}
    <path d="M12 15v7" />
    <path d="M9 19h6" />
    <circle cx="12" cy="9" r="6" />
  {:else if kind === 'globe'}
    <circle cx="12" cy="12" r="10" />
    <path d="M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20" />
    <path d="M2 12h20" />
  {:else if kind === 'dumbbell'}
    <path
      d="M17.596 12.768a2 2 0 1 0 2.829-2.829l-1.768-1.767a2 2 0 0 0 2.828-2.829l-2.828-2.828a2 2 0 0 0-2.829 2.828l-1.767-1.768a2 2 0 1 0-2.829 2.829z"
    />
    <path d="m2.5 21.5 1.4-1.4" />
    <path d="m20.1 3.9 1.4-1.4" />
    <path
      d="M5.343 21.485a2 2 0 1 0 2.829-2.828l1.767 1.768a2 2 0 1 0 2.829-2.829l-6.364-6.364a2 2 0 1 0-2.829 2.829l1.768 1.767a2 2 0 0 0-2.828 2.829z"
    />
    <path d="m9.6 14.4 4.8-4.8" />
  {:else if kind === 'footprints'}
    <path
      d="M4 16v-2.38C4 11.5 2.97 10.5 3 8c.03-2.72 1.49-6 4.5-6C9.37 2 10 3.8 10 5.5c0 3.11-2 5.66-2 8.68V16a2 2 0 1 1-4 0Z"
    />
    <path
      d="M20 20v-2.38c0-2.12 1.03-3.12 1-5.62-.03-2.72-1.49-6-4.5-6C14.63 6 14 7.8 14 9.5c0 3.11 2 5.66 2 8.68V20a2 2 0 1 0 4 0Z"
    />
    <path d="M16 17h4" />
    <path d="M4 13h4" />
  {:else if kind === 'sparkles'}
    <path
      d="M11.017 2.814a1 1 0 0 1 1.966 0l1.051 5.558a2 2 0 0 0 1.594 1.594l5.558 1.051a1 1 0 0 1 0 1.966l-5.558 1.051a2 2 0 0 0-1.594 1.594l-1.051 5.558a1 1 0 0 1-1.966 0l-1.051-5.558a2 2 0 0 0-1.594-1.594l-5.558-1.051a1 1 0 0 1 0-1.966l5.558-1.051a2 2 0 0 0 1.594-1.594z"
    />
    <path d="M20 2v4" />
    <path d="M22 4h-4" />
    <circle cx="4" cy="20" r="2" />
  {:else if kind === 'gem'}
    <path d="M10.5 3 8 9l4 13 4-13-2.5-6" />
    <path
      d="M17 3a2 2 0 0 1 1.6.8l3 4a2 2 0 0 1 .013 2.382l-7.99 10.986a2 2 0 0 1-3.247 0l-7.99-10.986A2 2 0 0 1 2.4 7.8l2.998-3.997A2 2 0 0 1 7 3z"
    />
    <path d="M2 9h20" />
  {:else if kind === 'pencil'}
    <path
      d="M21.174 6.812a1 1 0 0 0-3.986-3.987L3.842 16.174a2 2 0 0 0-.5.83l-1.321 4.352a.5.5 0 0 0 .623.622l4.353-1.32a2 2 0 0 0 .83-.497z"
    />
    <path d="m15 5 4 4" />
  {:else if kind === 'notebook-pen'}
    <path d="M13.4 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-7.4" />
    <path d="M2 6h4" />
    <path d="M2 10h4" />
    <path d="M2 14h4" />
    <path d="M2 18h4" />
    <path
      d="M21.378 5.626a1 1 0 1 0-3.004-3.004l-5.01 5.012a2 2 0 0 0-.506.854l-.837 2.87a.5.5 0 0 0 .62.62l2.87-.837a2 2 0 0 0 .854-.506z"
    />
  {:else}
    <circle cx="12" cy="12" r="10" />
    <path d="M12 16v-4" />
    <path d="M12 8h.01" />
  {/if}
</svg>
