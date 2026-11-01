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
 *
 * **光看「锐度在哪儿断」不够**（2026-11-01 用户报的「B 站的横版视频起播画面被裁切成竖图加虚化背景」）：
 * 一张本来就很横的 16:9 封面，只要主体居中、两侧是虚焦/暗背景/纯色墙，锐度剖面长得跟烘出来的填充一模一样
 * ——边上糊、中间尖，①②两条都会给一个 0.22~0.36 的窄条。全库 342 张 B 站封面（全是 16:9 原画，
 * 任何非 null 都是误判）实测 **36 张被裁（10.5%）**，也就是说卡片墙和播放器里有一成横版封面被切成竖条。
 * 所以候选边界还要过两道「这真是站方烘的填充吗」的验收：
 *   - **接缝**：填充是贴在真画面旁边的，接缝那一列**整列都在跳**（灰度差 > 12 的行占 72%~100%，
 *     两侧都比出来的最小值 0.31~1.00）；自然画面即便有暗背景，边界也只有一部分行在跳
 *     （同一批 36 张误判：0~0.24，中位 0.07）。取「左右两条缝里窄的那条 ≥ 0.3」。
 *   - **压暗**：站方把填充乘过一档暗，实测填充/中间 = 0.24~0.33；自然照片两侧很少有这种整片压暗
 *     （36 张误判里只有 1 张到 0.26，其余 0.52~2.14）。取 ≤ 0.45。
 * 加完这两道：36 张误判 **0 张被裁**；YouTube 那 10 张里留 9 张（丢掉的那张 `XFJ8fCUOYRI`
 * 放大看是张正常的 16:9 照片，本来就该丢——它的接缝 0.21/0.37、填充亮度比 1.28 都不像烘的）。
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
/** 接缝上一列里算「跳了一下」的灰度差（烘的填充是硬切，实测两侧相邻行差 20~80） */
const SEAM_STEP = 12;
/** 接缝那一列至少要有这么多行在跳，才算「整列硬切」（烘的填充 0.31~1.00；36 张自然画面误判 0~0.24） */
const SEAM_ROWS = 0.3;
/** 填充区压暗后的亮度上限（相对中间那条）：实测站方烘的填充是 0.24~0.33 */
const FILL_DARK = 0.45;

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

  /**
   * 接缝列里「整列都在跳」的行占多少。烘出来的填充是**贴着**真画面贴上去的，接缝两侧是硬切，
   * 于是那一列从上到下都在跳；自然画面里的边界（人的轮廓、墙角的明暗）只在一部分行上跳。
   * 在候选列 ±4 内取最好的那一列：边界定得偏一两像素也不至于判错。
   */
  const seamRows = (edge: number): number => {
    let best = 0;
    for (let x = Math.max(1, edge - 4); x <= Math.min(AW - 1, edge + 4); x += 1) {
      let hit = 0;
      for (let y = 0; y < AH; y += 1) {
        if (Math.abs(gray[y * AW + x] - gray[y * AW + x - 1]) > SEAM_STEP) hit += 1;
      }
      const rows = hit / AH;
      if (rows > best) best = rows;
    }
    return best;
  };

  /** 一块矩形区域的平均灰度（只在有候选时才调，一次约 1ms） */
  const regionGray = (xa: number, xb: number): number => {
    let sum = 0;
    for (let y = 0; y < AH; y += 1) {
      for (let x = xa; x < xb; x += 1) sum += gray[y * AW + x];
    }
    return xb > xa ? sum / ((xb - xa) * AH) : 0;
  };

  /**
   * 候选窄条的两道验收（理由和实测数字见文件头）。都过不了就不裁——宁可按整张铺满。
   */
  const fromHalf = (half: number): CoverCrop | null => {
    const share = (half * 2) / AW;
    if (share < 0.22 || share > 0.36) return null;
    const x0 = Math.round(mid - half);
    const x1 = Math.round(mid + half);
    if (seamRows(x0) < SEAM_ROWS || seamRows(x1) < SEAM_ROWS) return null;
    const inside = regionGray(x0, x1);
    const outerWidth = AW - (x1 - x0);
    if (outerWidth <= 0) return null;
    const outside = (regionGray(0, x0) * x0 + regionGray(x1, AW) * (AW - x1)) / outerWidth;
    if (outside > inside * FILL_DARK) return null;
    return { share, x0: x0 / AW, x1: x1 / AW };
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
