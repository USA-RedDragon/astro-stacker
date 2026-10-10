import type { CommandRecord, EditPayload } from './api/commands'
import type {
  OverrideField,
  Override,
  PlanBlock,
  Preview,
  RuleScore,
  SchedProject,
  SchedulerTarget,
  TonightSub,
  Twilight,
} from './api/scheduler'
import { hm } from './format'

export const PRIORITIES = ['Low', 'Normal', 'High']
export const STATES = ['Draft', 'Active', 'Inactive', 'Closed']
export const MINUTE_CHOICES = [0, 15, 30, 45, 60, 90, 120]

export const HOUR = 3600 * 1000

export function ms(v?: string | null): number {
  if (!v) return NaN
  const t = Date.parse(v)
  return isNaN(t) ? NaN : t
}

export type FilterKey = 'lum' | 'red' | 'green' | 'blue' | 'ha' | 'oiii' | 'sii' | 'other'

export function filterKey(f?: string | null): FilterKey {
  const s = (f ?? '').toLowerCase().replace(/[\s_-]/g, '')
  if (s === 'l' || s === 'lum' || s === 'luminance' || s === 'clear') return 'lum'
  if (s === 'r' || s === 'red') return 'red'
  if (s === 'g' || s === 'green') return 'green'
  if (s === 'b' || s === 'blue') return 'blue'
  if (s === 'h' || s === 'ha' || s === 'halpha' || s === 'hydrogenalpha') return 'ha'
  if (s === 'o' || s === 'oiii' || s === 'o3') return 'oiii'
  if (s === 's' || s === 'sii' || s === 's2') return 'sii'
  return 'other'
}

const FILTER_NAMES: Record<FilterKey, string> = {
  lum: 'Luminance',
  red: 'Red',
  green: 'Green',
  blue: 'Blue',
  ha: 'H-a',
  oiii: 'O-III',
  sii: 'S-II',
  other: '',
}

const FILTER_SHORT: Record<FilterKey, string> = {
  lum: 'L',
  red: 'R',
  green: 'G',
  blue: 'B',
  ha: 'Ha',
  oiii: 'OIII',
  sii: 'SII',
  other: '',
}

export function filterShort(f?: string | null): string {
  const k = filterKey(f)
  return k === 'other' ? f || '?' : FILTER_SHORT[k]
}

export function filterName(f?: string | null): string {
  const k = filterKey(f)
  return k === 'other' ? f || 'Unknown' : FILTER_NAMES[k]
}

export function filterColor(f?: string | null): string {
  const k = filterKey(f)
  return k === 'other' ? 'var(--muted-foreground)' : `var(--${k})`
}

export function priorityName(p?: number | null): string {
  return p === undefined || p === null ? '' : (PRIORITIES[p] ?? String(p))
}

export function stateName(s?: number | null): string {
  return s === undefined || s === null ? '' : (STATES[s] ?? String(s))
}

export function raHms(hours?: number | null): string {
  if (hours === undefined || hours === null || isNaN(hours)) return ''
  let total = Math.round((((hours % 24) + 24) % 24) * 3600)
  const h = Math.floor(total / 3600)
  total -= h * 3600
  const m = Math.floor(total / 60)
  const s = total - m * 60
  return `${String(h).padStart(2, '0')}h ${String(m).padStart(2, '0')}m ${String(s).padStart(2, '0')}s`
}

export function decDm(deg?: number | null): string {
  if (deg === undefined || deg === null || isNaN(deg)) return ''
  const sign = deg < 0 ? '−' : '+'
  const total = Math.round(Math.abs(deg) * 60)
  const d = Math.floor(total / 60)
  const m = total - d * 60
  return `${sign}${String(d).padStart(2, '0')}° ${String(m).padStart(2, '0')}′`
}

export function duration(seconds: number): string {
  const mins = Math.round(Math.max(0, seconds) / 60)
  const h = Math.floor(mins / 60)
  const m = mins % 60
  if (h === 0) return `${m} min`
  if (m === 0) return `${h} h`
  return `${h} h ${m} min`
}

export function signedHours(seconds: number): string {
  const h = seconds / 3600
  const v = Math.abs(h) < 0.05 ? 0 : h
  return (v > 0 ? '+' : v < 0 ? '−' : '') + Math.abs(v).toFixed(1) + ' h'
}

export function median(xs: number[]): number | null {
  const v = xs.filter((x) => isFinite(x)).sort((a, b) => a - b)
  if (!v.length) return null
  const mid = Math.floor(v.length / 2)
  return v.length % 2 ? v[mid] : (v[mid - 1] + v[mid]) / 2
}

export interface Run {
  start: number
  end: number
  wait: boolean
  project_id?: number
  project_name?: string
  target_id?: number
  target_name?: string
  blocks: PlanBlock[]
  pick?: PlanBlock
  reason?: string
  subs: number
}

export function runs(blocks: PlanBlock[] | null | undefined): Run[] {
  const out: Run[] = []
  for (const b of blocks ?? []) {
    const s = ms(b.start)
    const e = ms(b.end)
    if (isNaN(s) || isNaN(e)) continue
    const last = out[out.length - 1]
    const same = last && last.wait === b.wait && (b.wait || last.target_id === b.target_id) && !(b.picked && !b.wait)
    if (same) {
      last.end = Math.max(last.end, e)
      last.blocks.push(b)
      last.reason = b.reason || last.reason
      last.subs += b.count ?? 0
      continue
    }
    out.push({
      start: s,
      end: e,
      wait: b.wait,
      project_id: b.project_id,
      project_name: b.project_name,
      target_id: b.target_id,
      target_name: b.target_name,
      blocks: [b],
      pick: b.picked ? b : undefined,
      reason: b.reason,
      subs: b.count ?? 0,
    })
  }
  return out
}

export function runFilters(r: Run): string {
  const seen = new Map<string, number>()
  for (const b of r.blocks) {
    const name = filterName(b.filter)
    if (!seen.has(name)) seen.set(name, b.exposure_seconds ?? 0)
  }
  return [...seen.entries()].map(([n, s]) => (s ? `${n} ${Math.round(s)} s` : n)).join(' · ')
}

export interface Scale {
  t0: number
  t1: number
  x0: number
  x1: number
  x: (t: number) => number
}

export function scale(t0: number, t1: number, x0: number, x1: number): Scale {
  const span = t1 - t0 || 1
  return { t0, t1, x0, x1, x: (t: number) => x0 + ((t - t0) / span) * (x1 - x0) }
}

export function nightRange(p: Preview | null | undefined): [number, number] | null {
  if (!p) return null
  const tw = p.twilight ?? {}
  const startCandidates = [ms(tw.sunset), ms(tw.civil_dusk), ms(p.start)]
  const endCandidates = [ms(tw.sunrise), ms(tw.civil_dawn), ms(p.end)]
  for (const b of p.blocks ?? []) {
    startCandidates.push(ms(b.start))
    endCandidates.push(ms(b.end))
  }
  const starts = startCandidates.filter((t) => !isNaN(t))
  const ends = endCandidates.filter((t) => !isNaN(t))
  if (!starts.length || !ends.length) return null
  const t0 = Math.min(...starts) - HOUR / 2
  const t1 = Math.max(...ends) + HOUR / 2
  return t1 > t0 ? [t0, t1] : null
}

export interface Band {
  x: number
  w: number
  opacity: number
}

export function twilightBands(tw: Twilight | null | undefined, sc: Scale): Band[] {
  if (!tw) return []
  const levels: [number, number][] = [
    [sc.t0, 0.45],
    [ms(tw.sunset), 0.32],
    [ms(tw.civil_dusk), 0.22],
    [ms(tw.nautical_dusk), 0.1],
    [ms(tw.astronomical_dusk), 0],
    [ms(tw.astronomical_dawn), 0.1],
    [ms(tw.nautical_dawn), 0.22],
    [ms(tw.civil_dawn), 0.32],
    [ms(tw.sunrise), 0.45],
  ]
  const pts = levels.filter(([t]) => !isNaN(t))
  const out: Band[] = []
  for (let i = 0; i < pts.length; i++) {
    const [t, op] = pts[i]
    const next = i + 1 < pts.length ? pts[i + 1][0] : sc.t1
    const a = Math.max(t, sc.t0)
    const b = Math.min(next, sc.t1)
    if (op > 0 && b > a) out.push({ x: sc.x(a), w: sc.x(b) - sc.x(a), opacity: op })
  }
  return out
}

export function hourTicks(t0: number, t1: number, step = HOUR): number[] {
  const out: number[] = []
  for (let t = Math.ceil(t0 / step) * step; t <= t1; t += step) out.push(t)
  return out
}

export interface RuleColumn {
  rule: string
  weight: number
}

export interface PickRow {
  time: string
  target: string
  project: string
  scores: Record<string, number>
  total: number | null
  runner: string
  ends: string
}

export function pickTable(blocks: PlanBlock[] | null | undefined): { columns: RuleColumn[]; rows: PickRow[] } {
  const columns: RuleColumn[] = []
  const rows: PickRow[] = []
  for (const r of runs(blocks)) {
    if (r.wait || !r.pick) continue
    const p = r.pick
    const scores: Record<string, number> = {}
    for (const s of p.scores ?? []) {
      if (!columns.some((c) => c.rule === s.rule)) columns.push({ rule: s.rule, weight: s.weight })
      scores[s.rule] = s.weight * s.score
    }
    rows.push({
      time: hm(p.start),
      target: p.target_name ?? '',
      project: p.project_name ?? '',
      scores,
      total: p.total ?? (p.scores?.length ? p.scores.reduce((a, s) => a + s.weight * s.score, 0) : null),
      runner: p.runner_up ? `${p.runner_up.target_name} · ${p.runner_up.total.toFixed(2)}` : '—',
      ends: r.reason ? `${capitalise(r.reason)} at ${hm(r.end)}` : `Until ${hm(r.end)}`,
    })
  }
  return { columns, rows }
}

export function capitalise(s: string): string {
  return s ? s[0].toUpperCase() + s.slice(1) : s
}

export function contribution(s: RuleScore): number {
  return s.weight * s.score
}

export interface PlanSummary {
  darkStart: number
  darkEnd: number
  darkSeconds: number
  imagingSeconds: number
  targets: number
  projects: number
  subs: number
  waits: number
  waitSeconds: number
}

export function planSummary(p: Preview | null | undefined): PlanSummary {
  const tw = p?.twilight ?? {}
  const darkStart = ms(tw.astronomical_dusk)
  const darkEnd = ms(tw.astronomical_dawn)
  const out: PlanSummary = {
    darkStart,
    darkEnd,
    darkSeconds: !isNaN(darkStart) && !isNaN(darkEnd) ? Math.max(0, (darkEnd - darkStart) / 1000) : NaN,
    imagingSeconds: 0,
    targets: 0,
    projects: 0,
    subs: 0,
    waits: 0,
    waitSeconds: 0,
  }
  const targets = new Set<number>()
  const projects = new Set<number>()
  for (const r of runs(p?.blocks)) {
    const secs = (r.end - r.start) / 1000
    if (r.wait) {
      out.waits++
      out.waitSeconds += secs
      continue
    }
    out.imagingSeconds += secs
    out.subs += r.subs
    if (r.target_id !== undefined) targets.add(r.target_id)
    if (r.project_id !== undefined) projects.add(r.project_id)
  }
  out.targets = targets.size
  out.projects = projects.size
  return out
}

export interface TargetTime {
  id: number
  name: string
  seconds: number
}

export function timeByTarget(p: Preview | null | undefined): Map<number, TargetTime> {
  const m = new Map<number, TargetTime>()
  for (const b of p?.blocks ?? []) {
    if (b.wait || b.target_id === undefined) continue
    const secs = Math.max(0, (ms(b.end) - ms(b.start)) / 1000)
    if (isNaN(secs)) continue
    const cur = m.get(b.target_id) ?? { id: b.target_id, name: b.target_name ?? `Target ${b.target_id}`, seconds: 0 }
    cur.seconds += secs
    m.set(b.target_id, cur)
  }
  return m
}

export interface DiffRow {
  name: string
  before: number
  after: number
  change: string
}

export function planDiff(current: Preview | null | undefined, whatIf: Preview | null | undefined): DiffRow[] {
  const a = timeByTarget(current)
  const b = timeByTarget(whatIf)
  const ids = new Set<number>([...a.keys(), ...b.keys()])
  const changed: DiffRow[] = []
  const same: string[] = []
  for (const id of ids) {
    const before = a.get(id)?.seconds ?? 0
    const after = b.get(id)?.seconds ?? 0
    const name = a.get(id)?.name ?? b.get(id)?.name ?? `Target ${id}`
    if (Math.abs(after - before) < 180) {
      same.push(name)
      continue
    }
    const change = after === 0 ? 'no time' : before === 0 ? `${duration(after)}, new` : signedHours(after - before)
    changed.push({ name, before, after, change })
  }
  changed.sort((x, y) => Math.abs(y.after - y.before) - Math.abs(x.after - x.before))
  if (same.length) changed.push({ name: same.join(', '), before: 0, after: 0, change: 'no change' })
  return changed
}

export interface WhatIfChange {
  projectId: number
  field: OverrideField
  value: number
}

export const FIELD_LABELS: Record<OverrideField, string> = {
  priority: 'Priority',
  state: 'State',
  minimumtime: 'Minimum time',
}

export function fieldValue(p: SchedProject, f: OverrideField): number {
  return f === 'priority' ? p.priority : f === 'state' ? p.state : p.minimumtime
}

export function valueLabel(f: OverrideField, v: number): string {
  if (f === 'priority') return priorityName(v)
  if (f === 'state') return stateName(v)
  return `${v} min`
}

export function valueChoices(f: OverrideField, current: number): number[] {
  const all = f === 'priority' ? [2, 1, 0] : f === 'state' ? [1, 2, 0, 3] : MINUTE_CHOICES
  return all.filter((v) => v !== current)
}

export function addChange(list: WhatIfChange[], c: WhatIfChange): WhatIfChange[] {
  return list.filter((x) => !(x.projectId === c.projectId && x.field === c.field)).concat([c])
}

export function overrides(list: WhatIfChange[]): Override[] {
  return list.map((c) => ({ entity: 'project', id: c.projectId, field: c.field, value: c.value }))
}

export function editsFor(list: WhatIfChange[], projects: SchedProject[]): EditPayload[] {
  const out: EditPayload[] = []
  for (const c of list) {
    const p = projects.find((x) => x.id === c.projectId)
    if (!p) continue
    const before = fieldValue(p, c.field)
    if (before === c.value) continue
    let e = out.find((x) => x.id === p.id)
    if (!e) {
      e = { entity: 'project', id: p.id, guid: p.guid, name: p.name, changes: [] }
      out.push(e)
    }
    e.changes.push({ field: c.field, before, after: c.value })
  }
  return out
}

export const REJECT_VERDICTS = ['low_score', 'moon', 'rejected', 'off_target', 'duplicate', 'dead']

export function isRejected(s: Pick<TonightSub, 'verdict' | 'grading'>): boolean {
  return (s.verdict !== undefined && REJECT_VERDICTS.includes(s.verdict)) || s.grading === 'rejected'
}

export type Tone = 'ok' | 'warn' | 'bad' | 'info' | ''

export function verdictBadge(v?: string): { label: string; tone: Tone } {
  switch (v) {
    case 'added':
      return { label: 'Added', tone: 'ok' }
    case 'low_score':
      return { label: 'Low score', tone: 'bad' }
    case 'moon':
      return { label: 'Moon', tone: 'bad' }
    case 'rejected':
      return { label: 'Rejected in TS', tone: 'bad' }
    case 'off_target':
      return { label: 'Off target', tone: 'bad' }
    case 'duplicate':
      return { label: 'Duplicate', tone: 'bad' }
    case 'dead':
      return { label: 'Gave up', tone: 'bad' }
    case 'failed':
    case 'registration':
      return { label: 'Failed, will retry', tone: 'warn' }
    case 'calibration':
      return { label: 'Waiting for calibration', tone: 'warn' }
    case 'recalibrate':
      return { label: 'Recalibrating', tone: 'info' }
    case 'no_metadata':
      return { label: 'No scheduler record', tone: 'warn' }
    case undefined:
    case '':
      return { label: 'Not stacked yet', tone: '' }
    default:
      return { label: v, tone: '' }
  }
}

export interface ChartPoint {
  x: number
  y: number
  current: boolean
  rejected: boolean
}

export interface ChartPath {
  d: string
  current: boolean
}

export interface ChartMedian {
  x1: number
  x2: number
  y: number
  current: boolean
  value: number
}

export interface ChartTick {
  pos: number
  label: string
}

export interface ChartChange {
  x: number
  label: string
}

export interface SubChart {
  points: ChartPoint[]
  paths: ChartPath[]
  medians: ChartMedian[]
  yTicks: ChartTick[]
  xTicks: ChartTick[]
  changes: ChartChange[]
  firstLabel: string
  y: (v: number) => number
}

export interface ChartBox {
  x0: number
  x1: number
  yTop: number
  yBottom: number
}

function niceStep(span: number): number {
  const raw = span / 4
  const p = Math.pow(10, Math.floor(Math.log10(raw || 1)))
  const n = raw / p
  return (n <= 1 ? 1 : n <= 2 ? 2 : n <= 2.5 ? 2.5 : n <= 5 ? 5 : 10) * p
}

export function subChart(
  subs: TonightSub[],
  value: (s: TonightSub) => number | undefined,
  currentTargetId: number | undefined,
  t0: number,
  t1: number,
  box: ChartBox,
  opts: { zero?: boolean; minSpan?: number; unit?: string; include?: number[] } = {},
): SubChart {
  const sc = scale(t0, t1, box.x0, box.x1)
  const vals = [...subs.map(value), ...(opts.include ?? [])].filter((v): v is number => v !== undefined && isFinite(v))
  let lo = vals.length ? Math.min(...vals) : 0
  let hi = vals.length ? Math.max(...vals) : 1
  if (opts.zero) lo = 0
  const minSpan = opts.minSpan ?? 1
  if (hi - lo < minSpan) {
    const mid = opts.zero ? minSpan / 2 : (hi + lo) / 2
    lo = opts.zero ? 0 : mid - minSpan / 2
    hi = opts.zero ? minSpan : mid + minSpan / 2
  }
  const step = niceStep(hi - lo)
  lo = Math.floor(lo / step) * step
  hi = Math.ceil(hi / step) * step
  const y = (v: number) => box.yBottom - ((v - lo) / (hi - lo || 1)) * (box.yBottom - box.yTop)
  const yTicks: ChartTick[] = []
  for (let v = lo; v <= hi + step / 1000; v += step) {
    const dec = step < 1 ? (step < 0.1 ? 2 : 1) : 0
    yTicks.push({ pos: y(v), label: v.toFixed(dec) + (opts.unit ?? '') })
  }
  const xTicks = hourTicks(t0 + 10 * 60 * 1000, t1 - 10 * 60 * 1000).map((t) => ({ pos: sc.x(t), label: hm(t) }))
  const points: ChartPoint[] = []
  const paths: ChartPath[] = []
  const changes: ChartChange[] = []
  const byTarget = new Map<number, { xs: number[]; vals: number[] }>()
  let seg: string[] = []
  let segTarget: number | undefined
  let segCurrent = false
  const flush = () => {
    if (seg.length) paths.push({ d: seg.join(' '), current: segCurrent })
    seg = []
  }
  for (const s of subs) {
    const t = ms(s.time)
    const v = value(s)
    if (isNaN(t)) continue
    if (segTarget !== undefined && s.target_id !== segTarget) {
      flush()
      changes.push({ x: sc.x(t) - 4, label: s.target })
    }
    segTarget = s.target_id
    segCurrent = currentTargetId !== undefined && s.target_id === currentTargetId
    if (v === undefined || !isFinite(v)) continue
    const px = sc.x(t)
    const py = y(v)
    const rejected = isRejected(s)
    points.push({ x: px, y: py, current: segCurrent, rejected })
    if (!rejected) {
      seg.push(`${seg.length ? 'L' : 'M'}${px.toFixed(1)} ${py.toFixed(1)}`)
      const e = byTarget.get(s.target_id) ?? { xs: [], vals: [] }
      e.xs.push(px)
      e.vals.push(v)
      byTarget.set(s.target_id, e)
    }
  }
  flush()
  const medians: ChartMedian[] = []
  for (const [id, e] of byTarget) {
    const m = median(e.vals)
    if (m === null) continue
    medians.push({ x1: Math.min(...e.xs) - 10, x2: Math.max(...e.xs) + 4, y: y(m), current: id === currentTargetId, value: m })
  }
  return { points, paths, medians, yTicks, xTicks, changes, firstLabel: subs.length ? subs[0].target : '', y }
}

export function skipPayload(t: SchedulerTarget, wholeProject: boolean, minutes: number) {
  return {
    scope: wholeProject ? 'project' : 'target',
    target_id: t.target_id,
    target_name: t.target_name,
    project_id: t.project_id,
    project_name: t.project_name,
    minutes,
  }
}

export type ResumeMode = 'manual' | 'after' | 'at'

export function pausePayload(mount: 'track' | 'park', resume: ResumeMode, at: string) {
  const p: { mount: string; resume_after_minutes?: number; resume_at?: string } = { mount }
  if (resume === 'after') p.resume_after_minutes = 30
  if (resume === 'at' && /^\d{2}:\d{2}$/.test(at)) p.resume_at = at
  return p
}

export function historyBadge(r: Pick<CommandRecord, 'status' | 'applies_at' | 'applied_at'>, exposureEnd: string): { label: string; tone: Tone } {
  switch (r.status) {
    case 'pending': {
      const at = r.applies_at ? hm(r.applies_at) : exposureEnd
      return { label: at ? 'Applies at ' + at : 'Applies at the next plan', tone: 'info' }
    }
    case 'queued':
      return { label: 'Queued · PC unreachable', tone: 'warn' }
    case 'applied':
      return { label: 'Applied' + (r.applied_at ? ' ' + hm(r.applied_at) : ''), tone: 'ok' }
    case 'saved':
      return { label: 'Saved in the app', tone: 'ok' }
    case 'cancelled':
      return { label: 'Cancelled before it applied', tone: '' }
    case 'conflict':
      return { label: 'Conflict · not applied', tone: 'bad' }
    case 'rejected':
      return { label: 'Rejected by the scheduler', tone: 'bad' }
    case 'failed':
      return { label: 'Failed', tone: 'bad' }
  }
  return { label: r.status, tone: '' }
}

export function undoState(r: CommandRecord): { label: string; enabled: boolean; cancel: boolean; note: string } {
  if (r.status === 'queued' || r.status === 'pending') return { label: 'Cancel', enabled: true, cancel: true, note: '' }
  if (r.undone_by) return { label: 'Undone', enabled: false, cancel: false, note: 'Undone by change #' + r.undone_by.slice(0, 8) }
  const reverses = r.undo_of ? 'Reverses change #' + r.undo_of.slice(0, 8) : ''
  switch (r.status) {
    case 'applied':
      return { label: 'Undo', enabled: true, cancel: false, note: reverses || (r.applied_at ? 'Applied ' + hm(r.applied_at) : '') }
    case 'saved':
      return { label: 'Undo', enabled: true, cancel: false, note: reverses }
    case 'cancelled':
      return { label: 'Undo', enabled: false, cancel: false, note: 'Never reached the observatory' }
    default:
      return { label: 'Undo', enabled: false, cancel: false, note: 'Nothing was applied, so there is nothing to undo' }
  }
}

export function waitingFor(waiting: CommandRecord[], entity: string, ids: Set<number>): CommandRecord[] {
  return waiting.filter((c) => (c.objects ?? []).some((o) => o.entity === entity && o.id !== undefined && ids.has(o.id)))
}
