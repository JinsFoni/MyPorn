<script lang="ts">
  /**
   * 卡片封面：16:9 的框里画一张封面，竖屏视频把两侧换成封面自己的虚化。
   *
   * 为什么不是一张 `<img class="object-cover">`：YouTube 给竖屏视频的 `hq720`/`maxresdefault` 是
   * **1280×720 的 16:9 画布**，竖画面居中，两侧烘了一圈拉长又压暗的填充（本机量最近 8 张：
   * 两侧平均亮度 7~53、中间 72~164）。卡片上那就是「两侧发暗、还带拉长人形」，跟点开之后
   * 播放器里两侧那层流光完全不是一回事。所以探出真画面那一竖条（`lib/coverCrop.ts`），
   * 中间放 sharp 的那一条，两侧铺同一条糊开的颜色——和播放器、加载态是同一套语言。
   *
   * 两层都用 CSS，不画 canvas：糊的那层是 `background-image` + `blur()`，sharp 那层还是 `<img>`
   * （留着 `loading=lazy` 与 `onerror` 的兜底）。一屏十几张卡片，画布方案每张要多留几百 KB。
   */
  import { coverCropFor, isWideFrame } from '../lib/coverCrop';

  interface Props {
    src: string | null;
    /** 挂到最外层的补间（卡片悬停放大那种）：两层一起动，不然中条和两侧会错开 */
    scaleClass?: string;
    /** 整图那层（没探到窄条时）的类名，跟改造前保持一致 */
    imgClass?: string;
    /** 真画面那一竖条的圆角/投影之类；默认交给外层 overflow 裁 */
    alt?: string;
  }

  let { src, scaleClass = '', imgClass = '', alt = '' }: Props = $props();

  let share = $state<number | null>(null);
  let failed = $state(false);

  // 换封面就把上一条的结论清掉，别拿上一张的窄条去裁这一张
  $effect(() => {
    const url = src;
    share = null;
    failed = false;
    if (url === null) return;
  });

  function onload(event: Event): void {
    const img = event.currentTarget as HTMLImageElement;
    if (img.naturalWidth <= 1) return;
    if (!isWideFrame(img)) return;
    const crop = coverCropFor(img.currentSrc || src || '', img);
    if (crop) share = crop.share;
  }
</script>

<div class="relative h-full w-full overflow-hidden {scaleClass}">
  {#if src && !failed}
    {#if share !== null}
      <!-- 两侧：同一条真画面按 100/share 放大（正好让这一条铺满整块）再糊成颜色。
           往外撑 16px 是给 blur 的晕开留地方，不然四周会亮出一圈透明边 -->
      <div
        class="absolute -inset-4"
        style={`background-image:url(${src});background-size:${100 / share}% auto;background-position:center;filter:blur(18px) saturate(1.3) brightness(1.05)`}
        aria-hidden="true"
      ></div>
    {/if}
    <img
      {src}
      {alt}
      loading="lazy"
      decoding="async"
      class="absolute inset-0 h-full w-full object-cover {share !== null ? '' : imgClass}"
      style={share !== null ? `left:50%;transform:translateX(-50%);width:${share * 100}%` : ''}
      {onload}
      onerror={() => (failed = true)}
    />
  {:else}
    <div class="grid h-full w-full place-items-center text-dim">
      <svg viewBox="0 0 24 24" class="h-6 w-6" fill="none" stroke="currentColor" stroke-width="1.5">
        <rect x="3" y="5.5" width="18" height="13" rx="3" />
        <path d="M10.4 9.6v4.8l4-2.4z" fill="currentColor" stroke="none" />
      </svg>
    </div>
  {/if}
</div>
