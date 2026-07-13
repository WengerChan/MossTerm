/**
 * 格式化工具
 * --------------------------------------------------------------------
 * 字节、时间、时长、坐标 等的展示格式化。
 */

/**
 * 字节数 → 人类可读字符串（B / KB / MB / GB / TB）
 * 默认使用 1024 进制
 */
export function formatBytes(bytes: number, decimals = 1): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "—";
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB", "TB", "PB"];
  let value = bytes / 1024;
  let i = 0;
  while (value >= 1024 && i < units.length - 1) {
    value /= 1024;
    i += 1;
  }
  return `${value.toFixed(decimals)} ${units[i]}`;
}

/**
 * 速率：bytes/s → 人类可读
 */
export function formatRate(bytesPerSec: number, decimals = 1): string {
  if (!Number.isFinite(bytesPerSec) || bytesPerSec <= 0) return "0 B/s";
  return `${formatBytes(bytesPerSec, decimals)}/s`;
}

/**
 * 毫秒时长 → "HH:MM:SS" 或 "MM:SS"
 */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return "—";
  const totalSec = Math.floor(ms / 1000);
  const h = Math.floor(totalSec / 3600);
  const m = Math.floor((totalSec % 3600) / 60);
  const s = totalSec % 60;
  const pad = (n: number) => n.toString().padStart(2, "0");
  if (h > 0) return `${pad(h)}:${pad(m)}:${pad(s)}`;
  return `${pad(m)}:${pad(s)}`;
}

/**
 * 相对时间："3s ago" / "5m ago" / "2h ago" / "2024-01-01"
 */
export function formatRelativeTime(ts: number, now: number = Date.now()): string {
  if (!Number.isFinite(ts)) return "—";
  const diff = now - ts;
  if (diff < 0) return formatAbsoluteTime(ts);
  const sec = Math.floor(diff / 1000);
  if (sec < 60) return `${sec}s ago`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.floor(min / 60);
  if (hr < 24) return `${hr}h ago`;
  return formatAbsoluteTime(ts);
}

/** ISO / YYYY-MM-DD HH:mm */
export function formatAbsoluteTime(ts: number): string {
  const d = new Date(ts);
  const pad = (n: number) => n.toString().padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ` +
         `${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/**
 * 主机:端口 → "host:port"，port 为协议默认时省略
 */
export function formatHostPort(host: string, port: number, defaultPort: number): string {
  return port === defaultPort ? host : `${host}:${port}`;
}

/**
 * 截断字符串，超长部分加省略号
 */
export function truncate(text: string, maxLen: number): string {
  if (text.length <= maxLen) return text;
  if (maxLen <= 1) return "…";
  return text.slice(0, maxLen - 1) + "…";
}

/**
 * Unix mode (0755) → 字符串 "rwxr-xr-x"
 */
export function formatFileMode(mode: number): string {
  // 截取低 9 位
  const perms = ["---", "--x", "-w-", "-wx", "r--", "r-x", "rw-", "rwx"];
  const m = mode & 0o777;
  return (
    perms[(m >> 6) & 0b111] +
    perms[(m >> 3) & 0b111] +
    perms[m & 0b111]
  );
}

/**
 * 进度百分比（0-100），自动 clamp
 */
export function formatPercent(transferred: number, total: number): string {
  if (!Number.isFinite(total) || total <= 0) return "—";
  const pct = Math.max(0, Math.min(100, (transferred / total) * 100));
  return `${pct.toFixed(1)}%`;
}
