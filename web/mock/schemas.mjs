// A small hand written schema checker, derived from web/src/api/types.ts.
// check(value, schema) returns a list of problems, empty when it fits. Objects
// are strict: a field that is not in the schema is a problem.

const arr = (s) => ({ t: 'arr', s })
const rec = (s) => ({ t: 'rec', s })
const or = (...s) => ({ t: 'or', s })
const lit = (...v) => ({ t: 'lit', v })
const obj = (f, optional = []) => ({ t: 'obj', f, opt: new Set(optional) })
const str = 'string'
const num = 'number'
const bool = 'boolean'
const nul = 'null'

export function check(v, s, at = '$') {
  if (typeof s === 'string') {
    if (s === 'any') return []
    if (s === nul) return v === null ? [] : [`${at}: expected null`]
    return typeof v === s && (s !== 'number' || Number.isFinite(v)) ? [] : [`${at}: expected ${s}, got ${v === null ? 'null' : typeof v}`]
  }
  switch (s.t) {
    case 'lit':
      return s.v.includes(v) ? [] : [`${at}: expected one of ${s.v.join('|')}, got ${v}`]
    case 'or': {
      const all = s.s.map((x) => check(v, x, at))
      return all.some((e) => e.length === 0) ? [] : [`${at}: no alternative fits (${all.map((e) => e[0]).join(' / ')})`]
    }
    case 'arr':
      return Array.isArray(v) ? v.flatMap((x, i) => check(x, s.s, `${at}[${i}]`)) : [`${at}: expected an array`]
    case 'rec':
      if (v === null || typeof v !== 'object' || Array.isArray(v)) return [`${at}: expected an object`]
      return Object.entries(v).flatMap(([k, x]) => check(x, s.s, `${at}.${k}`))
    case 'obj': {
      if (v === null || typeof v !== 'object' || Array.isArray(v)) return [`${at}: expected an object`]
      const out = []
      for (const [k, sub] of Object.entries(s.f)) {
        if (!(k in v)) {
          if (!s.opt.has(k)) out.push(`${at}.${k}: missing`)
        } else out.push(...check(v[k], sub, `${at}.${k}`))
      }
      for (const k of Object.keys(v)) if (!(k in s.f)) out.push(`${at}.${k}: not in the contract`)
      return out
    }
  }
  return [`${at}: bad schema`]
}

const Bucket = obj({ requests: num, transformed: num, bytes_in: num, bytes_out: num })
const SeriesPoint = obj({ t: num, requests: num, bytes_in: num, bytes_out: num })
const Kind = lit('image', 'text', 'font', 'other')
const Strs = arr(str)
const Headers = or(nul, rec(Strs))

export const StatsSnapshot = obj({
  time: str, uptime_seconds: num, version: str,
  totals: obj({ requests: num, transformed: num, bytes_in: num, bytes_out: num, saved: num, saved_percent: num }),
  by_kind: rec(Bucket), by_codec: rec(Bucket), by_profile: rec(Bucket), by_decision: rec(num),
  latency: obj({ p50_ms: num, p95_ms: num, p99_ms: num, samples: num }),
  cache: obj({ hits: num, misses: num, hit_rate: num, memory_bytes: num, memory_items: num, disk_bytes: num, disk_items: num, evictions: num, expired: num, disk_errors: num }),
  connections: obj({ clients_active: num, requests_in_flight: num, tunnels_active: num, tunnels_total: num, tls_handshakes: num, tls_handshake_errors: num, upstream_errors: num, denied: num }),
  cpu: obj({ slots: num, busy: num, background_queue: num }),
  top_domains: or(nul, arr(obj({ domain: str, requests: num, transformed: num, bytes_in: num, bytes_out: num }))),
  series: obj({ seconds: arr(SeriesPoint), minutes: arr(SeriesPoint) }),
})

export const Flow = obj({
  id: str, time: str, method: str, scheme: str, host: str, path: str, status: num,
  client: obj({ device: str, addr: str }), kind: Kind, content_type: str, out_content_type: str,
  bytes_in: num, bytes_out: num, decision: lit('transformed', 'passthrough'), reason: str, codec: str, profile: str,
  duration_ms: num, cache_hit: bool, metric: str, measure: num, has_original: bool, has_result: bool,
  request_headers: Headers, response_headers: Headers,
})

export const FlowList = obj({ enabled: bool, capacity: num, total: num, flows: arr(Flow) })
export const FlowCompare = obj({ metric: lit('dssim'), distance: num, original_bytes: num, result_bytes: num, width: num, height: num, comparable: bool })

export const Device = obj({
  id: str, profile: str, created: str, last_seen: or(str, nul), active: bool, requests: num, bytes_in: num, bytes_out: num, saved: num,
})
export const Client = obj({
  key: str, device: str, addr: str, user_agent: str, first_seen: str, last_seen: str, active: bool, requests: num,
  in_flight: num, bytes_in: num, bytes_out: num, saved: num, profile: str, by_kind: rec(Bucket),
})
export const NewDevice = obj({ id: str, profile: str, token: str, config_url: str, config_qr_svg: str })

const Text = obj({ brotli: num, gzip: num, zstd: num, brotli_static: num })
export const ProfileInfo = obj({
  name: str, builtin: bool, modified: bool, lossless: bool, target_dssim: num, target_ssim: num, target_ssimulacra2: num,
  attempts: num, formats: Strs, resize_to_viewport: bool, photo_colors: num, quantize_png: bool, animated_to_still: bool,
  animated_quality: num, text: Text, override: rec('any'),
})
const Rule = obj({ match: str, action: lit('', 'passthrough', 'passthrough_tls'), profile: str, set: rec(or(str, num, bool)) }, ['action', 'profile', 'set'])
export const ConfigView = obj({
  config: rec('any'), default_profile: str, profiles: arr(ProfileInfo), rules: arr(Rule), state_file: str,
  managed_by_ui: obj({ default_profile: bool, profiles: bool, rules: bool }),
})
const Issue = obj({ index: num, field: str, column: num, message: str })
export const ValidateResult = obj({ ok: bool, issues: arr(Issue) })
export const ApiError = obj({ error: str, code: str, issues: arr(Issue) }, ['issues'])
export const TestResult = obj({
  explanations: arr(obj({ index: num, match: str, matched: bool, reason: str, action: str, profile: str })),
  action: lit('', 'passthrough', 'passthrough_tls'), profile: str,
  profile_source: lit('device', 'header', 'rule', 'save-data', 'default'),
  set: rec(or(str, num, bool)), outcome: lit('passthrough', 'process'), note: str,
})

export const LogLine = obj({ time: str, level: lit('DEBUG', 'INFO', 'WARN', 'ERROR'), message: str, attrs: rec(str) })
export const LogState = obj({ level: lit('debug', 'info', 'warn', 'error'), lines: arr(LogLine) })
export const CAInfo = obj({ available: bool, subject: str, not_before: str, not_after: str, sha256: str, page_url: str, magic_host: str })
export const Settings = obj({ flows: bool, domains: bool, capture: bool })
export const About = obj({
  version: str, go_version: str, os: str, arch: str, started: str, uptime_seconds: num, backend: str, backends: Strs,
  native: rec(bool), modules: arr(obj({ path: str, version: str })), state_dir: str, proxy_addr: str, tls: bool, mitm: bool, auth_mode: str,
})
