<script lang="ts">
  import officialIcon from '../assets/social/official.png';
  import twitterIcon from '../assets/social/twitter.png';
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

  /* Instagram 那枚图还没进来：用户发的 base64 在传输中截断，IDAT 的 CRC 对不上、解不出来，
     先空着走下面那条线稿兜底，等图到位再把 `instagram: instagramIcon` 加回来。 */
  const MARKS: Partial<Record<IconKind, string>> = {
    official: officialIcon,
    x: twitterIcon,
  };
</script>

{#if social.length}
  <ul class="flex flex-wrap items-center gap-1 {className}">
    {#each social as link (link.url)}
      {@const kind = kindOf(link)}
      {@const mark = MARKS[kind]}
      <li>
        <!-- 外链一律新窗口 + noopener：这些地址来自上游页面，不能让它拿到 window.opener。
             底座 32px 见方、图标 20px：品牌标本来就不是单色，硬压成我们那套线稿会认不出来，
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
                 在 #020408 上根本看不出形状，所以只对这一个做反相（黑牌变白牌、X 变黑）。 -->
            <img
              src={mark}
              alt=""
              width="48"
              height="48"
              class="h-5 w-5 {kind === 'x' ? 'invert' : ''}"
              loading="lazy"
              decoding="async"
            />
          {:else}
            <svg
              viewBox="0 0 24 24"
              class="h-[18px] w-[18px] text-muted"
              aria-hidden="true"
              fill="none"
              stroke="currentColor"
              stroke-width="1.8"
              stroke-linecap="round"
              stroke-linejoin="round"
            >
              {#if kind === 'instagram'}
                <rect x="3.5" y="3.5" width="17" height="17" rx="4.6" />
                <circle cx="12" cy="12" r="4" />
                <circle cx="16.9" cy="7.1" r="0.9" fill="currentColor" stroke="none" />
              {:else}
                <circle cx="12" cy="12" r="8.5" />
                <path d="M3.5 12h17" />
                <path d="M12 3.5c2.3 2.4 3.5 5.3 3.5 8.5S14.3 18.1 12 20.5c-2.3-2.4-3.5-5.3-3.5-8.5S9.7 5.9 12 3.5Z" />
              {/if}
            </svg>
          {/if}
        </a>
      </li>
    {/each}
  </ul>
{/if}
