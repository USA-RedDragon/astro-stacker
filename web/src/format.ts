export const SITE_TZ = 'America/Chicago'

const hmFmt = new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', hour12: false, timeZone: SITE_TZ })
const dateFmt = new Intl.DateTimeFormat('en-GB', { weekday: 'short', day: 'numeric', month: 'short', timeZone: SITE_TZ })

export function toDate(v: string | number | Date | undefined | null): Date | null {
  if (v === undefined || v === null || v === '') return null
  const d = v instanceof Date ? v : new Date(v)
  return isNaN(d.getTime()) ? null : d
}

export function hm(v: string | number | Date | undefined | null): string {
  const d = toDate(v)
  return d ? hmFmt.format(d) : ''
}

export function shortDate(v: string | number | Date | undefined | null): string {
  const d = toDate(v)
  return d ? dateFmt.format(d) : ''
}

export function mmss(seconds: number): string {
  const s = Math.max(0, Math.round(seconds))
  const m = Math.floor(s / 60)
  return `${m}:${String(s % 60).padStart(2, '0')}`
}

export function ago(v: string | number | Date | undefined | null, now: number = Date.now()): string {
  const d = toDate(v)
  if (!d) return ''
  const s = Math.max(0, Math.round((now - d.getTime()) / 1000))
  if (s < 60) return `${s} s ago`
  if (s < 3600) return `${Math.round(s / 60)} min ago`
  return `${Math.round(s / 3600)} h ago`
}

export function show(v: unknown): string {
  if (v === null || v === undefined) return '—'
  if (typeof v === 'string') return v
  return JSON.stringify(v)
}
