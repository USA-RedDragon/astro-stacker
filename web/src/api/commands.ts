import { api, query } from './client'

export type CommandStatus =
  | 'saved'
  | 'queued'
  | 'pending'
  | 'applied'
  | 'conflict'
  | 'rejected'
  | 'failed'
  | 'cancelled'

export type CommandCategory =
  | 'control'
  | 'priority'
  | 'goals'
  | 'rules'
  | 'plans'
  | 'templates'
  | 'created'
  | 'matching'
  | 'adoption'

export interface ObjectRef {
  entity: string
  id?: number
  guid?: string
  name: string
}

export interface Diff {
  object: ObjectRef
  field: string
  before: unknown
  after: unknown
}

export interface CommandRecord {
  id: string
  kind: string
  payload: unknown
  author: string
  undo_of?: string
  undone_by?: string
  title: string
  category: CommandCategory
  destination: 'observatory' | 'app'
  diffs: Diff[] | null
  objects: ObjectRef[] | null
  note?: string
  status: CommandStatus
  message?: string
  detail?: unknown
  transport?: string
  attempts: number
  applies_at?: string
  applied_at?: string
  created_at: string
  updated_at: string
}

export interface FieldChange {
  field: string
  before: unknown
  after: unknown
}

export interface EditPayload {
  entity?: string
  id: number
  guid?: string
  name: string
  parent?: string
  changes: FieldChange[]
}

export interface CommandFilter {
  author?: string
  category?: string
  q?: string
  status?: string
  limit?: number
  before?: string
}

export const isWaiting = (s: CommandStatus) => s === 'queued' || s === 'pending'

export const submitCommand = (kind: string, payload: unknown) =>
  api.post<CommandRecord>('/commands', { kind, payload })

export const editEntity = (kind: string, payload: EditPayload) => submitCommand(kind, payload)

export const undoCommand = (id: string) => api.post<CommandRecord>(`/commands/${encodeURIComponent(id)}/undo`)

export const cancelCommand = (id: string) => api.post<CommandRecord>(`/commands/${encodeURIComponent(id)}/cancel`)

export const getCommand = (id: string) => api.get<CommandRecord>(`/commands/${encodeURIComponent(id)}`)

export const listCommands = (f: CommandFilter = {}) =>
  api.get<CommandRecord[]>('/commands' + query(f as Record<string, string | number | undefined>))
