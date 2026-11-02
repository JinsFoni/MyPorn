<script module lang="ts">
  /**
   * 视频墙的栅格：一行最宽 4 个。
   *
   * 关注流、B 站、YouTube 三条墙卡片的形状完全一样（16:9 封面 + 标题 + 头像/频道），
   * 列数不该各写一份——B 站和 YouTube 原先各自抄了一遍同样的 class，关注流用的是默认值 5 个，
   * 同一块屏幕上一换页就多出一列（2xl 下 5×~290px vs 4×~360px，卡片大小跟着跳）。
   * 现在三处共用这一个常量。手机上一行 1 个、sm 起 2 个、xl 3 个、2xl 4 个。
   */
  export const WALL_GRID = 'grid grid-cols-1 gap-x-4 gap-y-6 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4';
</script>

<script lang="ts" generics="T">
  /**
   * IntersectionObserver 分页 + 页淘汰（离屏页卸载 DOM）。
   * 不引入虚拟滚动库：保留骨架占位（纯 CSS 尺寸，resize 安全），滚回视口时重新拉取。
   */
  import { onMount } from 'svelte';
  import type { Snippet } from 'svelte';
  import { messageFor } from '../lib/api';

  interface Page<T> {
    id: number;
    /** 拉取这一页时用的游标（null = 首屏） */
    cursor: string | null;
    /** 下一页游标 */
    next: string | null;
    items: T[];
    /** 即使 items 被卸载也要记住格子数，占位才不塌 */
    count: number;
    loaded: boolean;
  }

  interface Props {
    load: (cursor: string | null) => Promise<{ items: T[]; nextCursor: string | null }>;
    children: Snippet<[T]>;
    keyOf: (item: T) => string | number;
    /** 骨架形状，与 children 的卡片结构保持一致 */
    placeholder?: Snippet;
    empty?: Snippet;
    /** 变化即重置列表（例如切换筛选） */
    reloadKey?: number;
    /** 同时保留在 DOM 里的最多页数 */
    maxLoadedPages?: number;
    gridClass?: string;
    /**
     * 把当前已加载的条目按顺序报给父组件。
     * 灯箱要在「整条流」里前后翻，而条目列表是本组件内部状态，父组件拿不到。
     */
    onItems?: (items: T[]) => void;
  }

  let {
    load,
    children,
    keyOf,
    placeholder,
    empty,
    reloadKey = 0,
    maxLoadedPages = 4,
    gridClass = 'grid grid-cols-2 gap-x-4 gap-y-7 sm:grid-cols-3 xl:grid-cols-4 2xl:grid-cols-5',
    onItems,
  }: Props = $props();

  let pages = $state<Page<T>[]>([]);
  let loading = $state(false);
  let errorMessage = $state('');
  let done = $state(false);
  let sentinel = $state<HTMLDivElement | null>(null);

  let pageSeq = 0;
  let generation = 0;
  let seenReloadKey = 0;

  async function loadFirst(): Promise<void> {
    const gen = (generation += 1);
    loading = true;
    errorMessage = '';
    done = false;
    try {
      const res = await load(null);
      if (gen !== generation) return;
      pages = [
        {
          id: (pageSeq += 1),
          cursor: null,
          next: res.nextCursor,
          items: res.items,
          count: res.items.length,
          loaded: true,
        },
      ];
      done = res.nextCursor === null;
    } catch (err) {
      if (gen !== generation) return;
      errorMessage = messageFor(err);
    } finally {
      if (gen === generation) loading = false;
    }
    fillViewport();
  }

  async function loadMore(): Promise<void> {
    if (loading || done || errorMessage !== '') return;
    const last = pages[pages.length - 1];
    if (!last || last.next === null) {
      done = true;
      return;
    }
    const gen = generation;
    loading = true;
    try {
      const res = await load(last.next);
      if (gen !== generation) return;
      pages = [
        ...pages,
        {
          id: (pageSeq += 1),
          cursor: last.next,
          next: res.nextCursor,
          items: res.items,
          count: res.items.length,
          loaded: true,
        },
      ];
      done = res.nextCursor === null;
      evictPages();
    } catch (err) {
      if (gen !== generation) return;
      errorMessage = messageFor(err);
    } finally {
      if (gen === generation) loading = false;
    }
    fillViewport();
  }

  /** 保留首页与尾部若干页，中间的页卸载 DOM（只留占位格子） */
  function evictPages(): void {
    let loaded = pages.filter((p) => p.loaded).length;
    for (let i = 1; i < pages.length - 2 && loaded > maxLoadedPages; i += 1) {
      const page = pages[i];
      if (page && page.loaded) {
        page.loaded = false;
        page.items = [];
        loaded -= 1;
      }
    }
  }

  async function revivePage(page: Page<T>): Promise<void> {
    if (page.loaded) return;
    try {
      const res = await load(page.cursor);
      const idx = pages.findIndex((p) => p.id === page.id);
      const target = pages[idx];
      if (!target) return;
      target.items = res.items;
      target.count = res.items.length;
      target.next = res.nextCursor;
      target.loaded = true;
    } catch {
      /* 滚回来时会再次触发；这里不打断阅读 */
    }
  }

  /** 页占位重新进入视口 → 补数据 */
  function revive(node: HTMLElement, page: Page<T> | null) {
    if (!page) return { destroy() {} };
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) {
          io.disconnect();
          void revivePage(page);
        }
      },
      { rootMargin: '300px 0px' },
    );
    io.observe(node);
    return {
      destroy() {
        io.disconnect();
      },
    };
  }

  /**
   * 每次拉完回头看一眼：哨兵还在「视口下方 800px」以内就接着拉。
   *
   * 为什么非补这一手：IntersectionObserver 只在相交状态**变化**时回调。
   * 挂载那一下哨兵就在范围内，回调却正好落在首屏还在 loading 的时候被挡掉；
   * 之后列表变高把哨兵顶出视野、再滚回底部才又相交——若这次回调又撞上 loading，
   * 状态从此不再变化，列表就停在半路不再补页。
   * 实测（2026-11-01，关注流 40 条/页、1440×900）：从 4900px 退回，只追到第一页
   * 底部 3859px（scrollHeight 4760）就不动了；补上这一手之后一路追到 4900。
   */
  function fillViewport(): void {
    // 等这一帧的 DOM 落地再量：量早了读到的是上一页的高度，会连着多拉一页
    requestAnimationFrame(() => {
      const el = sentinel;
      const last = pages[pages.length - 1];
      // 末页一条都没进来就别再追了：高度不会再变，接着拉只会空转
      if (!el || !last || last.count === 0) return;
      if (loading || done || errorMessage !== '') return;
      if (el.getBoundingClientRect().top <= window.innerHeight + 800) void loadMore();
    });
  }

  function retry(): void {
    if (pages.length === 0) void loadFirst();
    else {
      errorMessage = '';
      void loadMore();
    }
  }

  onMount(() => {
    void loadFirst();
    const el = sentinel;
    if (!el) return;
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) void loadMore();
      },
      { rootMargin: '800px 0px' },
    );
    io.observe(el);
    return () => io.disconnect();
  });

  $effect(() => {
    const key = reloadKey;
    if (key === seenReloadKey) return;
    seenReloadKey = key;
    void loadFirst();
  });

  $effect(() => {
    if (!onItems) return;
    onItems(pages.filter((p) => p.loaded).flatMap((p) => p.items));
  });

  const isEmpty = $derived(!loading && errorMessage === '' && pages.length > 0 && pages.every((p) => p.count === 0));
</script>

{#snippet defaultFrame()}
  <div class="aspect-video rounded-xl bg-panel"></div>
  <div class="mt-2.5 flex items-start gap-2.5">
    <div class="h-9 w-9 shrink-0 rounded-full bg-panel"></div>
    <div class="min-w-0 flex-1">
      <div class="h-3 w-4/5 rounded-full bg-panel"></div>
      <div class="mt-2 h-2.5 w-1/2 rounded-full bg-panel"></div>
    </div>
  </div>
{/snippet}

{#snippet frames(count: number)}
  {#each Array.from({ length: count }, (_, i) => i) as slot (slot)}
    <div class="min-w-0 animate-pulse">
      {#if placeholder}{@render placeholder()}{:else}{@render defaultFrame()}{/if}
    </div>
  {/each}
{/snippet}

<div class={gridClass} aria-busy={loading}>
  {#each pages as page (page.id)}
    {#if page.loaded}
      {#each page.items as item (keyOf(item))}
        {@render children(item)}
      {/each}
    {:else}
      {#each Array.from({ length: page.count }, (_, i) => i) as slot (slot)}
        <div class="min-w-0" use:revive={slot === 0 ? page : null}>
          {#if placeholder}{@render placeholder()}{:else}{@render defaultFrame()}{/if}
        </div>
      {/each}
    {/if}
  {/each}

  {#if loading && pages.length === 0}
    {@render frames(8)}
  {/if}
</div>

{#if isEmpty}
  {#if empty}
    {@render empty()}
  {/if}
{/if}

{#if errorMessage}
  <div class="mt-8 flex flex-col items-center gap-3 text-center" role="alert">
    <p class="text-ui text-muted">{errorMessage}</p>
    <button type="button" class="btn" onclick={retry}>重试</button>
  </div>
{:else if done && pages.length > 0 && !isEmpty}
  <p class="py-8 text-center text-meta text-dim">没有更多了</p>
{/if}

<div bind:this={sentinel} class="h-px w-full" aria-hidden="true"></div>
