/**
 * 手写 history router（无依赖）。
 * - pushState / popstate
 * - 路径参数（:id）
 * - 404 兜底
 * - document.title 随路由更新
 * - 滚动位置：换页回顶，「返回」回原位，只改查询串（页内筛选）不动
 *
 * 滚动这块的三个坑（2026-11-01 全部实测过，见 ARCHITECTURE.md）：
 *  · 历史状态里的 `scrollY` 必须在**离开那一条历史记录之前**写好。浏览器不替你记，
 *    push 时塞个 0 进去，退回就是 0——原来「退出播放回顶部」正是这么来的。
 *  · 目标页是重建的（InfiniteList 只拉了第一页），一次 `scrollTo` 会被夹到底，
 *    所以要交给 lib/scrollRestore.ts 追着补，不是滚一次。
 *  · 只改查询串时**不能**滚：墙上的筛选胶囊（`?up=` / `?channel=` / `?mode=`）就是只改查询串，
 *    原来换一次筛选就滚一次顶。（YouTube 播放曾经也是这样：弹窗是 `?v=<id>`，
 *    2026-11-02 换成独立页 `/youtube/:videoId` 之后那条路没有了，机制留着。）
 */
import { restoreScroll } from './scrollRestore';
export type RouteName =
  | 'feed'
  | 'performers'
  | 'performer'
  | 'discover'
  | 'downloads'
  | 'library'
  | 'moments'
  | 'youtube'
  | 'ytvideo'
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
  // 媒体库：磁盘上已经下载下来的影片（和 /downloads 那个任务队列不是一回事）
  { name: 'library', pattern: '/library', title: '媒体库' },
  { name: 'moments', pattern: '/moments', title: '动态' },
  { name: 'youtube', pattern: '/youtube', title: 'YouTube' },
  // 单条 YouTube 视频：和 `/video/:id` 一样是整页播放（2026-11-02 从墙上的弹窗搬过来的）。
  // 参数是 YouTube 自己的视频 id（`dQw4w9WgXcQ` 那种字符串），不是 `yt_videos` 的行 id。
  { name: 'ytvideo', pattern: '/youtube/:videoId', title: '播放' },
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

/** 每个「路径+查询串」上一次停在哪（同一页来回切时也要能回去） */
const scrollMemory = new Map<string, number>();

function key(path: string, search: string): string {
  return `${path}${search}`;
}

/** 把当前位置写进**这一条**历史记录，浏览器前进/后退时才拿得到 */
function writeScrollState(y: number): void {
  const state = (window.history.state ?? {}) as Record<string, unknown>;
  if (state.scrollY === y) return;
  window.history.replaceState({ ...state, scrollY: y }, '', window.location.href);
}

const idle = { timer: 0 };
/**
 * 滚动停下（150ms 没有新事件）才往历史状态里写一次。
 * 不在每个 scroll 事件里写：Safari 对 `replaceState` 有频率限制（30 秒 100 次左右），
 * 长距离滚动一秒就能超；而真正要用到这个值的时刻只有「离开这一页」，
 * 平滑滚动到一半的位置也不值得记。navigate 里还会再补一次即时写入。
 */
function onScroll(): void {
  window.clearTimeout(idle.timer);
  idle.timer = window.setTimeout(() => {
    const y = Math.round(window.scrollY);
    scrollMemory.set(key(loc.path, loc.search), y);
    writeScrollState(y);
  }, 150);
}

/**
 * `scrollY === null` = 位置不动（只换查询串）。
 * 不是 0 也不是「跳过去」，是「别碰」——包括别取消正在进行的还原。
 */
function commit(scrollY: number | null): void {
  applyTitle(matchRoute(loc.path));
  if (scrollY === null) return;
  restoreScroll(scrollY);
}

export function navigate(
  to: string,
  opts: { replace?: boolean; restore?: boolean; keepScroll?: boolean } = {},
): void {
  const url = new URL(to, window.location.origin);
  if (url.pathname === loc.path && url.search === loc.search) return;
  const y = Math.round(window.scrollY);
  // 同一页只换查询串（页内筛选）：DOM 没重建，位置就不该动
  const keep = opts.keepScroll === true && url.pathname === loc.path;
  // 「离开的这一条」先把真实位置记下来：push 进去的 0 是给新页用的，别让它顶掉旧页的
  scrollMemory.set(key(loc.path, loc.search), y);
  writeScrollState(y);
  const target = keep ? y : opts.restore ? (scrollMemory.get(key(url.pathname, url.search)) ?? 0) : 0;
  if (opts.replace) {
    window.history.replaceState({ scrollY: target }, '', url.href);
  } else {
    window.history.pushState({ scrollY: target }, '', url.href);
  }
  loc.path = url.pathname;
  loc.search = url.search;
  commit(keep ? null : target);
}

/**
 * 只改查询串、保留其它参数（默认 replace，不占历史）。
 *
 * 页内筛选（`/bilibili?up=<id>&mode=unseen`、`/youtube?channel=<id>&mode=saved&v=<videoId>`）
 * 都靠它写进地址栏：换筛选条件不该在历史里留一串垃圾，但**必须留在 URL 上**——
 * B 站点开影片是整页跳转（`/video/:id`），退回时组件会重建，只有地址栏还记得你选了谁。
 */
export function patchQuery(
  patch: Record<string, string | null>,
  opts: { replace?: boolean; keepScroll?: boolean } = {},
): void {
  const q = new URLSearchParams(loc.search);
  for (const [k, v] of Object.entries(patch)) {
    if (v === null || v === '') q.delete(k);
    else q.set(k, v);
  }
  const search = q.toString();
  navigate(search === '' ? loc.path : `${loc.path}?${search}`, {
    replace: opts.replace ?? true,
    keepScroll: opts.keepScroll,
  });
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
  // 滚动位置边滚边记：后退/前进全靠它。`scroll` 用 passive，捕获阶段是为了
  // 顺带能听到内部滚动容器的（目前没有，留着不亏）。
  window.addEventListener('scroll', onScroll, { passive: true, capture: true });
  // 关页面前把最后的位置落库（Safari 的 bfcache 会把页面冻住，不动手就丢）
  window.addEventListener('pagehide', () => {
    const y = Math.round(window.scrollY);
    scrollMemory.set(key(loc.path, loc.search), y);
    writeScrollState(y);
  });
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
    // data-restore：这一跳是「退回原来那一页」，要接着原来的滚动位置
    // （影片页的「返回 B 站 / 返回关注」用它；普通链接一律回顶）
    navigate(`${el.pathname}${el.search}`, { restore: el.hasAttribute('data-restore') });
  });
  applyTitle(matchRoute(loc.path));
}
