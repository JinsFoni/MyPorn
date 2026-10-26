/**
 * Video.js 10 是一套自定义元素（`<video-player>` / `<video-skin>` / `<hlsjs-video>` / `<media-i18n>`），
 * 不在 Svelte 的内置元素表里。这里补一声明，只是让 svelte-check 认这些标签；属性名以官方文档为准。
 */
import type { HTMLAttributes, HTMLVideoAttributes } from 'svelte/elements';

declare module 'svelte/elements' {
  interface SvelteHTMLElements {
    'media-i18n': HTMLAttributes<HTMLElement> & { lang?: string };
    'video-player': HTMLAttributes<HTMLElement> & { poster?: string };
    'video-skin': HTMLAttributes<HTMLElement>;
    'hlsjs-video': HTMLVideoAttributes;
  }
}
