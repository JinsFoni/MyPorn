<script lang="ts">
  import officialIcon from '../assets/social/official.png';
  import twitterIcon from '../assets/social/twitter.png';
  import instagramIcon from '../assets/social/instagram.png';
  import type { SocialLink } from '../lib/types';

  interface Props {
    /** 站外链接（官方网址 / Twitter / Instagram …） */
    social: SocialLink[];
    /** 外层布局用的类（比如在简介那一行里靠右） */
    class?: string;
  }

  let { social, class: className = '' }: Props = $props();

  /* 只有图标、不带文字（用户给的三个品牌小图标：官方网址那枚是 OnlyFans 的蓝标，
     Twitter 那枚是 X 的方牌，Instagram 那枚是彩色方牌）。名字不消失，退到 `title` 和
     `aria-label` 里——鼠标悬停看得见，读屏也读得到，只是不再占版面。
     辨认靠域名而不是标签文字：标签是站点本地化的，域名是稳定的；认不出的域名退回
     地球那枚线稿，不硬套只有官方网址才用得上的品牌标。 */
  type IconKind = 'official' | 'x' | 'instagram' | 'web';

  function kindOf(link: SocialLink): IconKind {
    const u = link.url.toLowerCase();
    if (u.includes('twitter.com') || u.includes('//x.com') || u.includes('x.com/')) return 'x';
    if (u.includes('instagram.com')) return 'instagram';
    if (u.includes('onlyfans.com') || (link.label || '').includes('官方')) return 'official';
    return 'web';
  }

  const MARKS: Partial<Record<IconKind, string>> = {
    official: officialIcon,
    x: twitterIcon,
    instagram: instagramIcon,
  };
</script>

{#if social.length}
  <!-- `-my-1.5`：每个圆底座 32px、图标 22px，比旁边 20px 的正文行高一圈；上下各收 6px，
       图标中线才落在正文第一行的中线上（那一行是 `items-start` 排的）。 -->
  <ul class="flex flex-wrap items-center gap-2 -my-1.5 {className}">
    {#each social as link (link.url)}
      {@const kind = kindOf(link)}
      {@const mark = MARKS[kind]}
      <li>
        <!-- 外链一律新窗口 + noopener：这些地址来自上游页面，不能让它拿到 window.opener。
             底座 32px 见方、图标 22px：品牌标本来就不是单色，硬压成我们那套线稿会认不出来，
             所以这几个圆圈里是原色图；悬停只给一层白雾，不改图标本身。 -->
        <a
          href={link.url}
          target="_blank"
          rel="noreferrer noopener"
          title={link.label}
          aria-label={link.label}
          class="grid h-8 w-8 place-items-center rounded-full transition-colors hover:bg-white/10"
        >
          {#if mark}
            <!-- X 那枚原图是「黑色方牌挖空一个 X」：直接放上去等于一块比画布还黑的方块，
                 在 #020408 上根本看不出形状，所以只对这一个做反相（黑牌变白牌、X 变黑）。

                 尺寸按**墨迹**对齐，不按画布：三张 PNG 都是 48/50 见方、四周留白各不相同
                 （2026-11-04 在 `/performer/1` 上量的墨迹占比 —— OnlyFans 横向 100%、
                 X 84%、Instagram 只有 75%）。给同一个 22px 画布时，X 的图案 18.48px、
                 Instagram 只有 16.50px，所以 IG 看着小一圈。IG 单独给 26px 画布 →
                 19.5px 墨迹，比 X 的 18.48px 略大一档：IG 是圆角描边（墨迹像素占比 54.6%），
                 X 是实心方牌（65.8%），同样大小下描边那个一定显小，得找回来一点。 -->
            <img
              src={mark}
              alt=""
              width="48"
              height="48"
              class="{kind === 'instagram' ? 'h-[26px] w-[26px]' : 'h-[22px] w-[22px]'} {kind === 'x'
                ? 'invert'
                : ''}"
              loading="lazy"
              decoding="async"
            />
          {:else}
            <svg
              viewBox="0 0 24 24"
              class="h-[22px] w-[22px] text-muted"
              aria-hidden="true"
              fill="none"
              stroke="currentColor"
              stroke-width="1.8"
              stroke-linecap="round"
              stroke-linejoin="round"
            >
              <circle cx="12" cy="12" r="8.5" />
              <path d="M3.5 12h17" />
              <path d="M12 3.5c2.3 2.4 3.5 5.3 3.5 8.5S14.3 18.1 12 20.5c-2.3-2.4-3.5-5.3-3.5-8.5S9.7 5.9 12 3.5Z" />
            </svg>
          {/if}
        </a>
      </li>
    {/each}
  </ul>
{/if}
