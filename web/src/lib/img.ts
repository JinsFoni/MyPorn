/**
 * 图片 URL 规范化：远端地址一律走本地代理 /api/img?u=
 * 契约 §4：后端带 Referer/UA 抓取 + 磁盘缓存，失败给占位图（不会 4xx）。
 *
 * 隐私模式在这里单独走一条：不要 `w=`（显示宽度），改成 `s=`（小糊图的边长），
 * 让后端把封面缩到一百来像素、糊一遍再发回来——浏览器只要贴一张几 KB 的小纹理、
 * 自己拉伸成卡片大小就够糊了。
 *
 * 为什么不给大图套 `filter: blur()`：那等于给每张图各开一个合成层再做一遍高斯模糊，
 * 滚动时新进视口的卡片要重新 raster 一遍（Chrome trace 实测 raster 时间多出近一半），
 * 表现就是掉帧。缩图这条路把成本全挪到「一次、在服务器上、之后一直有缓存」。
 */
import { privacy } from './privacy.svelte';

const PROXY_PATH = '/api/img';

/**
 * 小糊图的流水线版本，跟着 `internal/imgcache` 的 `smallPipeline` 一起 +1。
 * 加进 URL 只为了一件事：换了生成方式（JPEG → PNG、改了缩放/模糊）以后，
 * 浏览器 HTTP 缓存里那份旧图别因为 `s=` 没变就继续被命中。
 */
const SMALL_PIPELINE = '2';

/**
 * 隐私模式要的小糊图宽度：**跟着显示宽度走**，固定值会翻车——
 * 一个尺寸给大封面是 4 倍放大（够糊），给 72px 的小头像就只有 1.5 倍（还认得出脸）。
 * 所以取显示宽度的四分之一，再夹在 16–160 之间
 * （16 是「36px 的小头像也糊成色块」的下限，160 是「大封面别糊到只剩纯色」的上限）。
 *
 * 早先取的是十分之一（48px 铺到 480px），糊是够糊，但小图太小，拉大后色块之间
 * 能看出**色带断层**；改成四分之一后后端按同样比例加大模糊半径（宽/12），
 * 糊的程度不变、却是平滑的过渡。见 internal/imgcache/blur.go。
 */
function privacyWidth(width?: number): number {
  const w = width && width > 0 ? Math.round(width / 4) : 16;
  return Math.min(160, Math.max(16, w));
}

export function imageUrl(raw: string | null | undefined, width?: number): string | null {
  if (!raw) return null;
  const small = privacy.on;

  // 后端拼好的代理地址：只改查询参数
  if (raw.startsWith(`${PROXY_PATH}?`)) {
    const params = new URLSearchParams(raw.slice(PROXY_PATH.length + 1));
    if (small) {
      params.set('s', String(privacyWidth(width)));
      params.set('sv', SMALL_PIPELINE);
      params.delete('w');
    } else if (width) {
      params.set('w', String(width));
    }
    return `${PROXY_PATH}?${params.toString()}`;
  }

  // 站内相对路径（后端已代理的相对链接）直接用；它们不是远端封面，没有小图版本
  if (raw.startsWith('/') && !raw.startsWith('//')) {
    if (small || !width) return raw;
    return `${raw}${raw.includes('?') ? '&' : '?'}w=${String(width)}`;
  }

  const proxied = `${PROXY_PATH}?u=${encodeURIComponent(raw)}`;
  if (small) return `${proxied}&s=${String(privacyWidth(width))}&sv=${SMALL_PIPELINE}`;
  return width ? `${proxied}&w=${String(width)}` : proxied;
}

/** 响应式候选宽度，给缩略图用（移动 2 列 ~200px，桌面 5 列 ~320px） */
export function thumbWidthForColumns(columns: number): number {
  if (columns <= 2) return 360;
  if (columns === 3) return 480;
  if (columns === 4) return 640;
  return 720;
}
