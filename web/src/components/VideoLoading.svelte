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
  }

  let { poster = null, posterFallback = null, kind = 'frame', label = '正在加载播放器' }: Props = $props();

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
</script>

<div class="video-veil" role="status" aria-live="polite" data-kind={kind}>
  {#if art}
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
  <span class="video-veil-scrim" aria-hidden="true"></span>
  <svg class="video-veil-spinner" viewBox="0 0 24 24" aria-hidden="true">
    <circle cx="12" cy="12" r="9" />
  </svg>
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
     再压 40% 黑，实测播放器区域只剩 40/255——那就是用户嘴里的「黑屏」。 */
  .video-veil[data-kind='poster'] .video-veil-art {
    filter: blur(3px) saturate(1.1) brightness(1.12);
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
