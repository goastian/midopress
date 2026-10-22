// The simulated MidoPress: traffic, clients, devices, flows, logs and the
// numbers of the dashboard. Nothing here knows about HTTP.

import { ConfigStore } from './config.mjs'
import { matches } from './filter.mjs'

export const FLOW_CAPACITY = 500
const MEM_MAX = 256 * 1024 * 1024
const LAT_MAX = 2000
const LEVELS = ['debug', 'info', 'warn', 'error']
export const KINDS = ['image', 'text', 'font', 'other']

const wpick = (items) => {
  let r = Math.random() * items.reduce((a, i) => a + i[1], 0)
  for (const [v, w] of items) if ((r -= w) < 0) return v
  return items[items.length - 1][0]
}
const pick = (a) => a[Math.floor(Math.random() * a.length)]
const rint = (a, b) => Math.floor(a + Math.random() * (b - a + 1))
const rlog = (a, b) => Math.exp(Math.log(a) + Math.random() * (Math.log(b) - Math.log(a)))
const hex = (n) => Array.from({ length: n }, () => Math.floor(Math.random() * 16).toString(16)).join('')
const zipf = (list) => wpick(list.map((h, i) => [h, 1 / (i + 1)]))
const clamp = (x, lo, hi) => Math.min(hi, Math.max(lo, x))
const bucket = () => ({ requests: 0, transformed: 0, bytes_in: 0, bytes_out: 0 })
const iso = (t) => new Date(t).toISOString()

const HOSTS = {
  image: ['cdn.example.com', 'images.unsplash.com', 'i.imgur.com', 'upload.wikimedia.org', 'static.cdninstagram.com',
    'pbs.twimg.com', 'media.licdn.com', 'cdn.shopify.com', 'i.ytimg.com', 'img.shields.io'],
  text: ['www.example.com', 'en.wikipedia.org', 'news.ycombinator.com', 'github.com', 'www.bbc.co.uk',
    'stackoverflow.com', 'www.reddit.com', 'developer.mozilla.org', 'api.github.com', 'www.nytimes.com'],
  font: ['fonts.gstatic.com', 'use.typekit.net', 'cdn.jsdelivr.net'],
  other: ['rr3---sn-4g5e6nsz.googlevideo.com', 'www.youtube.com', 'cdn.example.com', 'api.example.com',
    'www.google-analytics.com', 'open.spotify.com', 'download.example.org'],
}

const CLIENT_DEFS = [
  { device: 'laptop-mario', addr: '10.8.0.12', profile: '', w: 3, age: 41,
    ua: 'Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0' },
  { device: 'pixel-ana', addr: '192.168.1.41', profile: 'mobile', w: 3, age: 17,
    ua: 'Mozilla/5.0 (Linux; Android 15; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36' },
  { device: 'ipad-casa', addr: '192.168.1.52', profile: 'saver', w: 2, age: 9,
    ua: 'Mozilla/5.0 (iPad; CPU OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1' },
  { device: 'tablet-old', addr: '192.168.1.77', profile: '', w: 2, age: 120, idle: true,
    ua: 'Mozilla/5.0 (Linux; Android 9; SM-T510) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36' },
  { device: '', addr: '192.168.1.23', profile: '', w: 2, age: 0,
    ua: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0' },
  { device: '', addr: '10.8.0.7', profile: '', w: 1, age: 0,
    ua: 'Dalvik/2.1.0 (Linux; U; Android 13; SM-A546B Build/TP1A.220624.014)' },
]

const LOG_TEMPLATES = [
  ['info', 'transformed', () => ({ kind: 'image', codec: pick(['avif', 'webp', 'jxl']), saved: `${rint(20, 480)} KiB`, ms: String(rint(60, 700)) })],
  ['info', 'transformed', () => ({ kind: 'text', codec: pick(['br', 'zstd']), saved: `${rint(2, 90)} KiB`, ms: String(rint(5, 80)) })],
  ['debug', 'cache lookup', () => ({ result: pick(['hit', 'miss']), host: pick(HOSTS.image) })],
  ['debug', 'tunnel opened', () => ({ host: pick(HOSTS.other), client: `192.168.1.${rint(20, 90)}` })],
  ['info', 'device seen', () => ({ device: pick(['pixel-ana', 'ipad-casa', 'laptop-mario']) })],
  ['warn', 'upstream slow', () => ({ host: pick(HOSTS.image), ms: String(rint(2500, 6000)) })],
  ['warn', 'image skipped', () => ({ reason: pick(['not-smaller', 'budget']), host: pick(HOSTS.image) })],
  ['error', 'upstream error', () => ({ host: pick(HOSTS.other), err: pick(['connection reset by peer', 'i/o timeout', 'tls: handshake failure']) })],
]

const PASS_OTHER = [['type', 55], ['range', 12], ['rule', 10], ['budget', 8], ['no-transform', 6], ['disabled', 5], ['timeout', 4]]
const PASS_MEDIA = [['not-smaller', 40], ['budget', 12], ['rule', 15], ['no-transform', 10], ['timeout', 8], ['disabled', 5], ['range', 10]]
const MIME = { avif: 'image/avif', webp: 'image/webp', jxl: 'image/jxl', jpeg: 'image/jpeg', png: 'image/png', gif: 'image/gif' }

export class Sim {
  constructor(emit) {
    this.emit = emit
    this.flowSeq = 0
    this.runId = hex(4)
    this.config = new ConfigStore()
    this.reset()
  }

  reset() {
    const now = Date.now()
    this.startedAt = now
    this.speed = 1
    this.config.reset()
    this.settings = { flows: false, domains: false, capture: false }
    this.logLevel = 'info'
    this.totals = bucket()
    this.byKind = Object.fromEntries(KINDS.map((k) => [k, bucket()]))
    this.byCodec = {}
    this.byProfile = {}
    this.byDecision = {}
    this.domains = new Map()
    this.flows = []
    this.flowIndex = new Map()
    this.flowMeta = new Map()
    this.lat = []
    this.cache = { hits: 0, misses: 0, memory_bytes: 0, memory_items: 0, disk_bytes: 0, disk_items: 0, evictions: 0, expired: 0, disk_errors: 0 }
    this.conn = { requests_in_flight: 0, tunnels_active: 0, tunnels_total: 0, tls_handshakes: 0, tls_handshake_errors: 0, upstream_errors: 0, denied: 0 }
    this.cpu = { slots: 8, busy: 0, background_queue: 0 }
    this.logs = []
    this.initSeries(now)
    this.devices = new Map()
    this.clients = []
    for (const d of CLIENT_DEFS) this.addClient(d, now)
    this.log('info', 'midopress starting', { version: '0.1.0-mock' })
    this.log('info', 'proxy listening', { addr: '127.0.0.1:9999', tls: 'true' })
    this.log('info', 'web listening', { addr: '127.0.0.1:9998' })
    this.log('info', 'CA loaded', { subject: 'MidoPress Development CA' })
  }

  addClient(d, now) {
    const idle = Boolean(d.idle)
    const client = {
      key: d.device || d.addr, device: d.device, addr: d.addr, ua: d.ua, w: d.w,
      first_seen: now - (idle ? 3 * 864e5 : 6e4 * 3), last_seen: idle ? now - 25 * 6e4 : now,
      idle, requests: 0, in_flight: 0, bytes_in: 0, bytes_out: 0, by_kind: Object.fromEntries(KINDS.map((k) => [k, bucket()])),
    }
    if (idle) Object.assign(client, { requests: 1840, bytes_in: 96e6, bytes_out: 41e6 })
    this.clients.push(client)
    if (d.device) {
      this.devices.set(d.device, {
        id: d.device, profile: d.profile, created: now - d.age * 864e5,
        last_seen: idle ? client.last_seen : null, requests: client.requests, bytes_in: client.bytes_in, bytes_out: client.bytes_out,
      })
    }
  }

  uptime() {
    return Math.floor((Date.now() - this.startedAt) / 1000)
  }

  // ------------------------------------------------------------ series

  initSeries(now) {
    const sec = Math.floor(now / 1000)
    const min = Math.floor(sec / 60) * 60
    const zero = (t) => ({ t, requests: 0, bytes_in: 0, bytes_out: 0 })
    this.series = {
      seconds: Array.from({ length: 120 }, (_, i) => zero(sec - 119 + i)),
      minutes: Array.from({ length: 180 }, (_, i) => zero(min - (179 - i) * 60)),
    }
    this.zeroPoint = zero
  }

  advance(now) {
    const sec = Math.floor(now / 1000)
    const roll = (arr, cur, step) => {
      const steps = Math.min((cur - arr[arr.length - 1].t) / step, arr.length)
      for (let k = 1; k <= steps; k++) {
        arr.push(this.zeroPoint(cur - (steps - k) * step))
        arr.shift()
      }
    }
    roll(this.series.seconds, sec, 1)
    roll(this.series.minutes, Math.floor(sec / 60) * 60, 60)
  }

  // ------------------------------------------------------------ traffic

  profileInfo(name) {
    return this.config.resolve(name) || this.config.resolve(this.config.defaultProfile)
  }

  pickClient() {
    const live = this.clients.filter((c) => !c.idle)
    return wpick(live.map((c) => [c, c.w]))
  }

  genRequest() {
    const client = this.pickClient()
    const cfg = this.config
    let profile = this.devices.get(client.device)?.profile || ''
    if (!profile || !cfg.resolve(profile)) {
      const u = Math.random()
      const ruleProfile = cfg.rules.find((r) => r.profile)?.profile
      if (u < 0.03) profile = 'lossless'
      else if (u < 0.11 && ruleProfile) profile = ruleProfile
      else profile = cfg.defaultProfile
    }
    const info = this.profileInfo(profile)
    const kind = wpick([['image', 52], ['text', 26], ['font', 4], ['other', 18]])
    const r = {
      client, kind, profile: info.name, host: zipf(HOSTS[kind]), scheme: Math.random() < 0.88 ? 'https' : 'http',
      method: 'GET', status: 200, decision: 'passthrough', reason: '', codec: '', metric: '', measure: 0,
      out_ct: '', w: 0, h: 0,
    }
    if (kind === 'image') this.genImage(r, info)
    else if (kind === 'text') this.genText(r, info)
    else if (kind === 'font') this.genFont(r)
    else this.genOther(r)

    if (kind !== 'other') {
      if (Math.random() < 0.14) r.reason = wpick(kind === 'image' ? PASS_MEDIA : PASS_MEDIA.filter((x) => x[0] !== 'range'))
      else r.decision = 'transformed'
    }
    if (r.decision === 'transformed') {
      r.reason = 'transformed'
      r.bout = Math.max(200, Math.round(r.bin * r.ratio))
    } else {
      r.bout = r.bin
      r.codec = ''
      r.out_ct = ''
      r.metric = ''
      r.measure = 0
      if (r.reason === 'range') r.status = 206
    }
    r.cache_hit = Math.random() < this.hitRate()
    r.duration = r.cache_hit
      ? rint(2, 18)
      : r.reason === 'timeout' ? rint(3000, 6000)
      : r.decision === 'transformed' ? Math.round(rlog(kind === 'image' ? 80 : 12, kind === 'image' ? 900 : 140)) : Math.round(rlog(15, 400))
    return r
  }

  hitRate() {
    return 0.12 + 0.68 * (1 - Math.exp(-this.totals.requests / 30000))
  }

  genImage(r, info) {
    const t = wpick([['jpeg', 55], ['png', 20], ['webp', 15], ['gif', 10]])
    r.ct = MIME[t]
    r.bin = Math.round(t === 'gif' ? rlog(20e3, 2e6) : rlog(8e3, 900e3))
    r.w = pick([640, 800, 1024, 1280, 1600, 1920, 2400])
    r.h = Math.round(r.w * pick([0.5625, 0.6667, 0.75, 1]))
    const ext = t === 'jpeg' ? 'jpg' : t
    r.path = `/${pick(['media', 'images', 'uploads', 'photos'])}/${pick(['hero', 'cover', 'thumb', 'avatar', 'banner', 'gallery', 'product', 'photo'])}-${rint(1, 9999)}.${ext}${Math.random() < 0.3 ? `?w=${pick([400, 800, 1200])}` : ''}`
    if (info.lossless) {
      r.codec = wpick([[t === 'gif' ? 'webp' : t, 1], ['jxl', 1]])
      r.ratio = 0.8 + Math.random() * 0.15
    } else {
      r.codec = t === 'gif' ? 'webp' : wpick(info.formats.map((f, i) => [f, [5, 3, 1.5][i] || 1]))
      r.ratio = clamp(0.78 * Math.exp(-info.target_dssim * 90) * (0.7 + Math.random() * 0.6), 0.04, 0.96)
    }
    r.out_ct = MIME[r.codec] === r.ct ? '' : MIME[r.codec]
    r.metric = 'dssim'
    r.measure = info.lossless ? 0 : clamp(info.target_dssim * (0.45 + 0.55 * Math.random()), 0.0004, 0.012)
  }

  genText(r, info) {
    const sub = wpick([['html', 35], ['css', 15], ['js', 35], ['json', 15]])
    const n = rint(1, 40)
    r.ct = { html: 'text/html; charset=utf-8', css: 'text/css', js: 'application/javascript', json: 'application/json' }[sub]
    r.path = {
      html: pick(['/', `/news/${pick(['markets', 'tech', 'world'])}-${n}`, `/article/${rint(1000, 99999)}`, '/docs/getting-started', `/search?q=${pick(['proxy', 'avif', 'jpeg+xl'])}`]),
      css: `/assets/app.${hex(8)}.css`,
      js: `/static/js/${pick(['main', 'vendor', 'chunk'])}.${hex(8)}.js`,
      json: `/api/v2/${pick(['items', 'feed', 'user', 'search'])}?page=${n}`,
    }[sub]
    r.bin = Math.round(rlog(2e3, 400e3))
    r.codec = wpick([['br', 55], ['zstd', 25], ['gzip', 20]])
    const level = { br: info.text.brotli / 11, zstd: info.text.zstd / 19, gzip: info.text.gzip / 9 }[r.codec]
    r.ratio = clamp({ html: 0.24, css: 0.2, js: 0.3, json: 0.14 }[sub] * (1.25 - 0.35 * level) * (0.85 + Math.random() * 0.3), 0.05, 0.9)
  }

  genFont(r) {
    const ct = pick([['font/ttf', 'ttf'], ['font/otf', 'otf'], ['application/font-woff', 'woff']])
    r.ct = ct[0]
    r.path = `/fonts/${pick(['inter', 'roboto', 'lora', 'opensans'])}-${pick(['400', '700'])}.${ct[1]}`
    r.bin = Math.round(rlog(30e3, 300e3))
    r.codec = 'woff2'
    r.out_ct = 'font/woff2'
    r.ratio = 0.35 + Math.random() * 0.25
  }

  genOther(r) {
    const reason = wpick(PASS_OTHER)
    const video = reason === 'range' || Math.random() < 0.3
    const t = video
      ? ['video/mp4', 'mp4']
      : pick([['application/pdf', 'pdf'], ['application/zip', 'zip'], ['audio/mpeg', 'mp3'], ['application/octet-stream', 'bin']])
    r.ct = t[0]
    r.path = `/${video ? 'videos/clip' : 'files/report'}-${rint(1, 999)}.${t[1]}`
    r.bin = Math.round(rlog(50e3, 8e6))
    r.reason = reason
    if (!video && Math.random() < 0.05) r.method = 'POST'
  }

  record(r, { emitFlow = true } = {}) {
    const now = Date.now()
    const add = (b) => {
      b.requests++
      if (r.decision === 'transformed') b.transformed++
      b.bytes_in += r.bin
      b.bytes_out += r.bout
    }
    add(this.totals)
    add(this.byKind[r.kind])
    add((this.byCodec[r.codec || 'identity'] ||= bucket()))
    add((this.byProfile[r.profile] ||= bucket()))
    const dec = r.decision === 'transformed' ? 'transformed' : r.reason
    this.byDecision[dec] = (this.byDecision[dec] || 0) + 1
    for (const arr of [this.series.seconds, this.series.minutes]) {
      const p = arr[arr.length - 1]
      p.requests++
      p.bytes_in += r.bin
      p.bytes_out += r.bout
    }
    if (this.settings.domains) add(this.domainBucket(r.host))

    const c = this.cache
    if (r.cache_hit) c.hits++
    else {
      c.misses++
      if (r.decision === 'transformed' || Math.random() < 0.4) this.cacheStore(r.bout)
    }
    if (Math.random() < 0.0002) c.disk_errors++

    const cn = this.conn
    if (r.scheme === 'https' && Math.random() < 0.06) cn.tls_handshakes++
    if (Math.random() < 0.0008) cn.tls_handshake_errors++
    if (Math.random() < 0.03) cn.tunnels_total++
    if (r.reason === 'timeout') cn.upstream_errors++
    if (Math.random() < 0.002) cn.denied++

    const cl = r.client
    cl.requests++
    cl.bytes_in += r.bin
    cl.bytes_out += r.bout
    cl.last_seen = now
    add(cl.by_kind[r.kind])
    const dev = this.devices.get(cl.device)
    if (dev) {
      dev.requests++
      dev.bytes_in += r.bin
      dev.bytes_out += r.bout
      dev.last_seen = now
    }

    this.lat.push(r.duration)
    if (this.lat.length > LAT_MAX) this.lat.shift()
    if (this.settings.flows) this.addFlow(r, now, emitFlow)
  }

  domainBucket(host) {
    let b = this.domains.get(host)
    if (!b) this.domains.set(host, (b = bucket()))
    return b
  }

  cacheStore(bytes) {
    const c = this.cache
    c.memory_items++
    c.memory_bytes += bytes
    if (c.memory_bytes > MEM_MAX) {
      const avg = c.memory_bytes / c.memory_items
      const n = Math.min(c.memory_items - 1, Math.ceil((c.memory_bytes - MEM_MAX * 0.9) / avg))
      const moved = Math.round(n * avg)
      c.memory_bytes -= moved
      c.memory_items -= n
      c.evictions += n
      c.disk_items += n
      c.disk_bytes += moved
    }
  }

  burst(n) {
    const total = clamp(Math.floor(n) || 0, 0, 50000)
    for (let i = 0; i < total; i++) this.record(this.genRequest(), { emitFlow: i >= total - 30 })
    return total
  }

  // ------------------------------------------------------------ flows

  addFlow(r, now, emitFlow) {
    const capture = this.settings.capture && r.kind === 'image'
    const reqH = {
      Host: [r.host],
      'User-Agent': [r.client.ua],
      Accept: [r.kind === 'image' ? 'image/avif,image/webp,image/jxl,image/*;q=0.8' : r.kind === 'font' ? '*/*' : 'text/html,application/xhtml+xml,*/*;q=0.8'],
      'Accept-Encoding': ['gzip, deflate, br, zstd'],
      'Accept-Language': ['en-US,en;q=0.9'],
    }
    if (Math.random() < 0.4) reqH.Cookie = ['<hidden>']
    if (r.host.startsWith('api.')) reqH.Authorization = ['<hidden>']
    if (Math.random() < 0.3) reqH.Referer = [`https://${HOSTS.text[0]}/`]
    const resH = {
      'Content-Type': [r.ct],
      'Content-Length': [String(r.bin)],
      'Cache-Control': [pick(['public, max-age=31536000, immutable', 'max-age=3600', 'no-cache'])],
      Server: [pick(['nginx', 'cloudflare', 'AmazonS3', 'gws'])],
    }
    if (r.kind === 'text') resH['Content-Encoding'] = [pick(['gzip', 'br'])]
    if (Math.random() < 0.1) resH['Set-Cookie'] = ['<hidden>']
    const id = `${this.runId}-${(++this.flowSeq).toString(36).padStart(6, '0')}`
    const flow = {
      id, time: iso(now), method: r.method, scheme: r.scheme, host: r.host, path: r.path, status: r.status,
      client: { device: r.client.device, addr: r.client.addr }, kind: r.kind, content_type: r.ct,
      out_content_type: r.out_ct, bytes_in: r.bin, bytes_out: r.bout, decision: r.decision, reason: r.reason,
      codec: r.codec, profile: r.profile, duration_ms: r.duration, cache_hit: r.cache_hit, metric: r.metric,
      measure: Number(r.measure.toFixed(5)), has_original: capture, has_result: capture && r.decision === 'transformed',
      request_headers: reqH, response_headers: resH,
    }
    this.flows.unshift(flow)
    this.flowIndex.set(id, flow)
    if (r.kind === 'image') this.flowMeta.set(id, { w: r.w, h: r.h })
    while (this.flows.length > FLOW_CAPACITY) {
      const old = this.flows.pop()
      this.flowIndex.delete(old.id)
      this.flowMeta.delete(old.id)
    }
    if (emitFlow) this.emit('flow', flow)
  }

  flowContext(f) {
    return {
      host: f.host, url: `${f.scheme}://${f.host}${f.path}`, method: f.method, status: f.status,
      contentType: f.content_type, hasResponse: true, reqHeaders: f.request_headers, resHeaders: f.response_headers,
      src: f.client.addr, dst: f.host, body: '',
    }
  }

  /** `filter` must already be parsed (see parseFilter) or null. */
  listFlows({ ast, kind, limit }) {
    let list = this.flows
    if (kind) list = list.filter((f) => f.kind === kind)
    if (ast) list = list.filter((f) => matches(ast, this.flowContext(f)))
    return { enabled: this.settings.flows, capacity: FLOW_CAPACITY, total: list.length, flows: list.slice(0, limit) }
  }

  flowMetaFor(f) {
    return this.flowMeta.get(f.id) || null
  }

  har() {
    const entries = [...this.flows].reverse().map((f) => {
      const hdr = (h) => Object.entries(h || {}).flatMap(([name, vs]) => vs.map((value) => ({ name, value })))
      const url = `${f.scheme}://${f.host}${f.path}`
      const q = new URL(url).searchParams
      return {
        startedDateTime: f.time,
        time: f.duration_ms,
        request: {
          method: f.method, url, httpVersion: 'HTTP/1.1', cookies: [], headers: hdr(f.request_headers),
          queryString: [...q].map(([name, value]) => ({ name, value })), headersSize: -1, bodySize: 0,
        },
        response: {
          status: f.status, statusText: f.status === 206 ? 'Partial Content' : 'OK', httpVersion: 'HTTP/1.1', cookies: [],
          headers: hdr(f.response_headers), content: { size: f.bytes_out, mimeType: f.out_content_type || f.content_type },
          redirectURL: '', headersSize: -1, bodySize: f.bytes_out,
        },
        cache: {},
        timings: { send: 0, wait: f.duration_ms, receive: 0 },
        _midopress: { decision: f.decision, reason: f.reason, codec: f.codec, profile: f.profile },
      }
    })
    return { log: { version: '1.2', creator: { name: 'MidoPress mock', version: '0.1.0-mock' }, entries } }
  }

  // ------------------------------------------------------------ settings

  /** Returns a list of issues, empty when applied. */
  applySettings(body) {
    const issues = []
    if (!body || typeof body !== 'object' || Array.isArray(body)) {
      return [{ index: -1, field: '', column: 0, message: 'the body must be an object' }]
    }
    for (const [k, v] of Object.entries(body)) {
      if (!(k in this.settings)) issues.push({ index: -1, field: k, column: 0, message: `unknown setting "${k}"` })
      else if (typeof v !== 'boolean') issues.push({ index: -1, field: k, column: 0, message: `${k} must be true or false` })
    }
    if (issues.length) return issues
    const before = { ...this.settings }
    Object.assign(this.settings, body)
    if (!this.settings.flows) {
      this.flows = []
      this.flowIndex.clear()
      this.flowMeta.clear()
    }
    if (!this.settings.domains) this.domains.clear()
    if (!this.settings.capture) {
      for (const f of this.flows) f.has_original = f.has_result = false
    }
    if (JSON.stringify(before) !== JSON.stringify(this.settings)) {
      this.log('info', 'settings changed', Object.fromEntries(Object.entries(this.settings).map(([k, v]) => [k, String(v)])))
      this.emit('settings', { ...this.settings })
    }
    return []
  }

  // ------------------------------------------------------------ views

  clientView(c) {
    const now = Date.now()
    const dev = this.devices.get(c.device)
    return {
      key: c.key, device: c.device, addr: c.addr, user_agent: c.ua, first_seen: iso(c.first_seen), last_seen: iso(c.last_seen),
      active: !c.idle && now - c.last_seen < 120000, requests: c.requests, in_flight: c.idle ? 0 : c.in_flight,
      bytes_in: c.bytes_in, bytes_out: c.bytes_out, saved: c.bytes_in - c.bytes_out,
      profile: (dev && dev.profile) || this.config.defaultProfile, by_kind: JSON.parse(JSON.stringify(c.by_kind)),
    }
  }

  clientList() {
    return [...this.clients].sort((a, b) => b.last_seen - a.last_seen).map((c) => this.clientView(c))
  }

  deviceView(d) {
    const now = Date.now()
    return {
      id: d.id, profile: d.profile, created: iso(d.created), last_seen: d.last_seen ? iso(d.last_seen) : null,
      active: Boolean(d.last_seen) && now - d.last_seen < 120000, requests: d.requests, bytes_in: d.bytes_in,
      bytes_out: d.bytes_out, saved: d.bytes_in - d.bytes_out,
    }
  }

  removeDevice(id) {
    this.devices.delete(id)
    this.clients = this.clients.filter((c) => c.device !== id)
  }

  quantiles() {
    const s = [...this.lat].sort((a, b) => a - b)
    const q = (p) => (s.length ? s[Math.min(s.length - 1, Math.floor(p * s.length))] : 0)
    return { p50_ms: q(0.5), p95_ms: q(0.95), p99_ms: q(0.99), samples: s.length }
  }

  snapshot() {
    const now = Date.now()
    this.advance(now)
    const rb = (b) => ({ requests: Math.round(b.requests), transformed: Math.round(b.transformed), bytes_in: Math.round(b.bytes_in), bytes_out: Math.round(b.bytes_out) })
    const mapB = (o) => Object.fromEntries(Object.entries(o).map(([k, v]) => [k, rb(v)]))
    const t = this.totals
    const saved = t.bytes_in - t.bytes_out
    const c = this.cache
    const lookups = c.hits + c.misses
    const active = this.clients.filter((x) => !x.idle && now - x.last_seen < 120000).length
    return {
      time: iso(now),
      uptime_seconds: this.uptime(),
      version: '0.1.0-mock',
      totals: { ...rb(t), saved, saved_percent: t.bytes_in ? Number(((saved / t.bytes_in) * 100).toFixed(2)) : 0 },
      by_kind: mapB(this.byKind),
      by_codec: mapB(this.byCodec),
      by_profile: Object.fromEntries(Object.entries(this.byProfile).map(([k, v]) => [k, rb(v)]).filter(([, v]) => v.requests > 0)),
      by_decision: { ...this.byDecision },
      latency: this.quantiles(),
      cache: { ...c, hit_rate: lookups ? Number((c.hits / lookups).toFixed(4)) : 0 },
      connections: { clients_active: active, ...this.conn },
      cpu: { ...this.cpu },
      top_domains: this.settings.domains
        ? [...this.domains.entries()].sort((a, b) => b[1].requests - a[1].requests).slice(0, 15).map(([domain, b]) => ({ domain, ...b }))
        : null,
      series: { seconds: this.series.seconds.map((p) => ({ ...p })), minutes: this.series.minutes.map((p) => ({ ...p })) },
    }
  }

  metrics() {
    const t = this.totals
    const c = this.cache
    const lines = [
      '# HELP midopress_requests_total Responses handled.',
      '# TYPE midopress_requests_total counter',
      `midopress_requests_total ${t.requests}`,
      '# HELP midopress_transformed_total Responses changed.',
      '# TYPE midopress_transformed_total counter',
      `midopress_transformed_total ${t.transformed}`,
      '# TYPE midopress_bytes_in_total counter',
      `midopress_bytes_in_total ${t.bytes_in}`,
      '# TYPE midopress_bytes_out_total counter',
      `midopress_bytes_out_total ${t.bytes_out}`,
      '# TYPE midopress_cache_hits_total counter',
      `midopress_cache_hits_total ${c.hits}`,
      '# TYPE midopress_cache_misses_total counter',
      `midopress_cache_misses_total ${c.misses}`,
      '# TYPE midopress_uptime_seconds gauge',
      `midopress_uptime_seconds ${this.uptime()}`,
    ]
    for (const k of KINDS) lines.push(`midopress_requests_by_kind_total{kind="${k}"} ${this.byKind[k].requests}`)
    return lines.join('\n') + '\n'
  }

  // ------------------------------------------------------------ log

  log(level, message, attrs = {}) {
    if (LEVELS.indexOf(level) < LEVELS.indexOf(this.logLevel)) return
    const line = { time: new Date().toISOString(), level: level.toUpperCase(), message, attrs }
    this.logs.push(line)
    if (this.logs.length > 500) this.logs.shift()
    this.emit('log', line)
  }

  setLogLevel(level) {
    if (!LEVELS.includes(level)) return false
    this.logLevel = level
    this.log('info', 'log level changed', { level })
    return true
  }

  // ------------------------------------------------------------ services

  clearCache() {
    Object.assign(this.cache, { memory_bytes: 0, memory_items: 0, disk_bytes: 0, disk_items: 0 })
    this.log('info', 'cache cleared')
  }

  resetStats() {
    this.totals = bucket()
    this.byKind = Object.fromEntries(KINDS.map((k) => [k, bucket()]))
    this.byCodec = {}
    this.byProfile = {}
    this.byDecision = {}
    this.domains.clear()
    this.lat = []
    this.cache.hits = this.cache.misses = this.cache.evictions = this.cache.expired = 0
    this.initSeries(Date.now())
    this.log('info', 'statistics reset')
  }

  // ------------------------------------------------------------ the clock

  baseRate(up) {
    return 12 + 5 * Math.sin(up / 45) + 3 * Math.sin(up / 11)
  }

  tick() {
    const now = Date.now()
    this.advance(now)
    for (const b of Object.values(this.byProfile)) for (const k of Object.keys(b)) b[k] *= 0.97
    const rate = this.baseRate(this.uptime()) * this.speed
    const n = Math.max(0, Math.round(rate + (Math.random() - 0.5) * rate * 0.5))
    for (let i = 0; i < n; i++) this.record(this.genRequest())

    this.conn.requests_in_flight = Math.round(rate * 0.15 * (0.5 + Math.random()))
    this.conn.tunnels_active = clamp(this.conn.tunnels_active + rint(-2, 2), 2, 30)
    this.cpu.busy = clamp(Math.round(rate / 3 + Math.random() * 2), 0, this.cpu.slots)
    this.cpu.background_queue = Math.max(0, Math.round(rate * 0.6 * Math.random() - 3))
    for (const c of this.clients) c.in_flight = c.idle ? 0 : rint(0, 3)
    if (Math.random() < 0.3) this.cache.expired += rint(0, 3)

    if (Math.random() < 1 / 35) this.toggleClient(now)
    if (Math.random() < 0.3) {
      const [level, message, attrs] = pick(LOG_TEMPLATES)
      this.log(level, message, attrs())
    }
    this.emit('stats', this.snapshot())
  }

  toggleClient(now) {
    const c = pick(this.clients)
    if (c.idle) {
      c.idle = false
      c.last_seen = now
    } else if (this.clients.filter((x) => !x.idle).length > 1) c.idle = true
    else return
    this.emit('client', this.clientView(c))
  }
}
