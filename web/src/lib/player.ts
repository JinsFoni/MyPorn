/**
 * 播放器辅助逻辑（hls.js 只在这里动态 import，不进首屏包）。
 * 画质策略见 REQUIREMENTS FR-16：默认最高（含 4K，不设上限），手动切档并记忆。
 * 编码策略：分辨率优先，同分辨率内 AV1 > HEVC > VP9 > H.264（与后端 model.SortVariantsDesc 同口径）；
 * 浏览器解不了的编码排到最后，绝不让「优先 AV1」变成黑屏。
 */
import type { PlaybackInfo, PlaybackVariant } from './types';

type HlsCtor = typeof import('hls.js').default;

let hlsModule: Promise<HlsCtor> | null = null;

/** 动态加载 hls.js（仅视频页触发） */
export function loadHls(): Promise<HlsCtor> {
  hlsModule ??= import('hls.js').then((m) => m.default);
  return hlsModule;
}

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

export interface LevelLike {
  height: number;
  bitrate: number;
  /** hls.js 的 Level 上编码信息可能在 videoCodec / codecSet / attrs.CODECS 任一处 */
  codec?: string;
  videoCodec?: string;
  attrs?: Record<string, string>;
}

function levelCodec(level: LevelLike): string {
  return level.codec ?? level.videoCodec ?? level.attrs?.['CODECS'] ?? '';
}

/**
 * 找到与目标高度最接近的 hls 音视频流索引；-1 = 没有。
 * 同一高度有多个编码时（H.264 + AV1 各一条），选编码偏好靠前的那个。
 */
export function levelIndexForHeight(levels: readonly LevelLike[], height: number): number {
  let best = -1;
  let bestDelta = Number.POSITIVE_INFINITY;
  levels.forEach((level, index) => {
    const delta = Math.abs(level.height - height);
    if (delta > bestDelta) return;
    if (delta < bestDelta) {
      bestDelta = delta;
      best = index;
      return;
    }
    // 同高度：谁的编码偏好靠前用谁
    const rank = codecRankOf(levelCodec(level));
    const bestRank = best >= 0 ? codecRankOf(levelCodec(levels[best]!)) : 99;
    if (rank < bestRank) best = index;
  });
  return best;
}

/** Safari/iOS 原生支持 HLS，可跳过 hls.js */
export function supportsNativeHls(video: HTMLVideoElement): boolean {
  return video.canPlayType('application/vnd.apple.mpegurl') !== '';
}

export function prefersReducedMotion(): boolean {
  return window.matchMedia('(prefers-reduced-motion: reduce)').matches;
}

/** 有精确指针（鼠标）才做悬停预览，触屏不做 */
export function canHover(): boolean {
  return window.matchMedia('(hover: hover) and (pointer: fine)').matches;
}

export async function toggleFullscreen(el: HTMLElement): Promise<void> {
  try {
    if (document.fullscreenElement) await document.exitFullscreen();
    else await el.requestFullscreen();
  } catch {
    /* 用户拒绝或浏览器不支持，静默 */
  }
}
