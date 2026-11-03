/** 品牌小图标的种类。艺人主页的链接行、动态页的平台筛选共用这一套。 */
export type SocialBrand = 'official' | 'x' | 'instagram' | 'web';

/** 从链接认品牌：认域名不认标签——标签是站点本地化的，域名是稳定的。 */
export function socialBrandOf(url: string, label = ''): SocialBrand {
  const u = url.toLowerCase();
  if (u.includes('twitter.com') || u.includes('//x.com') || u.includes('x.com/')) return 'x';
  if (u.includes('instagram.com')) return 'instagram';
  if (u.includes('onlyfans.com') || label.includes('官方')) return 'official';
  return 'web';
}

/** 从平台 key（后端 `PlatformNames` 那套：`x` / `instagram`）认品牌；认不出的给地球。 */
export function socialBrandOfPlatform(key: string): SocialBrand {
  const k = key.toLowerCase();
  if (k === 'x' || k === 'twitter') return 'x';
  if (k === 'instagram') return 'instagram';
  if (k === 'onlyfans') return 'official';
  return 'web';
}
