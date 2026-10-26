/**
 * 底部控制条里的两个自定义按钮：**清晰度** 与 **氛围模式**。
 *
 * 为什么要把节点塞进皮肤的 shadow root：Video.js 10 的打包皮肤把整套控件关在自己的 shadow root 里，
 * `<video-skin>` 只留了一个默认插槽（放媒体元素和页面浮层），没有「往控制条里加按钮」的插槽；
 * 公开的 `--media-*` 也只能改颜色/圆角/字体。想让这两个按钮和自带的播放/音量/设置排在同一行、
 * 跟着控制条一起出现和收起，就只能挂进它右侧那组按钮容器里。
 *
 * 皮肤内部结构官方标注「可能随版本变」，所以这里只依赖两样相对稳的东西：
 *   1. 类名 `.video-controls-secondary`（右侧按钮组，自带 cast/画中画/全屏都在里面）；
 *   2. 皮肤自己的 `media-*` 自定义元素与 `.media-button`、`.media-menu-*` 类名——它们本身就是公开组件，
 *      用它们搭出来的按钮/菜单和自带的长得一样，不需要另造一套视觉。
 * 挂不上就返回 null 让上层重试；真失配的最坏结果是这两个按钮不出现，播放器其余部分不受影响。
 *
 * 状态同步是单向的：Svelte 侧状态变了调用 `update()`，这个模块只负责把状态写进 DOM，
 * 用户动按钮则通过 handlers 回调出去，自己不存状态。
 */
import type { QualityOption } from './player';

export interface PlayerBarState {
  /** 清晰度列表（降序）；只有一条时不显示清晰度按钮 */
  qualities: QualityOption[];
  /** 当前清晰度 id */
  selected: string | null;
  /** 氛围模式是否开启 */
  ambient: boolean;
}

export interface PlayerBarHandlers {
  selectQuality: (id: string) => void;
  toggleAmbient: () => void;
}

export interface PlayerBar {
  update: (state: PlayerBarState) => void;
  destroy: () => void;
}

/** 皮肤的样式表进不了我们的节点，所以自带一小段；只放在这个 shadow root 里 */
const BAR_CSS = `
.myporn-bar { display: contents; }
.myporn-bar-quality.media-button {
  display: flex;
  align-items: center;
  gap: 0.25em;
  width: auto;
  padding-inline: 0.5em;
  font-size: 0.92em;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.myporn-bar-chevron {
  width: 0.9em;
  height: 0.9em;
  flex-shrink: 0;
  opacity: 0.65;
  transition: rotate 0.15s ease;
}
.myporn-bar-quality[aria-expanded='true'] .myporn-bar-chevron { rotate: 180deg; }
.myporn-bar-icon {
  width: var(--media-icon-size, 18px);
  height: var(--media-icon-size, 18px);
  transition: opacity 0.15s ease;
}
.myporn-bar-text {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 2px;
}
.myporn-bar-hint {
  font-size: 0.85em;
  line-height: 1.1;
  opacity: 0.55;
}
.myporn-bar-check { opacity: 0; }
`;

const CHEVRON_SVG = `<svg viewBox="0 0 24 24" class="myporn-bar-chevron" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m7 10 5 5 5-5"/></svg>`;

/** 氛围模式的太阳：亮着 = 开，暗下去 = 关（和药丸里的强调色不是一套，用透明度表达更省事） */
const AMBIENT_SVG = `<svg viewBox="0 0 24 24" class="myporn-bar-icon" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" aria-hidden="true"><path d="M12 3v2M12 19v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M3 12h2M19 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/><circle cx="12" cy="12" r="3.6"/></svg>`;

const MENU_POPUP_CLASS = 'media-popup media-popup-surface media-menu-popup media-menu-resizable-popup';

/** 同页多个播放器时 id 不能撞；实际只会有一个，保险 */
let sequence = 0;

/**
 * 把两个按钮挂进皮肤的控制条右侧。皮肤还没渲染好（拿不到 shadow root / 控件组）时返回 null。
 */
export function mountPlayerBar(skin: HTMLElement, handlers: PlayerBarHandlers): PlayerBar | null {
  const root = skin.shadowRoot;
  const group = root?.querySelector('.video-controls-secondary');
  if (!root || !(group instanceof HTMLElement)) return null;

  const uid = `myporn-bar-${(sequence += 1)}`;

  const style = document.createElement('style');
  style.dataset.mypornBar = '';
  style.textContent = BAR_CSS;
  root.append(style);

  // ---------- 清晰度 ----------
  const qualityBtn = document.createElement('button');
  qualityBtn.type = 'button';
  qualityBtn.id = `${uid}-quality`;
  qualityBtn.className = 'media-button myporn-bar-quality';
  // 皮肤靠 commandfor → media-menu 的 id 把按钮和弹层绑起来（打开、aria-expanded、Esc 都归它管）
  qualityBtn.setAttribute('commandfor', `${uid}-menu`);
  qualityBtn.setAttribute('aria-label', '清晰度');
  qualityBtn.title = '清晰度';
  qualityBtn.innerHTML = `<span class="myporn-bar-quality-label"></span>${CHEVRON_SVG}`;
  const labelSpan = qualityBtn.querySelector('.myporn-bar-quality-label');
  if (!(labelSpan instanceof HTMLElement)) {
    style.remove();
    return null;
  }
  const qualityLabel = labelSpan;

  // ---------- 氛围模式 ----------
  const ambientBtn = document.createElement('button');
  ambientBtn.type = 'button';
  ambientBtn.id = `${uid}-ambient`;
  ambientBtn.className = 'media-button';
  ambientBtn.setAttribute('aria-label', '氛围模式');
  ambientBtn.title = '氛围模式';
  ambientBtn.innerHTML = AMBIENT_SVG;
  const ambientIcon = ambientBtn.querySelector('.myporn-bar-icon');
  ambientBtn.addEventListener('click', () => handlers.toggleAmbient());

  // 用一个 display:contents 的壳装着：壳本身不参与布局，子节点直接当控制条的 flex 项
  const holder = document.createElement('div');
  holder.className = 'myporn-bar';
  holder.append(qualityBtn, ambientBtn);

  // ---------- 清晰度弹层 ----------
  const menu = document.createElement('media-menu');
  menu.id = `${uid}-menu`;
  menu.className = MENU_POPUP_CLASS;
  menu.setAttribute('side', 'top');
  menu.setAttribute('align', 'end');
  const content = document.createElement('media-menu-content');
  content.id = `${uid}-menu-content`;
  content.className = 'media-menu-content';
  menu.append(content);

  /** 每项后面的对勾：不用皮肤 `[aria-checked]` 那套（我们的项是普通 media-menu-item），自己控透明度 */
  const checks = new Map<string, HTMLElement>();
  let itemsSignature = '';

  function rebuildItems(qualities: QualityOption[]): void {
    content.replaceChildren();
    checks.clear();
    for (const quality of qualities) {
      const item = document.createElement('media-menu-item');
      item.className = 'media-menu-radio-item';
      const text = document.createElement('span');
      text.className = 'myporn-bar-text';
      const name = document.createElement('span');
      name.textContent = quality.label;
      const hint = document.createElement('span');
      hint.className = 'myporn-bar-hint';
      hint.textContent = quality.hint;
      text.append(name, hint);
      const check = document.createElement('media-icon');
      check.setAttribute('name', 'check');
      check.setAttribute('aria-hidden', 'true');
      check.className = 'media-menu-radio-item-icon myporn-bar-check';
      item.append(text, check);
      // 点一项 → 皮肤会关掉弹层（completeMenuItemSelection），这里只管把选择交出去
      item.addEventListener('select', () => handlers.selectQuality(quality.id));
      content.append(item);
      checks.set(quality.id, check);
    }
  }

  function update(state: PlayerBarState): void {
    const signature = state.qualities.map((q) => `${q.id}\u0000${q.label}\u0000${q.hint}`).join('\u0001');
    if (signature !== itemsSignature) {
      itemsSignature = signature;
      rebuildItems(state.qualities);
    }

    // 只有一条清晰度时没什么可选的，藏起来（display 要手写，`.media-button` 的 display 会盖掉 [hidden]）
    const showQuality = state.qualities.length > 1;
    qualityBtn.style.display = showQuality ? '' : 'none';

    const current = state.qualities.find((q) => q.id === state.selected) ?? null;
    qualityLabel.textContent = current?.label ?? '清晰度';
    qualityBtn.setAttribute('aria-label', current === null ? '清晰度' : `清晰度：${current.label}`);
    for (const [id, check] of checks) check.style.opacity = id === state.selected ? '1' : '0';

    const ambientLabel = state.ambient ? '关闭氛围模式' : '开启氛围模式';
    ambientBtn.setAttribute('aria-pressed', state.ambient ? 'true' : 'false');
    ambientBtn.setAttribute('aria-label', ambientLabel);
    ambientBtn.title = ambientLabel;
    ambientIcon?.setAttribute('style', `opacity:${state.ambient ? '1' : '0.5'}`);
  }

  // 挂进控制条右侧：排在画中画/全屏左边、设置按钮右边，和自带按钮同一行
  group.insertBefore(holder, group.firstChild);
  group.append(menu);

  return {
    update,
    destroy() {
      holder.remove();
      menu.remove();
      style.remove();
      checks.clear();
    },
  };
}
