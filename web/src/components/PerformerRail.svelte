<script lang="ts">
  import AvatarRail from './AvatarRail.svelte';
  import { api } from '../lib/api';
  import { live } from '../lib/live.svelte';
  import type { Performer } from '../lib/types';

  /**
   * 关注页顶部的头像条：拉订阅艺人，交给 AvatarRail 画（样式口径见那边）。
   *
   * 点谁筛谁，不跳 `/performer/<id>`：同一个位置的 B 站页（`?up=`）和 YouTube 页
   * （`?channel=`）都是「一排头像当过滤器 + 下面一面墙」，三处手势一致。
   * 艺人主页的入口还在「艺人」页、管理页、播放页作者行。
   */
  interface Props {
    /** 当前筛的艺人 id；地址栏 `?performer=` 是唯一真源，这里只负责高亮 */
    selected?: number | null;
    /** 点一个头像：选中就回 id，再点一次回 null（取消筛选） */
    onSelect?: (id: number | null) => void;
    /** 列表拉到/刷新后回吐一份，页面拿它把 id 翻回名字（筛选胶囊 / 说明栏要写「只看谁」） */
    onLoad?: (items: Performer[]) => void;
  }

  let { selected = null, onSelect, onLoad }: Props = $props();

  /**
   * 要排掉的平台（对应后端 store.FeedExcludedPlatforms 那条规矩）：关注流不收它们的片子，
   * 把它们当筛选项只会筛出空列表。这里只有 krx18 需要排——B 站 UP 主在 store 那层已经
   * 排掉了（`p.platform <> 'bilibili'`），YouTube 频道压根不是 performers 行。
   */
  const FEED_EXCLUDED_PLATFORMS = ['krx18'];

  let items = $state<Performer[]>([]);
  let loaded = $state(false);
  let lastBump = 0;

  // 按最近同步排：刚同步回来、可能有新片的排前面
  const sorted = $derived(
    [...items].sort((a, b) => {
      const at = a.lastSyncAt ? Date.parse(a.lastSyncAt) : 0;
      const bt = b.lastSyncAt ? Date.parse(b.lastSyncAt) : 0;
      return bt - at;
    }),
  );

  const railItems = $derived(
    sorted.map((p) => ({
      id: p.id,
      name: p.name,
      avatarUrl: p.avatarUrl,
      title: `只看 ${p.name}`,
      dimmed: !p.enabled,
    })),
  );

  async function refresh(): Promise<void> {
    try {
      // 关注页的头像条是关注流的筛选项，而关注流不收 krx18 的片子
      // （它们在自己的 /krx18 页面里），所以那几个 tag 不该出现在这里。
      const res = await api.subscriptions({ excludePlatform: FEED_EXCLUDED_PLATFORMS });
      items = res.items;
      onLoad?.(items);
    } catch {
      /* 头像条只是筛选器，被限流/断网时静默降级，不打扰影片流 */
    } finally {
      loaded = true;
    }
  }

  $effect(() => {
    void refresh();
  });

  $effect(() => {
    const version = live.feedVersion;
    if (version === lastBump) return;
    lastBump = version;
    void refresh();
  });
</script>

<AvatarRail items={railItems} label="按艺人筛选" {selected} {onSelect} loading={!loaded} />
