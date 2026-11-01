/**
 * 竖屏封面的「真画面那一竖条」探测。
 *
 * 为什么需要：YouTube 给竖屏视频出的 `maxresdefault` / `hq720` 都是 **1280×720 的 16:9 画布**——
 * 竖画面居中，两侧是站方自己拿整帧拉过去、糊过又压暗的填充。本机量了最近 8 张封面：
 * 两侧平均亮度 7~53、中间 72~164，所以在播放器里（1259px 宽）看着就是一圈半黑不黑、
 * 还带着拉长人形的边，跟播放起来之后两侧那层流光完全不是一回事；卡片上则是「两侧一片发暗」。
 *
 * 判据用**锐度**：填充区糊过，高频被抹平；中间那一竖条是真画面。取列方向拉普拉斯绝对值的
 * 均值当锐度，必须在**原尺寸**上算——缩到 640 再量过一遍，糊的那圈还留着细节，
 * 同一个算法有 8/12 张探错（0.40~0.82，边界全跑偏）。原尺寸一次约 6ms（1280×720，
 * 读像素 1.6ms + 算 4.5ms），卡片按 URL 缓存结果，滚动时每张只算一次。
 *
 * 边界怎么定（踩过两版）：
 *   ① **最外侧的强极大值**：真画面与填充之间是硬切口，锐度在那一列是个尖峰（实测 7.1 / 11.6），
 *      而填充自己靠里那一段是被「拉过去又压暗」的缓坡（锐度 1~3）。取中间 10% 的峰值 C 的 40%
 *      （下限 2）当门槛，从中心往外找**最外侧**仍超过门槛的局部极大——这一版把 12 张里的 12 张都定到
 *      0.294~0.316，跟 yt-dlp 报的真实画幅（2026×3840 → 0.297、2160×3840 → 0.316）对得上。
 *   ② 兜底用「从两侧往里第一根过 2.5 的列 + ±4% 收到硬切口」：对锐度很低的封面（雪白背景、
 *      大面积虚焦）①可能找不到极大值，②还能给个大概。
 * 两版都要求落在 22%~36%（9:16 = 0.316）之外就判「不算窄条」——16:9 的原画封面（占满整幅）
 * 和 4:5 这类更宽的竖屏一律返回 null：宁可按老样子整张铺满，也不能把封面裁掉一块。
 * 实测 18 张封面：16 张探到窄条且落在 0.294~0.316，两张 16:9 原画（3840×2160、横屏）判为不裁。
 */

export interface CoverCrop {
  /** 真画面那一竖条占整幅宽度的比例（0~1） */
  share: number;
  /** 同样的位置换成 0~1 的左右边界，画布裁图时用 */
  x0: number;
  x1: number;
}

/** 探测用的画布只建一次：卡片滚动起来一屏十几张，每次新建会白扔 3.7MB 的 ImageData 缓冲 */
let probeCanvas: HTMLCanvasElement | null = null;
/** 已经探过的封面（键是图片地址）：同一张封面在墙、频道页、搜索结果里反复出现 */
const cache = new Map<string, CoverCrop | null>();

/** 没缓存就探一次；探过（哪怕是「探不出」）就不再重算 */
export function coverCropFor(src: string, img: HTMLImageElement): CoverCrop | null {
  const hit = cache.get(src);
  if (hit !== undefined) return hit;
  const crop = findCoverCrop(img);
  cache.set(src, crop);
  return crop;
}

/** 图片是不是「16:9 画布」：不是就不用探（竖版原图、4:3 缩略图里本来就没有烘进去的填充） */
export function isWideFrame(img: HTMLImageElement): boolean {
  if (img.naturalWidth === 0 || img.naturalHeight === 0) return false;
  const ratio = img.naturalWidth / img.naturalHeight;
  return ratio > 1.6 && ratio < 1.95;
}

export function findCoverCrop(img: HTMLImageElement): CoverCrop | null {
  const AW = Math.min(img.naturalWidth, 1280);
  const AH = Math.round((img.naturalHeight * AW) / img.naturalWidth);
  if (AW < 64 || AH < 64) return null;
  const probe = (probeCanvas ??= document.createElement('canvas'));
  probe.width = AW;
  probe.height = AH;
  const ctx = probe.getContext('2d', { willReadFrequently: true });
  if (!ctx) return null;
  ctx.clearRect(0, 0, AW, AH);
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

  const mid = AW / 2;
  const fromHalf = (half: number): CoverCrop | null => {
    const share = (half * 2) / AW;
    if (share < 0.22 || share > 0.36) return null;
    return { share, x0: (mid - half) / AW, x1: (mid + half) / AW };
  };

  // ① 从中心往外找最外侧的强极大值
  let centerPeak = 0;
  for (let x = Math.round(AW * 0.45); x <= Math.round(AW * 0.55); x += 1) {
    if (smooth[x] > centerPeak) centerPeak = smooth[x];
  }
  const strong = Math.max(2, centerPeak * 0.4);
  const isLocalMax = (x: number): boolean => smooth[x] >= smooth[x - 1] && smooth[x] >= smooth[x + 1];
  let leftPeak = -1;
  let rightPeak = -1;
  for (let x = Math.round(mid) - 1; x >= Math.round(AW * 0.1); x -= 1) {
    if (smooth[x] >= strong && isLocalMax(x)) leftPeak = x;
  }
  for (let x = Math.round(mid) + 1; x <= Math.round(AW * 0.9); x += 1) {
    if (smooth[x] >= strong && isLocalMax(x)) rightPeak = x;
  }
  if (leftPeak >= 0 && rightPeak >= 0) {
    const found = fromHalf(Math.min(mid - leftPeak, rightPeak - mid));
    if (found) return found;
  }

  // ② 兜底：两侧往里第一根过 2.5 的列，再对硬切口收 ±4%
  let left = -1;
  let right = -1;
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
  // YouTube 的填充永远左右对称：取两半里窄的那半，宽的那半报多了也不影响
  return fromHalf(Math.min(mid - edgeNear(left), edgeNear(right) - mid));
}
