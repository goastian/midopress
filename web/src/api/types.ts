// The contract between the web interface and the admin server of MidoPress
// (internal/admin). Every path is under /api/v1 and needs the admin token as
// `Authorization: Bearer <token>`, except the ones marked public, which are the
// ones the magic host (midopress.it) offers to any client of the proxy.
//
// Sizes are bytes, durations are milliseconds, times are RFC 3339 strings.
// A field that is `null` or absent means "not available", for instance the top
// domains when the aggregation is switched off.

// ---------------------------------------------------------------- stats

/** What a group of responses amounted to. */
export interface Bucket {
  requests: number
  /** responses that were changed (recoded, compressed, minified, converted) */
  transformed: number
  bytes_in: number
  bytes_out: number
}

export interface Totals extends Bucket {
  /** bytes_in - bytes_out */
  saved: number
  /** saved / bytes_in * 100, 0 when nothing went through */
  saved_percent: number
}

export interface Latency {
  p50_ms: number
  p95_ms: number
  p99_ms: number
  /** how many responses the quantiles come from */
  samples: number
}

export interface CacheStats {
  hits: number
  misses: number
  /** 0 to 1 */
  hit_rate: number
  memory_bytes: number
  memory_items: number
  disk_bytes: number
  disk_items: number
  evictions: number
  expired: number
  disk_errors: number
}

export interface ConnectionStats {
  /** distinct clients seen in the last two minutes */
  clients_active: number
  requests_in_flight: number
  tunnels_active: number
  tunnels_total: number
  /** bytes that went through tunnels without being touched (HTTPS that is not decrypted) */
  tunnel_bytes_up: number
  tunnel_bytes_down: number
  tls_handshakes: number
  tls_handshake_errors: number
  upstream_errors: number
  /** requests refused by authentication or access control */
  denied: number
}

export interface CPUStats {
  /** slots of the limiter of heavy work */
  slots: number
  busy: number
  /** image jobs waiting for the background workers */
  background_queue: number
}

export interface DomainStat extends Bucket {
  domain: string
}

export interface SeriesPoint {
  /** unix seconds of the start of the interval */
  t: number
  requests: number
  bytes_in: number
  bytes_out: number
}

export interface Series {
  /** the last 120 seconds, one point per second */
  seconds: SeriesPoint[]
  /** the last 180 minutes, one point per minute */
  minutes: SeriesPoint[]
}

export interface StatsSnapshot {
  time: string
  uptime_seconds: number
  version: string
  totals: Totals
  /** image, text, font, other */
  by_kind: Record<string, Bucket>
  /** what was sent: avif, webp, jxl, jpeg, png, gif, br, zstd, gzip, deflate, woff2, identity */
  by_codec: Record<string, Bucket>
  by_profile: Record<string, Bucket>
  /** why a response was left alone, or "transformed": the names of the pipeline */
  by_decision: Record<string, number>
  latency: Latency
  cache: CacheStats
  connections: ConnectionStats
  cpu: CPUStats
  /** null while the aggregation of domains is off */
  top_domains: DomainStat[] | null
  series: Series
}

// ---------------------------------------------------------------- flows

export type Kind = 'image' | 'text' | 'font' | 'other'

export interface ClientRef {
  /** id of the device when the proxy uses tokens, empty otherwise */
  device: string
  /** address the client connected from */
  addr: string
}

export interface Flow {
  id: string
  time: string
  method: string
  scheme: string
  host: string
  path: string
  status: number
  client: ClientRef
  kind: Kind
  content_type: string
  /** what was sent when it differs from content_type */
  out_content_type: string
  bytes_in: number
  bytes_out: number
  decision: 'transformed' | 'passthrough'
  /** transformed, or why it was left alone (type, rule, not-smaller, ...) */
  reason: string
  /** avif, webp, jxl, jpeg, png, gif, br, zstd, gzip, woff2, identity, "" when untouched */
  codec: string
  profile: string
  duration_ms: number
  cache_hit: boolean
  /** the quality metric of an image and the distance reached (dssim: lower is closer) */
  metric: string
  measure: number
  /** the bodies are kept, so /original, /transformed and /compare work */
  has_original: boolean
  has_result: boolean
  /** values of Authorization, Cookie and Set-Cookie are replaced by "<hidden>" */
  request_headers: Record<string, string[]> | null
  response_headers: Record<string, string[]> | null
}

export interface FlowList {
  /** whether the live list is switched on */
  enabled: boolean
  /** the capacity of the buffer */
  capacity: number
  total: number
  flows: Flow[]
}

export interface FlowCompare {
  metric: 'dssim'
  /** 0 means identical, 0.002 can not be told apart by eye */
  distance: number
  original_bytes: number
  result_bytes: number
  width: number
  height: number
  /** the pictures have the same size and could be compared */
  comparable: boolean
}

// ---------------------------------------------------------------- devices

/** A device with a token. */
export interface Device {
  id: string
  /** profile assigned to it, empty for the default */
  profile: string
  created: string
  /** last time it made a request, null when it never did since the start */
  last_seen: string | null
  active: boolean
  requests: number
  bytes_in: number
  bytes_out: number
  saved: number
}

/** Somebody that is connected, with or without a token. */
export interface Client {
  /** device id, or the address when there is no token */
  key: string
  device: string
  addr: string
  user_agent: string
  first_seen: string
  last_seen: string
  active: boolean
  requests: number
  in_flight: number
  bytes_in: number
  bytes_out: number
  saved: number
  profile: string
  /** the kinds of content it asked for */
  by_kind: Record<string, Bucket>
}

export interface NewDevice {
  id: string
  profile: string
  /** shown once */
  token: string
  /** what the Midori extension reads: midopress://config?... */
  config_url: string
  /** the same, as an SVG image of a QR code */
  config_qr_svg: string
}

// ---------------------------------------------------------------- config

export interface Rule {
  match: string
  action?: '' | 'passthrough' | 'passthrough_tls'
  profile?: string
  set?: Record<string, string | number | boolean>
}

/** A profile as the configuration describes it, all fields optional. */
export interface ProfileOverride {
  base?: string
  target_dssim?: number
  target_ssim?: number
  target_ssimulacra2?: number
  attempts?: number
  /** jxl, avif, webp */
  formats?: string[]
  resize_to_viewport?: boolean
  photo_colors?: number
  quantize_png?: boolean
  animated_to_still?: boolean
  animated_quality?: number
  text?: { brotli?: number; gzip?: number; zstd?: number; brotli_static?: number }
}

/** A profile as it works, with every value filled in. */
export interface ProfileInfo {
  name: string
  builtin: boolean
  /** true when the configuration changes a built-in profile */
  modified: boolean
  lossless: boolean
  target_dssim: number
  target_ssim: number
  target_ssimulacra2: number
  attempts: number
  formats: string[]
  resize_to_viewport: boolean
  photo_colors: number
  quantize_png: boolean
  animated_to_still: boolean
  animated_quality: number
  text: { brotli: number; gzip: number; zstd: number; brotli_static: number }
  /** the override that makes it, empty for an untouched built-in */
  override: ProfileOverride
}

export interface ConfigView {
  /** the effective configuration, secrets replaced by "<redacted>" */
  config: Record<string, unknown>
  default_profile: string
  profiles: ProfileInfo[]
  rules: Rule[]
  /** where the settings made in the interface are saved */
  state_file: string
  /** the settings of ui.yaml win over the configuration file */
  managed_by_ui: { default_profile: boolean; profiles: boolean; rules: boolean }
}

export interface ValidationIssue {
  /** index of the rule, -1 for a general problem */
  index: number
  field: string
  /** 1-based column inside `match`, 0 when it does not apply */
  column: number
  message: string
}

export interface ValidateResult {
  ok: boolean
  issues: ValidationIssue[]
}

export interface TestRequest {
  url: string
  method?: string
  /** media type of the response, to try the rules that look at it */
  content_type?: string
  status?: number
  /** headers of the request, for example X-MidoPress-Profile or Save-Data */
  headers?: Record<string, string>
  /** profile of the device that would make the request */
  device_profile?: string
  /** address of the client, for the ~src rules */
  src?: string
}

export interface RuleExplanation {
  index: number
  match: string
  matched: boolean
  /** which condition decided it */
  reason: string
  action: string
  profile: string
}

export interface TestResult {
  /** the rules are tried on the response; with no content type only the request side counts */
  explanations: RuleExplanation[]
  action: '' | 'passthrough' | 'passthrough_tls'
  /** the profile that would be used and where it comes from */
  profile: string
  profile_source: 'device' | 'header' | 'rule' | 'save-data' | 'default'
  set: Record<string, string | number | boolean>
  /** what the pipeline would do, as far as it can be told without the body */
  outcome: 'passthrough' | 'process'
  note: string
}

// ---------------------------------------------------------------- the rest

export interface LogLine {
  time: string
  level: 'DEBUG' | 'INFO' | 'WARN' | 'ERROR'
  message: string
  attrs: Record<string, string>
}

export interface LogState {
  level: 'debug' | 'info' | 'warn' | 'error'
  /** the last lines, oldest first */
  lines: LogLine[]
}

export interface CAInfo {
  available: boolean
  subject: string
  not_before: string
  not_after: string
  sha256: string
  /** the page that explains how to install it, on the magic host */
  page_url: string
  /** the proxy has to be used to reach the magic host */
  magic_host: string
}

export interface Settings {
  /** the live list of flows */
  flows: boolean
  /** the aggregation of traffic by domain */
  domains: boolean
  /** the bodies of images are kept so that the flows can be compared */
  capture: boolean
}

export interface Module {
  path: string
  version: string
}

export interface About {
  version: string
  go_version: string
  os: string
  arch: string
  started: string
  uptime_seconds: number
  backend: string
  backends: string[]
  native: Record<string, boolean>
  modules: Module[]
  state_dir: string
  proxy_addr: string
  tls: boolean
  mitm: boolean
  auth_mode: string
}

/** What the magic host tells a client about itself (public). */
export interface Me {
  device: string
  addr: string
  profile: string
  requests: number
  bytes_in: number
  bytes_out: number
  saved: number
  ca_trusted_hint: string
}

// ---------------------------------------------------------------- events

/**
 * GET /events (text/event-stream) sends these. `stats` once a second, `flow`
 * for each new flow while they are on, `client` when a client appears or goes,
 * `log` for each log line. The stream is a convenience: after a gap the pages
 * ask again for the full state.
 */
export type ServerEvent =
  | { event: 'stats'; data: StatsSnapshot }
  | { event: 'flow'; data: Flow }
  | { event: 'client'; data: Client }
  | { event: 'log'; data: LogLine }
  | { event: 'settings'; data: Settings }

export interface ApiError {
  error: string
  /** machine readable, for example "unauthorized", "invalid", "not_found" */
  code: string
  issues?: ValidationIssue[]
}
