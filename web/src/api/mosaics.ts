import { api, query } from './client'
import { submitCommand } from './commands'

export interface SkyPoint {
  ra: number
  dec: number
}

export type Footprint = [SkyPoint, SkyPoint, SkyPoint, SkyPoint]

export interface Rig {
  widthDeg: number
  heightDeg: number
  scaleArcsec: number
}

export interface PlannedPanel {
  n: number
  row: number
  col: number
  centre: SkyPoint
  rotationDeg: number
  footprint: Footprint
  covers: number
}

export interface FramingOption {
  id: string
  kind: string
  name: string
  detail: string
  rows: number
  cols: number
  rotationDeg: number
  overlapPct: number
  coverage: number
  panels: PlannedPanel[]
  hours: number
  nights: number
  seasons: number
  cost: string
  recommended: boolean
}

export interface Framing {
  rig: Rig
  rotation: number
  suggestedRotation: number
  overlap: number
  nightHours: number
  bestMonths: string[]
  siteKnown: boolean
  options: FramingOption[]
  chosen?: FramingOption
}

export interface FramingRequest {
  ra: number
  dec: number
  majorArcmin: number
  minorArcmin: number
  pa: number
  rotation?: number
  overlap?: number
  rows?: number
  cols?: number
  brick?: boolean
  hoursPerPanel?: number
  minAltitude?: number
}

export interface PanelFilter {
  filter: string
  object?: string
  source: 'goal' | 'ts' | 'none'
  kind?: string
  goal?: number
  achieved?: number
  snr?: number
  progress: number
  effectiveHours: number
  hoursNeeded: number
  plannedHours?: number
  desired?: number
  accepted?: number
  plateau?: boolean
  done?: boolean
  lowConfidence?: boolean
}

export interface MosaicPanel {
  number: number
  targetId: number
  targetGuid: string
  target: string
  objects: string[]
  row: number
  col: number
  ra: number
  dec: number
  rotation: number
  footprint: Footprint
  filters: PanelFilter[] | null
  progress: number
  weakest: string
  pastGoal: boolean
}

export interface Seam {
  project: string
  filter: string
  panelA: number
  panelB: number
  level: number
  difference: number
  slopeX: number
  slopeY: number
  step: number
  profile: string
  noiseA: number
  noiseB: number
  noiseRatio: number
  samples: number
  ok: boolean
  problems: string
  measuredAt: string
}

export interface PanelHealth {
  filter: string
  panel: number
  noise: number
  gapFraction: number
  gapDeg2: number
  gapWhere: string
}

export interface MosaicBuild {
  Filter: string
  Panels: number
  PanelsTotal: number
  UpdatedAt: string
}

export interface MosaicDetail {
  project: string
  projectGuid: string
  ts: { id: number; priority: number; state: number; minimumTime: number; minimumAltitude: number }
  adopted: boolean
  rows: number
  cols: number
  rotation: number
  layout: string
  filters: string[] | null
  panels: MosaicPanel[] | null
  complete: number
  average: number
  weakestPanel: number
  weakestFilter: string
  effectiveHours: number
  hoursLeft: number
  hoursUnknown: boolean
  balancing: { panelDeficit: number; panelDeficitSet: boolean; mosaicCompletion: number; on: boolean }
  seams: Seam[]
  health: PanelHealth[]
  needs: string[]
}

export interface MosaicSummary {
  project: string
  projectGuid: string
  panels: number
  adopted: boolean
  complete: number
  average: number
  weakestPanel: number
  weakestFilter: string
  seamWarnings: number
  balancing: boolean
}

export interface SeasonPace {
  hours: number
  nights: number
  from?: string
  to?: string
  seasonStart: string
  seasonEnd: string
}

export interface SeasonPlan {
  project: string
  strategy: string
  pace: string
  hoursPerSeason: number
  lastSeason?: SeasonPace
  currentSeason?: SeasonPace
  inSeason: boolean
  nightsLeft: number
  rows: { index: number; name: string; weakest: number; average: number; done: boolean }[]
  finishSeason: number
  compare: Record<string, number>
  months: { month: number; name: string; hours: number }[]
  siteKnown: boolean
  goalHoursSource: string
  panelPriority: { panel: number; hoursLeft: number; monthsLeft: number; priority: number; lastUsableMonth?: string; thisSeason: boolean }[]
}

export interface MosaicHistory {
  nights: string[]
  panels: { number: number; hours: number[] }[]
}

export interface AdoptedPanel {
  targetGuid: string
  target: string
  panel: number
  row: number
  col: number
}

export interface Adoption {
  id: number
  subject: string
  projectGuid?: string
  project: string
  kind: 'mosaic' | 'not_mosaic' | 'frames'
  confidence: string
  issue: string
  suggestion: string
  status: 'proposed' | 'auto' | 'accepted' | 'rejected'
  clean: boolean
  decidedBy?: string
  decidedAt?: string
  panels?: AdoptedPanel[] | null
  frames?: { object: string; count: number; target?: string; separationDeg: number }
}

export interface AdoptionReport {
  dryRun: boolean
  clean: number
  new: number
  review: number
  unchanged: number
  changed: number
  items: Adoption[]
}

export const RULE_PANEL_DEFICIT = 'Panel Deficit'
export const BALANCING_WEIGHT = 75

export const listMosaics = () => api.get<MosaicSummary[]>('/mosaics/projects')
export const getMosaic = (key: string) => api.get<MosaicDetail>('/mosaics/projects/' + encodeURIComponent(key))
export const getSeasons = (key: string, strategy: string, pace: string) =>
  api.get<SeasonPlan>('/mosaics/projects/' + encodeURIComponent(key) + '/seasons' + query({ strategy, pace }))
export const getHistory = (key: string) => api.get<MosaicHistory>('/mosaics/projects/' + encodeURIComponent(key) + '/history')
export const listAdoptions = () => api.get<Adoption[]>('/mosaics/adoption')
export const runAdoption = (dryRun: boolean) => api.post<AdoptionReport>('/mosaics/adoption/run', { dryRun })
export const frame = (req: FramingRequest, signal?: AbortSignal) => api.post<Framing>('/mosaics/framing', req, signal)
export const mosaicPreviews = (project: string) =>
  api.get<{ filter: string; preview_url?: string; panels: number; panels_total: number }[]>('/mosaics' + query({ project }))

export function setBalancing(d: MosaicDetail, on: boolean) {
  const before = d.balancing.panelDeficitSet ? d.balancing.panelDeficit : null
  return submitCommand('ruleweight.edit', {
    project_id: d.ts.id,
    project_guid: d.projectGuid,
    project_name: d.project,
    changes: [{ rule: RULE_PANEL_DEFICIT, before, after: on ? BALANCING_WEIGHT : 0 }],
  })
}

export function adopt(decisions: { id: number; subject: string; title: string; before: string; after: string }[]) {
  return submitCommand('mosaic.adopt', { decisions })
}

const deg = Math.PI / 180

export function project(centre: SkyPoint, p: SkyPoint): [number, number] {
  const d0 = centre.dec * deg
  const d = p.dec * deg
  const da = (p.ra - centre.ra) * deg
  const cosc = Math.sin(d0) * Math.sin(d) + Math.cos(d0) * Math.cos(d) * Math.cos(da)
  const xi = (Math.cos(d) * Math.sin(da)) / cosc / deg
  const eta = (Math.cos(d0) * Math.sin(d) - Math.sin(d0) * Math.cos(d) * Math.cos(da)) / cosc / deg
  return [xi, eta]
}

export function meanCentre(points: SkyPoint[]): SkyPoint {
  let x = 0
  let y = 0
  let z = 0
  for (const p of points) {
    x += Math.cos(p.dec * deg) * Math.cos(p.ra * deg)
    y += Math.cos(p.dec * deg) * Math.sin(p.ra * deg)
    z += Math.sin(p.dec * deg)
  }
  const ra = (Math.atan2(y, x) / deg + 360) % 360
  const dec = Math.atan2(z, Math.hypot(x, y)) / deg
  return { ra, dec }
}
