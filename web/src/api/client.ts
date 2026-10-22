import type {
  About, ApiError, CAInfo, Client, ConfigView, Device, Flow, FlowCompare, FlowList, LogState,
  NewDevice, ProfileOverride, Rule, ServerEvent, Settings, StatsSnapshot, TestRequest,
  TestResult, ValidateResult,
} from './types'

const BASE = '/api/v1'
const TOKEN_KEY = 'midopress.token'

export class ApiFailure extends Error {
  status: number
  body: ApiError
  constructor(status: number, body: ApiError) {
    super(body.error || `HTTP ${status}`)
    this.status = status
    this.body = body
  }
}

let token = localStorage.getItem(TOKEN_KEY) ?? ''
const listeners = new Set<() => void>()

export function getToken(): string {
  return token
}

export function setToken(value: string) {
  token = value.trim()
  if (token) localStorage.setItem(TOKEN_KEY, token)
  else localStorage.removeItem(TOKEN_KEY)
  listeners.forEach((fn) => fn())
}

/** Called when the token is set, cleared or rejected. */
export function onAuthChange(fn: () => void): () => void {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

function headers(extra?: HeadersInit): Headers {
  const h = new Headers(extra)
  if (token) h.set('Authorization', `Bearer ${token}`)
  return h
}

async function failure(res: Response): Promise<ApiFailure> {
  let body: ApiError = { error: res.statusText, code: 'http' }
  try {
    body = await res.json()
  } catch {
    /* not JSON */
  }
  if (res.status === 401) {
    token = ''
    localStorage.removeItem(TOKEN_KEY)
    listeners.forEach((fn) => fn())
  }
  return new ApiFailure(res.status, body)
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(BASE + path, {
    method,
    headers: headers(body === undefined ? undefined : { 'Content-Type': 'application/json' }),
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (!res.ok) throw await failure(res)
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

const get = <T>(path: string) => request<T>('GET', path)
const put = <T>(path: string, body: unknown) => request<T>('PUT', path, body)
const post = <T>(path: string, body?: unknown) => request<T>('POST', path, body ?? {})
const patch = <T>(path: string, body: unknown) => request<T>('PATCH', path, body)
const del = <T>(path: string) => request<T>('DELETE', path)

/** Downloads a file, with the token, since a plain link can not carry it. */
export async function download(path: string, filename: string) {
  const res = await fetch(BASE + path, { headers: headers() })
  if (!res.ok) throw await failure(res)
  const url = URL.createObjectURL(await res.blob())
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

/** Fetches a body (an image of a flow) as an object URL the page can show. */
export async function blobUrl(path: string): Promise<string> {
  const res = await fetch(BASE + path, { headers: headers() })
  if (!res.ok) throw await failure(res)
  return URL.createObjectURL(await res.blob())
}

export const api = {
  about: () => get<About>('/about'),
  stats: () => get<StatsSnapshot>('/stats'),

  flows: (params: { filter?: string; kind?: string; limit?: number } = {}) => {
    const q = new URLSearchParams()
    if (params.filter) q.set('filter', params.filter)
    if (params.kind) q.set('kind', params.kind)
    if (params.limit) q.set('limit', String(params.limit))
    const s = q.toString()
    return get<FlowList>('/flows' + (s ? `?${s}` : ''))
  },
  flow: (id: string) => get<Flow>(`/flows/${encodeURIComponent(id)}`),
  flowCompare: (id: string) => get<FlowCompare>(`/flows/${encodeURIComponent(id)}/compare`),
  flowBody: (id: string, which: 'original' | 'transformed') =>
    blobUrl(`/flows/${encodeURIComponent(id)}/${which}`),
  clearFlows: () => del<void>('/flows'),
  exportHar: () => download('/flows.har', 'midopress.har'),

  settings: () => get<Settings>('/settings'),
  saveSettings: (s: Partial<Settings>) => put<Settings>('/settings', s),

  config: () => get<ConfigView>('/config'),
  saveRules: (rules: Rule[]) => put<ConfigView>('/config/rules', rules),
  saveProfile: (name: string, o: ProfileOverride) =>
    put<ConfigView>(`/config/profiles/${encodeURIComponent(name)}`, o),
  deleteProfile: (name: string) => del<ConfigView>(`/config/profiles/${encodeURIComponent(name)}`),
  setDefaultProfile: (profile: string) => put<ConfigView>('/config/default-profile', { profile }),
  validateRules: (rules: Rule[]) => post<ValidateResult>('/config/validate', { rules }),
  testUrl: (req: TestRequest) => post<TestResult>('/config/test', req),
  forgetUiSettings: () => del<ConfigView>('/config/ui'),

  devices: () => get<Device[]>('/devices'),
  addDevice: (id: string, profile: string) => post<NewDevice>('/devices', { id, profile }),
  setDeviceProfile: (id: string, profile: string) =>
    patch<Device>(`/devices/${encodeURIComponent(id)}`, { profile }),
  revokeDevice: (id: string) => del<void>(`/devices/${encodeURIComponent(id)}`),
  clients: () => get<Client[]>('/clients'),

  ca: () => get<CAInfo>('/ca'),
  downloadCA: (kind: 'pem' | 'der' | 'p12') => download(`/ca.${kind}`, `midopress-ca.${kind === 'pem' ? 'crt' : kind}`),

  log: () => get<LogState>('/log'),
  setLogLevel: (level: string) => put<LogState>('/log', { level }),

  reload: () => post<void>('/service/reload'),
  clearCache: () => post<void>('/service/cache/clear'),
  resetStats: () => post<void>('/service/stats/reset'),
  shutdown: () => post<void>('/service/shutdown'),
}

/**
 * Follows the event stream. EventSource can not send the token, so this reads
 * the stream with fetch. It reconnects by itself, with a growing pause, and
 * returns a function that stops it.
 */
export function subscribe(
  onEvent: (e: ServerEvent) => void,
  onState?: (connected: boolean) => void,
): () => void {
  const ctl = new AbortController()
  let stopped = false

  async function run() {
    let pause = 500
    while (!stopped) {
      try {
        const res = await fetch(BASE + '/events', { headers: headers(), signal: ctl.signal })
        if (!res.ok || !res.body) throw await failure(res)
        onState?.(true)
        pause = 500
        const reader = res.body.pipeThrough(new TextDecoderStream()).getReader()
        let buffer = ''
        for (;;) {
          const { value, done } = await reader.read()
          if (done) break
          buffer += value
          let at: number
          while ((at = buffer.indexOf('\n\n')) >= 0) {
            const block = buffer.slice(0, at)
            buffer = buffer.slice(at + 2)
            let event = 'message'
            let data = ''
            for (const line of block.split('\n')) {
              if (line.startsWith('event:')) event = line.slice(6).trim()
              else if (line.startsWith('data:')) data += line.slice(5).trim()
            }
            if (data) {
              try {
                onEvent({ event, data: JSON.parse(data) } as ServerEvent)
              } catch {
                /* a line that is not JSON is ignored */
              }
            }
          }
        }
      } catch (e) {
        if (stopped) return
        if (e instanceof ApiFailure && e.status === 401) return
      }
      onState?.(false)
      await new Promise((r) => setTimeout(r, pause))
      pause = Math.min(pause * 2, 8000)
    }
  }
  void run()
  return () => {
    stopped = true
    ctl.abort()
  }
}
