/**
 * 底部控制条里的自定义控件：**上一个 / 下一个**、**清晰度**、**氛围模式**，外加设置菜单里的
 * **自动切换** 开关。
 *
 * 为什么要把节点塞进皮肤的 shadow root：Video.js 10 的打包皮肤把整套控件关在自己的 shadow root 里，
 * `<video-skin>` 只留了一个默认插槽（放媒体元素和页面浮层），没有「往控制条里加按钮」的插槽；
 * 公开的 `--media-*` 也只能改颜色/圆角/字体。想让这些按钮和自带的播放/音量/设置排在同一行、
 * 跟着控制条一起出现和收起，就只能挂进它右侧那组按钮容器里。
 *
 * 皮肤内部结构官方标注「可能随版本变」，所以这里只依赖两样相对稳的东西：
 *   1. 类名 `.video-controls-secondary`（右侧按钮组，自带 cast/画中画/全屏都在里面）与
 *      `.media-settings-menu-trigger`（齿轮键，`commandfor` 指向它打开的那个菜单）；
 *   2. 皮肤自己的 `media-*` 自定义元素与 `.media-button`、`.media-menu-*` 类名——它们本身就是公开组件，
 *      用它们搭出来的按钮/菜单和自带的长得一样，不需要另造一套视觉。
 * 挂不上就返回 null 让上层重试；真失配的最坏结果是这几个控件不出现，播放器其余部分不受影响。
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
  /**
   * 上一个 / 下一个：给的是邻居片名（工具提示用）。
   * `null` = 这个页面不提供切歌（YouTube 弹窗以外的地方、单条影片详情等），整组按钮不出现；
   * 字段本身为 null = 那一侧到头了，按钮留着但按不动（保留位置，控制条不会因此跳一下）。
   */
  nav: { prev: string | null; next: string | null } | null;
  /** 设置菜单里那个开关的状态；`null` = 这个页面没有「下一个」可切，开关不出现 */
  autoNext: boolean | null;
}

export interface PlayerBarHandlers {
  selectQuality: (id: string) => void;
  toggleAmbient: () => void;
  goPrev: () => void;
  goNext: () => void;
  toggleAutoNext: () => void;
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
/* 到头了：留着位置、按不动。皮肤自己只在 aria-disabled 上改光标，这里补上明显的暗化 */
.myporn-bar-step[aria-disabled='true'] {
  opacity: 0.35;
  cursor: default;
  pointer-events: none;
}
.myporn-bar-auto-text { white-space: nowrap; }
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

/**
 * 上一个 / 下一个：竖条 + 三角，指向播放方向。和音量、全屏那些实心图标同一套语言
 * （皮肤自带的图标里没有 skip 这一类，只能自己画）。
 */
const PREV_SVG = `<svg viewBox="0 0 24 24" class="myporn-bar-icon" fill="currentColor" aria-hidden="true"><rect x="3.6" y="5" width="2.4" height="14" rx="1"/><path d="M20.4 5.6v12.8L10.2 12z"/></svg>`;
const NEXT_SVG = `<svg viewBox="0 0 24 24" class="myporn-bar-icon" fill="currentColor" aria-hidden="true"><rect x="18" y="5" width="2.4" height="14" rx="1"/><path d="M3.6 5.6v12.8L13.8 12z"/></svg>`;

const MENU_POPUP_CLASS = 'media-popup media-popup-surface media-menu-popup media-menu-resizable-popup';

/** 同页多个播放器时 id 不能撞；实际只会有一个，保险 */
let sequence = 0;

/**
 * 找设置（齿轮）菜单的顶层内容块。
 *
 * 齿轮是皮肤自带的 `<button class="media-settings-menu-trigger" commandfor="…">`，
 * `commandfor` 指向它打开的那个 `<media-menu>`；菜单里第一个 `.media-menu-content`
 * 就是顶层列表（子菜单的 content 都嵌在各自的 menu-item 里面，不是它的子节点）。
 * 找不到就返回 null，调用方跳过这个开关（其余控件照挂）。
 */
function settingsMenuContent(root: ShadowRoot): HTMLElement | null {
  const trigger = root.querySelector('.media-settings-menu-trigger');
  const menuID = trigger?.getAttribute('commandfor');
  if (!menuID) return null;
  const menu = root.getElementById(menuID);
  if (!menu) return null;
  const content = menu.querySelector(':scope > media-menu-content');
  return content instanceof HTMLElement ? content : null;
}

/**
 * 把这些控件挂进皮肤：按钮进控制条右侧，自动切换开关进设置菜单。
 * 皮肤还没渲染好（拿不到 shadow root / 控件组）时返回 null。
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

  // ---------- 上一个 / 下一个 ----------
  const stepBtn = (label: string, svg: string, command: () => void): HTMLButtonElement => {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'media-button myporn-bar-step';
    btn.innerHTML = svg;
    btn.setAttribute('aria-label', label);
    btn.title = label;
    btn.addEventListener('click', () => {
      // 到头了按钮已经 pointer-events:none，这里再挡一次（键盘/脚本触发也算）
      if (btn.getAttribute('aria-disabled') === 'true') return;
      command();
    });
    return btn;
  };
  const prevBtn = stepBtn('上一个', PREV_SVG, handlers.goPrev);
  const nextBtn = stepBtn('下一个', NEXT_SVG, handlers.goNext);

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
  holder.append(prevBtn, nextBtn, qualityBtn, ambientBtn);

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

  // 弹层自己带一块实底。皮肤那层是「10% 白 + 背景模糊」的磨砂玻璃——菜单只有两三行、
  // 又压在控制条那条深色带上时还看得清，清晰度菜单有四五档、往上顶到视频画面上，
  // 白字就糊进亮画面里了（实测 2207 那条橙色海报：除选中项外其余档位几乎读不出来）。
  // 顺手把高度上限从皮肤的 224px 放开：五档（4K 片）是 5×46=230px，差一点就要滚动。
  const menuStyle = document.createElement('style');
  menuStyle.textContent = `
#${uid}-menu {
  background: rgb(12 14 18 / 0.94);
  border: 1px solid rgb(255 255 255 / 0.12);
  max-height: min(70vh, 22rem);
}
#${uid}-menu > .media-menu-content { max-height: inherit; }
`;
  root.append(menuStyle);

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

  // ---------- 设置菜单里的「自动切换」开关 ----------
  /**
   * 挂进皮肤自带的齿轮菜单，而不是自己另开一个弹层：用户找「设置」的时候只会去那个齿轮，
   * 一个播放器里出现两个设置入口比省事更糟。找不到菜单就是这一项不出现（按钮照旧）。
   */
  const settingsContent = settingsMenuContent(root);
  let autoItem: HTMLElement & { checked: boolean } | null = null;
  let autoSeparator: HTMLElement | null = null;
  let autoIcon: HTMLElement | null = null;
  if (settingsContent) {
    const separator = document.createElement('media-menu-separator');
    separator.className = 'media-menu-separator';
    const item = document.createElement('media-menu-checkbox-item') as HTMLElement & { checked: boolean };
    item.className = 'media-menu-radio-item myporn-bar-auto';
    item.setAttribute('aria-label', '自动切换下一个：本片播完自动播放在看的那份列表里的下一条');
    item.title = '本片播完自动播放在看的那份列表里的下一条';
    // 一行就够：皮肤里那些项（速度、画质）都是一行文字 + 右侧状态，多一行说明会把行高
    // 从 32px 顶到 50px，和相邻的项对不齐。说明放 aria-label/title。
    const text = document.createElement('span');
    text.className = 'myporn-bar-auto-text';
    text.textContent = '自动切换下一个';
    const check = document.createElement('media-icon');
    check.setAttribute('name', 'check');
    check.setAttribute('aria-hidden', 'true');
    check.className = 'media-menu-radio-item-icon myporn-bar-check';
    item.append(text, check);
    // 这个元素自己会翻转 checked 并保持菜单打开（见 @videojs/html 的 checkbox-item），
    // 状态的真源仍在 Svelte 那边：这里只把「用户点了」转出去，下一轮 update() 再把值写回来。
    item.addEventListener('checked-change', () => handlers.toggleAutoNext());
    settingsContent.append(separator, item);
    autoItem = item;
    autoSeparator = separator;
    autoIcon = check;
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

    // 上一个 / 下一个：这个页面不提供就整组不出现；提供了但那一侧到头了就按不动
    const nav = state.nav;
    prevBtn.style.display = nav === null ? 'none' : '';
    nextBtn.style.display = nav === null ? 'none' : '';
    if (nav !== null) {
      setStep(prevBtn, '上一个', nav.prev);
      setStep(nextBtn, '下一个', nav.next);
    }

    if (autoItem !== null) {
      autoItem.style.display = state.autoNext === null ? 'none' : '';
      autoItem.checked = state.autoNext === true;
      // 皮肤只在 `[aria-checked]` + radio-item 上给对勾设可见度，而且那是 neutral 主题的规则；
      // 这里和我们自己的清晰度对勾一样，自己控透明度。
      autoIcon?.setAttribute('style', `opacity:${state.autoNext === true ? '1' : '0'}`);
    }
  }

  /** 把「到没到头」写进按钮：到头就暗下去、按不动，但位置留着，控制条不会因此抖一下 */
  function setStep(btn: HTMLButtonElement, label: string, title: string | null): void {
    const atEnd = title === null;
    btn.setAttribute('aria-disabled', atEnd ? 'true' : 'false');
    const full = atEnd ? label : `${label}：${title}`;
    btn.setAttribute('aria-label', full);
    btn.title = full;
  }

  // 挂进控制条右侧：排在画中画/全屏左边、设置按钮右边，和自带按钮同一行
  group.insertBefore(holder, group.firstChild);
  group.append(menu);

  return {
    update,
    destroy() {
      holder.remove();
      menu.remove();
      // 设置菜单里那两项也是我们插进去的：不摘掉的话，重建播放器时会越堆越多
      autoItem?.remove();
      autoSeparator?.remove();
      style.remove();
      menuStyle.remove();
      checks.clear();
    },
  };
}
