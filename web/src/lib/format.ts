/**
 * 时间 / 数字格式化。不引库：Intl.DateTimeFormat('zh-CN') + 手写相对时间。
 */

import type { Body } from './types';

const dateTimeFmt = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' });

function toDate(iso: string | null | undefined): Date | null {
  if (!iso) return null;
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? null : d;
}

/** 日期：2025-10-26（本地时区；Intl 的 zh-CN 会给 2025/10/26，这里要横杠） */
export function formatDate(iso: string | null | undefined): string {
  const d = toDate(iso);
  if (!d) return '—';
  const month = String(d.getMonth() + 1).padStart(2, '0');
  const day = String(d.getDate()).padStart(2, '0');
  return `${String(d.getFullYear())}-${month}-${day}`;
}

export function formatDateTime(iso: string | null | undefined): string {
  const d = toDate(iso);
  return d ? dateTimeFmt.format(d) : '—';
}

/** 相对时间：刚刚 / N 分钟前 / N 小时前 / N 天前 / N 个月前 / N 年前 */
export function relativeTime(iso: string | null | undefined): string {
  const d = toDate(iso);
  if (!d) return '—';
  const seconds = Math.round((Date.now() - d.getTime()) / 1000);
  if (seconds < 0) return '刚刚';
  if (seconds < 60) return '刚刚';
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${String(minutes)}分钟前`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${String(hours)}小时前`;
  const days = Math.floor(hours / 24);
  if (days < 30) return `${String(days)}天前`;
  const months = Math.floor(days / 30);
  if (months < 12) return `${String(months)}个月前`;
  return `${String(Math.floor(months / 12))}年前`;
}

/** 未来时间：即将 / N 分钟后 / N 小时后 / N 天后（用于「下次同步」） */
export function relativeFuture(iso: string | null | undefined): string {
  const d = toDate(iso);
  if (!d) return '—';
  const seconds = Math.round((d.getTime() - Date.now()) / 1000);
  if (seconds <= 60) return '即将';
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${String(minutes)}分钟后`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${String(hours)}小时后`;
  return `${String(Math.floor(hours / 24))}天后`;
}

/** 秒 -> 03:24 / 1:03:24 */
export function formatDuration(totalSeconds: number): string {
  const safe = Math.max(0, Math.floor(totalSeconds));
  const hours = Math.floor(safe / 3600);
  const minutes = Math.floor((safe % 3600) / 60);
  const seconds = safe % 60;
  const mm = hours > 0 ? String(minutes).padStart(2, '0') : String(minutes);
  return hours > 0
    ? `${String(hours)}:${mm}:${String(seconds).padStart(2, '0')}`
    : `${mm}:${String(seconds).padStart(2, '0')}`;
}

/** 播放器时间轴：总是 mm:ss */
export function formatTimecode(totalSeconds: number): string {
  const safe = Math.max(0, Math.floor(totalSeconds));
  const minutes = Math.floor(safe / 60);
  const seconds = safe % 60;
  return `${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}`;
}

/** 12345 -> 1.2 万 */
export function formatCount(value: number | null | undefined): string {
  if (value === null || value === undefined) return '—';
  if (value < 10000) return value.toLocaleString('zh-CN');
  if (value < 100000000) return `${(value / 10000).toFixed(value % 10000 === 0 ? 0 : 1)}万`;
  return `${(value / 100000000).toFixed(1)}亿`;
}

/** 排名：整数加千分位（不走 formatCount 的「万/亿」缩写——「排名 1.2万」没人看得懂）；0 表示没数据 */
export function formatRank(value: number | null | undefined): string {
  if (!value || value <= 0) return '';
  return value.toLocaleString('zh-CN');
}

/** 身材：167cm · B89 · W64 · H91（后端已归一到厘米，这里只管排版；缺项直接不写） */
export function formatBody(body: Body | null | undefined): string {
  if (!body) return '';
  const parts: string[] = [];
  if (body.heightCm > 0) parts.push(`${String(body.heightCm)}cm`);
  if (body.bust > 0) parts.push(`B${String(body.bust)}`);
  if (body.waist > 0) parts.push(`W${String(body.waist)}`);
  if (body.hips > 0) parts.push(`H${String(body.hips)}`);
  return parts.join(' · ');
}

export function formatBandwidth(bitsPerSecond: number): string {
  if (!Number.isFinite(bitsPerSecond) || bitsPerSecond <= 0) return '—';
  return `${(bitsPerSecond / 1_000_000).toFixed(1)} Mbps`;
}

/** 字节数 -> 1.2 GB（1024 进制） */
export function formatBytes(bytes: number | null | undefined): string {
  if (bytes === null || bytes === undefined || !Number.isFinite(bytes) || bytes < 0) return '—';
  if (bytes === 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  const digits = value >= 100 || unit === 0 ? 0 : 1;
  return `${value.toFixed(digits)} ${units[unit] ?? 'TB'}`;
}

/** 下载速度 */
export function formatSpeed(bytesPerSecond: number | null | undefined): string {
  if (!bytesPerSecond || bytesPerSecond <= 0) return '—';
  return `${formatBytes(bytesPerSecond)}/s`;
}

/** 剩余时间：45 秒 / 3 分 / 1 小时 5 分 */
export function formatEta(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined || !Number.isFinite(seconds) || seconds <= 0) return '—';
  const s = Math.round(seconds);
  if (s < 60) return `${String(s)} 秒`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${String(m)} 分`;
  return `${String(Math.floor(m / 60))} 小时 ${String(m % 60)} 分`;
}
