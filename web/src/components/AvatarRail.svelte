<script lang="ts">
  import { imageUrl } from '../lib/img';
  import type { RailItem } from '../lib/rail';

  /**
   * 头像条：横向滚动的圆头像＝**过滤器**（关注页 / 动态页 / B 站页 / YouTube 页共用一套）。
   *
   * 尺寸：**72px 圆头像**（`h-18 w-18`，取 128px 的图按 2x 稍有余量），列宽就是头像宽（`w-18` = 72px），
   * 姓名 `text-ui` 14px（2026-11-04 从 12px「再大一点点」）、**单行截断**、**居中在头像正下方**
   * （同日要求；此前是 80px 宽、左对齐）。
   * 历史：2026-11-02「头像改小一点」72 → 56；2026-11-04「再调大一点点」56 → 64 → 72 并要求名字居中。
   * **列宽与头像等宽（都 72px）是刻意的**：这样首列头像的左边仍落在内容左边缘（白环靠滚动盒 6px 内边距活着），
   * 名字居中不会把整列往右推 4px（若列宽 80 而头像 72，居中后头像会右移 4px）。
   * **不显示任何计数**；
   * 点头像＝把 `id` 交回去，由页面写地址栏；选中态是头像外 2px 白环 + 姓名 `font-medium`，
   * 再点同一个取消（这是**唯一**的取消入口：下面那行筛选行不再有带 ✕ 的名字胶囊）。
   *
   * 容器 `flex gap-4 overflow-x-auto no-scrollbar`（间距 12 → 16px，2026-11-04 用户要求「间距再宽一点点」），
   * 用「负外边距 + 等量内边距」做贴边滚动：
   * 首列头像永远与内容左边缘（桌面 296 / 移动 12px）对齐，滚动盒自己往两侧多伸出去同样多。
   *
   * **白环画在头像盒子外面，所以滚动盒必须留出 6px 内边距，否则会被自己裁掉**
   * （`overflow-x: auto` 会让 `overflow-y` 的计算值也变成 `auto`，内容在 padding box 处被切）。
   * 选中环 = `ring-2` + `ring-offset-2` = 头像外 4px；「正在同步」小圆点的 `ring-2` 再往外 2px。
   * 2026-11-02 实测（1600 宽、选中第一颗）：`paddingTop=0`、`roomTop=-4`、`roomLeft=-4`
   * —— 白环的上弧和左弧各被切掉 4px，看起来像「圈没画完」。现在四个方向都留 6px
   * （桌面 `px-1.5`、移动 `px-3`/`px-4`、上下 `pt-1.5`，多出的 2px 吃设备像素取整），
   * 再用等量负外边距（`-mx-1.5`、`-mt-1.5`）把盒子拉回去：头像仍在 296/40，
   * 下面第一条卡片仍在 202，一个像素都不动。
   *
   * 首屏给同尺寸灰圆占位，避免加载完成时整页跳动。
   *
   * 这个组件只管画：谁来、怎么拉由页面决定（关注页是 PerformerRail，另两页各自有频道列表）。
   */
  interface Props {
    items: RailItem[];
    /** 无障碍标签：「按艺人筛选」/「按 UP 主筛选」/「按频道筛选」 */
    label: string;
    /** 当前选中的 id（地址栏是唯一真源，这里只负责高亮） */
    selected?: number | null;
    /** 点一个头像：选中就回 id，再点一次回 null（取消筛选） */
    onSelect?: (id: number | null) => void;
    /** 列表还在路上：画同样数量的灰圆占位 */
    loading?: boolean;
  }

  let { items, label, selected = null, onSelect, loading = false }: Props = $props();

  /** 头像会过期（平台 CDN 的签名/防盗链），失败的那一张退回首字母，别再反复拉 */
  let failed = $state<Record<number, boolean>>({});
  let listEl = $state<HTMLUListElement | null>(null);

  function pick(id: number): void {
    onSelect?.(selected === id ? null : id);
  }

  /**
   * 选中项滚进视野。深链接进来（`/bilibili?up=89`、`/?performer=38`、复制给别人）时，
   * 被筛的那位可能排在第四五行、已经滚出右边——只画白环的话，用户看到的是「筛了但不知道筛了谁」。
   * 用 rect 差算 scrollLeft 而不是 `scrollIntoView()`：后者会连整页一起竖着滚一下。
   */
  $effect(() => {
    const el = listEl;
    const id = selected;
    if (!el || id === null) return;
    const btn = el.querySelector<HTMLElement>(`[data-rail-id="${String(id)}"]`);
    if (!btn) return;
    const rect = btn.getBoundingClientRect();
    const box = el.getBoundingClientRect();
    if (rect.left < box.left) el.scrollLeft += rect.left - box.left - 12;
    else if (rect.right > box.right) el.scrollLeft += rect.right - box.right + 12;
  });
</script>

{#if items.length > 0}
  <nav class="mb-4 lg:mb-5" aria-label={label}>
    <ul
      bind:this={listEl}
      class="no-scrollbar -mx-3 -mt-1.5 flex gap-4 overflow-x-auto px-3 pb-1 pt-1.5 sm:-mx-4 sm:px-4 lg:-mx-1.5 lg:px-1.5"
    >
      {#each items as item (item.id)}
        <li class="shrink-0">
          <button
            type="button"
            onclick={() => pick(item.id)}
            data-rail-id={item.id}
            aria-pressed={selected === item.id}
            class="group flex w-18 flex-col items-center gap-1 text-center"
            title={selected === item.id
              ? `${item.title ?? item.name}（再点一次看全部）`
              : (item.title ?? item.name)}
          >
            <span class="relative block">
              <span
                class="block h-18 w-18 overflow-hidden rounded-full bg-panel {selected === item.id
                  ? 'ring-2 ring-fg ring-offset-2 ring-offset-ink'
                  : ''}"
              >
                {#if item.avatarUrl && !failed[item.id]}
                  <img
                    data-media
                    src={imageUrl(item.avatarUrl, 128) ?? ''}
                    onerror={() => (failed = { ...failed, [item.id]: true })}
                    alt=""
                    width="128"
                    height="128"
                    loading="lazy"
                    decoding="async"
                    class="h-full w-full object-cover transition duration-300 {item.dimmed
                      ? 'opacity-50'
                      : selected === item.id
                        ? 'opacity-100'
                        : 'opacity-90 group-hover:opacity-100'}"
                  />
                {:else}
                  <span class="flex h-full w-full items-center justify-center text-title text-dim">
                    {item.name.trim().charAt(0).toUpperCase()}
                  </span>
                {/if}
              </span>
              {#if item.busy}
                <span
                  class="absolute right-0 top-0 h-2 w-2 animate-pulse rounded-full bg-hi ring-2 ring-ink"
                  title="正在同步"
                ></span>
              {/if}
            </span>
            <span
              class="w-full truncate text-center text-ui transition-colors {selected === item.id
                ? 'font-medium text-fg'
                : 'text-dim group-hover:text-fg'} {item.dimmed ? 'opacity-50' : ''}"
            >
              {item.name}
            </span>
          </button>
        </li>
      {/each}
    </ul>
  </nav>
{:else if loading}
  <!-- 占位行跟真身用同一套外框（等量负边距 + 内边距 + 横向滚动）：6 颗 72px 圆
       在 390 宽的手机上装不下（6×72 + 5×16 = 512 > 366 可用），不加滚动会把整页顶出横向滚动条。 -->
  <div class="mb-4 lg:mb-5" aria-hidden="true">
    <div
      class="no-scrollbar -mx-3 -mt-1.5 flex gap-4 overflow-x-auto px-3 pb-1 pt-1.5 sm:-mx-4 sm:px-4 lg:-mx-1.5 lg:px-1.5"
    >
      {#each Array.from({ length: 6 }, (_, i) => i) as i (i)}
        <div class="flex w-18 shrink-0 flex-col items-center gap-1">
          <div class="h-18 w-18 rounded-full bg-panel"></div>
          <div class="h-3 w-12 rounded-full bg-panel"></div>
        </div>
      {/each}
    </div>
  </div>
{/if}
