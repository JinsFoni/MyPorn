<script lang="ts">
  import { api, ApiError, messageFor } from '../lib/api';
  import { navigate, router } from '../lib/router.svelte';

  let password = $state('');
  let submitting = $state(false);
  let error = $state('');
  let locked = $state(false);

  const nextPath = $derived(router.query.get('next') ?? '/');

  async function submit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (submitting || password.length === 0) return;
    submitting = true;
    error = '';
    try {
      await api.login(password);
      password = '';
      const target = nextPath.startsWith('/') && !nextPath.startsWith('//') ? nextPath : '/';
      navigate(target, { replace: true });
    } catch (err) {
      error = messageFor(err);
      if (err instanceof ApiError && err.code === 'too_many_attempts') {
        locked = true;
        password = '';
      }
    } finally {
      submitting = false;
    }
  }
</script>

<main class="relative isolate flex min-h-dvh flex-col items-center justify-center px-4 py-10">
  <div
    class="canvas-glow pointer-events-none absolute inset-x-0 top-0 -z-10 h-[49vh]"
    aria-hidden="true"
  ></div>

  <div class="relative w-full max-w-sm">
    <p class="text-hero font-normal tracking-[-0.01em] text-fg">MyPorn</p>
    <p class="mt-2 text-ui text-muted">输入访问密码进入你的影片库。</p>

    <form class="mt-8 space-y-3" onsubmit={submit} novalidate>
      <div>
        <label for="password" class="label-meta mb-1.5">密码</label>
        <input
          id="password"
          class="field"
          type="password"
          name="password"
          autocomplete="current-password"
          required
          bind:value={password}
          disabled={submitting || locked}
          aria-describedby={error ? 'login-error' : undefined}
          aria-invalid={error ? 'true' : undefined}
        />
      </div>

      {#if error}
        <p id="login-error" class="text-ui text-brick" role="alert">{error}</p>
      {/if}

      <button type="submit" class="btn btn-primary w-full" disabled={submitting || locked || password.length === 0}>
        {submitting ? '正在验证…' : locked ? '已锁定' : '登录'}
      </button>
    </form>

    <p class="mt-6 text-meta text-dim">
      单用户自托管 · {locked ? '连续失败 5 次会锁定 5 分钟，稍后再试。' : '密码只保存在你自己的服务器上。'}
    </p>
  </div>
</main>
