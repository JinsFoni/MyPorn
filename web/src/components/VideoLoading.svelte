<script lang="ts">
  /**
   * 播放器加载态：把一张图糊成一片环境光垫底，中间转一个细环。
   *
   * 为什么不用黑底 + 一行「正在解析播放地址…」：那片黑跟加载完的封面差得太远，一闪一换很晃眼；
   * 而等片源解析完、直接把封面原样铺满又很糊（源图本身只有几百像素宽，放到一千多像素就是糊的）。
   * 所以干脆顺着封面本来就糊这件事做：模糊放大当背景（跟氛围模式同一套观感），中间放加载环，
   * 视觉上就是「镜头正在对上焦」，从解析中一直过渡到真正出画面。
   *
   * 底图交给调用方决定：还没出画面时是封面，播放中缓冲（拖动进度条、卡网络）时是播放器自己
   * 抓下来的最后一帧——所以缓冲时糊的是你刚才看的画面，不会突然跳回封面，更不会黑一下。
   */
  interface Props {
    /** 底图地址（封面 / 播放器抓下来的最后一帧）；没有就只留一层暗底，加载环照转 */
    poster?: string | null;
    /** 读屏文案：画面上不显示字 */
    label?: string;
  }

  let { poster = null, label = '正在加载播放器' }: Props = $props();
</script>

<div class="video-veil" role="status" aria-live="polite">
  {#if poster}
    <img class="video-veil-art" src={poster} alt="" aria-hidden="true" decoding="async" />
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
    /* 底图由调用方给（封面，或播放时抓下来的最后一帧），这里只是它缺席时的兜底：
       同理不能是不透明的，否则会把底图整个盖掉，拖动进度条时就是「一整块黑」 */
    background: rgb(2 4 8 / 0.85);
    /* 盖住的只是画面，播放/暂停/全屏这些照旧能点 */
    pointer-events: none;
  }

  .video-veil-art {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    object-fit: cover;
    /* 放大一点：blur 会把四周糊到透明，露出底色就有一道边 */
    transform: scale(1.16);
    filter: blur(22px) saturate(1.2);
  }

  .video-veil-scrim {
    position: absolute;
    inset: 0;
    background: radial-gradient(circle at 50% 50%, rgb(2 4 8 / 0.04), rgb(2 4 8 / 0.4));
  }

  .video-veil-spinner {
    position: relative;
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
