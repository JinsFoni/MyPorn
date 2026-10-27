/**
 * 隐私模式：全站的封面、头像、缩略图一律糊成一片——地铁上、工位旁边有人凑过来，
 * 也看不出是谁、在干什么。
 *
 * 状态只存本机（`localStorage`），不写后端：这是「这台设备此刻怎么显示」，
 * 不是账号设置，换台设备本来就该自己重新决定一次。
 *
 * 落地方式不上组件：开关只往 `<html>` 上打一个 `data-privacy="on"`，
 * 模糊交给 app.css 里那一条规则，作用对象是所有带 `data-media` 的媒体图。
 * 所以新加图片只要标上 `data-media` 就自动被管住，不用知道隐私模式的存在；
 * 图标、平台 logo 之类不标，保持清晰可辨。
 */
const KEY = 'myporn.privacy';

function stored(): boolean {
  try {
    return window.localStorage.getItem(KEY) === '1';
  } catch {
    /* 浏览器自身的隐私模式下 localStorage 可能不可读，那就当它关着 */
    return false;
  }
}

function apply(on: boolean): void {
  document.documentElement.dataset.privacy = on ? 'on' : 'off';
}

class PrivacyStore {
  on = $state(stored());

  toggle(): void {
    this.set(!this.on);
  }

  set(on: boolean): void {
    this.on = on;
    apply(on);
    try {
      window.localStorage.setItem(KEY, on ? '1' : '0');
    } catch {
      /* 写不进去就只在这次会话里生效，不影响用 */
    }
  }
}

export const privacy = new PrivacyStore();

// 模块一加载就落到 <html> 上。图片是异步来的，等组件 onMount 再打
// 就晚了——首屏那几张会用原图闪一下，正好是隐私模式最不想出现的那一眼。
apply(privacy.on);
