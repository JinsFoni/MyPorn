/**
 * 滚动位置还原。
 *
 * 为什么不能只 `window.scrollTo(y)` 了事：关注 / B 站 / YouTube 三页退回时是整块重建的，
 * 组件重新挂载、InfiniteList 只拉了第一页，文档高度根本不到 y——`scrollTo` 会被夹到
 * 当前底部，看起来就是「退回顶部」。
 *
 * 所以这里不是滚一次，是「先滚到能到的位置，等列表把后面的页补上来再接着滚」，
 * 直到到位、或者再也长不高（列表到底了）、或者用户自己动了手（滚轮 / 触摸 / 按键 / 点按）。
 *
 * 到位之后还得再盯一会儿（HOLD）：卡片是 `content-visibility:auto` + 懒加载图片，
 * 分页里超出 maxLoadedPages 的中段页会被换成占位格子，文档高度在随后几百毫秒里还会变；
 * 高度一缩，浏览器就把 scrollY 一起夹回去。实测（2026-11-01，关注流 1440×900）：
 * 目标 4900 一帧就到了，但文档随后从 5927 缩到 5343，人被连带着弹到 4443——差一整排卡片。
 *
 * 会自己接上来的那半边在 InfiniteList 的 fillViewport：哨兵进了「视口下方 800px」就补页，
 * 所以只要本模块还停在底部不动，它就一页页补到够高为止。
 */

/** 连续多久没有任何「进展」（高度和位置都没变）就收手：分页接口慢的时候也够等 */
const STALL_MS = 2500;
/** 到位之后继续盯多久：这段时间里高度一变就把位置找回来 */
const HOLD_MS = 900;
/** 兜底上限，别让一个长不高的列表把 rAF 一直占着 */
const LIMIT_MS = 12000;

let target: number | null = null;
let frame = 0;
let startedAt = 0;
let progressAt = 0;
let holdFrom = 0;
let lastY = -1;
let lastHeight = -1;

function stop(): void {
  if (frame !== 0) cancelAnimationFrame(frame);
  frame = 0;
  target = null;
  holdFrom = 0;
  window.removeEventListener('wheel', interrupt, true);
  window.removeEventListener('touchstart', interrupt, true);
  window.removeEventListener('keydown', interrupt, true);
  window.removeEventListener('pointerdown', interrupt, true);
}

/** 人一动就把控制权交还：还原是为了「别让人白跑一趟」，不是把人按在原地 */
function interrupt(): void {
  stop();
}

function step(): void {
  frame = 0;
  if (target === null) return;
  const now = performance.now();
  const max = Math.max(0, document.documentElement.scrollHeight - window.innerHeight);
  const want = Math.min(target, max);
  if (Math.abs(window.scrollY - want) > 1) window.scrollTo({ top: want, behavior: 'instant' });
  const y = window.scrollY;
  const height = document.documentElement.scrollHeight;
  if (y !== lastY || height !== lastHeight) {
    lastY = y;
    lastHeight = height;
    progressAt = now;
  }
  if (Math.abs(y - target) <= 1) {
    if (holdFrom === 0) holdFrom = now;
    // 到位、且高度也稳住了，才算完
    if (now - holdFrom >= HOLD_MS && now - progressAt >= HOLD_MS) {
      stop();
      return;
    }
  } else {
    holdFrom = 0;
  }
  if (now - startedAt > LIMIT_MS || now - progressAt > STALL_MS) {
    stop();
    return;
  }
  frame = requestAnimationFrame(step);
}

/** 滚到 y（0 = 直接回顶）；内容还不够高时会一路追着补 */
export function restoreScroll(y: number): void {
  stop();
  if (y <= 0) {
    window.scrollTo({ top: 0, behavior: 'instant' });
    return;
  }
  target = Math.round(y);
  startedAt = performance.now();
  progressAt = startedAt;
  lastY = -1;
  lastHeight = -1;
  window.addEventListener('wheel', interrupt, { capture: true, passive: true });
  window.addEventListener('touchstart', interrupt, { capture: true, passive: true });
  window.addEventListener('keydown', interrupt, true);
  window.addEventListener('pointerdown', interrupt, true);
  frame = requestAnimationFrame(step);
}
