/**
 * 播放器辅助逻辑（hls.js 只在这里动态 import，不进首屏包）。
 * 画质策略见 REQUIREMENTS FR-16：默认最高（含 4K，不设上限），手动切档并记忆。
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

/** 按 (height, bandwidth) 降序 */
export function sortVariantsDesc(variants: readonly PlaybackVariant[]): PlaybackVariant[] {
  return [...variants].sort((a, b) => b.height - a.height || b.bandwidth - a.bandwidth);
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
}

/** 找到与目标高度最接近的 hls 音视频流索引；-1 = 没有 */
export function levelIndexForHeight(levels: readonly LevelLike[], height: number): number {
  let best = -1;
  let bestDelta = Number.POSITIVE_INFINITY;
  levels.forEach((level, index) => {
    const delta = Math.abs(level.height - height);
    if (delta < bestDelta) {
      bestDelta = delta;
      best = index;
    }
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
