import { api, query } from './client'

export type Reachability = 'unknown' | 'online' | 'offline' | 'unconfigured'

export type SchedulerState = 'imaging' | 'waiting' | 'paused' | 'idle' | 'stopped'

export type SchedulerActivity =
  | 'preparing'
  | 'slewing'
  | 'centering'
  | 'focusing'
  | 'guiding'
  | 'dithering'
  | 'meridian_flip'
  | 'switching_filter'
  | 'waiting'
  | 'starting_exposure'
  | 'exposing'
  | 'downloading'
  | 'saving'
  | 'busy'

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
  activity?: SchedulerActivity | string | null
  activity_detail?: string | null
  activity_since?: string | null
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

export interface HFRLimit {
  target_id: number
  filter: string
  mean: number
  sd: number
  samples: number
  limit: number
}

export interface TonightSubs {
  since: string
  subs: TonightSub[]
  latest?: TonightSub
  hfr_sigma?: number
  hfr_limits?: HFRLimit[]
}

export type ConditionSource = 'none' | 'error' | 'prometheus' | 'symmetricds'

export interface SourceState {
  source: ConditionSource
  error?: string
}

export interface WeatherReport extends SourceState {
  connected?: boolean
  cloud_cover?: number
  rain_rate?: number
  wind_speed?: number
  wind_gust?: number
  humidity?: number
  dew_point?: number
  temperature?: number
  sky_temperature?: number
  sky_brightness?: number
  pressure?: number
}

export interface SafetyReport extends SourceState {
  connected?: boolean
  safe?: boolean
}

export interface MountReport extends SourceState {
  connected?: boolean
  tracking?: boolean
  parked?: boolean
  slewing?: boolean
  at_home?: boolean
  altitude?: number
  flip_hours?: number
}

export interface PowerReport extends SourceState {
  model?: string
  charge?: number
  input_voltage?: number
  flags?: string[]
  on_battery: boolean
  low_battery: boolean
  on_battery_seconds?: number
  shutdown_seconds?: number
}

export interface SyncReport extends SourceState {
  node?: string
  heartbeat?: string
  last_batch?: string
  lag_seconds?: number
  errors: number
}

export interface Conditions {
  at: string
  weather: WeatherReport
  safety: SafetyReport
  mount: MountReport
  power: PowerReport
  sync: SyncReport
}

export interface MoonNight {
  samples: { t: string; alt: number }[]
  illumination: number
  age: number
  waxing: boolean
  rises: string[]
  sets: string[]
  next_new: string
  next_full: string
}

export type Night = 'tonight' | 'tomorrow'

export const getSchedulerStatus = () => api.get<SchedulerStatus>('/scheduler/status')

export const getPreview = (night: Night = 'tonight', fresh = false) =>
  api.get<Preview>('/scheduler/preview' + query({ night, fresh: fresh ? 1 : undefined }))

export const postPreview = (night: Night, overrides: Override[]) =>
  api.post<Preview>('/scheduler/preview', { night, overrides })

export const getProjects = () => api.get<SchedProject[]>('/scheduler/projects')

export const getTonightSubs = (since?: string) => api.get<TonightSubs>('/scheduler/tonight-subs' + query({ since }))

export const getConditions = () => api.get<Conditions>('/scheduler/conditions')

export const getMoon = (start: string, end: string) => api.get<MoonNight>('/scheduler/moon' + query({ start, end }))
