<script lang="ts">
  /**
   * 播放器加载态：一张垫底的图（封面，或者播放中缓冲时抓下来的那一帧）+ 中间一个转环。
   *
   * 为什么不用黑底 + 一行「正在解析播放地址…」：那片黑跟加载完的封面差得太远，一闪一换很晃眼；
   * 而等片源解析完、直接把封面原样铺满又很糊（源图本身只有几百像素宽，放到一千多像素就是糊的）。
   * 所以干脆顺着封面本来就糊这件事做：原尺寸模糊当背景（不放大，见 .video-veil-art 的注释），中间放加载环，
   * 视觉上就是「镜头正在对上焦」，从解析中一直过渡到真正出画面。
   *
   * **画面上一个字都不写**：转环自己说得清「还在等」，多一行「正在解析最高画质…」只是把注意力
   * 从封面拽到一行灰字上。等得值不值得是封面和转环的事，不是文案的事。
   * 读屏仍然有话说——`label` 走 sr-only，视觉上不占地方。
   *
   * 两种底图分开对待（`kind`）：
   *   - `poster`：还没出过任何一帧，垫的是平台封面。这一步**可能持续好几秒**（冷启动要跑一次
   *     yt-dlp、再下第一段分片），所以只轻轻糊一点、几乎不压暗。曾经这里跟下面一样糊 6px、
   *     再盖 40% 黑，深色封面糊完就是一块近乎全黑的方块，只剩中间一个小转环——用户报的
   *     「起播黑屏」就是它（本机量到播放器区域平均亮度只剩 40/255，而这些封面自己有 60/255）。
   *   - `frame`：播放中缓冲（拖进度条、网络卡住），垫的是刚才那一帧。糊得重一点，
   *     免得把「正在等数据」看成「画面暂停了」。
   */
  interface Props {
    /** 底图地址（封面 / 播放器抓下来的最后一帧）；没有就只留一层暗底，加载环照转 */
    poster?: string | null;
    /** 底图取不到时退而求其次的那张（大封面挂了可以退到 hqdefault 这类小图） */
    posterFallback?: string | null;
    /** 底图是什么：`poster` 封面，`frame` 播放中抓下来的帧 */
    kind?: 'poster' | 'frame';
    /** 读屏文案（视觉上不渲染：画面上只留转环） */
    label?: string;
    /**
     * 会**画在画面上**的一行进度说明，只在「等得比转环更长」时传。
     *
     * 上面那条「画面上一个字都不写」的规矩是为「一两秒的对焦」定的；B 站 4K 那一档是另一回事——
     * 服务端要先把 554 MB~1.77 GB 的整条流下完才能合成，头一次点要等 1~3 分钟。
     * 那种长度上不给任何反馈（只转圈），用户只会得出一个结论：这档放不了。
     * 所以这里留一个口子：不传就没字，传了才显示百分比。
     */
    detail?: string | null;
  }

  let {
    poster = null,
    posterFallback = null,
    kind = 'frame',
    label = '正在加载播放器',
    detail = null,
  }: Props = $props();

  /**
   * 当前该用哪张图。后端抓不到封面时发的是一张 1×1 的透明 PNG（不是 4xx），
   * 所以「没图」不能只看 error 事件——naturalWidth ≤ 1 也要算，否则底图就是一片黑。
   */
  const first = poster ?? posterFallback ?? null;
  /** 来自 props 的那张（换底图时用它判断要不要重置失败状态） */
  let source = $state<string | null>(first);
  let art = $state<string | null>(first);
  let tried = $state<string | null>(first);

  // 缓冲态换成「抓下来的那一帧」、或者换影片换了封面时，重新从 props 那张开始试
  $effect(() => {
    const next = poster ?? posterFallback ?? null;
    if (next === source) return;
    source = next;
    art = next;
    tried = next;
  });

  function onArtLoaded(event: Event): void {
    if ((event.currentTarget as HTMLImageElement).naturalWidth > 1) return;
    onArtBroken();
  }

  function onArtBroken(): void {
    const fallback = posterFallback !== null && posterFallback !== '' ? posterFallback : null;
    if (fallback === null || fallback === tried) {
      art = null;
      return;
    }
    art = fallback;
    tried = fallback;
  }

  /**
   * 封面里「真内容」那一竖条占的位置（0~1），探不到就返回 null。
   *
   * 为什么要在像素上找：YouTube 给竖屏视频出的 `maxresdefault`/`hq720` 都是 **1280×720 的 16:9 画布**，
   * 竖画面居中，两侧是它自己拿整帧拉过去、糊过又压暗的填充。在 1259px 宽的播放器里那圈填充是
   * 按原尺寸铺开的：本机量了最近 8 张，两侧平均亮度 7~53，中间 72~164——所以看上去就是一圈
   * 半黑不黑、还带着拉长人形的边，跟播放起来之后两侧那层流光完全不是一回事。
   *
   * 判据用**锐度**：填充区糊过，高频被抹平；中间那一竖条是真画面。取列方向拉普拉斯绝对值的
   * 均值当锐度，在**原尺寸**上算（缩到 640 算过一遍，糊的那圈还留着细节，量出来的边界能偏 3~6%）。
   * 实测这一批封面：填充区锐度 0.1~1.0，中间 3~18，阈值取 2.5 就能分开。
   *
   * 边界还会往外多认 1~3%（糊的过渡带本身也有锐度），而 YouTube 那圈填充永远是**左右对称**的，
   * 所以取左右两半里**窄的那一半**再对着硬切口收一下：宽的那半报多了也不影响。
   *
   * 只认「接近 9:16 的窄条」（22%~36% 宽）：16:9 的原画封面（占满整幅）和 4:5 这类更宽的竖屏
   * 都会落在窗口外，一律不裁——宁可按老样子整张铺满，也不能把封面裁掉一块。
   */
  function findContentSpan(img: HTMLImageElement): { x0: number; x1: number } | null {
    const AW = Math.min(img.naturalWidth, 1280);
    const AH = Math.max(1, Math.round((img.naturalHeight * AW) / img.naturalWidth));
    const probe = document.createElement('canvas');
    probe.width = AW;
    probe.height = AH;
    const ctx = probe.getContext('2d', { willReadFrequently: true });
    if (!ctx) return null;
    ctx.drawImage(img, 0, 0, AW, AH);
    let rgba: Uint8ClampedArray;
    try {
      rgba = ctx.getImageData(0, 0, AW, AH).data;
    } catch {
      // 跨源封面（没走 /api/img 代理的那些平台）读不了像素：不猜，整张铺满
      return null;
    }
    const gray = new Float32Array(AW * AH);
    for (let i = 0; i < AW * AH; i += 1) {
      const p = i * 4;
      gray[i] = 0.299 * rgba[p] + 0.587 * rgba[p + 1] + 0.114 * rgba[p + 2];
    }
    const sharp = new Float32Array(AW);
    for (let x = 1; x < AW - 1; x += 1) {
      let sum = 0;
      for (let y = 1; y < AH - 1; y += 1) {
        const i = y * AW + x;
        sum += Math.abs(4 * gray[i] - gray[i - 1] - gray[i + 1] - gray[i - AW] - gray[i + AW]);
      }
      sharp[x] = sum / (AH - 2);
    }
    // 5 列的滑动平均：单列的噪点不至于被当成边界
    const smooth = new Float32Array(AW);
    for (let x = 2; x < AW - 2; x += 1) {
      smooth[x] = (sharp[x - 2] + sharp[x - 1] + sharp[x] + sharp[x + 1] + sharp[x + 2]) / 5;
    }
    let left = -1;
    let right = -1;
    const mid = AW / 2;
    for (let x = Math.round(AW * 0.02); x < mid; x += 1) {
      if (smooth[x] > 2.5) {
        left = x;
        break;
      }
    }
    for (let x = Math.round(AW * 0.98); x > mid; x -= 1) {
      if (smooth[x] > 2.5) {
        right = x;
        break;
      }
    }
    if (left < 0 || right < 0) return null;
    // 收到那道硬切口上：±4% 里锐度最高的那一列就是「真画面」的第一列
    const window = Math.round(AW * 0.04);
    const edgeNear = (from: number): number => {
      let best = from;
      let bestValue = -1;
      for (let x = Math.max(2, from - window); x <= Math.min(AW - 3, from + window); x += 1) {
        if (sharp[x] > bestValue) {
          bestValue = sharp[x];
          best = x;
        }
      }
      return best;
    };
    const half = Math.min(mid - edgeNear(left), edgeNear(right) - mid);
    const share = (half * 2) / AW;
    if (share < 0.22 || share > 0.36) return null;
    return { x0: (mid - half) / AW, x1: (mid + half) / AW };
  }

  /** 合成好的两层：底下那张糊成色块（填两侧），上面那张按原比例放中间 */
  type Composed = { img: HTMLImageElement; sx: number; sw: number; sh: number };
  let composed = $state<Composed | null>(null);
  let fillCanvas = $state<HTMLCanvasElement | null>(null);
  let artCanvas = $state<HTMLCanvasElement | null>(null);

  // 每次换封面重新探一次：探到窄条才合成，探不到就走原来的 <img>
  $effect(() => {
    const src = art;
    composed = null;
    if (src === null || kind !== 'poster') return;
    const img = new Image();
    img.decoding = 'async';
    img.onload = () => {
      if (img.naturalWidth <= 1) return;
      const span = findContentSpan(img);
      if (!span) return;
      const sx = Math.round(span.x0 * img.naturalWidth);
      composed = {
        img,
        sx,
        sw: Math.round((span.x1 - span.x0) * img.naturalWidth),
        sh: img.naturalHeight,
      };
    };
    img.src = src;
    return () => {
      img.onload = null;
      img.onerror = null;
    };
  });

  // 两层都画到 canvas 上：底下一张按 cover 铺满再交给 CSS 糊，上面一张按原生比例
  $effect(() => {
    const c = composed;
    const fill = fillCanvas;
    const top = artCanvas;
    if (!c || !fill || !top) return;
    const FW = 48;
    const FH = 27;
    fill.width = FW;
    fill.height = FH;
    const fctx = fill.getContext('2d');
    if (fctx) {
      const scale = Math.max(FW / c.sw, FH / c.sh);
      const w = c.sw * scale;
      const h = c.sh * scale;
      fctx.drawImage(c.img, c.sx, 0, c.sw, c.sh, (FW - w) / 2, (FH - h) / 2, w, h);
    }
    top.width = c.sw;
    top.height = c.sh;
    top.getContext('2d')?.drawImage(c.img, c.sx, 0, c.sw, c.sh, 0, 0, c.sw, c.sh);
  });
</script>

<div class="video-veil" role="status" aria-live="polite" data-kind={kind}>
  {#if art}
    {#if composed}
      <!-- 封面态：底下那张把画面中心那一竖条铺满整块再糊成色块，上面那张按原生比例摆在中间。
           这样两侧是「封面自己的颜色」，不是 YouTube 塞进来的那圈拉长的糊图。 -->
      <canvas class="video-veil-art video-veil-fill" bind:this={fillCanvas} aria-hidden="true"></canvas>
      <canvas class="video-veil-poster" bind:this={artCanvas} aria-hidden="true"></canvas>
    {:else}
      <!-- 缓冲态的帧、或者探不出窄条的封面：一张图铺满（cover）就对了 -->
      <img
        class="video-veil-art"
        src={art}
        alt=""
        aria-hidden="true"
        decoding="async"
        onload={onArtLoaded}
        onerror={onArtBroken}
      />
    {/if}
  {/if}
  <span class="video-veil-scrim" aria-hidden="true"></span>
  <svg class="video-veil-spinner" viewBox="0 0 24 24" aria-hidden="true">
    <circle cx="12" cy="12" r="9" />
  </svg>
  {#if detail}
    <p class="video-veil-detail">{detail}</p>
  {/if}
  <span class="sr-only">{label}</span>
</div>

<style>
  .video-veil {
    position: absolute;
    inset: 0;
    /* 必须压在画面之上、**控制条之下**：皮肤的控制条是 z-index 30、它那条暗角是 10，
       所以这里只取 5。取高了拖动进度条时进度条会被这层糊图整个盖掉——手指还按在
       一条看不见的进度条上，是最难受的一种「消失了」 */
    z-index: 5;
    display: grid;
    place-items: center;
    overflow: hidden;
    /* 底图缺席时的兜底：不是纯黑，而是一层比页面更深的冷调渐变——
       「一块黑」和「一张还没出来的图」在观感上是两件事，后者不该像故障 */
    background: radial-gradient(circle at 50% 42%, rgb(26 33 43), rgb(4 6 10) 72%);
    /* 盖住的只是画面，播放/暂停/全屏这些照旧能点 */
    pointer-events: none;
  }

  .video-veil-spinner {
    position: relative;
    grid-area: 1 / 1;
  }

  /* 进度说明压在转环下面一行（同一格里往下挤），字号克制：
     它的作用是「我知道还在动」，不是要把视线钉在这里。 */
  .video-veil-detail {
    position: relative;
    grid-area: 1 / 1;
    margin-top: 4.5rem;
    font-size: 0.8125rem;
    line-height: 1.2;
    font-variant-numeric: tabular-nums;
    color: rgb(255 255 255 / 0.82);
    text-shadow: 0 1px 3px rgb(0 0 0 / 0.6);
  }

  /* 不能靠 transform: scale 去盖 blur 的四周——原来那 1.08 会让缓冲时糊的这帧比播放中的画面
     整整大 8%，等真画面出来再收回去，就是一眼能看出的「画面缩了一下」。
     改成把盒子往外撑 6px：画面只被撑大约 2%（1112 宽的播放器上，边缘的景物挪 11px），
     肉眼基本看不出跟播放时的大小差；而 blur 的晕开主要落在外扩那一圈里、被 overflow 裁掉。
     （外扩 1px 的效果是量出来的：白底上第一列亮度 6px 外扩是 146/169，12px 是 165/169，
      不外扩只有 91/169——那圈暗边在亮画面上能看出来，所以至少留 6px。） */
  .video-veil-art {
    position: absolute;
    /* <img> 的 width/height 写 auto 会退回图片自身的固有尺寸（640×360），光靠 inset 撑不开；
       而且 Tailwind 的 preflight 给了 img { max-width: 100% }，不解除就会把宽度又夹回 100% */
    inset: -6px;
    width: calc(100% + 12px);
    height: calc(100% + 12px);
    max-width: none;
    max-height: none;
    object-fit: cover;
    /* 只糊到「看不懂细节」，别糊到「看不出是什么」：糊得越重越像一张被硬拉大的图。
       6px 是调出来的：还认得出是谁、在干什么，但细节全化开，正好是「正在对上焦」那个瞬间 */
    filter: blur(6px) saturate(1.15);
  }

  /* 封面那一版：这一步要等好几秒，画面必须一眼看出「这是那部片子的封面」。
     本机量的：这批封面自身的平均亮度只有 60/255（室内、夜戏居多），按上面那套糊 6px
     再压 40% 黑，实测播放器区域只剩 40/255——那就是用户嘴里的「黑屏」。
     竖屏封面在 16:9 的盒子里用 cover 就是硬裁（1080×1920 的封面按宽度铺满后高 2239px，
     只露出中间 708px 的一条，等于把人脸放大三四倍），所以封面态底下那张干脆糊成色块，
     整张封面交给上面那张 `.video-veil-poster`。 */
  .video-veil[data-kind='poster'] .video-veil-art {
    filter: blur(3px) saturate(1.1) brightness(1.12);
  }

  /* 两侧那层虚化背景：源图只有中间那一竖条（≈400×720），铺到 1259px 宽后被放大三倍多，
     3px 的糊根本盖不住（实测还能一眼认出侧边是谁的手和脸），80px 才只剩一片颜色。 */
  .video-veil[data-kind='poster'] .video-veil-fill {
    filter: blur(80px) saturate(1.25) brightness(1.06);
  }

  /* 完整封面：按原生比例摆在中间，两侧露出底下那层虚化。
     `height: 100% + width: auto` 对 canvas 来说就是「按固有像素比例」，不会被拉变形。
     带一点投影，免得封面的白边跟虚化背景糊在一起、看不出边界在哪。 */
  .video-veil-poster {
    position: absolute;
    top: 0;
    bottom: 0;
    left: 50%;
    width: auto;
    height: 100%;
    max-width: 100%;
    transform: translateX(-50%);
    object-fit: contain;
    filter: drop-shadow(0 0 24px rgb(2 4 8 / 0.55));
  }

  /* 手机上播放器只有 200 出头高，6px 的糊按画面比例算是桌面的三倍，而且那圈外扩占的份额也翻倍
     （6px 外扩在 206 高的小画面上就是 6% 的放大）。所以按屏宽折一半，两边保持同一种观感 */
  @media (max-width: 639px) {
    .video-veil-art {
      inset: -3px;
      width: calc(100% + 6px);
      height: calc(100% + 6px);
      filter: blur(3px) saturate(1.15);
    }

    .video-veil[data-kind='poster'] .video-veil-art {
      filter: blur(1.5px) saturate(1.1) brightness(1.12);
    }
    .video-veil[data-kind='poster'] .video-veil-fill {
      filter: blur(36px) saturate(1.25) brightness(1.06);
    }
  }

  .video-veil-scrim {
    position: absolute;
    inset: 0;
    background: radial-gradient(circle at 50% 50%, rgb(2 4 8 / 0.04), rgb(2 4 8 / 0.4));
  }

  /* 封面态只留一点点暗角给转环垫底：再压暗就是把封面重新变回黑屏 */
  .video-veil[data-kind='poster'] .video-veil-scrim {
    background: radial-gradient(circle at 50% 50%, rgb(2 4 8 / 0) 32%, rgb(2 4 8 / 0.22));
  }

  .video-veil-spinner {
    width: 2.75rem;
    height: 2.75rem;
    fill: none;
    stroke: rgb(245 250 255 / 0.95);
    stroke-width: 1.8;
    stroke-linecap: round;
    /* 一圈露出四分之一，剩下的留白 */
    stroke-dasharray: 56.5;
    stroke-dashoffset: 37;
    animation: video-veil-turn 0.9s linear infinite;
    /* 底下可能是很亮的画面（拖动进度条时糊的就是当前帧），光靠白环压不住：
       先铺一层柔和的暗晕，再用一圈窄影把边缘勾出来 */
    filter: drop-shadow(0 0 12px rgb(2 4 8 / 0.55)) drop-shadow(0 1px 2px rgb(2 4 8 / 0.9));
  }

  @keyframes video-veil-turn {
    to {
      transform: rotate(360deg);
    }
  }

  @media (prefers-reduced-motion: reduce) {
    /* 别转：改成呼吸一下，表示还在等 */
    .video-veil-spinner {
      animation: video-veil-breathe 1.8s ease-in-out infinite;
    }
    @keyframes video-veil-breathe {
      50% {
        opacity: 0.3;
      }
    }
  }
</style>
