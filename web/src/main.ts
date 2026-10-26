import './app.css';
import { mount } from 'svelte';
import App from './App.svelte';
import { installRouter } from './lib/router.svelte';
import { live } from './lib/live.svelte';

installRouter();

const target = document.getElementById('app');
if (!target) throw new Error('#app not found');

mount(App, { target });

// PWA：registerType 'prompt' —— 新版本就绪后只在应用内提示，不打断播放
if (import.meta.env.PROD && 'serviceWorker' in navigator) {
  void import('virtual:pwa-register')
    .then(({ registerSW }) => {
      registerSW({
        immediate: true,
        onNeedRefresh() {
          live.toast('有新版本，刷新页面后生效', 'info');
        },
        onOfflineReady() {
          live.toast('应用外壳已可离线打开', 'success');
        },
      });
    })
    .catch(() => {
      /* SW 注册失败（非安全上下文等）不影响使用 */
    });
}
