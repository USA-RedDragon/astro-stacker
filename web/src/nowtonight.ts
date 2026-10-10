import type { Conditions, HFRLimit, MoonNight, PlanBlock, PowerReport, Preview, SchedProject, SyncReport, TonightSub, WeatherReport, SafetyReport, MountReport } from './api/scheduler'
import { SITE_TZ, hm, shortDate } from './format'
import { ms, timeByTarget, type Tone } from './plan'

export interface Row {
  label: string
  value: string
}

export interface Pill {
  label: string
  value: string
  dot: string
  title?: string
}

const DOT_OK = 'var(--ok)'
const DOT_WARN = 'var(--warn)'
const DOT_BAD = 'var(--bad)'
const DOT_IDLE = 'var(--muted-foreground)'

export function noSourceText(s: { source: string; error?: string } | undefined, what: string): string {
  if (!s || s.source === 'none') return `No data source for ${what}.` + (s?.error ? ' ' + s.error.charAt(0).toUpperCase() + s.error.slice(1) + '.' : '')
  if (s.source === 'error') return `The ${what} source did not answer: ${s.error ?? 'unknown error'}.`
  return ''
}

export function hasData(s: { source: string } | undefined): boolean {
  return !!s && s.source !== 'none' && s.source !== 'error'
}

export function skyLabel(cloud?: number): string {
  if (cloud === undefined) return '—'
  if (cloud < 20) return 'Clear'
  if (cloud < 70) return 'Cloudy'
  return 'Overcast'
}

const kmh = (mps: number) => Math.round(mps * 3.6)

export function weatherRows(w: WeatherReport | undefined): Row[] {
  if (!w || !hasData(w)) return []
  if (w.connected === false) return []
  const rows: Row[] = []
  if (w.cloud_cover !== undefined) rows.push({ label: 'Sky', value: `${skyLabel(w.cloud_cover)} · ${Math.round(w.cloud_cover)}% cloud` })
  if (w.rain_rate !== undefined) rows.push({ label: 'Rain', value: w.rain_rate > 0 ? 'Raining' : 'Dry' })
  if (w.wind_speed !== undefined) rows.push({ label: 'Wind', value: `${kmh(w.wind_speed)} km/h` + (w.wind_gust !== undefined && w.wind_gust > w.wind_speed ? `, gusts ${kmh(w.wind_gust)}` : '') })
  if (w.humidity !== undefined) rows.push({ label: 'Humidity', value: `${Math.round(w.humidity)}%` })
  if (w.temperature !== undefined && w.dew_point !== undefined) rows.push({ label: 'Dew point gap', value: `${Math.round(w.temperature - w.dew_point)} °C` })
  else if (w.dew_point !== undefined) rows.push({ label: 'Dew point', value: `${Math.round(w.dew_point)} °C` })
  if (w.temperature !== undefined) rows.push({ label: 'Air', value: `${w.temperature.toFixed(1)} °C` })
  if (w.sky_temperature !== undefined) rows.push({ label: 'Sky temperature', value: `${w.sky_temperature.toFixed(1)} °C` })
  return rows
}

export function safetyBadge(s: SafetyReport | undefined): { label: string; tone: Tone } {
  if (!s || !hasData(s)) return { label: 'No data', tone: '' }
  if (s.connected === false) return { label: 'Monitor off', tone: 'warn' }
  if (s.safe === true) return { label: 'Safe', tone: 'ok' }
  if (s.safe === false) return { label: 'Unsafe', tone: 'bad' }
  return { label: 'Unknown', tone: '' }
}

export function weatherNote(w: WeatherReport | undefined, moonLine: string): string {
  const parts: string[] = []
  if (w && hasData(w) && w.connected === false) parts.push('The weather device is not connected in NINA.')
  else if (w && hasData(w)) parts.push('The weather device does not report sky quality.')
  if (moonLine) parts.push(moonLine)
  return parts.join(' ')
}

export function hmDuration(seconds: number): string {
  const m = Math.max(0, Math.round(seconds / 60))
  return `${Math.floor(m / 60)}:${String(m % 60).padStart(2, '0')}`
}

export interface PowerView {
  label: string
  tone: Tone
  charge: string
  model: string
  alert: string
  shutdown: string
  voltage: string
}

export function powerView(p: PowerReport | undefined): PowerView | null {
  if (!p || !hasData(p)) return null
  const onBattery = p.on_battery
  let alert = ''
  if (onBattery) {
    const on = p.on_battery_seconds
    alert = on !== undefined ? `On battery ${hmDuration(on)}` : 'On battery'
    if (p.shutdown_seconds && on !== undefined) {
      const left = p.shutdown_seconds - on
      alert += left > 0 ? ` · shutdown in ${hmDuration(left)}` : ' · shutdown due'
    }
  }
  return {
    label: onBattery ? (p.low_battery ? 'Battery low' : 'On battery') : p.flags?.includes('OL') ? 'On line' : 'Unknown',
    tone: onBattery ? (p.low_battery ? 'bad' : 'warn') : p.flags?.includes('OL') ? 'ok' : '',
    charge: p.charge !== undefined ? `${Math.round(p.charge)}%` : '—',
    model: p.model ?? '',
    alert,
    shutdown: p.shutdown_seconds ? `${p.shutdown_seconds} s on battery → park → Powerbox off → shutdown` : '',
    voltage: onBattery && p.input_voltage !== undefined ? `Input ${Math.round(p.input_voltage)} V` : '',
  }
}

export function lagText(seconds: number): string {
  if (seconds < 90) return `${Math.round(seconds)} s`
  if (seconds < 5400) return `${Math.round(seconds / 60)} min`
  return `${Math.round(seconds / 3600)} h`
}

function mountPill(m: MountReport | undefined): Pill {
  if (!m || !hasData(m)) return { label: 'Mount', value: m?.source === 'error' ? 'no answer' : 'no data source', dot: DOT_IDLE, title: noSourceText(m, 'mount') }
  if (m.connected === false) return { label: 'Mount', value: 'disconnected', dot: DOT_IDLE }
  if (m.slewing) return { label: 'Mount', value: 'slewing', dot: DOT_OK }
  if (m.parked) return { label: 'Mount', value: 'parked', dot: DOT_IDLE }
  if (m.tracking) return { label: 'Mount', value: 'tracking', dot: DOT_OK }
  if (m.at_home) return { label: 'Mount', value: 'at home', dot: DOT_IDLE }
  return { label: 'Mount', value: 'idle', dot: DOT_WARN }
}

function safetyPill(s: SafetyReport | undefined): Pill {
  if (!s || !hasData(s)) return { label: 'Safety monitor', value: s?.source === 'error' ? 'no answer' : 'no data source', dot: DOT_IDLE, title: noSourceText(s, 'safety monitor') }
  if (s.connected === false) return { label: 'Safety monitor', value: 'disconnected', dot: DOT_IDLE }
  if (s.safe === true) return { label: 'Safety monitor', value: 'safe', dot: DOT_OK }
  if (s.safe === false) return { label: 'Safety monitor', value: 'unsafe', dot: DOT_BAD }
  return { label: 'Safety monitor', value: 'unknown', dot: DOT_IDLE }
}

export const SYNC_WARN_SECONDS = 20 * 60

function syncPill(s: SyncReport | undefined): Pill {
  if (!s || !hasData(s)) return { label: 'Sync lag', value: s?.source === 'error' ? 'no access' : 'no data source', dot: DOT_IDLE, title: noSourceText(s, 'SymmetricDS') }
  const title = 'Time since SymmetricDS last delivered a batch or heartbeat from ' + (s.node || 'the other node')
  if (s.errors > 0) return { label: 'Sync lag', value: `${s.errors} ${s.errors === 1 ? 'batch' : 'batches'} in error`, dot: DOT_BAD, title }
  if (s.lag_seconds === undefined) return { label: 'Sync lag', value: '—', dot: DOT_IDLE, title }
  return { label: 'Sync lag', value: lagText(s.lag_seconds), dot: s.lag_seconds > SYNC_WARN_SECONDS ? DOT_WARN : DOT_OK, title }
}

export function conditionPills(c: Conditions | null): Pill[] {
  return [syncPill(c?.sync), mountPill(c?.mount), safetyPill(c?.safety)]
}

export function phaseName(age: number, synodic = 29.530588): string {
  const f = age / synodic
  if (f < 0.03 || f > 0.97) return 'new moon'
  if (f < 0.22) return 'waxing crescent'
  if (f < 0.28) return 'first quarter'
  if (f < 0.47) return 'waxing gibbous'
  if (f < 0.53) return 'full moon'
  if (f < 0.72) return 'waning gibbous'
  if (f < 0.78) return 'last quarter'
  return 'waning crescent'
}

export function moonLine(m: MoonNight | null, t0: number, t1: number): string {
  if (!m) return ''
  const pct = Math.round(m.illumination * 100)
  const inside = (v: string) => {
    const t = ms(v)
    return t >= t0 && t <= t1
  }
  const rises = m.rises.filter(inside)
  const sets = m.sets.filter(inside)
  const anyUp = m.samples.some((s) => s.alt > 0 && ms(s.t) >= t0 && ms(s.t) <= t1)
  let where: string
  if (!anyUp) where = 'below the horizon all night'
  else if (!rises.length && !sets.length) where = 'up all night'
  else {
    const bits: string[] = []
    if (sets.length) bits.push('sets ' + hm(sets[0]))
    if (rises.length) bits.push('rises ' + hm(rises[0]))
    where = bits.join(', ')
  }
  return `Moon ${pct}%, ${where}.`
}

export function moonPhaseText(m: MoonNight | null): string {
  if (!m) return ''
  const phase = phaseName(m.age)
  const parts = [`${Math.round(m.illumination * 100)}% lit`]
  if (phase !== 'new moon' && phase !== 'full moon') parts.push(phase)
  const nn = ms(m.next_new)
  const nf = ms(m.next_full)
  if (!isNaN(nn) && (isNaN(nf) || nn <= nf)) parts.push('new moon ' + shortDate(m.next_new))
  else if (!isNaN(nf)) parts.push('full moon ' + shortDate(m.next_full))
  return parts.join(' · ')
}

export function hfrLimitFor(limits: HFRLimit[] | undefined, subs: TonightSub[], targetId: number | undefined, filter?: string): HFRLimit | null {
  if (!limits?.length || targetId === undefined) return null
  const want = filter ?? [...subs].reverse().find((s) => s.target_id === targetId)?.filter
  if (!want) return null
  return limits.find((l) => l.target_id === targetId && l.filter === want) ?? null
}

export interface BalanceWarning {
  project: string
  projectGuid: string
  text: string
  balancingOff: boolean
}

function balancingWeights(blocks: PlanBlock[] | null | undefined): number[] {
  const out: number[] = []
  for (const b of blocks ?? []) {
    for (const s of b.scores ?? []) {
      if (/mosaic|panel/i.test(s.rule)) out.push(s.weight)
    }
  }
  return out
}

export function mosaicBalance(current: Preview | null | undefined, whatIf: Preview | null | undefined, projects: SchedProject[]): BalanceWarning[] {
  if (!whatIf) return []
  const after = timeByTarget(whatIf)
  const before = timeByTarget(current)
  const weights = [...balancingWeights(whatIf.blocks), ...balancingWeights(current?.blocks)]
  const balancingOff = weights.length > 0 && weights.every((w) => w === 0)
  const out: BalanceWarning[] = []
  for (const p of projects) {
    if (!p.isMosaic) continue
    const panels = p.targets.filter((t) => t.active)
    if (panels.length < 2) continue
    const secs = panels.map((t) => after.get(t.id)?.seconds ?? 0)
    const total = secs.reduce((a, b) => a + b, 0)
    if (total <= 0) continue
    const top = Math.max(...secs)
    const zero = secs.filter((s) => s === 0).length
    const topBefore = Math.max(...panels.map((t) => before.get(t.id)?.seconds ?? 0))
    if (top < 3600 || top / total < 0.6 || zero < panels.length / 2 || top <= topBefore) continue
    let text = `One ${p.name} panel would get ${(top / 3600).toFixed(1)} h while ${zero} of its ${panels.length} panels get none.`
    if (balancingOff) text += ' Panel balancing is off for this mosaic (its rule weight is 0).'
    out.push({ project: p.name, projectGuid: p.guid, text, balancingOff })
  }
  return out
}

const dayFmt = new Intl.DateTimeFormat('en-GB', { day: 'numeric', timeZone: SITE_TZ })
const monFmt = new Intl.DateTimeFormat('en-GB', { month: 'short', timeZone: SITE_TZ })

export function nightSpan(start: Date): string {
  const end = new Date(start.getTime() + 24 * 3600 * 1000)
  const m1 = monFmt.format(start)
  const m2 = monFmt.format(end)
  return m1 === m2 ? `${dayFmt.format(start)}–${dayFmt.format(end)} ${m1}` : `${dayFmt.format(start)} ${m1}–${dayFmt.format(end)} ${m2}`
}
