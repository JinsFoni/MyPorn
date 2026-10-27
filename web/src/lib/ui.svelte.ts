/**
 * 全局浮层状态（runes 模块，无状态库）。
 *
 * 设置是「URL 上的弹窗」：面板本身盖在任意页面上，但地址栏是 `/settings`
 * （分类写在 hash 里，如 `/settings#libraries`），所以能收藏、能直接分享、
 * 刷新后还在原来的分类上。侧边栏齿轮、下载页「管理本地库」、下载对话框的
 * 「去创建库」都只是调用 `openSettings(pane)`。
 */
import { navigate, router } from './router.svelte';

export type SettingsPane =
  | 'sync'
  | 'network'
  | 'upload'
  | 'download'
  | 'social'
  | 'youtube'
  | 'libraries'
  | 'account';

const PANE_IDS: readonly SettingsPane[] = [
  'sync',
  'network',
  'upload',
  'download',
  'social',
  'youtube',
  'libraries',
  'account',
];

/** `/settings#upload` → 'upload'；hash 不认识就用同步 */
export function paneFromHash(hash: string): SettingsPane {
  const id = hash.replace(/^#/, '');
  return PANE_IDS.find((p) => p === id) ?? 'sync';
}

function settingsPath(pane: SettingsPane): string {
  return pane === 'sync' ? '/settings' : `/settings#${pane}`;
}

class UiStore {
  settingsOpen = $state(false);
  settingsPane = $state<SettingsPane>('sync');
  /** 关闭时回到哪一页（打开弹窗前所在的路径） */
  private from = '/';

  /** 按钮入口：改地址栏 + 打开弹窗（push 一条历史，浏览器后退能直接关掉） */
  openSettings(pane: SettingsPane = 'sync'): void {
    if (router.path !== '/settings') this.from = router.path;
    this.settingsPane = pane;
    this.settingsOpen = true;
    navigate(settingsPath(pane));
  }

  /** 关闭入口（Esc / 遮罩 / ✕ / 页脚）：回到打开前的那一页，地址栏不留 /settings */
  closeSettings(): void {
    this.settingsOpen = false;
    if (router.path !== '/settings') return;
    const back = this.from === '/settings' ? '/' : this.from;
    navigate(back, { replace: true });
  }

  /** 路由同步用：地址变成 /settings 时把弹窗打开（刷新、直接访问、前进后退都会走到这） */
  openFromRoute(pane: SettingsPane): void {
    this.settingsPane = pane;
    this.settingsOpen = true;
  }

  /** 路由同步用：地址不再是 /settings 时静默关掉，别再导航 */
  closeFromRoute(): void {
    this.settingsOpen = false;
  }

  /** 切换分类：地址栏同步（replace，不堆历史），方便直接分享某一类 */
  setPane(pane: SettingsPane): void {
    this.settingsPane = pane;
    if (!this.settingsOpen || router.path !== '/settings') return;
    const url = settingsPath(pane);
    if (`${window.location.pathname}${window.location.hash}` === url) return;
    window.history.replaceState(window.history.state, '', url);
  }
}

export const ui = new UiStore();
