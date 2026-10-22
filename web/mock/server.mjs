// Development mock of the MidoPress administration API (docs/09-admin-api.md,
// web/src/api/types.ts). Plain ESM, no dependencies.
//
//   node mock/server.mjs                  listens on 127.0.0.1:9998
//   PORT=9000 MOCK_TOKEN=secret node mock/server.mjs
//
// Everything under /api/v1 needs `Authorization: Bearer dev-token` (except
// /api/v1/health). When web/dist exists it is served too, with a fallback to
// index.html, so the mock can stand in for the real server.
//
// Simulation controls, no token needed:
//   POST /mock/reset                 back to the initial state (flow ids never repeat)
//   POST /mock/burst  {"requests":n} add n requests of traffic now (default 100, max 50000)
//   POST /mock/speed  {"factor":x}   scales the traffic rate (0 to 100, default 1)
//
// Differences from the real server: the by_profile counters decay (about 3 per
// cent per second) so that a change of profile shows within seconds;
// POST /service/shutdown answers 202 but does not stop the mock; the host
// names, devices and numbers are invented.

import http from 'node:http'
import { randomBytes, timingSafeEqual } from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { Sim, KINDS } from './sim.mjs'
import { parseFilter, FilterError } from './filter.mjs'
import { CA_PEM, CA_DER, CA_P12, caInfo } from './ca.mjs'
import { pictureSvg, qrPlaceholderSvg } from './svg.mjs'

const API = '/api/v1'
const DEVICE_ID = /^[A-Za-z0-9._-]{1,64}$/
const MIME = {
  '.html': 'text/html; charset=utf-8', '.js': 'text/javascript', '.mjs': 'text/javascript', '.css': 'text/css',
  '.json': 'application/json', '.svg': 'image/svg+xml', '.png': 'image/png', '.jpg': 'image/jpeg', '.ico': 'image/x-icon',
  '.woff2': 'font/woff2', '.woff': 'font/woff', '.txt': 'text/plain', '.map': 'application/json', '.webmanifest': 'application/manifest+json',
}

class HttpError extends Error {
  constructor(status, code, message, issues) {
    super(message)
    this.status = status
    this.code = code
    this.issues = issues
  }
}

const invalid = (issues, message = 'invalid request') => new HttpError(422, 'invalid', message, issues)
const notFound = (what = 'not found') => new HttpError(404, 'not_found', what)
const isObj = (x) => x && typeof x === 'object' && !Array.isArray(x)

function compile(pattern) {
  const names = []
  const re = pattern.replace(/:(\w+)/g, (_, n) => (names.push(n), '([^/]+)'))
  return { re: new RegExp(`^${re}$`), names }
}

export function startServer({
  port = 9998, host = '127.0.0.1', token = 'dev-token',
  distDir = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'dist'),
  tick = true,
  pingMs = 15000,
} = {}) {
  const streams = new Set()
  const broadcast = (event, data) => {
    const chunk = `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`
    for (const res of streams) res.write(chunk)
  }
  const sim = new Sim(broadcast)
  const cfg = sim.config
  const view = () => cfg.view({ flows: sim.settings.flows })
  const tokenBuf = Buffer.from(token)

  const authorized = (req) => {
    const m = /^Bearer (.+)$/.exec(req.headers.authorization || '')
    if (!m) return false
    const given = Buffer.from(m[1])
    return given.length === tokenBuf.length && timingSafeEqual(given, tokenBuf)
  }

  // ------------------------------------------------------------ handlers
  // A handler returns [status, body] (JSON), [204], or { raw: true, ... }.

  const raw = (body, type, extra = {}) => ({ raw: true, status: 200, body, type, ...extra })

  const flowOr404 = (id) => {
    const f = sim.flowIndex.get(id)
    if (!f) throw notFound(`no flow "${id}"`)
    return f
  }

  const flowBody = (id, transformed) => {
    const f = flowOr404(id)
    const meta = sim.flowMetaFor(f)
    if (!meta || !(transformed ? f.has_result : f.has_original)) throw notFound('the body was not kept')
    const scale = transformed ? 1.02 : 1
    const svg = pictureSvg({
      id: f.id, width: Math.round(meta.w * scale), height: Math.round(meta.h * scale),
      bytes: transformed ? f.bytes_out : f.bytes_in, codec: f.codec, transformed,
    })
    return raw(svg, 'image/svg+xml')
  }

  const flowCompare = (id) => {
    const f = flowOr404(id)
    const meta = sim.flowMetaFor(f)
    if (!meta || !f.has_original || !f.has_result) throw notFound('the bodies were not kept')
    const distance = f.measure >= 0.0004 && f.measure <= 0.012 ? f.measure : 0.0004 + (parseInt(id.slice(-3), 36) % 1000) / 1000 * 0.0116
    return [200, {
      metric: 'dssim', distance: Number(distance.toFixed(5)), original_bytes: f.bytes_in, result_bytes: f.bytes_out,
      width: Math.round(meta.w * 1.02), height: Math.round(meta.h * 1.02), comparable: true,
    }]
  }

  const listFlows = (q) => {
    const issues = []
    let ast = null
    const expr = q.get('filter')
    if (expr) {
      try {
        ast = parseFilter(expr)
      } catch (e) {
        if (!(e instanceof FilterError)) throw e
        issues.push({ index: -1, field: 'filter', column: e.column, message: e.message })
      }
    }
    const kind = q.get('kind') || ''
    if (kind && !KINDS.includes(kind)) issues.push({ index: -1, field: 'kind', column: 0, message: 'kind must be image, text, font or other' })
    let limit = 500
    if (q.get('limit') !== null && q.get('limit') !== '') {
      limit = Number(q.get('limit'))
      if (!Number.isInteger(limit) || limit < 1 || limit > 500) {
        issues.push({ index: -1, field: 'limit', column: 0, message: 'limit must be an integer between 1 and 500' })
      }
    }
    if (issues.length) throw invalid(issues)
    return [200, sim.listFlows({ ast, kind, limit })]
  }

  const configChange = (issues) => {
    if (issues.length) throw invalid(issues)
    return [200, view()]
  }

  const checkDeviceProfile = (profile) => {
    if (profile !== undefined && profile !== '' && !cfg.resolve(profile)) {
      throw invalid([{ index: -1, field: 'profile', column: 0, message: `unknown profile "${profile}"` }])
    }
  }

  const requestHost = (req) => (req.headers.host || 'midopress.example').replace(/:\d+$/, '')

  const routes = []
  const route = (method, pattern, handler) => routes.push({ method, ...compile(API + pattern), handler })

  route('GET', '/health', () => [200, { ok: true }])
  route('GET', '/about', () => [200, {
    version: '0.1.0-mock', go_version: 'go1.24 (mock)', os: 'linux', arch: 'amd64', started: new Date(sim.startedAt).toISOString(),
    uptime_seconds: sim.uptime(), backend: 'native', backends: ['native', 'wasm'],
    native: { avif: true, webp: true, jxl: true, dssim: true, brotli: true, zstd: true },
    modules: [{ path: 'github.com/example/mock-codec', version: 'v1.4.2' }, { path: 'golang.org/x/net', version: 'v0.30.0' }, { path: 'github.com/klauspost/compress', version: 'v1.17.11' }],
    state_dir: '/home/user/.midopress', proxy_addr: '127.0.0.1:9999', tls: true, mitm: true, auth_mode: 'token',
  }])
  route('GET', '/stats', () => [200, sim.snapshot()])
  route('GET', '/settings', () => [200, { ...sim.settings }])
  route('PUT', '/settings', ({ body }) => {
    const issues = sim.applySettings(body)
    if (issues.length) throw invalid(issues)
    return [200, { ...sim.settings }]
  })

  route('GET', '/flows', ({ url }) => listFlows(url.searchParams))
  route('DELETE', '/flows', () => {
    sim.flows = []
    sim.flowIndex.clear()
    sim.flowMeta.clear()
    return [204]
  })
  route('GET', '/flows.har', () => raw(JSON.stringify(sim.har()), 'application/json', { headers: { 'Content-Disposition': 'attachment; filename="midopress.har"' } }))
  route('GET', '/flows/:id', ({ params }) => [200, flowOr404(params.id)])
  route('GET', '/flows/:id/original', ({ params }) => flowBody(params.id, false))
  route('GET', '/flows/:id/transformed', ({ params }) => flowBody(params.id, true))
  route('GET', '/flows/:id/compare', ({ params }) => flowCompare(params.id))

  route('GET', '/config', () => [200, view()])
  route('PUT', '/config/rules', ({ body }) => configChange(cfg.putRules(body)))
  route('PUT', '/config/profiles/:name', ({ params, body }) => configChange(cfg.putProfile(params.name, body)))
  route('DELETE', '/config/profiles/:name', ({ params }) => {
    const name = params.name
    if (cfg.custom[name] && [...sim.devices.values()].some((d) => d.profile === name)) {
      throw new HttpError(409, 'conflict', `a device uses the profile "${name}"`)
    }
    const refused = cfg.deleteProfile(name)
    if (refused) throw new HttpError(refused.status, refused.status === 404 ? 'not_found' : 'conflict', refused.message)
    return [200, view()]
  })
  route('PUT', '/config/default-profile', ({ body }) => configChange(cfg.setDefault(isObj(body) ? body.profile : undefined)))
  route('DELETE', '/config/ui', () => {
    cfg.forgetUi()
    return [200, view()]
  })
  route('POST', '/config/validate', ({ body }) => {
    const issues = cfg.validateRules(isObj(body) ? body.rules : undefined)
    return [200, { ok: issues.length === 0, issues }]
  })
  route('POST', '/config/test', ({ body }) => {
    const r = cfg.test(body)
    if (r.issues) throw invalid(r.issues)
    return [200, r.result]
  })

  route('GET', '/devices', () => [200, [...sim.devices.values()].map((d) => sim.deviceView(d))])
  route('POST', '/devices', ({ body, req }) => {
    const id = isObj(body) ? body.id : undefined
    if (typeof id !== 'string' || !DEVICE_ID.test(id)) {
      throw invalid([{ index: -1, field: 'id', column: 0, message: 'id must be 1 to 64 characters: letters, digits, dot, underscore and dash' }])
    }
    checkDeviceProfile(body.profile)
    if (sim.devices.has(id)) throw new HttpError(409, 'conflict', `the device "${id}" already exists`)
    const profile = body.profile || ''
    const tok = randomBytes(32).toString('hex')
    sim.devices.set(id, { id, profile, created: Date.now(), last_seen: null, requests: 0, bytes_in: 0, bytes_out: 0 })
    sim.log('info', 'device created', { device: id })
    const configUrl = `midopress://config?host=${encodeURIComponent(requestHost(req))}&port=9999&user=${encodeURIComponent(id)}&token=${tok}&tls=1`
    return [201, { id, profile, token: tok, config_url: configUrl, config_qr_svg: qrPlaceholderSvg(configUrl) }]
  })
  route('PATCH', '/devices/:id', ({ params, body }) => {
    const d = sim.devices.get(params.id)
    if (!d) throw notFound(`no device "${params.id}"`)
    if (!isObj(body) || typeof body.profile !== 'string') {
      throw invalid([{ index: -1, field: 'profile', column: 0, message: 'profile is required (empty for the default)' }])
    }
    checkDeviceProfile(body.profile)
    d.profile = body.profile
    return [200, sim.deviceView(d)]
  })
  route('DELETE', '/devices/:id', ({ params }) => {
    if (!sim.devices.has(params.id)) throw notFound(`no device "${params.id}"`)
    sim.removeDevice(params.id)
    sim.log('info', 'device removed', { device: params.id })
    return [204]
  })
  route('GET', '/clients', () => [200, sim.clientList()])

  route('GET', '/ca', () => [200, caInfo()])
  route('GET', '/ca.pem', () => raw(CA_PEM, 'application/x-pem-file', { headers: { 'Content-Disposition': 'attachment; filename="midopress-ca.pem"' } }))
  route('GET', '/ca.der', () => raw(CA_DER, 'application/x-x509-ca-cert', { headers: { 'Content-Disposition': 'attachment; filename="midopress-ca.der"' } }))
  route('GET', '/ca.p12', () => raw(CA_P12, 'application/x-pkcs12', { headers: { 'Content-Disposition': 'attachment; filename="midopress-ca.p12"' } }))

  route('GET', '/log', () => [200, { level: sim.logLevel, lines: sim.logs }])
  route('PUT', '/log', ({ body }) => {
    if (!isObj(body) || !sim.setLogLevel(body.level)) {
      throw invalid([{ index: -1, field: 'level', column: 0, message: 'level must be debug, info, warn or error' }])
    }
    return [200, { level: sim.logLevel, lines: sim.logs }]
  })

  route('POST', '/service/reload', () => (sim.log('info', 'configuration reloaded'), [204]))
  route('POST', '/service/cache/clear', () => (sim.clearCache(), [204]))
  route('POST', '/service/stats/reset', () => (sim.resetStats(), [204]))
  route('POST', '/service/shutdown', () => (sim.log('warn', 'shutdown requested (ignored by the mock)'), [202]))
  route('GET', '/metrics', () => raw(sim.metrics(), 'text/plain; version=0.0.4; charset=utf-8'))

  const events = (req, res) => {
    res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-store', Connection: 'keep-alive', 'X-Accel-Buffering': 'no' })
    res.write(': connected\n\n')
    res.write(`event: stats\ndata: ${JSON.stringify(sim.snapshot())}\n\n`)
    streams.add(res)
    req.on('close', () => streams.delete(res))
    return { handled: true }
  }

  // ------------------------------------------------------------ plumbing

  const send = (res, status, body, type = 'application/json; charset=utf-8', headers = {}) => {
    const buf = body === undefined ? null : Buffer.isBuffer(body) ? body : Buffer.from(typeof body === 'string' ? body : JSON.stringify(body))
    res.writeHead(status, { 'Cache-Control': 'no-store', ...(buf ? { 'Content-Type': type, 'Content-Length': buf.length } : {}), ...headers })
    res.end(buf)
  }
  const sendError = (res, status, code, message, issues) => send(res, status, issues ? { error: message, code, issues } : { error: message, code })

  const readBody = (req) => new Promise((resolve, reject) => {
    const chunks = []
    let size = 0
    req.on('data', (c) => {
      size += c.length
      if (size > 1 << 20) reject(new HttpError(413, 'too_large', 'body too large'))
      else chunks.push(c)
    })
    req.on('end', () => {
      const text = Buffer.concat(chunks).toString('utf8')
      if (!text.trim()) return resolve(undefined)
      try {
        resolve(JSON.parse(text))
      } catch {
        reject(new HttpError(400, 'bad_request', 'the body is not valid JSON'))
      }
    })
    req.on('error', reject)
  })

  const mockControl = async (req, res, pathname) => {
    if (req.method !== 'POST') return sendError(res, 405, 'method_not_allowed', 'use POST')
    const body = (await readBody(req)) || {}
    if (pathname === '/mock/reset') {
      sim.reset()
      broadcast('settings', { ...sim.settings })
      return send(res, 200, { ok: true })
    }
    if (pathname === '/mock/burst') {
      const n = body.requests === undefined ? 100 : Number(body.requests)
      if (!Number.isFinite(n) || n < 0) return sendError(res, 422, 'invalid', 'requests must be a positive number')
      return send(res, 200, { ok: true, requests: sim.burst(n) })
    }
    if (pathname === '/mock/speed') {
      const f = Number(body.factor)
      if (!Number.isFinite(f) || f < 0 || f > 100) return sendError(res, 422, 'invalid', 'factor must be between 0 and 100')
      sim.speed = f
      return send(res, 200, { ok: true, factor: f })
    }
    return sendError(res, 404, 'not_found', 'unknown mock control')
  }

  const serveStatic = (req, res, pathname) => {
    if (!fs.existsSync(path.join(distDir, 'index.html'))) return sendError(res, 404, 'not_found', 'not found')
    let file = path.normalize(path.join(distDir, decodeURIComponent(pathname)))
    if (!file.startsWith(distDir)) return sendError(res, 404, 'not_found', 'not found')
    if (!fs.existsSync(file) || fs.statSync(file).isDirectory()) {
      if (path.extname(pathname)) return sendError(res, 404, 'not_found', 'not found')
      file = path.join(distDir, 'index.html')
    }
    const type = MIME[path.extname(file)] || 'application/octet-stream'
    res.writeHead(200, { 'Content-Type': type, 'Cache-Control': file.endsWith('index.html') ? 'no-cache' : 'public, max-age=3600' })
    fs.createReadStream(file).pipe(res)
  }

  const server = http.createServer(async (req, res) => {
    try {
      const url = new URL(req.url, 'http://mock')
      const pathname = url.pathname
      if (pathname.startsWith('/mock/')) return await mockControl(req, res, pathname)
      if (pathname !== API && !pathname.startsWith(API + '/')) return serveStatic(req, res, pathname)

      if (pathname !== `${API}/health` && !authorized(req)) {
        return sendError(res, 401, 'unauthorized', 'missing or invalid admin token')
      }
      if (pathname === `${API}/events`) {
        if (req.method !== 'GET') return sendError(res, 405, 'method_not_allowed', 'use GET')
        return events(req, res)
      }
      let allowed = false
      for (const r of routes) {
        const m = r.re.exec(pathname)
        if (!m) continue
        allowed = true
        if (r.method !== req.method) continue
        const params = Object.fromEntries(r.names.map((n, i) => [n, decodeURIComponent(m[i + 1])]))
        const body = ['POST', 'PUT', 'PATCH'].includes(req.method) ? await readBody(req) : undefined
        const out = r.handler({ req, url, params, body })
        if (out.raw) return send(res, out.status, out.body, out.type, out.headers)
        const [status, payload] = out
        return send(res, status, payload)
      }
      if (allowed) return sendError(res, 405, 'method_not_allowed', `${req.method} is not allowed here`)
      return sendError(res, 404, 'not_found', `no such endpoint: ${pathname}`)
    } catch (e) {
      if (e instanceof HttpError) return sendError(res, e.status, e.code, e.message, e.issues)
      console.error(e)
      return sendError(res, 500, 'internal', 'internal error of the mock')
    }
  })

  const timers = []
  if (tick) timers.push(setInterval(() => sim.tick(), 1000))
  timers.push(setInterval(() => { for (const res of streams) res.write(': ping\n\n') }, pingMs))

  return new Promise((resolve) => {
    server.listen(port, host, () => {
      resolve({
        server, sim, port: server.address().port, token,
        close: () => new Promise((done) => {
          timers.forEach(clearInterval)
          for (const res of streams) res.end()
          streams.clear()
          server.close(() => done())
          server.closeAllConnections()
        }),
      })
    })
  })
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const port = Number(process.env.PORT) || 9998
  const token = process.env.MOCK_TOKEN || 'dev-token'
  const s = await startServer({ port, token })
  console.log(`MidoPress mock listening on http://127.0.0.1:${s.port} (token: ${token})`)
}
