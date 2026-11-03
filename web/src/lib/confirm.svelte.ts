/**
 * 统一的确认框（替代 `window.confirm`）。
 *
 * 原生弹窗画在浏览器那一层，不是页面的一部分：深色界面里蹦出一个白框、按钮是系统字体、
 * 不能改文案排版，手机浏览器还会在上面顶一行域名。2026-11-04 用户要求「确认弹窗的样式
 * 改成和 UI 统一的风格」，于是把仓库里 6 处 `window.confirm`（删照片、删本地库、
 * 取消 YouTube / B 站 / 动态订阅、删视频连文件夹）统一收口到这里。
 *
 * 用法（调用方本来就是 async，直接 await）：
 *
 * ```ts
 * if (!(await confirmDialog.ask({
 *   title: '从照片墙删掉这张？',
 *   body: '以后同步也不会再抓回来。已经存进本地库的文件不会被删除。',
 *   confirmText: '删除',
 * }))) return;
 * ```
 *
 * 为什么是 Promise 而不是回调：这些调用点原来就写成「confirm 返回 false 就 return」，
 * 换成 Promise 之后除了一行 `await`，控制流一个字都不用改。
 */

export interface ConfirmOptions {
  /** 标题：一句话说清「要发生什么」，用问句 */
  title: string;
  /** 正文：后果说明，可以用 `\n` 分段（渲染时 `whitespace-pre-line`） */
  body?: string;
  /** 确认按钮文案，默认「删除」 */
  confirmText?: string;
  /** 取消按钮文案，默认「取消」 */
  cancelText?: string;
  /** 危险操作（红按钮）还是一般操作（主色按钮），默认危险——现在全是删除 */
  tone?: 'danger' | 'primary';
}

class ConfirmStore {
  open = $state(false);
  options = $state<ConfirmOptions>({ title: '' });

  #resolve: ((ok: boolean) => void) | null = null;

  ask(opts: ConfirmOptions): Promise<boolean> {
    // 上一个还没答完就来了新请求：先把上一个按「取消」结掉，
    // 否则那个 await 会永远挂着（回调链里的 busy 标记也就永远不解除）
    this.#resolve?.(false);
    this.options = opts;
    this.open = true;
    return new Promise<boolean>((resolve) => {
      this.#resolve = resolve;
    });
  }

  /** 用户作答（按钮 / Esc / 点背景）。重复调用无副作用。 */
  settle(ok: boolean): void {
    if (!this.open) return;
    this.open = false;
    const resolve = this.#resolve;
    this.#resolve = null;
    resolve?.(ok);
  }
}

export const confirmDialog = new ConfirmStore();
