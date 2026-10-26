<script lang="ts">
  import { onMount } from 'svelte';
  import type { Snippet } from 'svelte';
  import TopBar from './TopBar.svelte';
  import Toasts from './Toasts.svelte';
  import { live } from '../lib/live.svelte';
  import { installScrollProbe } from '../lib/scroll';
  import { connectEvents } from '../lib/sse';
  import { router } from '../lib/router.svelte';
  import { ui } from '../lib/ui.svelte';
  import type { JobType } from '../lib/types';

  interface Props {
    title: string;
    children: Snippet;
  }

  let { title, children }: Props = $props();

  interface NavItem {
    href: string;
    label: string;
    icon: 'feed' | 'performers' | 'discover' | 'downloads' | 'settings';
    isActive: (path: string) => boolean;
  }

  const NAV: readonly NavItem[] = [
    { href: '/', label: '关注', icon: 'feed', isActive: (p) => p === '/' || p.startsWith('/video') },
    { href: '/performers', label: '艺人', icon: 'performers', isActive: (p) => p.startsWith('/performer') },
    { href: '/discover', label: '发现', icon: 'discover', isActive: (p) => p.startsWith('/discover') },
    { href: '/downloads', label: '下载', icon: 'downloads', isActive: (p) => p.startsWith('/downloads') },
    { href: '/settings', label: '设置', icon: 'settings', isActive: (p) => p.startsWith('/settings') },
  ];

  const JOB_LABEL: Record<JobType, string> = {
    sync: '同步',
    download: '下载',
    upload: '上传',
    strm: '生成 strm',
  };

  const activeHref = $derived(NAV.find((item) => item.isActive(router.path))?.href ?? '');

  // 设置不进侧边列表，单独摆在侧边栏右下角（只显示齿轮图标）；移动端底部 tab 仍保留五项
  const SIDE_NAV = NAV.filter((item) => item.icon !== 'settings');
  const settingsActive = $derived(ui.settingsOpen);

  /** 移动端底部 tab 的共用样式 */
  const TAB_CLASS = (active: boolean): string =>
    `flex flex-col items-center gap-1 pb-2 pt-2.5 transition-colors ${active ? 'text-fg' : 'text-dim'}`;

  onMount(() => {
    installScrollProbe();
    let bumpTimer = 0;
    const scheduleBump = (): void => {
      if (bumpTimer !== 0) return;
      bumpTimer = window.setTimeout(() => {
        bumpTimer = 0;
        live.bumpFeed();
      }, 1200);
    };

    const stop = connectEvents(
      {
        'job.updated': (job) => {
          live.applyJobEvent(job);
          if (job.status === 'done' && job.type === 'sync') {
            live.bumpFeed();
            live.toast('同步完成', 'success');
          } else if (job.status === 'done' && job.type === 'download') {
            live.toast('下载完成', 'success');
          } else if (job.status === 'failed') {
            const reason = job.error ? `：${job.error}` : '';
            live.toast(`${JOB_LABEL[job.type]}失败${reason}`, 'error');
          }
        },
        'sync.progress': (progress) => {
          live.applySync(progress);
        },
        'video.new': () => {
          scheduleBump();
        },
      },
      { onStatus: (connected) => live.setConnected(connected) },
    );

    // 首次进来对齐一次下载队列，保证角标准确
    live.refreshDownloads(true);
    void live.ensurePerformerAvatars();

    const onOffline = (): void => live.toast('网络已断开，实时更新暂停', 'error');
    const onOnline = (): void => live.toast('网络已恢复', 'info');

    window.addEventListener('offline', onOffline);
    window.addEventListener('online', onOnline);

    return () => {
      stop();
      window.clearTimeout(bumpTimer);
      window.removeEventListener('offline', onOffline);
      window.removeEventListener('online', onOnline);
    };
  });
</script>

{#snippet navIcon(name: NavItem['icon'])}
  <svg
    viewBox="0 0 24 24"
    class="h-[1.375rem] w-[1.375rem] shrink-0"
    fill="none"
    stroke="currentColor"
    stroke-width="1.5"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
  >
    {#if name === 'feed'}
      <!-- 关注：一帧画面 + 播放三角（比原来的表格线更像“视频流”） -->
      <rect x="3" y="5" width="18" height="14" rx="4" />
      <path d="M9.2 9.5v5L13.6 12z" fill="currentColor" />
    {:else if name === 'performers'}
      <circle cx="9" cy="9" r="3" />
      <path d="M3.5 19a5.5 5.5 0 0 1 11 0" />
      <path d="M16 6.2a3 3 0 0 1 0 5.6M17.5 19a5.5 5.5 0 0 0-2-4.2" />
    {:else if name === 'discover'}
      <circle cx="10.75" cy="10.75" r="5.75" />
      <path d="m15.5 15.5 4 4" />
    {:else if name === 'downloads'}
      <path d="M12 4v10" />
      <path d="m8 10.5 4 4 4-4" />
      <path d="M4.5 17.5h15" />
    {:else}
      <!-- 设置：齿轮 -->
      <path
        d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"
      />
      <circle cx="12" cy="12" r="3" />
    {/if}
  </svg>
{/snippet}

{#snippet tabInner(item: NavItem)}
  <span class="relative">
    {@render navIcon(item.icon)}
    {#if item.href === '/downloads' && live.activeDownloads > 0}
      <span
        class="absolute -right-2 -top-1 min-w-[0.9375rem] rounded-full bg-hi px-1 text-[0.625rem] font-medium leading-[0.9375rem] text-ink tabular-nums"
        aria-hidden="true"
      >
        {live.activeDownloads}
      </span>
    {/if}
  </span>
  <span class="text-[0.6875rem] leading-none">{item.label}</span>
{/snippet}

<!-- isolate：让 -z-10 的环境光留在这一层里，否则会被 body 的画布底色盖掉 -->
<div class="relative isolate min-h-dvh">
  <!-- 环境光：beeg 同款——顶部 49vh 的彩色底图 + 压暗层，跟着内容一起滚走 -->
  <div
    class="canvas-glow pointer-events-none absolute inset-x-0 top-0 -z-10 h-[49vh]"
    aria-hidden="true"
  ></div>

  <a
    href="#main"
    class="sr-only focus:not-sr-only focus:fixed focus:left-3 focus:top-3 focus:z-50 focus:rounded-full focus:bg-hi focus:px-4 focus:py-2 focus:text-ui focus:text-ink"
  >
    跳到内容
  </a>

  <!-- 移动端顶栏：48px，无描边。毛玻璃只留在固定的几条栏上（卡片上的模糊层已全部移除）。
       底色从上到下渐淡，好让顶部的环境光透过栏底自然接上，不在 48px 处留一道硬边。 -->
  <header
    class="fixed inset-x-0 top-0 z-40 bg-linear-to-b from-ink/92 via-ink/72 to-ink/35 pt-safe backdrop-blur-xl backdrop-saturate-150 lg:hidden"
  >
    <div class="flex h-12 items-center justify-between px-3 sm:px-4">
      <a
        data-link
        href="/"
        class="flex h-9 shrink-0 items-center text-title font-medium tracking-[-0.01em] text-fg"
        aria-label="MyPorn 首页"
      >
        MyPorn
      </a>

      <span
        class="flex items-center gap-1.5 px-1 text-meta text-dim"
        title={live.connected ? '实时事件已连接' : '实时事件未连接，正在重试'}
      >
        <span
          class="h-1.5 w-1.5 rounded-full {live.connected ? 'bg-sage' : 'bg-dim'}"
          aria-hidden="true"
        ></span>
        <span class="sr-only sm:not-sr-only">{live.connected ? '实时' : '重连中'}</span>
      </span>
    </div>
  </header>

  <!-- 侧边导航（桌面端）：悬浮式——四边留出空隙的圆角玻璃块，不贴着视口边缘，
       自身可滚动；没有描边也没有投影，浮起来的感觉全靠圆角、间隙和毛玻璃。 -->
  <aside
    class="fixed left-3 top-3 bottom-3 z-40 hidden w-60 flex-col overflow-y-auto rounded-xl bg-panel/70 p-2.5 backdrop-blur-xl backdrop-saturate-150 lg:flex"
    aria-label="侧边导航"
  >
    <a
      data-link
      href="/"
      class="flex h-10 shrink-0 items-center rounded-xl px-3 text-title font-medium tracking-[-0.01em] text-fg"
      aria-label="MyPorn 首页"
    >
      MyPorn
    </a>

    <nav class="mt-2 flex flex-col gap-0.5" aria-label="主导航">
      {#each SIDE_NAV as item (item.href)}
        <a
          data-link
          href={item.href}
          aria-current={activeHref === item.href ? 'page' : undefined}
          class="flex h-10 items-center gap-3 rounded-xl px-3 text-ui transition-colors {activeHref ===
          item.href
            ? 'bg-raise text-fg'
            : 'text-muted hover:bg-panel hover:text-fg'}"
        >
          {@render navIcon(item.icon)}
          <span class="min-w-0 flex-1 truncate">{item.label}</span>
          {#if item.href === '/downloads' && live.activeDownloads > 0}
            <span
              class="rounded-full bg-hi px-[0.3rem] text-[0.6875rem] font-medium leading-4 tabular-nums text-ink"
              aria-label={`${String(live.activeDownloads)} 个进行中的下载`}
            >
              {live.activeDownloads}
            </span>
          {/if}
        </a>
      {/each}
    </nav>

    <div class="mt-auto flex items-center justify-end pt-6">
      <!-- 设置：右下角，只有齿轮图标，不给底色（命中区仍是 36px 圆形，只靠颜色表示状态）。
           点开的是弹窗（SettingsDialog），不跳页面；打开时齿轮变白 -->
      <button
        type="button"
        aria-label="设置"
        title="设置"
        aria-haspopup="dialog"
        aria-expanded={settingsActive}
        onclick={() => ui.openSettings()}
        class="grid h-9 w-9 shrink-0 place-items-center rounded-full transition-colors {settingsActive
          ? 'text-fg'
          : 'text-muted hover:text-fg'}"
      >
        {@render navIcon('settings')}
      </button>
    </div>
  </aside>

  <!-- 桌面端给悬浮侧边栏让出 240px + 两侧的 12px 空隙；移动端顶栏已隐藏，内边距一起换 -->
  <div class="lg:pl-[16.5rem]">
    <main
      id="main"
      class="mx-auto w-full max-w-[112rem] px-3 pb-[calc(4.75rem+env(safe-area-inset-bottom))] pt-[calc(4.25rem+env(safe-area-inset-top))] sm:px-4 lg:px-8 lg:pb-16 lg:pt-10"
    >
      {#if title}
        <TopBar {title} />
      {/if}
      {@render children()}
    </main>
  </div>

  <!-- 移动端底部导航：无描边、无阴影，毛玻璃浮在内容上 -->
  <nav
    class="fixed inset-x-0 bottom-0 z-40 bg-ink/85 pb-safe backdrop-blur-xl backdrop-saturate-150 lg:hidden"
    aria-label="主导航"
  >
    <ul class="grid grid-cols-5">
      {#each NAV as item (item.href)}
        {@const isActive = item.icon === 'settings' ? settingsActive : activeHref === item.href}
        <li>
          {#if item.icon === 'settings'}
            <!-- 设置同样开弹窗，不跳页面 -->
            <button
              type="button"
              aria-label="设置"
              aria-haspopup="dialog"
              aria-expanded={isActive}
              onclick={() => ui.openSettings()}
              class="{TAB_CLASS(isActive)} w-full"
            >
              {@render tabInner(item)}
            </button>
          {:else}
            <a
              data-link
              href={item.href}
              aria-current={isActive ? 'page' : undefined}
              class={TAB_CLASS(isActive)}
            >
              {@render tabInner(item)}
            </a>
          {/if}
        </li>
      {/each}
    </ul>
  </nav>

  <Toasts />
</div>
