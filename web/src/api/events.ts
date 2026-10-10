import { API_BASE } from './client'

type Handler = (data: any) => void

const handlers = new Map<string, Set<Handler>>()
let source: EventSource | null = null

function ensure() {
  if (source || typeof EventSource === 'undefined') return
  source = new EventSource(API_BASE + '/events')
  source.onmessage = (ev) => {
    let data: any
    try {
      data = JSON.parse(ev.data)
    } catch {
      return
    }
    const set = handlers.get(data?.type)
    if (set) set.forEach((h) => h(data))
    const any = handlers.get('*')
    if (any) any.forEach((h) => h(data))
  }
}

export function onEvent(type: string, h: Handler): () => void {
  ensure()
  let set = handlers.get(type)
  if (!set) {
    set = new Set()
    handlers.set(type, set)
  }
  set.add(h)
  return () => set!.delete(h)
}
