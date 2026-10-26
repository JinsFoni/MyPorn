/**
 * 全局浮层状态（runes 模块，无状态库）。
 * 设置是个弹窗而不是页面：侧边栏的齿轮、下载对话框里的「去创建库」都往这里发信号，
 * 由 App 里挂着的 <SettingsDialog /> 统一渲染，任何页面上都能盖出来。
 */
export type SettingsPane = 'sync' | 'network' | 'upload' | 'download' | 'libraries' | 'account';

class UiStore {
  settingsOpen = $state(false);
  settingsPane = $state<SettingsPane>('sync');

  openSettings(pane: SettingsPane = 'sync'): void {
    this.settingsPane = pane;
    this.settingsOpen = true;
  }

  closeSettings(): void {
    this.settingsOpen = false;
  }
}

export const ui = new UiStore();
