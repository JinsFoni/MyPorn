/**
 * 图片 URL 规范化：远端地址一律走本地代理 /api/img?u=
 * 契约 §4：后端带 Referer/UA 抓取 + 磁盘缓存，失败给占位图（不会 4xx）。
 */

const PROXY_PATH = '/api/img';

export function imageUrl(raw: string | null | undefined, width?: number): string | null {
  if (!raw) return null;
  if (raw.startsWith(`${PROXY_PATH}?`)) {
    if (!width) return raw;
    return raw.includes('w=') ? raw : `${raw}&w=${String(width)}`;
  }
  // 站内相对路径（后端已代理的相对链接）直接用
  if (raw.startsWith('/') && !raw.startsWith('//')) {
    if (!width) return raw;
    return `${raw}${raw.includes('?') ? '&' : '?'}w=${String(width)}`;
  }
  const proxied = `${PROXY_PATH}?u=${encodeURIComponent(raw)}`;
  return width ? `${proxied}&w=${String(width)}` : proxied;
}

/** 响应式候选宽度，给缩略图用（移动 2 列 ~200px，桌面 5 列 ~320px） */
export function thumbWidthForColumns(columns: number): number {
  if (columns <= 2) return 360;
  if (columns === 3) return 480;
  if (columns === 4) return 640;
  return 720;
}
