import { api } from './client'

export type GoalKind = 'snr' | 'depth'

export interface Point {
  x: number
  y: number
}

export interface Goal {
  targetGuid: string
  filter: string
  kind: GoalKind
  snr: number
  depth: number
  plateauStop: boolean
  region?: Point[]
}

export interface Progress {
  object: string
  filter: string
  targetGuid?: string
  kind: GoalKind
  goal: number
  achieved: number
  progress: number
  snr: number
  depth: number
  effectiveHours: number
  hoursNeeded: number
  gainPerHourPct: number
  plateau: boolean
  lowConfidence: boolean
  region: boolean
  done: boolean
  measuredAt: string
}

export interface FilterGoal {
  filter: string
  stackFilter: string
  goal: Goal
  goalSet: boolean
  measured: boolean
  progress?: Progress
  error?: string
  accepted: number
  desired: number
  acceptedHours: number
}

export interface Plan {
  id: number
  guid: string
  templateId: number
  template: string
  filter: string
  exposure: number
  exposureRaw: number
  desired: number
  acquired: number
  accepted: number
  enabled: boolean
  gain: number | null
  moonSeparation: number
  moonWidth: number
}

export interface Season {
  nightsLeft: number
  outOfSeason: boolean
  seasonEnd?: string
  computedFor?: string
}

export interface Target {
  id: number
  guid: string
  name: string
  active: boolean
  raHours: number | null
  dec: number | null
  rotation: number
  panel?: number
  plans: Plan[]
  goals: FilterGoal[]
  weakest?: FilterGoal
  progress: number
  effectiveHours: number
  season?: Season
  novelty: number
  rarity: number
  lastSub?: string
  exposureSet: string
  goalMode: GoalKind
}

export interface RuleWeight {
  name: string
  weight: number
  missing: boolean
}

export interface Project {
  id: number
  guid: string
  name: string
  description: string
  state: string
  priority: string
  minimumTime: number
  minimumAltitude: number
  isMosaic: boolean
  targets: Target[]
  exposureSet: string
  progress: number
  weakestTarget?: string
  weakest?: FilterGoal
  season?: Season
  novelty: number
  rarity: number
  lastSub?: string
  ruleWeights: RuleWeight[]
  goalDriven: boolean
}

export interface Template {
  id: number
  guid: string
  name: string
  filter: string
  defaultExposure: number
  gain: number | null
  offset: number | null
  bin: number | null
  twilight: string
  moonEnabled: boolean
  moonSeparation: number
  moonWidth: number
  moonDown: boolean
  maximumHumidity: number
  usedByPlans: number
  usedByTargets: number
}

export interface SetItem {
  template: string
  exposure: number
}

export interface ExposureSet {
  id: string
  name: string
  hint: string
  items: SetItem[]
}

export interface Rule {
  name: string
  defaultWeight: number
  new: boolean
  description: string
}

export interface Snapshot {
  frame: { widthDeg: number; heightDeg: number; scale: number }
  projects: Project[]
  templates: Template[]
  sets: ExposureSet[]
  rules: Rule[]
}

export interface ProjectDetail {
  project: Project
  rules: Rule[]
  sets: ExposureSet[]
  templates: Template[]
}

export interface TargetEffect {
  targetId: number
  name: string
  project: string
  current: string
  effect: string
  changes: boolean
}

export interface ApplySetDraft {
  payload?: unknown
  effects: TargetEffect[]
  missing?: string[]
}

export interface PanelDraft {
  name?: string
  raHours: number
  dec: number
  rotation: number
}

export interface ProjectDraft {
  name: string
  catalog?: string
  match?: string
  matchWith?: string
  priority: string
  minimumAltitude: number
  minimumTime: number
  setId: string
  desired?: number
  goal: { kind: GoalKind; snr: number; depth: number; plateauStop?: boolean }
  panels: PanelDraft[]
}

export interface StackMaster {
  filter: string
  subs: number
  width: number
  height: number
  preview_url: string
  crop?: { x: number; y: number; w: number; h: number }
}

export const getPlanning = () => api.get<Snapshot>('/planning')
export const getProject = (id: number | string) => api.get<ProjectDetail>(`/planning/projects/${id}`)
export const draftApplySet = (body: { setId: string; mode: string; targetIds: number[]; desired?: number }) =>
  api.post<ApplySetDraft>('/planning/applyset/draft', body)
export const draftProject = (d: ProjectDraft) => api.post<unknown>('/planning/projects/draft', d)
export const getStacks = (object: string) => api.get<StackMaster[]>('/stacks?object=' + encodeURIComponent(object))

export const RULE_KEYS = ['Project Priority', 'Setting Soonest', 'Percent Complete', 'Target Switch Penalty', 'Mosaic Completion', 'Novelty', 'Rarity']

export function uuid(): string {
  const b = new Uint8Array(16)
  crypto.getRandomValues(b)
  b[6] = (b[6] & 0x0f) | 0x40
  b[8] = (b[8] & 0x3f) | 0x80
  const h = Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('')
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`
}

export const PRIORITY_INDEX: Record<string, number> = { Low: 0, Normal: 1, High: 2 }
export const STATE_INDEX: Record<string, number> = { Draft: 0, Active: 1, Inactive: 2, Closed: 3 }

const FILTER_COLORS: Record<string, string> = {
  'H-a': 'var(--ha)',
  'O-III': 'var(--oiii)',
  'S-II': 'var(--sii)',
  Luminance: 'var(--lum)',
  Red: 'var(--red)',
  Green: 'var(--green)',
  Blue: 'var(--blue)',
}

export function filterColor(stackFilter: string): string {
  return FILTER_COLORS[stackFilter] ?? 'var(--muted-foreground)'
}

export function r1(v: number): string {
  return (Math.round(v * 10) / 10).toFixed(1)
}

export function r2(v: number): string {
  return (Math.round(v * 100) / 100).toFixed(2)
}

export function pct(v: number): string {
  return Math.round(Math.max(0, Math.min(1, v)) * 100) + '%'
}

export function raText(h: number | null): string {
  if (h === null || h === undefined) return '—'
  const t = Math.round(h * 3600)
  const hh = Math.floor(t / 3600)
  const mm = Math.floor((t % 3600) / 60)
  const ss = t % 60
  return `${hh}h ${String(mm).padStart(2, '0')}m ${String(ss).padStart(2, '0')}s`
}

export function decText(d: number | null): string {
  if (d === null || d === undefined) return '—'
  const s = d < 0 ? '−' : '+'
  const a = Math.abs(d)
  const deg = Math.floor(a)
  const min = Math.round((a - deg) * 60)
  return `${s}${deg}° ${String(min).padStart(2, '0')}′`
}

export function seasonLabel(s?: Season): { label: string; cls: string } {
  if (!s) return { label: 'Season unknown', cls: '' }
  if (s.outOfSeason) return { label: 'Out of season', cls: 'muted' }
  if (s.nightsLeft <= 60) return { label: `Closing · ≈ ${s.nightsLeft} nights`, cls: 'violet' }
  return { label: `In season · ≈ ${s.nightsLeft} nights`, cls: 'muted' }
}

export function goalTiming(fg: FilterGoal): string {
  const p = fg.progress
  if (!p) return fg.error ? fg.error : 'No master yet'
  if (p.done) return p.progress >= 1 ? 'goal met' : 'plateau reached'
  if (p.hoursNeeded < 0) return 'time needed unknown'
  return `≈ ${r1(p.hoursNeeded)} h more`
}
