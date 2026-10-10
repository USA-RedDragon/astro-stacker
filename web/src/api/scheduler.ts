import { api, query } from './client'

export type Reachability = 'unknown' | 'online' | 'offline' | 'unconfigured'

export type SchedulerState = 'imaging' | 'waiting' | 'paused' | 'idle' | 'stopped'

export interface RuleScore {
  rule: string
  weight: number
  score: number
}

export interface SchedulerExposure {
  plan_id?: number
  filter: string
  seconds: number
  started_at?: string
  ends_at?: string
  number?: number
}

export interface SchedulerTarget {
  project_id: number
  project_name: string
  project_guid?: string
  target_id: number
  target_name: string
  target_guid?: string
  priority?: number
  is_mosaic?: boolean
  ra_hours?: number
  dec_degrees?: number
  rotation?: number
  picked_at?: string
  minimum_time_end?: string
  hard_stop?: string
}

export interface SchedulerSkip {
  scope: 'target' | 'project'
  target_id?: number
  project_id?: number
  name?: string
  until?: string
}

export interface SchedulerStatus {
  reachable: Reachability
  last_answer?: string
  since?: string
  live?: boolean
  error?: string
  version?: string
  time?: string
  web_editing?: boolean
  api?: boolean
  state?: SchedulerState | string
  paused?: boolean
  pause_requested?: boolean
  pause?: { mount?: string; resume_at?: string } | null
  target?: SchedulerTarget | null
  exposure?: SchedulerExposure | null
  wait?: { until?: string; target_name?: string } | null
  scores?: RuleScore[] | null
  score_total?: number | null
  skips?: SchedulerSkip[] | null
  pending?: { id: string; kind: string; received_at?: string }[] | null
  last_plan_at?: string
}

export interface Twilight {
  sunset?: string | null
  civil_dusk?: string | null
  nautical_dusk?: string | null
  astronomical_dusk?: string | null
  astronomical_dawn?: string | null
  nautical_dawn?: string | null
  civil_dawn?: string | null
  sunrise?: string | null
}

export interface PlanBlock {
  start: string
  end: string
  wait: boolean
  project_id?: number
  project_name?: string
  target_id?: number
  target_name?: string
  filter?: string
  exposure_seconds?: number
  count?: number
  picked?: boolean
  scores?: RuleScore[] | null
  total?: number | null
  runner_up?: { target_name: string; total: number } | null
  reason?: string
}

export interface LeftOut {
  project_id?: number
  project_name?: string
  target_id?: number
  target_name?: string
  reason?: string
}

export interface Preview {
  start?: string
  end?: string
  generated_at?: string
  twilight?: Twilight | null
  blocks?: PlanBlock[] | null
  left_out?: LeftOut[] | null
  skips?: SchedulerSkip[] | null
}

export type OverrideField = 'priority' | 'state' | 'minimumtime'

export interface Override {
  entity: 'project'
  id: number
  field: OverrideField
  value: number
}

export interface SchedPlan {
  id: number
  guid: string
  filter: string
  template: string
  exposure: number
  defaultExposure: number
  desired: number
  acquired: number
  accepted: number
  enabled: boolean
}

export interface SchedTarget {
  id: number
  guid: string
  name: string
  active: boolean
  ra: number | null
  dec: number | null
  rotation: number
  plans: SchedPlan[]
}

export interface SchedProject {
  id: number
  guid: string
  name: string
  state: number
  priority: number
  minimumtime: number
  isMosaic: boolean
  targets: SchedTarget[]
  lastImage?: string
}

export interface TonightSub {
  id: number
  time: string
  project_id: number
  project: string
  target_id: number
  target: string
  filter: string
  exposure?: number
  hfr?: number
  stars?: number
  guiding_rms?: number
  guiding_rms_ra?: number
  guiding_rms_dec?: number
  grading: 'accepted' | 'rejected' | 'pending'
  file?: string
  verdict?: string
  score?: number
  weight?: number
  gain?: number
  ccd_temp?: number
  processed_at?: string
  preview_url?: string
}

export interface TonightSubs {
  since: string
  subs: TonightSub[]
  latest?: TonightSub
}

export type Night = 'tonight' | 'tomorrow'

export const getSchedulerStatus = () => api.get<SchedulerStatus>('/scheduler/status')

export const getPreview = (night: Night = 'tonight', fresh = false) =>
  api.get<Preview>('/scheduler/preview' + query({ night, fresh: fresh ? 1 : undefined }))

export const postPreview = (night: Night, overrides: Override[]) =>
  api.post<Preview>('/scheduler/preview', { night, overrides })

export const getProjects = () => api.get<SchedProject[]>('/scheduler/projects')

export const getTonightSubs = (since?: string) => api.get<TonightSubs>('/scheduler/tonight-subs' + query({ since }))
