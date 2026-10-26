<script lang="ts">
  import type { ProfileField, SocialLink } from '../lib/types';

  interface Props {
    /** 资料行（性别、身高、出身地……），顺序就是平台页面上的顺序 */
    profile: ProfileField[];
    /** 站外链接（官方网址 / Twitter / Instagram …） */
    social: SocialLink[];
    /** 简介（平台上的「简介」区块），可能很长 */
    bio?: string | null;
  }

  let { profile, social, bio = null }: Props = $props();

  /* 只做三档单色字形：X、Instagram、其余一律地球。
     整个设计稿只有冷白一个色相，所以不引品牌原色（Instagram 的渐变在这里会炸）。
     认域名而不是认标签文字——标签是站点本地化的，域名是稳定的。 */
  type IconKind = 'x' | 'instagram' | 'web';

  function kindOf(url: string): IconKind {
    const u = url.toLowerCase();
    if (u.includes('twitter.com') || u.includes('//x.com') || u.includes('x.com/')) return 'x';
    if (u.includes('instagram.com')) return 'instagram';
    return 'web';
  }
</script>

{#snippet icon(kind: IconKind)}
  <svg
    viewBox="0 0 24 24"
    class="h-3.5 w-3.5 shrink-0"
    aria-hidden="true"
    fill="none"
    stroke="currentColor"
    stroke-width="1.6"
    stroke-linecap="round"
    stroke-linejoin="round"
  >
    {#if kind === 'x'}
      <path
        fill="currentColor"
        stroke="none"
        d="M17.53 3h2.94l-6.42 7.34L21.6 21h-5.9l-4.2-5.5L6.7 21H3.75l6.87-7.85L3 3h6.05l3.8 5.03L17.53 3Zm-1.03 16.2h1.63L7.4 4.72H5.65L16.5 19.2Z"
      />
    {:else if kind === 'instagram'}
      <rect x="3.5" y="3.5" width="17" height="17" rx="4.6" />
      <circle cx="12" cy="12" r="4" />
      <circle cx="16.9" cy="7.1" r="0.9" fill="currentColor" stroke="none" />
    {:else}
      <circle cx="12" cy="12" r="8.5" />
      <path d="M3.5 12h17" />
      <path d="M12 3.5c2.3 2.4 3.5 5.3 3.5 8.5S14.3 18.1 12 20.5c-2.3-2.4-3.5-5.3-3.5-8.5S9.7 5.9 12 3.5Z" />
    {/if}
  </svg>
{/snippet}

{#if bio || profile.length || social.length}
  <!-- 左边是资料（宽），右边是链接（窄）；窄屏两张卡上下叠。
       资料字段按「标签 / 值」两列排，窄屏单列、sm 起两列——字段多时不会拉成一条长蛇。 -->
  <section class="mt-6 grid gap-3 lg:grid-cols-[minmax(0,1.65fr)_minmax(0,1fr)]">
    {#if bio || profile.length}
      <div class="panel p-4 sm:p-5">
        <h3 class="label-meta">个人资料</h3>

        {#if bio}
          <!-- 简介：站点上就是一段自由文本，换行原样保留 -->
          <p class="mt-2.5 whitespace-pre-line text-ui text-muted">{bio}</p>
        {/if}

        {#if profile.length}
          <dl
            class="grid grid-cols-1 gap-x-6 gap-y-2 sm:grid-cols-2 {bio
              ? 'mt-3.5 border-t border-line pt-3.5'
              : 'mt-2.5'}"
          >
            {#each profile as field (field.label)}
              <div class="grid grid-cols-[5rem_minmax(0,1fr)] items-baseline gap-2">
                <dt class="text-meta text-dim">{field.label}</dt>
                <dd class="min-w-0 break-words text-ui text-fg">{field.value}</dd>
              </div>
            {/each}
          </dl>
        {/if}
      </div>
    {/if}

    {#if social.length}
      <div class="panel h-fit p-4 sm:p-5">
        <h3 class="label-meta">社交媒体</h3>
        <ul class="mt-2.5 flex flex-wrap gap-2">
          {#each social as link (link.url)}
            <li>
              <!-- 外链一律新窗口 + noopener：这些地址来自上游页面，不能让它拿到 window.opener -->
              <a
                href={link.url}
                target="_blank"
                rel="noreferrer noopener"
                title="在新窗口打开 {link.label}"
                class="chip bg-raise transition-colors hover:bg-white/15 hover:text-fg"
              >
                {@render icon(kindOf(link.url))}
                <span>{link.label}</span>
              </a>
            </li>
          {/each}
        </ul>
      </div>
    {/if}
  </section>
{/if}
