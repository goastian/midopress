// node --test mock/
import { test, before, after } from 'node:test'
import assert from 'node:assert/strict'
import http from 'node:http'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { startServer } from './server.mjs'
import * as S from './schemas.mjs'
import { check } from './schemas.mjs'

let srv
let base
const TOKEN = 'test-token'

before(async () => {
  srv = await startServer({ port: 0, token: TOKEN, pingMs: 400, distDir: path.join(os.tmpdir(), 'midopress-no-dist') })
  base = `http://127.0.0.1:${srv.port}`
})
after(async () => {
  await srv.close()
})

async function call(method, url, body, { auth = true } = {}) {
  const headers = {}
  if (auth) headers.Authorization = `Bearer ${TOKEN}`
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  const res = await fetch(base + url, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) })
  const text = await res.text()
  let json
  try {
    json = text ? JSON.parse(text) : undefined
  } catch {
    json = undefined
  }
  return { status: res.status, headers: res.headers, text, json }
}
const get = (u, o) => call('GET', u, undefined, o)
const api = (u) => `/api/v1${u}`

function shape(value, schema) {
  const problems = check(value, schema)
  assert.deepEqual(problems.slice(0, 5), [], 'shape mismatch')
}
function ok(r, status, schema) {
  assert.equal(r.status, status, `${r.status}: ${r.text.slice(0, 300)}`)
  if (schema) shape(r.json, schema)
  return r.json
}
function invalid(r, field, column) {
  assert.equal(r.status, 422, r.text)
  shape(r.json, S.ApiError)
  assert.equal(r.json.code, 'invalid')
  const issue = r.json.issues.find((i) => i.field === field)
  assert.ok(issue, `no issue for ${field}: ${r.text}`)
  if (column !== undefined) assert.equal(issue.column, column, r.text)
  return issue
}

async function fresh() {
  await call('POST', '/mock/reset', {}, { auth: false })
  await call('POST', '/mock/speed', { factor: 0 }, { auth: false })
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

// ---------------------------------------------------------------- access

test('every endpoint of the API refuses a missing or wrong token', async () => {
  const list = [
    ['GET', '/about'], ['GET', '/stats'], ['GET', '/events'], ['GET', '/flows'], ['GET', '/flows/x'], ['GET', '/flows.har'],
    ['GET', '/settings'], ['PUT', '/settings'], ['DELETE', '/flows'], ['GET', '/config'], ['PUT', '/config/rules'],
    ['PUT', '/config/profiles/x'], ['DELETE', '/config/profiles/x'], ['PUT', '/config/default-profile'], ['DELETE', '/config/ui'],
    ['POST', '/config/validate'], ['POST', '/config/test'], ['GET', '/devices'], ['POST', '/devices'], ['PATCH', '/devices/x'],
    ['DELETE', '/devices/x'], ['GET', '/clients'], ['GET', '/ca'], ['GET', '/ca.pem'], ['GET', '/ca.der'], ['GET', '/ca.p12'],
    ['GET', '/log'], ['PUT', '/log'], ['GET', '/metrics'], ['POST', '/service/reload'], ['POST', '/service/cache/clear'],
    ['POST', '/service/stats/reset'], ['POST', '/service/shutdown'],
  ]
  for (const [m, p] of list) {
    const r = await call(m, api(p), m === 'GET' || m === 'DELETE' ? undefined : {}, { auth: false })
    assert.equal(r.status, 401, `${m} ${p}`)
    shape(r.json, S.ApiError)
    assert.equal(r.json.code, 'unauthorized')
  }
  const r = await fetch(base + api('/stats'), { headers: { Authorization: 'Bearer wrong' } })
  assert.equal(r.status, 401)
  assert.equal(r.headers.get('cache-control'), 'no-store')
})

test('health needs no token; unknown paths and methods', async () => {
  ok(await get(api('/health'), { auth: false }), 200)
  const r = await get(api('/nothing'))
  assert.equal(r.status, 404)
  assert.equal(r.json.code, 'not_found')
  assert.equal((await call('POST', api('/stats'), {})).status, 405)
  const bad = await fetch(base + api('/settings'), { method: 'PUT', headers: { Authorization: `Bearer ${TOKEN}` }, body: '{oops' })
  assert.equal(bad.status, 400)
})

// ---------------------------------------------------------------- reading

test('stats: shape, series lengths and domains off', async () => {
  await fresh()
  const s = ok(await get(api('/stats')), 200, S.StatsSnapshot)
  assert.equal(s.series.seconds.length, 120)
  assert.equal(s.series.minutes.length, 180)
  s.series.seconds.forEach((p, i, a) => i && assert.equal(p.t, a[i - 1].t + 1))
  s.series.minutes.forEach((p, i, a) => i && assert.equal(p.t, a[i - 1].t + 60))
  assert.equal(s.top_domains, null)
  assert.equal(s.totals.requests, 0)
})

test('about, settings, ca, log, metrics, clients', async () => {
  ok(await get(api('/about')), 200, S.About)
  ok(await get(api('/settings')), 200, S.Settings)
  ok(await get(api('/ca')), 200, S.CAInfo)
  ok(await get(api('/log')), 200, S.LogState)
  const clients = ok(await get(api('/clients')), 200)
  shape(clients, { t: 'arr', s: S.Client })
  assert.ok(clients.length >= 4 && clients.length <= 6)
  assert.ok(clients.some((c) => c.device) && clients.some((c) => !c.device) && clients.some((c) => !c.active))
  const m = await get(api('/metrics'))
  assert.equal(m.status, 200)
  assert.match(m.headers.get('content-type'), /text\/plain/)
  assert.match(m.text, /^midopress_requests_total \d+$/m)
})

test('certificate downloads', async () => {
  const pem = await get(api('/ca.pem'))
  assert.equal(pem.status, 200)
  assert.match(pem.text, /^-----BEGIN CERTIFICATE-----/)
  const der = await fetch(base + api('/ca.der'), { headers: { Authorization: `Bearer ${TOKEN}` } })
  assert.equal(der.status, 200)
  assert.equal(der.headers.get('content-type'), 'application/x-x509-ca-cert')
  assert.ok((await der.arrayBuffer()).byteLength > 200)
  const p12 = await fetch(base + api('/ca.p12'), { headers: { Authorization: `Bearer ${TOKEN}` } })
  assert.equal(p12.headers.get('content-type'), 'application/x-pkcs12')
  assert.ok((await p12.arrayBuffer()).byteLength > 100)
})

test('log level and service actions', async () => {
  const st = ok(await call('PUT', api('/log'), { level: 'debug' }), 200, S.LogState)
  assert.equal(st.level, 'debug')
  invalid(await call('PUT', api('/log'), { level: 'loud' }), 'level')
  await call('PUT', api('/log'), { level: 'info' })
  assert.ok(ok(await get(api('/log')), 200).lines.length <= 500)

  await call('POST', '/mock/burst', { requests: 50 }, { auth: false })
  assert.equal((await call('POST', api('/service/reload'))).status, 204)
  assert.equal((await call('POST', api('/service/cache/clear'))).status, 204)
  assert.equal(ok(await get(api('/stats')), 200).cache.memory_bytes, 0)
  assert.equal((await call('POST', api('/service/stats/reset'))).status, 204)
  assert.equal(ok(await get(api('/stats')), 200).totals.requests, 0)
  assert.equal((await call('POST', api('/service/shutdown'))).status, 202)
})

// ---------------------------------------------------------------- events

test('events: a real event stream with stats, flow and settings events', async () => {
  await fresh()
  await call('POST', '/mock/speed', { factor: 1 }, { auth: false })
  const seen = { stats: [], flow: 0, settings: 0, ping: 0, type: '' }
  const req = http.get(base + api('/events'), { headers: { Authorization: `Bearer ${TOKEN}` } })
  const res = await new Promise((resolve) => req.on('response', resolve))
  seen.type = res.headers['content-type']
  let buf = ''
  res.setEncoding('utf8')
  res.on('data', (d) => {
    buf += d
    let i
    while ((i = buf.indexOf('\n\n')) >= 0) {
      const block = buf.slice(0, i)
      buf = buf.slice(i + 2)
      if (block.startsWith(':')) {
        if (block.includes('ping')) seen.ping++
        continue
      }
      const m = /^event: (\w+)\ndata: (.*)$/s.exec(block)
      assert.ok(m, `bad event block: ${block.slice(0, 80)}`)
      const data = JSON.parse(m[2])
      if (m[1] === 'stats') seen.stats.push(data)
      else if (m[1] === 'flow') {
        shape(data, S.Flow)
        seen.flow++
      } else if (m[1] === 'settings') {
        shape(data, S.Settings)
        seen.settings++
      }
    }
  })
  await sleep(500)
  await call('PUT', api('/settings'), { flows: true })
  await sleep(2700)
  req.destroy()
  assert.match(seen.type, /^text\/event-stream/)
  assert.ok(seen.stats.length >= 3, `stats events: ${seen.stats.length}`)
  for (const s of seen.stats) {
    shape(s, S.StatsSnapshot)
    assert.equal(s.series.seconds.length, 120)
    assert.equal(s.series.minutes.length, 180)
  }
  assert.ok(seen.flow > 0, 'flow events while flows are on')
  assert.equal(seen.settings, 1)
  assert.ok(seen.ping >= 1, 'ping comments')
  await fresh()
})

// ---------------------------------------------------------------- flows

test('flows are off by default and PUT /settings turns them on and off', async () => {
  await fresh()
  const off = ok(await get(api('/flows')), 200, S.FlowList)
  assert.equal(off.enabled, false)
  assert.deepEqual(off.flows, [])
  await call('POST', '/mock/burst', { requests: 20 }, { auth: false })
  assert.deepEqual(ok(await get(api('/flows')), 200).flows, [])

  const s = ok(await call('PUT', api('/settings'), { flows: true }), 200, S.Settings)
  assert.equal(s.flows, true)
  await call('POST', '/mock/burst', { requests: 40 }, { auth: false })
  const on = ok(await get(api('/flows')), 200, S.FlowList)
  assert.equal(on.enabled, true)
  assert.equal(on.capacity, 500)
  assert.equal(on.flows.length, 40)
  assert.ok(Date.parse(on.flows[0].time) >= Date.parse(on.flows[39].time), 'newest first')

  invalid(await call('PUT', api('/settings'), { flows: 'yes' }), 'flows')
  invalid(await call('PUT', api('/settings'), { nothing: true }), 'nothing')
  assert.equal((await call('DELETE', api('/flows'))).status, 204)
  assert.equal(ok(await get(api('/flows')), 200).flows.length, 0)
  await call('POST', '/mock/burst', { requests: 10 }, { auth: false })
  await call('PUT', api('/settings'), { flows: false })
  const gone = ok(await get(api('/flows')), 200)
  assert.equal(gone.enabled, false)
  assert.equal(gone.flows.length, 0)
})

test('flows: capacity, unique ids, hidden header values, single flow', async () => {
  await fresh()
  await call('PUT', api('/settings'), { flows: true })
  await call('POST', '/mock/burst', { requests: 700 }, { auth: false })
  const l = ok(await get(api('/flows')), 200, S.FlowList)
  assert.equal(l.flows.length, 500)
  const first = l.flows[0].id
  assert.equal(new Set(l.flows.map((f) => f.id)).size, 500)
  for (const f of l.flows) {
    for (const h of ['Cookie', 'Authorization', 'Set-Cookie']) {
      for (const v of [...(f.request_headers[h] || []), ...(f.response_headers[h] || [])]) assert.equal(v, '<hidden>')
    }
  }
  assert.deepEqual(ok(await get(api(`/flows/${first}`)), 200, S.Flow).id, first)
  assert.equal((await get(api('/flows/does-not-exist'))).status, 404)

  await call('DELETE', api('/flows'))
  await call('POST', '/mock/burst', { requests: 5 }, { auth: false })
  const after = ok(await get(api('/flows')), 200).flows
  assert.ok(after.every((f) => f.id !== first), 'ids never repeat')
  const kinds = new Set(l.flows.map((f) => f.kind))
  assert.ok(kinds.has('image') && kinds.has('text'))
  assert.ok(l.flows.some((f) => f.decision === 'transformed') && l.flows.some((f) => f.decision === 'passthrough'))
})

test('flows: filter language, kind and limit', async () => {
  await fresh()
  await call('PUT', api('/settings'), { flows: true })
  await call('POST', '/mock/burst', { requests: 400 }, { auth: false })
  const q = async (filter, extra = '') => {
    const r = await get(api(`/flows?filter=${encodeURIComponent(filter)}${extra}`))
    return ok(r, 200, S.FlowList)
  }
  const all = (await q('')).total
  assert.equal(all, 400)

  const images = await q('~t image/')
  assert.ok(images.flows.length > 0 && images.flows.every((f) => f.content_type.startsWith('image/')))
  const notImages = await q('!~t image/')
  assert.equal(images.total + notImages.total, all)
  assert.equal((await q('~t image/ | !~t image/')).total, all)
  assert.equal((await q('~t image/ & !~t image/')).total, 0)
  assert.equal((await q('~all')).total, all)
  assert.equal((await q('~none')).total, 0)

  const host = images.flows[0].host
  const byHost = await q(`~d ${host.replace(/\./g, '\\.')} & ~t image/`)
  assert.ok(byHost.flows.length > 0 && byHost.flows.every((f) => f.host === host && f.kind === 'image'))
  const paren = await q(`(~d ${host.replace(/\./g, '\\.')} | ~c 206) & ~t image/`)
  assert.ok(paren.total >= byHost.total)
  assert.ok(paren.flows.every((f) => f.content_type.startsWith('image/') && (f.host === host || f.status === 206)))

  assert.equal((await q('~c 200')).flows.every((f) => f.status === 200), true)
  assert.equal((await q('~m GET')).total + (await q('!~m GET')).total, all)
  assert.equal((await q('~u "\\.jpg(\\?|$)"')).flows.every((f) => /\.jpg(\?|$)/i.test(f.path)), true)
  assert.ok((await q('~hq "Accept-Encoding: .*br"')).total === all)
  assert.ok((await q('~src 192.168.1.0/24')).flows.every((f) => f.client.addr.startsWith('192.168.1.')))

  const kind = ok(await get(api('/flows?kind=text')), 200, S.FlowList)
  assert.ok(kind.flows.length > 0 && kind.flows.every((f) => f.kind === 'text'))
  const limited = await q('~t image/', '&limit=7')
  assert.equal(limited.flows.length, 7)
  assert.equal(limited.total, images.total)
  invalid(await get(api('/flows?kind=video')), 'kind')
  invalid(await get(api('/flows?limit=9999')), 'limit')

  invalid(await get(api('/flows?filter=' + encodeURIComponent('~d (a'))), 'filter', 4)
  invalid(await get(api('/flows?filter=' + encodeURIComponent('~t image/ & '))), 'filter', 13)
  invalid(await get(api('/flows?filter=' + encodeURIComponent('~t image/ & ~zz x'))), 'filter', 13)
  invalid(await get(api('/flows?filter=' + encodeURIComponent('~c abc'))), 'filter', 4)
})

test('flows: bodies, compare and HAR', async () => {
  await fresh()
  await call('PUT', api('/settings'), { flows: true })
  await call('POST', '/mock/burst', { requests: 100 }, { auth: false })
  const nocap = ok(await get(api('/flows?kind=image')), 200).flows[0]
  assert.equal(nocap.has_original, false)
  assert.equal((await get(api(`/flows/${nocap.id}/original`))).status, 404)
  assert.equal((await get(api(`/flows/${nocap.id}/compare`))).status, 404)

  await call('DELETE', api('/flows'))
  await call('PUT', api('/settings'), { capture: true })
  await call('POST', '/mock/burst', { requests: 200 }, { auth: false })
  const list = ok(await get(api('/flows?kind=image')), 200).flows
  const withBodies = list.find((f) => f.has_original && f.has_result)
  assert.ok(withBodies, 'an image flow with both bodies')
  const o = await get(api(`/flows/${withBodies.id}/original`))
  const t = await get(api(`/flows/${withBodies.id}/transformed`))
  for (const r of [o, t]) {
    assert.equal(r.status, 200)
    assert.equal(r.headers.get('content-type'), 'image/svg+xml')
    assert.match(r.text, /^<svg /)
  }
  assert.notEqual(o.text, t.text)
  assert.match(o.text, /ORIGINAL/)
  assert.match(t.text, /TRANSFORMED/)
  const cmp = ok(await get(api(`/flows/${withBodies.id}/compare`)), 200, S.FlowCompare)
  assert.ok(cmp.distance >= 0.0004 && cmp.distance <= 0.012, String(cmp.distance))
  assert.ok(cmp.width > 0 && cmp.height > 0)

  const passOnly = list.find((f) => f.has_original && !f.has_result)
  if (passOnly) assert.equal((await get(api(`/flows/${passOnly.id}/transformed`))).status, 404)

  const har = ok(await get(api('/flows.har')), 200)
  const flows = ok(await get(api('/flows')), 200).flows
  assert.equal(har.log.version, '1.2')
  assert.equal(har.log.entries.length, flows.length)
  const e = har.log.entries[0]
  for (const k of ['startedDateTime', 'time', 'request', 'response', 'cache', 'timings']) assert.ok(k in e, k)
  assert.ok(e.request.url.startsWith('http') && Array.isArray(e.request.headers) && e.response.content.mimeType)
})

// ---------------------------------------------------------------- domains and simulation

test('top domains appear with the setting and add up', async () => {
  await fresh()
  ok(await call('PUT', api('/settings'), { domains: true }), 200)
  await call('POST', '/mock/burst', { requests: 600 }, { auth: false })
  const s = ok(await get(api('/stats')), 200, S.StatsSnapshot)
  assert.ok(s.top_domains.length >= 10 && s.top_domains.length <= 15, String(s.top_domains.length))
  const sum = s.top_domains.reduce((a, d) => a + d.requests, 0)
  assert.ok(sum <= s.totals.requests && sum > s.totals.requests * 0.7)
  assert.ok(s.top_domains.every((d, i, a) => i === 0 || a[i - 1].requests >= d.requests))
  assert.equal(s.totals.requests, 600)
  assert.equal(s.series.seconds.reduce((a, p) => a + p.requests, 0), 600)
  assert.ok(s.totals.saved > 0 && s.latency.samples > 0)
  assert.equal(s.by_kind.image.requests + s.by_kind.text.requests + s.by_kind.font.requests + s.by_kind.media.requests + s.by_kind.other.requests, 600)
  await call('PUT', api('/settings'), { domains: false })
  assert.equal(ok(await get(api('/stats')), 200).top_domains, null)
})

test('which hosts the bytes that were not saved came from', async () => {
  await fresh()
  await call('PUT', api('/settings'), { domains: true })
  await call('POST', '/mock/burst', { requests: 800 }, { auth: false })
  const s = ok(await get(api('/stats')), 200, S.StatsSnapshot)
  assert.ok(s.by_reason.length > 3)
  assert.ok(s.by_reason.every((r, i, a) => i === 0 || a[i - 1].bytes_in >= r.bytes_in))
  assert.ok(s.top_missed.length > 0 && s.top_missed.length <= 10)
  assert.ok(s.top_missed.every((m, i, a) => i === 0 || a[i - 1].missed_bytes >= m.missed_bytes))
  const reason = s.by_reason.find((r) => r.reason !== 'transformed').reason
  const m = ok(await get(api(`/stats/missed?reason=${reason}`)), 200, S.MissedByReason)
  assert.equal(m.enabled, true)
  assert.ok(m.hosts.length > 0)
  assert.ok(m.hosts.every((h) => h.kinds.every((k) => k.reason === reason)))
  ok(await get(api('/stats/missed')), 422)
  await call('PUT', api('/settings'), { domains: false })
  const off = ok(await get(api(`/stats/missed?reason=${reason}`)), 200, S.MissedByReason)
  assert.deepEqual([off.enabled, off.hosts], [false, []])
  assert.equal(ok(await get(api('/stats')), 200).top_missed, null)
})

test('mock controls: reset, burst, speed', async () => {
  await fresh()
  const b = ok(await call('POST', '/mock/burst', { requests: 30 }, { auth: false }), 200)
  assert.equal(b.requests, 30)
  assert.equal(ok(await get(api('/stats')), 200).totals.requests, 30)
  ok(await call('POST', '/mock/reset', {}, { auth: false }), 200)
  assert.equal(ok(await get(api('/stats')), 200).totals.requests, 0)
  assert.equal((await call('POST', '/mock/speed', { factor: -1 }, { auth: false })).status, 422)
  assert.equal((await call('POST', '/mock/speed', { factor: 3 }, { auth: false })).status, 200)
  await call('POST', '/mock/speed', { factor: 0 }, { auth: false })
})

test('the simulation moves on its own and a change of default profile shows in by_profile', async () => {
  await fresh()
  await call('POST', '/mock/speed', { factor: 10 }, { auth: false })
  ok(await call('PUT', api('/config/default-profile'), { profile: 'extreme' }), 200, S.ConfigView)
  await sleep(2500)
  const s = ok(await get(api('/stats')), 200)
  assert.ok(s.totals.requests > 100, String(s.totals.requests))
  const bp = s.by_profile
  assert.ok(bp.extreme.requests > (bp.balanced?.requests || 0), JSON.stringify(Object.keys(bp)))
  assert.ok(s.latency.p50_ms > 0 && s.latency.p95_ms >= s.latency.p50_ms && s.latency.p99_ms >= s.latency.p95_ms)
  const rec = s.series.seconds.filter((p) => p.requests > 0)
  assert.ok(rec.length >= 1)
  await fresh()
})

// ---------------------------------------------------------------- config

test('config view: built-in profiles filled in, custom profile, rules', async () => {
  await fresh()
  const c = ok(await get(api('/config')), 200, S.ConfigView)
  const by = Object.fromEntries(c.profiles.map((p) => [p.name, p]))
  for (const n of ['lossless', 'balanced', 'saver', 'extreme']) assert.ok(by[n].builtin)
  assert.equal(by.balanced.target_dssim, 0.002)
  assert.equal(by.saver.target_dssim, 0.006)
  assert.equal(by.extreme.target_dssim, 0.015)
  assert.equal(by.lossless.lossless, true)
  assert.equal(by.mobile.builtin, false)
  assert.equal(c.default_profile, 'balanced')
  assert.equal(c.rules.length, 2)
  assert.equal(c.config.web.admin_token, '<redacted>')
})

test('PUT /config/rules: validation with columns, then persistence', async () => {
  await fresh()
  const put = (rules) => call('PUT', api('/config/rules'), rules)

  invalid(await put([{ match: '~d (a', action: 'passthrough' }]), 'match', 4)
  invalid(await put([{ match: '~t image/ & ~zz', profile: 'saver' }]), 'match', 13)
  invalid(await put([{ match: '~c 20', profile: 'saver' }]), 'match', 4)
  invalid(await put([{ match: '~t image/ & ', profile: 'saver' }]), 'match', 13)
  invalid(await put([{ match: '', profile: 'saver' }]), 'match', 1)
  invalid(await put([{ match: '~d x & ~t image/', action: 'passthrough_tls' }]), 'match', 8)
  invalid(await put([{ match: '~d x', profile: 'nope' }]), 'profile')
  invalid(await put([{ match: '~d x', action: 'drop' }]), 'action')
  invalid(await put([{ match: '~d x', set: { nothing: 1 } }]), 'set.nothing')
  invalid(await put([{ match: '~d x', set: { quality: 101 } }]), 'set.quality')
  invalid(await put([{ match: '~d x', set: { minify_js: 'max' } }]), 'set.minify_js')
  invalid(await put([{ match: '~d x', set: { images: 'yes' } }]), 'set.images')
  invalid(await put([{ match: '~d x' }]), 'action')
  const notList = await put({ match: '~d x' })
  invalid(notList, 'rules')
  const two = invalid(await put([{ match: '~d a', profile: 'saver' }, { match: '~u (', profile: 'saver' }]), 'match')
  assert.equal(two.index, 1)
  assert.equal(ok(await get(api('/config')), 200).rules.length, 2, 'nothing changed')

  const good = [
    { match: '~d (bank|paypal)\\.', action: 'passthrough_tls' },
    { match: '~u "\\.(mp4|webm)$" | ~t video/', action: 'passthrough' },
    { match: '~d cdn\\.example\\.com & ~t image/', profile: 'mobile', set: { quality: 40, minify_js: 'safe', images: true } },
    { match: '~src 10.0.0.0/8 & ~d ^intra\\.', action: 'passthrough_tls' },
    { match: '~hq X-MidoPress-Bypass', action: 'passthrough' },
    { match: '~all', set: { minify_css: true } },
  ]
  const v = ok(await put(good), 200, S.ConfigView)
  assert.equal(v.rules.length, 6)
  assert.equal(v.managed_by_ui.rules, true)
  assert.deepEqual(ok(await get(api('/config')), 200).rules, v.rules)

  const val = ok(await call('POST', api('/config/validate'), { rules: [{ match: '~d (', profile: 'saver' }] }), 200, S.ValidateResult)
  assert.equal(val.ok, false)
  assert.equal(val.issues[0].column, 4)
  assert.equal(ok(await call('POST', api('/config/validate'), { rules: good }), 200, S.ValidateResult).ok, true)
  assert.equal(ok(await call('POST', api('/config/validate'), {}), 200, S.ValidateResult).ok, false)

  const reset = ok(await call('DELETE', api('/config/ui')), 200, S.ConfigView)
  assert.equal(reset.rules.length, 2)
  assert.equal(reset.managed_by_ui.rules, false)
})

test('profiles: create, change, reset, delete and conflicts', async () => {
  await fresh()
  const night = ok(await call('PUT', api('/config/profiles/night'), { base: 'saver', target_dssim: 0.01, text: { brotli: 4 } }), 200, S.ConfigView)
  const p = night.profiles.find((x) => x.name === 'night')
  assert.equal(p.builtin, false)
  assert.equal(p.target_dssim, 0.01)
  assert.equal(p.text.brotli, 4)
  assert.equal(p.text.zstd, 12, 'inherited from saver')
  assert.equal(night.managed_by_ui.profiles, true)

  const mod = ok(await call('PUT', api('/config/profiles/balanced'), { target_dssim: 0.003 }), 200)
  const b = mod.profiles.find((x) => x.name === 'balanced')
  assert.equal(b.modified, true)
  assert.equal(b.target_dssim, 0.003)
  assert.deepEqual(b.override, { target_dssim: 0.003 })
  const back = ok(await call('DELETE', api('/config/profiles/balanced')), 200, S.ConfigView)
  assert.equal(back.profiles.find((x) => x.name === 'balanced').modified, false)
  assert.equal(back.profiles.find((x) => x.name === 'balanced').target_dssim, 0.002)

  invalid(await call('PUT', api('/config/profiles/bad'), { target_dssim: 5 }), 'target_dssim')
  invalid(await call('PUT', api('/config/profiles/bad'), { formats: ['gif'] }), 'formats')
  invalid(await call('PUT', api('/config/profiles/bad'), { colour: 1 }), 'colour')
  invalid(await call('PUT', api('/config/profiles/Bad Name'), {}), 'name')

  await call('PUT', api('/config/default-profile'), { profile: 'night' })
  assert.equal((await call('DELETE', api('/config/profiles/night'))).status, 409)
  ok(await call('PUT', api('/config/default-profile'), { profile: 'saver' }), 200)
  invalid(await call('PUT', api('/config/default-profile'), { profile: 'missing' }), 'profile')
  assert.equal((await call('DELETE', api('/config/profiles/mobile'))).status, 409, 'a device uses mobile')
  await call('PUT', api('/config/rules'), [{ match: '~d x', profile: 'night' }])
  assert.equal((await call('DELETE', api('/config/profiles/night'))).status, 409, 'a rule uses night')
  await call('PUT', api('/config/rules'), [{ match: '~d x', action: 'passthrough' }])
  ok(await call('DELETE', api('/config/profiles/night')), 200, S.ConfigView)
  assert.equal((await call('DELETE', api('/config/profiles/night'))).status, 404)
  await call('DELETE', api('/config/ui'))
})

test('POST /config/test: rules explained and the precedence of the profile', async () => {
  await fresh()
  await call('DELETE', api('/config/ui'))
  await call('PUT', api('/config/rules'), [
    { match: '~d ^bank\\.', action: 'passthrough_tls' },
    { match: '~d example\\.com & ~t image/', profile: 'extreme', set: { quality: 30 } },
  ])
  const t = async (body) => ok(await call('POST', api('/config/test'), body), 200, S.TestResult)

  const rule = await t({ url: 'https://www.example.com/a.jpg', content_type: 'image/jpeg' })
  assert.equal(rule.profile, 'extreme')
  assert.equal(rule.profile_source, 'rule')
  assert.deepEqual(rule.set, { quality: 30 })
  assert.equal(rule.outcome, 'process')
  assert.deepEqual(rule.explanations.map((e) => e.matched), [false, true])
  assert.ok(rule.explanations.every((e) => e.reason && e.match))

  const noType = await t({ url: 'https://www.example.com/a.jpg' })
  assert.equal(noType.profile_source, 'default', 'a rule on the response is not tried without one')
  assert.match(noType.note, /request side/)

  const header = await t({ url: 'https://www.example.com/a.jpg', content_type: 'image/jpeg', headers: { 'X-MidoPress-Profile': 'lossless' } })
  assert.equal(header.profile, 'lossless')
  assert.equal(header.profile_source, 'header')

  const device = await t({ url: 'https://www.example.com/a.jpg', content_type: 'image/jpeg', headers: { 'X-MidoPress-Profile': 'lossless' }, device_profile: 'mobile' })
  assert.equal(device.profile, 'mobile')
  assert.equal(device.profile_source, 'device')

  const sd = await t({ url: 'https://other.org/', headers: { 'Save-Data': 'on' } })
  assert.equal(sd.profile, 'saver')
  assert.equal(sd.profile_source, 'save-data')
  const ruleOverSd = await t({ url: 'https://www.example.com/a.jpg', content_type: 'image/jpeg', headers: { 'Save-Data': 'on' } })
  assert.equal(ruleOverSd.profile_source, 'rule')
  const def = await t({ url: 'https://other.org/' })
  assert.deepEqual([def.profile, def.profile_source], ['balanced', 'default'])

  const pass = await t({ url: 'https://bank.example.net/login' })
  assert.equal(pass.action, 'passthrough_tls')
  assert.equal(pass.outcome, 'passthrough')

  invalid(await call('POST', api('/config/test'), {}), 'url')
  invalid(await call('POST', api('/config/test'), { url: 'nonsense' }), 'url')
  await call('DELETE', api('/config/ui'))
})

// ---------------------------------------------------------------- devices

test('device lifecycle', async () => {
  await fresh()
  const list = ok(await get(api('/devices')), 200)
  shape(list, { t: 'arr', s: S.Device })
  assert.ok(list.length >= 3)

  invalid(await call('POST', api('/devices'), { id: 'bad id!', profile: '' }), 'id')
  invalid(await call('POST', api('/devices'), { id: 'x'.repeat(65) }), 'id')
  invalid(await call('POST', api('/devices'), { id: 'ok', profile: 'missing' }), 'profile')

  const made = ok(await call('POST', api('/devices'), { id: 'phone_1.test', profile: 'saver' }), 201, S.NewDevice)
  assert.match(made.token, /^[0-9a-f]{64}$/)
  assert.match(made.config_url, /^midopress:\/\/config\?host=127\.0\.0\.1&port=\d+&user=phone_1\.test&token=[0-9a-f]{64}&tls=1$/)
  assert.match(made.config_qr_svg, /^<svg /)
  const other = ok(await call('POST', api('/devices'), { id: 'phone2' }), 201, S.NewDevice)
  assert.notEqual(other.token, made.token)
  assert.equal(other.profile, '')

  assert.equal((await call('POST', api('/devices'), { id: 'phone_1.test' })).status, 409)
  const listed = ok(await get(api('/devices')), 200).find((d) => d.id === 'phone_1.test')
  shape(listed, S.Device)
  assert.equal(listed.last_seen, null)
  assert.equal(listed.profile, 'saver')

  const patched = ok(await call('PATCH', api('/devices/phone_1.test'), { profile: 'extreme' }), 200, S.Device)
  assert.equal(patched.profile, 'extreme')
  invalid(await call('PATCH', api('/devices/phone_1.test'), { profile: 'missing' }), 'profile')
  assert.equal((await call('PATCH', api('/devices/ghost'), { profile: '' })).status, 404)

  assert.equal((await call('DELETE', api('/devices/phone_1.test'))).status, 204)
  assert.equal((await call('DELETE', api('/devices/phone_1.test'))).status, 404)
  assert.equal((await call('DELETE', api('/devices/phone2'))).status, 204)
  assert.ok(!ok(await get(api('/devices')), 200).some((d) => d.id.startsWith('phone')))
})

// ---------------------------------------------------------------- static

test('web/dist is served with a history fallback', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'midopress-dist-'))
  fs.mkdirSync(path.join(dir, 'assets'))
  fs.writeFileSync(path.join(dir, 'index.html'), '<!doctype html><title>mock</title>')
  fs.writeFileSync(path.join(dir, 'assets', 'a.js'), 'console.log(1)')
  const s2 = await startServer({ port: 0, token: TOKEN, distDir: dir, tick: false })
  try {
    const b2 = `http://127.0.0.1:${s2.port}`
    const root = await fetch(b2 + '/')
    assert.match(await root.text(), /<title>mock<\/title>/)
    const deep = await fetch(b2 + '/flows/some/route')
    assert.equal(deep.status, 200)
    assert.match(deep.headers.get('content-type'), /text\/html/)
    const js = await fetch(b2 + '/assets/a.js')
    assert.match(js.headers.get('content-type'), /javascript/)
    assert.equal((await fetch(b2 + '/assets/missing.js')).status, 404)
    assert.equal((await fetch(b2 + '/api/v1/stats')).status, 401)
  } finally {
    await s2.close()
    fs.rmSync(dir, { recursive: true, force: true })
  }
})
