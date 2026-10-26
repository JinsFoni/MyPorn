/**
 * 全局滚动探针。
 *
 * 卡片在滚动经过时会不断触发 pointerenter —— 如果这时去解析播放地址、
 * 挂载预览视频，就会和滚动抢主线程/解码器，表现为掉帧。这里只维护一个
 * 共享的 passive 监听，代价接近 0，用来让上层「滚动中别做额外的事」。
 */

let lastScrollAt = 0;
let installed = false;

export function installScrollProbe(): void {
  if (installed) return;
  installed = true;
  lastScrollAt = performance.now();
  window.addEventListener(
    'scroll',
    () => {
      lastScrollAt = performance.now();
    },
    { passive: true, capture: true },
  );
}

/** 最近 withinMs 毫秒内有没有滚动过 */
export function scrollingRecently(withinMs = 260): boolean {
  return performance.now() - lastScrollAt < withinMs;
}
