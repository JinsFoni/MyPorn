<script lang="ts">
  import QRCode from 'qrcode';
  import { api, messageFor } from '../lib/api';
  import type { BiliQrcodePoll } from '../lib/types';

  /**
   * B 站扫码登录。
   *
   * 为什么要这个组件（2026-10-30 两次实测）：手填 Cookie 这条路有两个躲不开的坑——
   * 1. SESSDATA 是 HttpOnly，控制台 document.cookie 里没有它，抄来的串打 nav 一律 -101；
   * 2. 手填的串没有 refresh_token，B 站一作废登录态就只能重填。
   * 扫码把五个字段（含 refresh_token）一次拿到并直接落库，前端不碰任何凭据。
   */
  interface Props {
    open: boolean;
    onClose: () => void;
    /** 登录成功（服务端已写库）后回调，调用方刷新健康状态即可 */
    onSuccess: (uname: string) => void;
  }

  let { open, onClose, onSuccess }: Props = $props();

  /** 二维码有效期 180 秒，与 B 站一致；轮询 2 秒一次（和 bili-sync 一样）。 */
  const QR_EXPIRE_SECONDS = 180;
  const POLL_MS = 2000;

  let imageUrl = $state('');
  let status = $state<'loading' | 'showing' | 'scanned' | 'success' | 'expired' | 'error'>('loading');
  let message = $state('');
  let countdown = $state(QR_EXPIRE_SECONDS);
  let uname = $state('');
  let timer: ReturnType<typeof setTimeout> | null = null;
  let countdownTimer: ReturnType<typeof setInterval> | null = null;
  let pollKey = '';
  let stopped = false;

  function clearTimers(): void {
    if (timer !== null) {
      clearTimeout(timer);
      timer = null;
    }
    if (countdownTimer !== null) {
      clearInterval(countdownTimer);
      countdownTimer = null;
    }
  }

  async function start(): Promise<void> {
    stopped = false;
    clearTimers();
    status = 'loading';
    message = '';
    imageUrl = '';
    uname = '';
    countdown = QR_EXPIRE_SECONDS;
    try {
      const qr = await api.biliQrcodeGenerate();
      pollKey = qr.key;
      imageUrl = await QRCode.toDataURL(qr.url, { width: 240, margin: 1 });
      status = 'showing';
      countdownTimer = setInterval(() => {
        countdown -= 1;
        if (countdown <= 0) {
          status = 'expired';
          message = '二维码已过期';
          clearTimers();
        }
      }, 1000);
      schedulePoll();
    } catch (err) {
      status = 'error';
      message = messageFor(err);
    }
  }

  function schedulePoll(): void {
    timer = setTimeout(() => void poll(), POLL_MS);
  }

  async function poll(): Promise<void> {
    if (stopped) return;
    try {
      const res: BiliQrcodePoll = await api.biliQrcodePoll(pollKey);
      if (res.status === 'success') {
        clearTimers();
        status = 'success';
        uname = res.uname ?? '';
        onSuccess(res.uname ?? '');
        return;
      }
      if (res.status === 'expired') {
        clearTimers();
        status = 'expired';
        message = res.message ?? '二维码已过期';
        return;
      }
      if (res.status === 'scanned') {
        status = 'scanned';
        message = res.message ?? '已扫描，请在手机上确认';
      } else {
        message = res.message ?? '等待扫码';
      }
      schedulePoll();
    } catch (err) {
      // 单次失败不结束会话：扫码要几十秒，中间抖一下很正常。
      message = messageFor(err);
      schedulePoll();
    }
  }

  function close(): void {
    stopped = true;
    clearTimers();
    onClose();
  }

  $effect(() => {
    if (open) {
      void start();
    } else {
      stopped = true;
      clearTimers();
    }
    return () => {
      stopped = true;
      clearTimers();
    };
  });
</script>

{#if open}
  <div class="fixed inset-0 z-50 flex items-end justify-center sm:items-center">
    <button
      type="button"
      class="absolute inset-0 bg-ink/80 backdrop-blur-sm"
      aria-label="关闭"
      onclick={close}
    ></button>

    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="bili-login-title"
      tabindex="-1"
      class="relative w-full max-w-sm rounded-t-2xl bg-raise p-4 mb-safe sm:rounded-2xl"
    >
      <div class="flex items-start justify-between gap-3">
        <div>
          <h2 id="bili-login-title" class="text-title font-medium tracking-tight text-fg">扫码登录 B 站</h2>
          <p class="mt-0.5 text-meta text-dim">用手机 B 站 App 扫一下，登录态自动存进服务端</p>
        </div>
        <button
          type="button"
          class="btn btn-ghost -mr-1 h-8 w-8 shrink-0 p-0"
          aria-label="关闭"
          onclick={close}
        >
          <svg viewBox="0 0 20 20" class="h-4 w-4" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
            <path d="m5.5 5.5 9 9M14.5 5.5l-9 9" />
          </svg>
        </button>
      </div>

      <div class="mt-4 flex flex-col items-center gap-3">
        {#if status === 'success'}
          <div class="flex h-[240px] w-[240px] flex-col items-center justify-center gap-2 rounded-xl bg-panel">
            <span class="text-2xl text-sage">✓</span>
            <p class="text-ui text-fg">{uname ? `已登录 ${uname}` : '登录成功'}</p>
            <p class="text-meta text-dim">1080P 以上可用，且能自动续期</p>
          </div>
        {:else if imageUrl}
          <div class="relative">
            <img src={imageUrl} alt="B 站登录二维码" class="h-[240px] w-[240px] rounded-xl bg-white p-2" />
            {#if status === 'expired'}
              <button
                type="button"
                class="absolute inset-0 flex flex-col items-center justify-center gap-2 rounded-xl bg-ink/80 text-ui text-fg"
                onclick={() => void start()}
              >
                <span>二维码已过期</span>
                <span class="text-meta text-dim">点一下重新生成</span>
              </button>
            {/if}
          </div>
        {:else}
          <div class="flex h-[240px] w-[240px] items-center justify-center rounded-xl bg-panel text-meta text-dim">
            {status === 'error' ? '二维码拿不到' : '正在生成二维码…'}
          </div>
        {/if}

        <p class="text-center text-meta {status === 'error' ? 'text-brick' : 'text-dim'}">
          {#if status === 'showing'}
            打开手机 B 站 → 我的 → 扫一扫（{countdown} 秒后过期）
          {:else}
            {message}
          {/if}
        </p>

        {#if status === 'expired' || status === 'error'}
          <button type="button" class="btn h-9" onclick={() => void start()}>重新生成</button>
        {/if}
      </div>

      <p class="mt-4 text-meta text-dim">
        扫码比手填 Cookie 多拿到两样东西：HttpOnly 的 SESSDATA（手抄永远抄不到），以及
        refresh_token——有它才能在 B 站作废登录态前自动续期，不用再反复重填。
      </p>
    </div>
  </div>
{/if}
