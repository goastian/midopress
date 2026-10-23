// Profiles, rules and their validation, and the "which rule would apply" test.

import { FilterError, parseFilter, atomsOf, HOST_ONLY, explain } from './filter.mjs'

const clone = (x) => JSON.parse(JSON.stringify(x))

const BUILTIN = {
  lossless: {
    lossless: true, target_dssim: 0, target_ssim: 1, target_ssimulacra2: 100, attempts: 1,
    formats: ['jxl', 'webp'], resize_to_viewport: false, photo_colors: 0, quantize_png: false,
    animated_to_still: false, animated_quality: 100, avif_effort: 0, no_transform: 'respect', minify_js: false,
    text: { brotli: 5, gzip: 6, zstd: 5, brotli_static: 11, static_wait_ms: 300 },
  },
  balanced: {
    lossless: false, target_dssim: 0.002, target_ssim: 0.99, target_ssimulacra2: 70, attempts: 5,
    formats: ['jxl', 'avif', 'webp'], resize_to_viewport: false, photo_colors: 4096, quantize_png: false,
    animated_to_still: false, animated_quality: 70, avif_effort: 4, no_transform: 'respect', minify_js: false,
    text: { brotli: 6, gzip: 6, zstd: 7, brotli_static: 10, static_wait_ms: 300 },
  },
  saver: {
    lossless: false, target_dssim: 0.006, target_ssim: 0.975, target_ssimulacra2: 55, attempts: 5,
    formats: ['avif', 'jxl', 'webp'], resize_to_viewport: true, photo_colors: 2048, quantize_png: true,
    animated_to_still: false, animated_quality: 50, avif_effort: 5, no_transform: 'images', minify_js: true,
    text: { brotli: 9, gzip: 9, zstd: 12, brotli_static: 11, static_wait_ms: 1000 },
  },
  extreme: {
    lossless: false, target_dssim: 0.015, target_ssim: 0.94, target_ssimulacra2: 40, attempts: 6,
    formats: ['avif', 'webp'], resize_to_viewport: true, photo_colors: 1024, quantize_png: true,
    animated_to_still: true, animated_quality: 30, avif_effort: 5, no_transform: 'images', minify_js: true,
    text: { brotli: 11, gzip: 9, zstd: 19, brotli_static: 11, static_wait_ms: 1500 },
  },
}

export const BUILTIN_NAMES = Object.keys(BUILTIN)
const RANK = { lossless: 0, balanced: 1, saver: 2, extreme: 3 }
const NAME_RE = /^[a-z][a-z0-9_-]{0,31}$/
const FORMATS = ['jxl', 'avif', 'webp']
const MINIFY = ['off', 'safe', 'aggressive']
const ACTIONS = ['', 'passthrough', 'passthrough_tls']
const RULE_KEYS = ['match', 'action', 'profile', 'set']
const SET_KEYS = ['minify_html', 'minify_css', 'minify_js', 'minify_svg', 'minify_json', 'images', 'text', 'quality', 'no_transform']
const NO_TRANSFORM = ['respect', 'images', 'all']

const issue = (index, field, message, column = 0) => ({ index, field, column, message })
const isObj = (x) => x && typeof x === 'object' && !Array.isArray(x)
const isNum = (x) => typeof x === 'number' && Number.isFinite(x)

function initialState() {
  return {
    defaultProfile: 'balanced',
    overrides: {},
    custom: {
      mobile: {
        base: 'saver', target_dssim: 0.004, formats: ['avif', 'webp'], resize_to_viewport: true,
        text: { brotli: 5, zstd: 5 },
      },
    },
    rules: [
      { match: '~d (bank|banco|paypal|health)\\.', action: 'passthrough_tls' },
      { match: '~d cdn\\.example\\.com & ~t image/', profile: 'extreme' },
    ],
    managed: { default_profile: false, profiles: false, rules: false },
  }
}

function checkOverride(o) {
  const out = []
  if (!isObj(o)) return [issue(-1, 'profile', 'the profile must be an object')]
  const allowed = ['base', 'target_dssim', 'target_ssim', 'target_ssimulacra2', 'attempts', 'formats',
    'resize_to_viewport', 'photo_colors', 'quantize_png', 'animated_to_still', 'animated_quality', 'avif_effort', 'no_transform', 'minify_js', 'text']
  for (const k of Object.keys(o)) if (!allowed.includes(k)) out.push(issue(-1, k, `unknown field "${k}"`))
  const range = (k, lo, hi, int) => {
    if (o[k] === undefined) return
    if (!isNum(o[k]) || o[k] < lo || o[k] > hi || (int && !Number.isInteger(o[k]))) {
      out.push(issue(-1, k, `${k} must be ${int ? 'an integer' : 'a number'} between ${lo} and ${hi}`))
    }
  }
  range('target_dssim', 0, 1)
  range('target_ssim', 0, 1)
  range('target_ssimulacra2', 0, 100)
  range('attempts', 1, 10, true)
  range('photo_colors', 0, 1 << 24, true)
  range('animated_quality', 1, 100, true)
  range('avif_effort', 0, 10, true)
  if (o.no_transform !== undefined && !NO_TRANSFORM.includes(o.no_transform)) out.push(issue(-1, 'no_transform', 'no_transform must be respect, images or all'))
  for (const k of ['resize_to_viewport', 'quantize_png', 'animated_to_still', 'minify_js']) {
    if (o[k] !== undefined && typeof o[k] !== 'boolean') out.push(issue(-1, k, `${k} must be true or false`))
  }
  if (o.base !== undefined && !BUILTIN_NAMES.includes(o.base)) {
    out.push(issue(-1, 'base', `base must be one of ${BUILTIN_NAMES.join(', ')}`))
  }
  if (o.formats !== undefined) {
    if (!Array.isArray(o.formats) || o.formats.length === 0 || o.formats.some((f) => !FORMATS.includes(f))) {
      out.push(issue(-1, 'formats', `formats must be a non empty list of ${FORMATS.join(', ')}`))
    }
  }
  if (o.text !== undefined) {
    if (!isObj(o.text)) out.push(issue(-1, 'text', 'text must be an object'))
    else {
      const lim = { brotli: [0, 11], gzip: [1, 9], zstd: [1, 22], brotli_static: [0, 11], static_wait_ms: [0, 60000] }
      for (const [k, v] of Object.entries(o.text)) {
        if (!lim[k]) out.push(issue(-1, `text.${k}`, `unknown text level "${k}"`))
        else if (!Number.isInteger(v) || v < lim[k][0] || v > lim[k][1]) {
          out.push(issue(-1, `text.${k}`, `${k} must be an integer between ${lim[k][0]} and ${lim[k][1]}`))
        }
      }
    }
  }
  return out
}

function checkRule(rule, i, profiles) {
  const out = []
  if (!isObj(rule)) return [issue(i, '', 'a rule must be an object')]
  for (const k of Object.keys(rule)) if (!RULE_KEYS.includes(k)) out.push(issue(i, k, `unknown field "${k}"`))
  let ast = null
  if (typeof rule.match !== 'string' || rule.match.trim() === '') {
    out.push(issue(i, 'match', 'match is empty', 1))
  } else {
    try {
      ast = parseFilter(rule.match)
    } catch (e) {
      if (!(e instanceof FilterError)) throw e
      out.push(issue(i, 'match', e.message, e.column))
    }
  }
  if (rule.action !== undefined && !ACTIONS.includes(rule.action)) {
    out.push(issue(i, 'action', 'action must be empty, passthrough or passthrough_tls'))
  } else if (rule.action === 'passthrough_tls' && ast) {
    const bad = atomsOf(ast).find((a) => !HOST_ONLY.has(a.name))
    if (bad) {
      out.push(issue(i, 'match', `passthrough_tls can only use ~d, ~src and ~dst, not ~${bad.name}`, bad.col))
    }
  }
  if (rule.profile !== undefined && rule.profile !== '' && !profiles.includes(rule.profile)) {
    out.push(issue(i, 'profile', `unknown profile "${rule.profile}"`))
  }
  if (rule.set !== undefined) {
    if (!isObj(rule.set)) out.push(issue(i, 'set', 'set must be an object'))
    else {
      for (const [k, v] of Object.entries(rule.set)) {
        const f = `set.${k}`
        if (!SET_KEYS.includes(k)) out.push(issue(i, f, `unknown key "${k}"`))
        else if (k.startsWith('minify_')) {
          if (typeof v !== 'boolean' && !MINIFY.includes(v)) out.push(issue(i, f, `${k} must be off, safe, aggressive or a boolean`))
        } else if (k === 'quality') {
          if (!Number.isInteger(v) || v < 1 || v > 100) out.push(issue(i, f, 'quality must be an integer between 1 and 100'))
        } else if (k === 'no_transform') {
          if (!NO_TRANSFORM.includes(v)) out.push(issue(i, f, 'no_transform must be one of respect, images, all'))
        } else if (typeof v !== 'boolean') out.push(issue(i, f, `${k} must be true or false`))
      }
    }
  }
  const acts = (rule.action || rule.profile) || (isObj(rule.set) && Object.keys(rule.set).length)
  if (!acts && out.length === 0) out.push(issue(i, 'action', 'the rule does nothing: give an action, a profile or set'))
  return out
}

export class ConfigStore {
  constructor() {
    this.reset()
  }

  reset() {
    Object.assign(this, initialState())
  }

  names() {
    return [...BUILTIN_NAMES, ...Object.keys(this.custom)]
  }

  resolve(name) {
    const builtin = BUILTIN_NAMES.includes(name)
    if (!builtin && !this.custom[name]) return null
    const override = builtin ? this.overrides[name] || {} : this.custom[name]
    const base = builtin ? name : override.base || 'balanced'
    const info = clone(BUILTIN[base])
    for (const [k, v] of Object.entries(override)) {
      if (k === 'base') continue
      if (k === 'text') Object.assign(info.text, v)
      else info[k] = clone(v)
    }
    return {
      name, builtin, modified: builtin && Boolean(this.overrides[name]),
      ...info, lossless: BUILTIN[base].lossless, override: clone(override),
    }
  }

  view(extra = {}) {
    return {
      config: {
        listen: { addr: '127.0.0.1:9999', socks5: '', http3: false },
        mitm: { enabled: true, ca_cert: '/home/user/.midopress/ca.crt', ca_key: '<redacted>', leaf_ttl: '48h' },
        auth: { mode: 'token', block_global: true, devices_file: '/home/user/.midopress/devices.json' },
        cache: { dir: '/home/user/.cache/midopress', max_size: '5GiB', memory: '256MiB' },
        web: {
          addr: '127.0.0.1:9998', admin_token_file: '/home/user/.midopress/admin.token',
          admin_token: '<redacted>', flow_buffer: 500, capture_bytes: 8388608, ...extra,
        },
        log: { level: 'info', urls: false },
      },
      default_profile: this.defaultProfile,
      profiles: this.names().map((n) => this.resolve(n)),
      rules: clone(this.rules),
      state_file: '/home/user/.midopress/ui.yaml',
      managed_by_ui: { ...this.managed },
    }
  }

  validateRules(rules) {
    if (!Array.isArray(rules)) return [issue(-1, 'rules', 'the body must be a list of rules')]
    const names = this.names()
    return rules.flatMap((r, i) => checkRule(r, i, names))
  }

  putRules(rules) {
    const issues = this.validateRules(rules)
    if (issues.length) return issues
    this.rules = rules.map((r) => {
      const out = { match: r.match }
      if (r.action) out.action = r.action
      if (r.profile) out.profile = r.profile
      if (r.set && Object.keys(r.set).length) out.set = { ...r.set }
      return out
    })
    this.managed.rules = true
    return []
  }

  putProfile(name, body) {
    if (!NAME_RE.test(name)) {
      return [issue(-1, 'name', 'a profile name has 1 to 32 characters: a-z, 0-9, _ and -, starting with a letter')]
    }
    const issues = checkOverride(body)
    if (issues.length) return issues
    if (BUILTIN_NAMES.includes(name)) {
      if (body.base !== undefined) return [issue(-1, 'base', 'a built-in profile has no base')]
      this.overrides[name] = clone(body)
    } else this.custom[name] = clone(body)
    this.managed.profiles = true
    return []
  }

  /** Returns null when done, or { status, message } when refused. */
  deleteProfile(name) {
    if (BUILTIN_NAMES.includes(name)) {
      delete this.overrides[name]
      return null
    }
    if (!this.custom[name]) return { status: 404, message: `no profile "${name}"` }
    if (this.defaultProfile === name) return { status: 409, message: `"${name}" is the default profile` }
    if (this.rules.some((r) => r.profile === name)) return { status: 409, message: `a rule uses the profile "${name}"` }
    delete this.custom[name]
    return null
  }

  setDefault(name) {
    if (typeof name !== 'string' || !this.resolve(name)) return [issue(-1, 'profile', `unknown profile "${name}"`)]
    this.defaultProfile = name
    this.managed.default_profile = true
    return []
  }

  forgetUi() {
    const keep = initialState()
    Object.assign(this, keep, { custom: keep.custom })
  }

  /** Which rules and which profile would apply to a request. */
  test(req) {
    const issues = []
    if (!isObj(req) || typeof req.url !== 'string' || req.url === '') issues.push(issue(-1, 'url', 'url is required'))
    let url = null
    if (issues.length === 0) {
      try {
        url = new URL(req.url)
        if (!/^https?:$/.test(url.protocol)) throw new Error('scheme')
      } catch {
        issues.push(issue(-1, 'url', 'url must be an absolute http or https URL'))
      }
    }
    if (issues.length) return { issues }
    const headers = req.headers || {}
    const lower = Object.fromEntries(Object.entries(headers).map(([k, v]) => [k.toLowerCase(), v]))
    const hasResponse = Boolean(req.content_type) || Boolean(req.status)
    const ctx = {
      host: url.hostname, url: req.url, method: (req.method || 'GET').toUpperCase(),
      status: req.status || 0, contentType: req.content_type || '', hasResponse,
      reqHeaders: Object.fromEntries(Object.entries(headers).map(([k, v]) => [k, [String(v)]])),
      resHeaders: req.content_type ? { 'Content-Type': [req.content_type] } : {},
      src: req.src || '', dst: url.hostname, body: '',
    }
    let action = ''
    let ruleProfile = ''
    const set = {}
    const explanations = this.rules.map((rule, index) => {
      const r = explain(parseFilter(rule.match), ctx)
      if (r.value) {
        if (!action && rule.action) action = rule.action
        if (!ruleProfile && rule.profile) ruleProfile = rule.profile
        for (const [k, v] of Object.entries(rule.set || {})) if (!(k in set)) set[k] = v
      }
      return {
        index, match: rule.match, matched: r.value, reason: r.reason,
        action: rule.action || '', profile: rule.profile || '',
      }
    })

    const notes = []
    if (!hasResponse) notes.push('no content type or status given: only the request side of the rules was tried')
    let profile = this.defaultProfile
    let source = 'default'
    const hdrProfile = lower['x-midopress-profile']
    if (req.device_profile && this.resolve(req.device_profile)) {
      profile = req.device_profile
      source = 'device'
    } else if (hdrProfile && this.resolve(hdrProfile)) {
      profile = hdrProfile
      source = 'header'
    } else if (ruleProfile) {
      profile = ruleProfile
      source = 'rule'
    } else if (/^(on|1)$/i.test(lower['save-data'] || '') && (RANK[this.defaultProfile] ?? 2) < RANK.saver) {
      profile = 'saver'
      source = 'save-data'
    }
    if (req.device_profile && !this.resolve(req.device_profile)) notes.push(`unknown device profile "${req.device_profile}" ignored`)
    if (hdrProfile && !this.resolve(hdrProfile)) notes.push(`unknown X-MidoPress-Profile "${hdrProfile}" ignored`)
    if (action === 'passthrough_tls' && url.protocol === 'http:') notes.push('plain http: passthrough_tls behaves as passthrough')
    const outcome = action ? 'passthrough' : 'process'
    notes.push(
      action ? `a rule says ${action}, the response is not touched` : `the pipeline would work with the profile "${profile}" (${source})`,
    )
    return { result: { explanations, action, profile, profile_source: source, set, outcome, note: notes.join('; ') } }
  }
}
