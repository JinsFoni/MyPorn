/**
 * 播放器辅助逻辑：画质/编码策略 + 片源种类判定。
 * 播放器本体是 Video.js 10（`<video-player>` + `<video-skin>`，见 components/VideoPlayer.svelte），
 * 清晰度仍然由后端返回的变体列表驱动（每个变体一条独立清单/直链），所以这里保留的是「选哪条」的规则。
 *
 * 画质策略见 REQUIREMENTS FR-16：默认最高（含 4K，不设上限），手动切档并记忆。
 * 编码策略：分辨率优先，同分辨率内 AV1 > HEVC > VP9 > H.264（与后端 model.SortVariantsDesc 同口径）；
 * 浏览器解不了的编码排到最后，绝不让「优先 AV1」变成黑屏。
 */
import type { PlaybackInfo, PlaybackVariant } from './types';

const QUALITY_KEY = 'myporn.quality.height';

/** 记忆的画质高度（px），null = 无偏好 → 用最高 */
export function rememberedHeight(): number | null {
  try {
    const raw = window.localStorage.getItem(QUALITY_KEY);
    if (!raw) return null;
    const n = Number.parseInt(raw, 10);
    return Number.isFinite(n) && n > 0 ? n : null;
  } catch {
    return null;
  }
}

export function rememberHeight(height: number): void {
  try {
    window.localStorage.setItem(QUALITY_KEY, String(height));
  } catch {
    /* 隐私模式下 localStorage 可能不可写，忽略 */
  }
}

// ---------- 片源种类 ----------

/** Video.js 里 HLS 的 MIME（`application/x-mpegURL` 是它的别名，两种都认） */
export const HLS_TYPE = 'application/vnd.apple.mpegurl';
export const MP4_TYPE = 'video/mp4';

/**
 * 这一批变体要不要走 hls.js。
 * 只要有一条是 HLS 就用 `<hlsjs-video>`（它放 mp4 时退回原生解码），
 * 全都不是才用原生 `<video>`——那样连 hls.js 都不用下载。
 */
export function needsHlsEngine(variants: readonly PlaybackVariant[]): boolean {
  return variants.some((v) => v.hls);
}

/** 变体 → Video.js `source.type` */
export function sourceTypeFor(isHls: boolean): string {
  return isHls ? HLS_TYPE : MP4_TYPE;
}

// ---------- 编码偏好 ----------

export type CodecFamily = '' | 'av1' | 'hevc' | 'vp9' | 'avc';

/** 归一化 HLS 的 CODECS 字符串（avc1.64001f,mp4a.40.2 → avc），与后端 model.CodecFamily 同口径 */
export function codecFamily(codec: string): CodecFamily {
  // 一个 CODECS 里可能同时有视频和音频，只看第一段
  const c = codec.split(',')[0]?.trim().toLowerCase() ?? '';
  if (c === '') return '';
  if (c.startsWith('av01') || c.startsWith('av1')) return 'av1';
  if (c.startsWith('hev1') || c.startsWith('hvc1') || c.startsWith('hevc')) return 'hevc';
  if (c.startsWith('vp09') || c.startsWith('vp9')) return 'vp9';
  if (c.startsWith('avc1') || c.startsWith('avc3') || c.startsWith('avc')) return 'avc';
  return '';
}

/** 编码族的兜底 codec 串：平台只给了族名时也能拿去问浏览器 */
const CODEC_PROBE: Record<CodecFamily, string> = {
  '': '',
  av1: 'av01.0.05M.08',
  hevc: 'hev1.1.6.L93.B0',
  vp9: 'vp09.00.10.08',
  avc: 'avc1.42E01E',
};

const decodeSupport = new Map<string, boolean>();

/**
 * 这台设备能不能解这个编码。
 * AV1 在 Safari 17.4 之前、部分安卓机与老显卡上没有硬解，HEVC 也常被浏览器关掉，
 * 所以「优先 AV1」必须过这一关。未知编码按 H.264 对待（不因缺信息把片源废掉）。
 */
export function canDecodeCodec(codec: string): boolean {
  const family = codecFamily(codec);
  if (family === '' || family === 'avc') return true;
  const cached = decodeSupport.get(family);
  if (cached !== undefined) return cached;

  const probe = codec === '' || codec.includes(',') ? CODEC_PROBE[family] : codec.split(',')[0]!.trim();
  const mime = `video/mp4; codecs="${probe}"`;
  let ok = false;
  try {
    const ms = window.MediaSource;
    if (ms && typeof ms.isTypeSupported === 'function') ok = ms.isTypeSupported(mime);
    else ok = document.createElement('video').canPlayType(mime) !== '';
  } catch {
    ok = false;
  }
  decodeSupport.set(family, ok);
  return ok;
}

/** 编码角标（H.264 不显示，那是默认）；解不了的返回 null */
export function codecTag(variant: PlaybackVariant): string | null {
  switch (codecFamily(variant.codec)) {
    case 'av1':
      return canDecodeCodec(variant.codec) ? 'AV1' : null;
    case 'hevc':
      return canDecodeCodec(variant.codec) ? 'HEVC' : null;
    case 'vp9':
      return 'VP9';
    default:
      return null;
  }
}

/** 同分辨率内的编码偏好序，数字越小越优先；解不了的排到最后（9） */
function codecRankOf(codec: string): number {
  switch (codecFamily(codec)) {
    case 'av1':
      return canDecodeCodec(codec) ? 0 : 9;
    case 'hevc':
      return canDecodeCodec(codec) ? 1 : 8;
    case 'vp9':
      return 2;
    default:
      return 3;
  }
}

/** 按 (height, 编码偏好, bandwidth) 降序 */
export function sortVariantsDesc(variants: readonly PlaybackVariant[]): PlaybackVariant[] {
  return [...variants].sort(
    (a, b) =>
      b.height - a.height || codecRankOf(a.codec) - codecRankOf(b.codec) || b.bandwidth - a.bandwidth,
  );
}

/**
 * 选默认变体：优先用户记忆的画质 → 契约里的 defaultVariantId → 最高的一个。
 */
export function pickVariant(info: PlaybackInfo, preferredHeight: number | null = rememberedHeight()): PlaybackVariant | null {
  const sorted = sortVariantsDesc(info.variants);
  if (sorted.length === 0) return null;
  if (preferredHeight !== null) {
    const remembered = sorted.find((v) => v.height === preferredHeight);
    if (remembered) return remembered;
  }
  const byContract = sorted.find((v) => v.id === info.defaultVariantId);
  return byContract ?? sorted[0];
}

export function variantLabel(variant: PlaybackVariant): string {
  if (variant.height >= 2160) return '4K';
  return `${String(variant.height)}p`;
}

export function prefersReducedMotion(): boolean {
  return window.matchMedia('(prefers-reduced-motion: reduce)').matches;
}

/** 有精确指针（鼠标）才做悬停预览，触屏不做 */
export function canHover(): boolean {
  return window.matchMedia('(hover: hover) and (pointer: fine)').matches;
}
