<script lang="ts">
  /**
   * 文档级滚动条——一根浮在右边缘的发丝药丸，自己量、自己画。
   * 口径与原生那根一致：4px 宽、冷白 20%（`--color-line-strong`）、全圆角。
   *
   * 为什么不再用原生滚动条：经典滚动条一定会在右边缘吃出一条 16px 的槽，
   * 而**根元素的背景图铺不进那条槽**——槽里只会画 html 的 `background-color`。
   * 实测（headed Chrome，1600×950，`::-webkit-scrollbar{width:16px}`）：
   * 给 html 挂一层 400px 高的纯红带、`background-size:100vw 400px` 之后截图，
   * 内容区 x≤783 是 (255,0,0)，槽 x=784..799 仍是画布色 (2,4,8)。
   * 也就是说根滚动条的底永远是那块纯色：顶部环境光铺不过去，卡片贴着右边缘时
   * 就变成一条显眼的黑竖条——「背景跟页面统一」用原生条做不到。
   * 非根容器不受此限（它自己的背景图会画到滚动条底下，同一个实验里
   * 800px 宽的 `overflow-y:scroll` 容器在 x=784..799 仍是渐变红），
   * 所以对话框、面板里的原生条保持原样，不用换。
   *
   * 于是：html 的原生条关掉（`html::-webkit-scrollbar{display:none}`，写 `width:0`
   * 会被忽略、槽照旧占位），换成这个 fixed 浮层。行为对齐 macOS 的浮条：滚动时出现、停手 900ms 淡出、
   * 指针压在条上或拖动时保持可见；按住往下拖就是滚动。
   */
  import { onMount } from 'svelte';

  /** 再短就抓不住了（1600×950 下 3000px 的页面大约 300px 长） */
  const THUMB_MIN = 36;
  /** 停手多久淡出 */
  const IDLE_MS = 900;

  let thumbH = $state(0);
  let thumbTop = $state(0);
  /** 文档长到需要滚动条了吗（不够长就整个不渲染） */
  let armed = $state(false);
  /** 现在是「看得见」的状态：正在滚 / 指针压在上面 / 正在拖 */
  let shown = $state(false);
  let dragging = $state(false);
  let hovered = false;

  let idleTimer: ReturnType<typeof setTimeout> | undefined;
  let dragFromY = 0;
  let dragFromScroll = 0;

  /** 一次读全几何：文档高的变化（图片加载、列表翻页）都靠它兜住 */
  function geometry(): { viewH: number; maxScroll: number; h: number; top: number } | null {
    const doc = document.documentElement;
    const viewH = doc.clientHeight || window.innerHeight;
    const maxScroll = doc.scrollHeight - viewH;
    if (maxScroll <= 4) return null;
    const h = Math.max(THUMB_MIN, Math.round((viewH * viewH) / doc.scrollHeight));
    const top = Math.round((window.scrollY / maxScroll) * (viewH - h));
    return { viewH, maxScroll, h, top };
  }

  function measure(): void {
    const g = geometry();
    if (!g) {
      armed = false;
      shown = false;
      return;
    }
    armed = true;
    thumbH = g.h;
    thumbTop = g.top;
  }

  function armIdle(): void {
    clearTimeout(idleTimer);
    idleTimer = setTimeout(() => {
      if (!hovered && !dragging) shown = false;
    }, IDLE_MS);
  }

  /** 只认文档自己的滚动：对话框里的列表也会冒泡到这里（scroll 不冒泡但会走捕获） */
  function onScroll(event: Event): void {
    const target = event.target;
    if (target !== document && target !== document.documentElement && target !== document.body) return;
    measure();
    if (!armed) return;
    shown = true;
    armIdle();
  }

  function onPointerDown(event: PointerEvent): void {
    if (!armed) return;
    const g = geometry();
    if (!g) return;
    dragging = true;
    shown = true;
    dragFromY = event.clientY;
    dragFromScroll = window.scrollY;
    (event.currentTarget as HTMLElement).setPointerCapture(event.pointerId);
    event.preventDefault();
  }

  function onPointerMove(event: PointerEvent): void {
    if (!dragging) return;
    const g = geometry();
    if (!g) return;
    const travel = g.viewH - g.h;
    if (travel <= 0) return;
    const next = dragFromScroll + ((event.clientY - dragFromY) * g.maxScroll) / travel;
    window.scrollTo({ top: Math.min(g.maxScroll, Math.max(0, next)), behavior: 'instant' });
  }

  function onPointerUp(event: PointerEvent): void {
    if (!dragging) return;
    dragging = false;
    const el = event.currentTarget as HTMLElement;
    if (el.hasPointerCapture(event.pointerId)) el.releasePointerCapture(event.pointerId);
    armIdle();
  }

  onMount(() => {
    measure();
    window.addEventListener('scroll', onScroll, { passive: true, capture: true });
    // 文档高度变了（翻页、图片落位、切页）要重新算长度和位置
    const ro = new ResizeObserver(() => measure());
    ro.observe(document.documentElement);
    if (document.body) ro.observe(document.body);
    return () => {
      clearTimeout(idleTimer);
      window.removeEventListener('scroll', onScroll, { capture: true });
      ro.disconnect();
    };
  });
</script>

{#if armed}
  <!-- 外层 10px 宽＝原生那格的槽宽，只是拿来当命中区（4px 的条太细、不好抓）；
       里面那根 4px 居中的发丝才是看得见的滚动条。 -->
  <div
    class="pointer-events-none fixed right-0 z-40 flex w-2.5 justify-center transition-opacity duration-200 ease-out"
    class:pointer-events-auto={shown}
    style:top="{thumbTop}px"
    style:height="{thumbH}px"
    style:opacity={shown ? 1 : 0}
    data-scroll-thumb
    aria-hidden="true"
    onpointerdown={onPointerDown}
    onpointermove={onPointerMove}
    onpointerup={onPointerUp}
    onpointercancel={onPointerUp}
    onpointerenter={() => {
      hovered = true;
      shown = true;
      clearTimeout(idleTimer);
    }}
    onpointerleave={() => {
      hovered = false;
      if (!dragging) armIdle();
    }}
  >
    <!-- 口径跟原生那根完全一致：4px 宽、冷白 20%（`--color-line-strong`）、全圆角药丸，
         两侧各留 3px（原生是 10px 槽 + 3px 透明边框做出来的同样留白）。 -->
    <div class="h-full w-1 touch-none rounded-full bg-[var(--color-line-strong)]"></div>
  </div>
{/if}
