const units = ['B', 'KB', 'MB', 'GB', 'TB']

/** 1536 -> "1.5 KB". Decimal units, as the network people count. */
export function bytes(n: number | null | undefined, digits = 1): string {
  if (n == null || !isFinite(n)) return '-'
  const sign = n < 0 ? '-' : ''
  let v = Math.abs(n)
  let i = 0
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000
    i++
  }
  return sign + (i === 0 ? String(Math.round(v)) : v.toFixed(v >= 100 ? 0 : digits)) + ' ' + units[i]
}

export function percent(n: number | null | undefined, digits = 1): string {
  if (n == null || !isFinite(n)) return '-'
  return n.toFixed(digits) + '%'
}

export function ratio(n: number | null | undefined, digits = 1): string {
  if (n == null || !isFinite(n)) return '-'
  return (n * 100).toFixed(digits) + '%'
}

export function ms(n: number | null | undefined): string {
  if (n == null || !isFinite(n)) return '-'
  if (n >= 1000) return (n / 1000).toFixed(n >= 10000 ? 0 : 1) + ' s'
  if (n >= 100) return Math.round(n) + ' ms'
  return n.toFixed(n >= 10 ? 0 : 1) + ' ms'
}

export function count(n: number | null | undefined): string {
  if (n == null || !isFinite(n)) return '-'
  return new Intl.NumberFormat().format(n)
}

export function compact(n: number | null | undefined): string {
  if (n == null || !isFinite(n)) return '-'
  return new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 }).format(n)
}

export function duration(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds))
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m ${s % 60}s`
  return `${s}s`
}

/** "3 min ago", "just now". */
export function ago(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return 'never'
  const diff = Math.round((now - new Date(iso).getTime()) / 1000)
  if (diff < 5) return 'just now'
  if (diff < 60) return `${diff} s ago`
  if (diff < 3600) return `${Math.floor(diff / 60)} min ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)} h ago`
  return `${Math.floor(diff / 86400)} d ago`
}

export function clock(iso: string): string {
  const d = new Date(iso)
  return d.toLocaleTimeString(undefined, { hour12: false }) + '.' + String(d.getMilliseconds()).padStart(3, '0')
}

export function dateTime(iso: string | null | undefined): string {
  return iso ? new Date(iso).toLocaleString() : '-'
}

/** The distance of dssim: 0.00123 */
export function distance(n: number | null | undefined): string {
  if (n == null || !isFinite(n)) return '-'
  return n.toFixed(5)
}
