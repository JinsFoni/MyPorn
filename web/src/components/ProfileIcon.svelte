<script lang="ts">
  import { PROFILE_GLYPHS } from '../lib/profileGlyphs';

  interface Props {
    /** 资料行的标签文字（「出身地」「体重」……），用来挑字形 */
    label: string;
    /** 这一行的值。只有「性别」用它——同一个标签下 ♀ / ♂ 得分开画 */
    value?: string;
  }

  let { label, value = '' }: Props = $props();

  /* 只做单色字形，跟侧边栏 / 社交链接同一套：24 网格、2 描边、16px（跟 `mr-1.5` 的 6px 间距，
     跟 https://github.com/JinsFoni/peach 的资料卡同一口径——那边就是 16px / stroke 2）。
     认标签里的关键词而不是整串相等——站点是中文站，但字段按艺人有增有减，
     还可能冒出英文标签，所以用 includes 兜住。顺序即优先级（先具体后笼统）。
     例外一：`身材`、`发色`、`感情`、`种族`、`纹身` 五枚是用户给的 iconfont 图标——1024 网格、
     填充而非描边，那几段上万字符的 `d` 住在 `$lib/profileGlyphs`，走下面 `glyph` 那一支渲染。
     例外二：`胸部`（胸罩）和 `穿环`（带珠的环）是照用户给的 PNG 重画的线稿——原图是位图，
     放大后逐行量出骨点，再按本项目的 2 描边落成矢量。
     例外三：`体重`（哑铃）、`性别` 的 ♂ 以及页头的 `排名`（皇冠）取自 peach 的雪碧图
     （`web/index.html` 的 `#i-dumbbell` / `#i-mars` / `#i-crown`）；页头的 `影片` 是照用户给的
     24px PNG 描的（清单条 + 带播放三角的画面）。 */
  type IconKind =
    | 'heart'
    | 'users'
    | 'female'
    | 'male'
    | 'pin'
    | 'height'
    | 'weight'
    | 'ruler'
    | 'body'
    | 'bra'
    | 'globe'
    | 'ethnicity'
    | 'eye'
    | 'hair'
    | 'drop'
    | 'tattoo'
    | 'tag'
    | 'piercing'
    | 'star'
    | 'calendar'
    | 'film'
    | 'rank'
    | 'dot';

  /** 哪几枚走「填充字形」（`$lib/profileGlyphs` 里那几枚 iconfont 图标）。 */
  const FILL_GLYPHS: Partial<Record<IconKind, keyof typeof PROFILE_GLYPHS>> = {
    body: 'body',
    hair: 'hair',
    heart: 'heart',
    ethnicity: 'ethnicity',
    tattoo: 'tattoo',
  };

  function kindOf(label: string, value: string): IconKind {
    const s = label.toLowerCase();
    const has = (...keys: string[]) => keys.some((k) => s.includes(k));

    if (has('感情', '婚姻', '恋爱', '状态')) return 'heart';
    if (has('喜欢', '取向')) return 'users';
    // 性别按值分：女 → ♀（用户给的那枚）、男 → ♂（peach 的 `i-mars`），认不出时仍按 ♀。
    if (has('性别', 'gender')) return /男|male/i.test(value) && !/女|female/i.test(value) ? 'male' : 'female';
    if (has('出身', '出生地', '来自', '籍贯', '地点', '城市', '国家')) return 'pin';
    if (has('身高', 'height')) return 'height';
    if (has('体重', 'weight')) return 'weight';
    if (has('身材', 'body')) return 'body';
    if (has('三围', '罩杯', 'measure')) return 'ruler';
    if (has('种族', 'ethnic')) return 'ethnicity';
    if (has('民族', '语言', '国籍')) return 'globe';
    if (has('眼睛', 'eye')) return 'eye';
    if (has('发色', '头发', 'hair')) return 'hair';
    if (has('颜色', 'color')) return 'drop';
    if (has('纹身', 'tattoo')) return 'tattoo';
    if (has('穿环', 'pierc')) return 'piercing';
    if (has('胸部', '假奶', '乳房', 'bra', 'bust')) return 'bra';
    if (has('手术', '其他特征')) return 'tag';
    if (has('兴趣', '爱好', 'hobb')) return 'star';
    if (has('影片', '作品数', 'film')) return 'film';
    if (has('排名', 'rank')) return 'rank';
    if (has('年份', '出道', '活跃', '生日', '年龄', 'age', 'year')) return 'calendar';
    return 'dot';
  }

  /* 尺寸：一律 16×16 方框。填充字形按各自的紧框等比缩进去（`$lib/profileGlyphs` 里注了
     每一枚画成多大：躯干 10.9×16、发色 15.3×16、感情 16×13……），线稿自己撑满 16。 */
  const kind = $derived(kindOf(label, value));
  const glyph = $derived(FILL_GLYPHS[kind] ? PROFILE_GLYPHS[FILL_GLYPHS[kind]!] : null);
</script>

<svg
  viewBox={glyph ? glyph.viewBox : '0 0 24 24'}
  class="mr-1.5 inline-block h-4 w-4 shrink-0 align-[-0.15em]"
  aria-hidden="true"
  fill="none"
  stroke="currentColor"
  stroke-width={glyph ? 0 : 2}
  stroke-linecap="round"
  stroke-linejoin="round"
>
  {#if glyph}
    <!-- 填充字形（1024 网格、没有描边）：靠 fill 上色，颜色跟着标签走 -->
    {#each glyph.paths as d}
      <path {d} fill="currentColor" stroke="none" />
    {/each}
  {:else if kind === 'bra'}
    <!-- 胸罩：照用户给的 24px PNG 描的（两条肩带 + 两个罩杯 + 横着的下围）。罩杯是开口的
         ——下沿直接落在下围那条线上，内侧两条边在中间合成 V（中间那道「山丘」），
         所以这五段共用一套描边参数，画出来才跟原图一样细。 -->
    <path d="M3.2 2.9 Q4.5 6.2 4.4 9.6" />
    <path d="M20.8 2.9 Q19.5 6.2 19.6 9.6" />
    <path d="M3.8 18.8 Q2.6 18.8 2.55 15.8 V11.3 Q2.55 10.5 3.2 10.5 H8.2 Q8.9 10.5 9.2 11 L12 15.3" />
    <path d="M20.2 18.8 Q21.4 18.8 21.45 15.8 V11.3 Q21.45 10.5 20.8 10.5 H15.8 Q15.1 10.5 14.8 11 L12 15.3" />
    <path d="M3.8 18.8 H20.2" />
  {:else if kind === 'piercing'}
    <!-- 穿环：一个大环，下沿穿着一颗实心珠（照用户给的 30px PNG 量出来的比例） -->
    <circle cx="12" cy="11.9" r="8.8" />
    <circle cx="12" cy="20" r="2.4" fill="currentColor" stroke="none" />
  {:else if kind === 'female'}
    <!-- ♀：一个环 + 竖直的茎 + 一道横（照用户给的 32px PNG 量的） -->
    <circle cx="12" cy="8.3" r="5" />
    <path d="M12 14.3v7.1" />
    <path d="M8.2 17.6h7.6" />
  {:else if kind === 'male'}
    <!-- ♂（peach 的 `i-mars`） -->
    <circle cx="10" cy="14" r="6" />
    <path d="M16 3h5v5" />
    <path d="m21 3-6.75 6.75" />
  {:else if kind === 'film'}
    <!-- 影片计数：一条清单条 + 一块带播放三角的画面（照用户给的 24px PNG 描的，
         原件就是 24 网格、2px 边，所以坐标和描边都能 1:1 照抄；清单条两端是平头不是圆头，
         方框的角也是直角，所以这两处单独把 `round` 改回 `butt` / `miter`）。 -->
    <path d="M5 4h14" stroke-linecap="butt" />
    <rect x="3" y="8" width="18" height="12" stroke-linejoin="miter" />
    <path d="M10 11v5.6l4.7-2.8z" fill="currentColor" stroke="none" />
  {:else if kind === 'rank'}
    <!-- 皇冠（peach 的 `i-crown`）：页头「排名」那一行 -->
    <path
      d="M11.562 3.266a.5.5 0 0 1 .876 0L15.39 8.87a1 1 0 0 0 1.516.294L21.183 5.5a.5.5 0 0 1 .798.519l-2.834 10.246a1 1 0 0 1-.956.734H5.81a1 1 0 0 1-.957-.734L2.02 6.02a.5.5 0 0 1 .798-.519l4.276 3.664a1 1 0 0 0 1.516-.294z"
    />
    <path d="M5 21h14" />
  {:else if kind === 'users'}
    <circle cx="9.5" cy="7" r="4" />
    <path d="M2.5 21v-2a4 4 0 0 1 4-4h6a4 4 0 0 1 4 4v2" />
    <path d="M17 3.4a4 4 0 0 1 0 7.2" />
    <path d="M21.5 21v-2a4 4 0 0 0-3-3.6" />
  {:else if kind === 'pin'}
    <path d="M20 10c0 5.4-8 12-8 12s-8-6.6-8-12a8 8 0 0 1 16 0Z" />
    <circle cx="12" cy="10" r="2.8" />
  {:else if kind === 'height'}
    <path d="M12 3.5v17" />
    <path d="M8.5 7 12 3.5 15.5 7" />
    <path d="M8.5 17 12 20.5 15.5 17" />
  {:else if kind === 'weight'}
    <!-- 哑铃（peach 的 `i-dumbbell`） -->
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
  {:else if kind === 'eye'}
    <path d="M2.8 12S6.4 5.8 12 5.8 21.2 12 21.2 12 17.6 18.2 12 18.2 2.8 12 2.8 12Z" />
    <circle cx="12" cy="12" r="2.8" />
  {:else if kind === 'drop'}
    <path d="M12 3.5c3.3 3.9 5.3 6.4 5.3 8.7a5.3 5.3 0 0 1-10.6 0c0-2.3 2-4.8 5.3-8.7Z" />
  {:else if kind === 'tag'}
    <path d="M20.5 12.8 12.8 20.5a2.1 2.1 0 0 1-3 0L3.5 14.2V3.5h10.7l6.3 6.3a2.1 2.1 0 0 1 0 3Z" />
    <circle cx="8" cy="8" r="1.4" />
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
