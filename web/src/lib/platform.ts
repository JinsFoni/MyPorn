/**
 * 平台的显示名。只在「给人看的地方」用（alt / title / 键值行）：
 * 图标、角标这类有视觉标识的地方一律用 PlatformIcon，不要写成文字。
 *
 * M4 会加 xhamster / beeg，表里没有的平台回退成代号本身，不至于露出空白。
 */
export const PLATFORM_LABEL: Record<string, string> = {
  pornhub: 'Pornhub',
  xhamster: 'xHamster',
  beeg: 'beeg',
  youtube: 'YouTube',
  bilibili: 'bilibili',
};

export function platformLabel(platform: string): string {
  return PLATFORM_LABEL[platform] ?? platform;
}
