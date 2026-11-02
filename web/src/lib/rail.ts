/**
 * 头像条（关注页 / B 站页 / YouTube 页共用的那一排 72px 圆头像）的数据形状。
 *
 * 三个页面各自从自己的接口拿列表（订阅艺人 / UP 主 / 频道），在这里抹平成同一种：
 * 要筛的是 `id`，版面上写 `name`、画 `avatarUrl`，别的都不画。
 * 计数、未看数这类信息只进 `title`（悬停可见、读屏可读），不占版面——
 * 「不显示任何计数」是关注页头像条定下的口径，另两页跟着它。
 */
export interface RailItem {
  id: number;
  name: string;
  avatarUrl?: string | null;
  /** 悬停提示（`名字 · 已收录 12 条 · 未看 3`），不传就只用名字 */
  title?: string;
  /** 暂停同步的源：头像压暗，和原来胶囊上的 `opacity-50` 同一个意思 */
  dimmed?: boolean;
  /** 正在同步：头像右上角点一颗呼吸的亮点（原来是胶囊上的小圆点） */
  busy?: boolean;
}
