import { api } from './client'

export type Reachability = 'unknown' | 'online' | 'offline' | 'unconfigured'

export interface SchedulerExposure {
  filter: string
  seconds: number
  started_at: string
  ends_at: string
  number?: number
}

export interface SchedulerTarget {
  project_id: number
  project_name: string
  target_id: number
  target_name: string
  priority?: number
  is_mosaic?: boolean
}

export interface SchedulerStatus {
  reachable: Reachability
  last_answer?: string
  since?: string
  web_editing?: boolean
  paused?: boolean
  pause?: { mount: string; resume_at?: string; requested?: boolean }
  state?: string
  target?: SchedulerTarget | null
  exposure?: SchedulerExposure | null
  [k: string]: unknown
}

export const getSchedulerStatus = () => api.get<SchedulerStatus>('/scheduler/status')
