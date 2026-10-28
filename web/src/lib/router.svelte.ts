/**
 * 手写 history router（无依赖）。
 * - pushState / popstate
 * - 路径参数（:id）
 * - 404 兜底
 * - document.title 随路由更新
 * - 滚动位置：push 回到顶部，pop 还原
 */
export type RouteName =
  | 'feed'
  | 'performers'
  | 'performer'
  | 'discover'
  | 'downloads'
  | 'moments'
  | 'youtube'
  | 'bilibili'
  | 'manage'
  | 'video'
  | 'settings'
  | 'login';

type RouteDef = { name: RouteName; pattern: string; title: string };

const ROUTES: readonly RouteDef[] = [
  { name: 'feed', pattern: '/', title: '关注' },
  { name: 'performers', pattern: '/performers', title: '艺人' },
  { name: 'performer', pattern: '/performer/:id', title: '艺人' },
  { name: 'discover', pattern: '/discover', title: '发现艺人' },
  { name: 'downloads', pattern: '/downloads', title: '下载' },
  { name: 'moments', pattern: '/moments', title: '动态' },
  { name: 'youtube', pattern: '/youtube', title: 'YouTube' },
  { name: 'bilibili', pattern: '/bilibili', title: 'B 站' },
  { name: 'manage', pattern: '/manage', title: '管理' },
  { name: 'video', pattern: '/video/:id', title: '播放' },
  { name: 'settings', pattern: '/settings', title: '设置' },
  { name: 'login', pattern: '/login', title: '登录' },
];

export type RouteMatch =
  | { name: RouteName; params: Record<string, string>; title: string }
  | { name: 'not-found'; params: Record<string, string>; title: string };

const SITE = 'MyPorn';

const loc = $state({ path: window.location.pathname, search: window.location.search });

function normalize(path: string): string {
  const p = path.startsWith('/') ? path : `/${path}`;
  return p.length > 1 && p.endsWith('/') ? p.slice(0, -1) : p;
}

export function matchRoute(path: string): RouteMatch {
  const segs = normalize(path)
    .split('/')
    .filter((s) => s.length > 0);
  for (const route of ROUTES) {
    const pat = route.pattern.split('/').filter((s) => s.length > 0);
    if (pat.length !== segs.length) continue;
    const params: Record<string, string> = {};
    let ok = true;
    for (let i = 0; i < pat.length; i += 1) {
      const p = pat[i];
      const s = segs[i];
      if (p.startsWith(':')) params[p.slice(1)] = decodeURIComponent(s);
      else if (p !== s) {
        ok = false;
        break;
      }
    }
    if (ok) return { name: route.name, params, title: route.title };
  }
  return { name: 'not-found', params: {}, title: '页面不存在' };
}

export function setPageTitle(title: string): void {
  document.title = `${title} · ${SITE}`;
}

function applyTitle(m: RouteMatch): void {
  setPageTitle(m.title);
}

const scrollMemory = new Map<string, number>();

function key(path: string, search: string): string {
  return `${path}${search}`;
}

function commit(scrollY: number): void {
  const m = matchRoute(loc.path);
  applyTitle(m);
  window.scrollTo({ top: scrollY, behavior: 'instant' });
}

export function navigate(to: string, opts: { replace?: boolean } = {}): void {
  const url = new URL(to, window.location.origin);
  if (url.pathname === loc.path && url.search === loc.search) return;
  if (opts.replace) {
    window.history.replaceState({ scrollY: window.scrollY }, '', url.href);
  } else {
    scrollMemory.set(key(loc.path, loc.search), window.scrollY);
    window.history.pushState({ scrollY: 0 }, '', url.href);
  }
  loc.path = url.pathname;
  loc.search = url.search;
  commit(0);
}

export function back(): void {
  window.history.back();
}

export const router = {
  get path(): string {
    return loc.path;
  },
  get search(): string {
    return loc.search;
  },
  get query(): URLSearchParams {
    return new URLSearchParams(loc.search);
  },
  get match(): RouteMatch {
    return matchRoute(loc.path);
  },
  get isLogin(): boolean {
    return normalize(loc.path) === '/login';
  },
};

/** 安装全局监听：popstate + data-link 锚点拦截。只调用一次（main.ts）。 */
export function installRouter(): void {
  window.history.replaceState({ ...(window.history.state as object | null), scrollY: window.scrollY }, '');
  window.addEventListener('popstate', (event) => {
    const state = event.state as { scrollY?: number } | null;
    loc.path = window.location.pathname;
    loc.search = window.location.search;
    const target = state?.scrollY ?? scrollMemory.get(key(loc.path, loc.search)) ?? 0;
    commit(target);
  });
  document.addEventListener('click', (event) => {
    if (event.defaultPrevented || event.button !== 0) return;
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    const el = event.target instanceof Element ? event.target.closest('a') : null;
    if (!(el instanceof HTMLAnchorElement) || !el.hasAttribute('data-link')) return;
    if (el.target && el.target !== '_self') return;
    if (el.origin !== window.location.origin) return;
    event.preventDefault();
    navigate(`${el.pathname}${el.search}`);
  });
  applyTitle(matchRoute(loc.path));
}
