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
const SMALL_PIPELINE = '3';

/**
 * 隐私模式要的小糊图宽度：**跟着显示宽度走**，固定值会翻车——
 * 一个尺寸给大封面是 4 倍放大（够糊），给 72px 的小头像就只有 1.5 倍（还认得出脸）。
 * 所以取显示宽度的四分之一，再夹在 16–160 之间
 * （16 是「36px 的小头像也糊成色块」的下限，160 是「大封面别糊到只剩纯色」的上限）。
 *
 * 早先取的是十分之一（48px 铺到 480px），糊是够糊，但小图太小，拉大后色块之间
 * 能看出**色带断层**；改成四分之一后后端按同样比例加大模糊半径（宽/18、叠三遍），
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

/**
 * 同一张图的「糊版」小图 URL：照片墙上给留白垫底用（2026-11-03）。
 *
 * 为什么不用 CSS `filter: blur()` 做垫底：和隐私模式同一个理由（见文件头）——每张图各开一个
 * 合成层再模糊，滚动时新进视口的格子要重新 raster。这里直接问后端要 `s=`：它把原图缩到几十像素、
 * 盒式模糊三遍、编成 PNG（一两 KB），浏览器把它拉伸铺满格子，糊得均匀又不吃 CPU。
 *
 * `s` 取显示宽度的 1/4（夹在 32–160，和隐私模式同一个比例）：一开始试的 1/8（480→60px）
 * 糊到只剩一块死板的纯色，看着像「格子里垫了块色块」而不是「照片自己的虚影」——放大到 120px
 * 才留得住构图（实测 60px 时那张草地横图整块墨绿，120px 能看出人和草）。
 */
export function blurredImageUrl(raw: string | null | undefined, width?: number): string | null {
  if (!raw) return null;
  const s = String(backdropWidth(width));

  if (raw.startsWith(`${PROXY_PATH}?`)) {
    const params = new URLSearchParams(raw.slice(PROXY_PATH.length + 1));
    params.set('s', s);
    params.set('sv', SMALL_PIPELINE);
    params.delete('w');
    return `${PROXY_PATH}?${params.toString()}`;
  }

  // 站内相对路径（已落盘的小图）没有 `s=` 那条流水线，直接拿原图凑合，别拼坏地址。
  if (raw.startsWith('/') && !raw.startsWith('//')) return raw;

  return `${PROXY_PATH}?u=${encodeURIComponent(raw)}&s=${s}&sv=${SMALL_PIPELINE}`;
}

/** 垫底小图的宽度：显示宽度的四分之一，夹在 32–160（太小糊成色块，太大就没虚化感）。 */
function backdropWidth(width?: number): number {
  const w = width && width > 0 ? Math.round(width / 4) : 96;
  return Math.min(160, Math.max(32, w));
}

/** 响应式候选宽度，给缩略图用（移动 2 列 ~200px，桌面 5 列 ~320px） */
export function thumbWidthForColumns(columns: number): number {
  if (columns <= 2) return 360;
  if (columns === 3) return 480;
  if (columns === 4) return 640;
  return 720;
}
