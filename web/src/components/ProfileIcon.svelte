<script lang="ts">
  import { BODY_GLYPH_PATH, BODY_GLYPH_VIEWBOX } from '../lib/bodyGlyph';

  interface Props {
    /** 资料行的标签文字（「出身地」「体重」……），用来挑字形 */
    label: string;
  }

  let { label }: Props = $props();

  /* 只做单色字形，跟侧边栏 / 社交链接同一套：24 网格、1.6 描边、14px。
     认标签里的关键词而不是整串相等——站点是中文站，但字段按艺人有增有减，
     还可能冒出英文标签，所以用 includes 兜住。顺序即优先级（先具体后笼统）。
     例外一：`身材` 用的是用户给的 iconfont 剪影（1024 网格、填充），见 `$lib/bodyGlyph`。
     例外二：`体重`（哑铃）和 `种族`（脚印）两枚取自 https://github.com/JinsFoni/peach
     的资料卡（它 `web/index.html` 的 `#i-dumbbell` / `#i-footprints`），其余仍是本项目自己那套。 */
  type IconKind =
    | 'heart'
    | 'users'
    | 'user'
    | 'pin'
    | 'height'
    | 'weight'
    | 'ruler'
    | 'body'
    | 'globe'
    | 'footprints'
    | 'eye'
    | 'drop'
    | 'tag'
    | 'pen'
    | 'ring'
    | 'star'
    | 'calendar'
    | 'dot';

  function kindOf(label: string): IconKind {
    const s = label.toLowerCase();
    const has = (...keys: string[]) => keys.some((k) => s.includes(k));

    if (has('感情', '婚姻', '恋爱', '状态')) return 'heart';
    if (has('喜欢', '取向')) return 'users';
    if (has('性别', 'gender')) return 'user';
    if (has('出身', '出生地', '来自', '籍贯', '地点', '城市', '国家')) return 'pin';
    if (has('身高', 'height')) return 'height';
    if (has('体重', 'weight')) return 'weight';
    if (has('身材', 'body')) return 'body';
    if (has('三围', '罩杯', 'measure')) return 'ruler';
    if (has('种族', 'ethnic')) return 'footprints';
    if (has('民族', '语言', '国籍')) return 'globe';
    if (has('眼睛', 'eye')) return 'eye';
    if (has('发色', 'hair') || has('颜色', 'color')) return 'drop';
    if (has('纹身', 'tattoo')) return 'pen';
    if (has('穿环', 'pierc')) return 'ring';
    if (has('胸部', '假奶', '乳房', '手术', '其他特征')) return 'tag';
    if (has('兴趣', '爱好', 'hobb')) return 'star';
    if (has('年份', '出道', '活跃', '生日', '年龄', 'age', 'year')) return 'calendar';
    return 'dot';
  }

  /* 尺寸：常见字形是 14×14 的方框；`身材` 那枚是 1 : 3.4 的站姿剪影，框跟着收成 4×14，
     这样右边标签只被推开 8px（方框要推 20px），左栏那几行看起来还是齐的。 */
  const kind = $derived(kindOf(label));
</script>

<svg
  viewBox={kind === 'body' ? BODY_GLYPH_VIEWBOX : '0 0 24 24'}
  class={kind === 'body'
    ? 'mr-1 inline-block h-3.5 w-1 shrink-0 align-[-0.15em]'
    : 'mr-1.5 inline-block h-3.5 w-3.5 shrink-0 align-[-0.15em]'}
  aria-hidden="true"
  fill="none"
  stroke="currentColor"
  stroke-width={kind === 'body' ? 0 : 1.6}
  stroke-linecap="round"
  stroke-linejoin="round"
>
  {#if kind === 'body'}
    <!-- 唯一的填充字形：1024 网格、没有描边，靠 fill 上色 -->
    <path d={BODY_GLYPH_PATH} fill="currentColor" stroke="none" />
  {:else if kind === 'heart'}
    <path
      d="M20.84 4.61a5.5 5.5 0 0 0-7.78 0L12 5.67l-1.06-1.06a5.5 5.5 0 1 0-7.78 7.78l1.06 1.06L12 21.23l7.78-7.78 1.06-1.06a5.5 5.5 0 0 0 0-7.78z"
    />
  {:else if kind === 'users'}
    <circle cx="9.5" cy="7" r="4" />
    <path d="M2.5 21v-2a4 4 0 0 1 4-4h6a4 4 0 0 1 4 4v2" />
    <path d="M17 3.4a4 4 0 0 1 0 7.2" />
    <path d="M21.5 21v-2a4 4 0 0 0-3-3.6" />
  {:else if kind === 'user'}
    <circle cx="12" cy="8" r="3.6" />
    <path d="M5 20.5c0-3.6 3.1-6 7-6s7 2.4 7 6" />
  {:else if kind === 'pin'}
    <path d="M20 10c0 5.4-8 12-8 12s-8-6.6-8-12a8 8 0 0 1 16 0Z" />
    <circle cx="12" cy="10" r="2.8" />
  {:else if kind === 'height'}
    <path d="M12 3.5v17" />
    <path d="M8.5 7 12 3.5 15.5 7" />
    <path d="M8.5 17 12 20.5 15.5 17" />
  {:else if kind === 'weight'}
    <path
      d="M17.596 12.768a2 2 0 1 0 2.829-2.829l-1.768-1.767a2 2 0 0 0 2.828-2.829l-2.828-2.828a2 2 0 0 0-2.829 2.828l-1.767-1.768a2 2 0 1 0-2.829 2.829z"
    />
    <path d="m2.5 21.5 1.4-1.4" />
    <path d="m20.1 3.9 1.4-1.4" />
    <path
      d="M5.343 21.485a2 2 0 1 0 2.829-2.828l1.767 1.768a2 2 0 1 0 2.829-2.829l-6.364-6.364a2 2 0 1 0-2.829 2.829l1.768 1.767a2 2 0 0 0-2.828 2.829z"
    />
    <path d="m9.6 14.4 4.8-4.8" />
  {:else if kind === 'ruler'}
    <rect x="2.5" y="8" width="19" height="8" rx="2" />
    <path d="M7 8v3" />
    <path d="M11 8v4" />
    <path d="M15 8v3" />
    <path d="M19 8v4" />
  {:else if kind === 'globe'}
    <circle cx="12" cy="12" r="8.5" />
    <path d="M3.5 12h17" />
    <path d="M12 3.5c2.3 2.4 3.5 5.3 3.5 8.5S14.3 18.1 12 20.5c-2.3-2.4-3.5-5.3-3.5-8.5S9.7 5.9 12 3.5Z" />
  {:else if kind === 'footprints'}
    <path
      d="M4 16v-2.38C4 11.5 2.97 10.5 3 8c.03-2.72 1.49-6 4.5-6C9.37 2 10 3.8 10 5.5c0 3.11-2 5.66-2 8.68V16a2 2 0 1 1-4 0Z"
    />
    <path
      d="M20 20v-2.38c0-2.12 1.03-3.12 1-5.62-.03-2.72-1.49-6-4.5-6C14.63 6 14 7.8 14 9.5c0 3.11 2 5.66 2 8.68V20a2 2 0 1 0 4 0Z"
    />
    <path d="M16 17h4" />
    <path d="M4 13h4" />
  {:else if kind === 'eye'}
    <path d="M2.8 12S6.4 5.8 12 5.8 21.2 12 21.2 12 17.6 18.2 12 18.2 2.8 12 2.8 12Z" />
    <circle cx="12" cy="12" r="2.8" />
  {:else if kind === 'drop'}
    <path d="M12 3.5c3.3 3.9 5.3 6.4 5.3 8.7a5.3 5.3 0 0 1-10.6 0c0-2.3 2-4.8 5.3-8.7Z" />
  {:else if kind === 'tag'}
    <path d="M20.5 12.8 12.8 20.5a2.1 2.1 0 0 1-3 0L3.5 14.2V3.5h10.7l6.3 6.3a2.1 2.1 0 0 1 0 3Z" />
    <circle cx="8" cy="8" r="1.4" />
  {:else if kind === 'pen'}
    <path d="M16.6 3.4a2.9 2.9 0 0 1 4 4L8 20.1l-5.5 1.5 1.5-5.5L16.6 3.4Z" />
    <path d="M15 5l4 4" />
  {:else if kind === 'ring'}
    <circle cx="12" cy="12" r="7.2" />
    <circle cx="12" cy="12" r="2.2" />
  {:else if kind === 'star'}
    <path d="M12 3.8l2.5 5.1 5.6.8-4 4 .9 5.6-5-2.7-5 2.7.9-5.6-4-4 5.6-.8z" />
  {:else if kind === 'calendar'}
    <rect x="3.5" y="5" width="17" height="15.5" rx="3.2" />
    <path d="M8 3v4" />
    <path d="M16 3v4" />
    <path d="M3.5 10.2h17" />
  {:else}
    <circle cx="12" cy="12" r="3" />
  {/if}
</svg>
