/**
 * 侧边栏 / 移动端底栏高亮用的「这一页算哪一格」上下文。
 *
 * `/video/:id` 是**跨平台复用**的路由：关注流、艺人页、B 站墙、krx18 墙、媒体库点开的
 * 都是它。侧边栏原来只看路径前缀（`/video` 前缀 → 关注），于是放 B 站 / krx18 的片时
 * 高亮的是「关注」——那两格明明各有自己的入口（2026-11-05 用户报的就是这个）。
 *
 * 路径前缀回答不了这个问题，影片自己的 `platform` 才是唯一真源：播放页加载到详情就写进来。
 * 但详情要一次请求，**在它回来之前不能先亮「关注」再跳走**（2026-11-05 第二轮：
 * 「点击播放时会先跳到关注，然后跳回 B 站 / krx18」），所以这里多给两条同步的线索：
 *
 *  1. `?back=`（卡片链接自带来源、刷新后 query 也还在）→ 由 `navHrefForBack` 直接翻译成那一格；
 *  2. 都没有就**保持上一格亮着**（`AppShell` 里的 `lastHref`），等详情回来再settle。
 *
 * 三条线索的先后：平台（准）→ `?back=`（同步、绝大多数点击路径都有）→ 上一格。
 */

/** 平台 → 侧边栏那一格。表里没有的平台（pornhub / xhamster / beeg）本身就是「关注」流的内容。 */
const PLATFORM_HREF: Record<string, string> = {
  youtube: '/youtube',
  bilibili: '/bilibili',
  krx18: '/krx18',
};

/** 「关注」那一格（`/`）。 */
export const FEED_HREF = '/';

/** 平台名 → 导航 href；不在表里返回 null（交给调用方按「关注」处理）。 */
export function navHrefForPlatform(platform: string | null | undefined): string | null {
  return platform ? (PLATFORM_HREF[platform] ?? null) : null;
}

/**
 * 从 `?back=` 猜这一页该亮哪一格。返回 null ＝ 这条来源说明不了
 * （`/performer/:id`、`/library`、`/manage` 那种能装任何平台的页面，或者压根没有 back）。
 *
 * 只认站内绝对路径的前缀（和 `Video.svelte` 里 `backTo` 的校验同一口径，别被带去外站）。
 */
export function navHrefForBack(back: string | null | undefined): string | null {
  if (!back || !back.startsWith('/')) return null;
  for (const prefix of ['/krx18', '/bilibili', '/youtube'] as const) {
    if (back === prefix || back.startsWith(prefix + '?') || back.startsWith(prefix + '/')) return prefix;
  }
  // `/` 和 `/?performer=12&mode=new` 都是关注流：那一流本来就排掉了 bilibili / youtube / krx18
  if (back === '/' || back.startsWith('/?')) return FEED_HREF;
  return null;
}

class NavContext {
  /** 正在播的那条片子的平台（`bilibili` / `krx18` / `pornhub` …）；不在影片页时为 null */
  videoPlatform = $state<string | null>(null);

  setVideoPlatform(platform: string | null): void {
    if (this.videoPlatform !== platform) this.videoPlatform = platform;
  }
}

export const nav = new NavContext();
