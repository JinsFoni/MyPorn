/**
 * 侧边栏 / 底栏高亮用的「这一页算哪一格」上下文。
 *
 * `/video/:id` 是**跨平台复用**的路由：关注流、艺人页、B 站墙、krx18 墙、媒体库点开的
 * 都是它。侧边栏原来只看路径前缀（`/video` 前缀 → 关注），于是放 B 站 / krx18 的片时
 * 高亮的是「关注」——那两格明明各有自己的入口（2026-11-05 用户报的就是这个）。
 *
 * 路径前缀回答不了这个问题，影片自己的 `platform` 才是唯一真源：播放页加载到详情就写进来，
 * 侧边栏据此把 `bilibili` / `krx18` 的片归到各自的格子上（映射表在 AppShell 里，
 * 因为「哪一格」是导航自己的事，这里只存事实）。
 *
 * 不在影片页时是 null，谁也不影响。
 */
class NavContext {
  /** 正在播的那条片子的平台（`bilibili` / `krx18` / `pornhub` …）；不在影片页时为 null */
  videoPlatform = $state<string | null>(null);

  setVideoPlatform(platform: string | null): void {
    if (this.videoPlatform !== platform) this.videoPlatform = platform;
  }
}

export const nav = new NavContext();
