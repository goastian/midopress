import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { join, resolve } from 'node:path'

const admin = 'http://127.0.0.1:9998/api/v1'
const token = readFileSync(join(homedir(), '.midopress/admin.token'), 'utf8').trim()
const headers = { Authorization: 'Bearer ' + token, 'Content-Type': 'application/json' }
async function api(path, body) {
  const response = await fetch(admin + path, { method: body ? 'POST' : 'GET', headers,
    body: body ? JSON.stringify(body) : undefined, signal: AbortSignal.timeout(10000) })
  assert.ok(response.ok, `${path}: HTTP ${response.status}`)
  return response.json()
}
const config = await api('/config')
const resource = 'https://www.gstatic.com/webp/gallery/1.jpg'
const accept = 'image/avif,image/webp,image/png,image/jpeg,image/gif'
async function image(url, extra = {}) {
  const started = performance.now()
  const response = await fetch(url, { headers: { Accept: accept, ...extra }, credentials: 'omit',
    redirect: 'error', signal: AbortSignal.timeout(35000) })
  const body = Buffer.from(await response.arrayBuffer())
  return { body, status: response.status, ms: performance.now() - started,
    before: Number(response.headers.get('X-MidoPress-Original-Bytes')),
    after: Number(response.headers.get('X-MidoPress-Result-Bytes')),
    type: response.headers.get('Content-Type'), cache: response.headers.get('X-MidoPress-Cache'),
    decision: response.headers.get('X-MidoPress-Decision'), backend: response.headers.get('X-MidoPress-Backend'),
    metric: response.headers.get('X-MidoPress-Metric'), measure: response.headers.get('X-MidoPress-Measure') }
}
const rows = []
let signed
for (const name of ['balanced', 'saver', 'extreme', 'lossless']) {
  const profile = config.profiles.find(p => p.name === name)
  assert.ok(profile)
  signed = await api('/images/sign', { url: resource, profile: name, width: 0, dpr: 1, format: 'auto', version: 'acceptance-17' })
  const original = await image(signed.original_url)
  const cold = await image(signed.optimized_url)
  const warm = await image(signed.optimized_url)
  assert.equal(original.status, 200)
  assert.equal(cold.status, 200)
  assert.equal(warm.status, 200)
  assert.equal(cold.before, original.body.length)
  assert.equal(cold.after, cold.body.length)
  assert.ok(cold.body.length <= original.body.length)
  assert.deepEqual(cold.body, warm.body)
  assert.equal(warm.cache, 'hit')
  if (cold.metric === 'dssim') assert.ok(Number(cold.measure) <= profile.target_dssim, 'full-resolution metric must satisfy the profile')
  const { body, ...result } = cold
  rows.push({ profile: name, ...result, saved_percent: 100 * (1 - body.length / original.body.length),
    original_ms: original.ms, warm_ms: warm.ms, warm_cache: warm.cache,
    quality_policy: profile.quality_policy, color_policy: profile.color_policy, target_dssim: profile.target_dssim })
}
const forged = new URL(signed.optimized_url)
forged.searchParams.set('w', '1')
assert.equal((await image(forged.href)).status, 403)
assert.equal((await image(signed.optimized_url, { Cookie: 'session=acceptance-fixture' })).status, 409)
const refused = await fetch(admin + '/images/sign', { method: 'POST', headers,
  body: JSON.stringify({ url: 'http://127.0.0.1/private', profile: 'balanced' }) })
assert.equal(refused.status, 422)
const result = { updated: new Date().toISOString(), resource, kind: 'real local image API; four profiles; cold variant and warm conversion cache; origin is fetched each time',
  rows, negative: { forged_signature: 403, navigation_cookie: 409, private_destination: 422 } }
writeFileSync(resolve('../artifacts/image-live-results.json'), JSON.stringify(result, null, 2))
console.log(JSON.stringify(result))
