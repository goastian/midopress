// Serves the files of a directory from a local origin, exactly as they are, and
// fetches them through a throw-away MidoPress, once cold and once after the
// background work has finished. The same files, in the same order, go through any
// binary, so two binaries can be compared without the noise of real sites.
//
//   node scenarios/replay.mjs --bin ../bin/midopress --label images-new \
//        --dir ../artifacts/corpus-images --profiles balanced,saver,extreme
//   node scenarios/replay.mjs ... --chunked   # no Content-Length, as many CDNs send
//   node scenarios/replay.mjs ... --no-transform   # the origin says Cache-Control: no-transform
//   node scenarios/replay.mjs ... --burst 40  # 40 requests at the same time
//   node scenarios/replay.mjs ... --only '^(photo|kodak)'  # the files whose name matches
import { readdirSync, readFileSync, mkdirSync, writeFileSync } from 'node:fs'
import http from 'node:http'
import { extname, join, resolve } from 'node:path'
import { metric, settle, startServer } from './lib.mjs'

const arg = (name, def) => {
  const i = process.argv.indexOf('--' + name)
  return i > 0 ? process.argv[i + 1] : def
}
const flag = (name) => process.argv.includes('--' + name)

const bin = resolve(arg('bin', '../bin/midopress'))
const dir = resolve(arg('dir', '../artifacts/corpus-images'))
const label = arg('label', 'replay')
const profiles = arg('profiles', 'balanced,saver,extreme').split(',')
const accept = arg('accept', 'image/avif,image/webp,image/png,image/svg+xml,image/*;q=0.8,*/*;q=0.5')
const chunked = flag('chunked')
const noTransform = flag('no-transform')
const burst = Number(arg('burst', '0'))
const only = new RegExp(arg('only', '.'))
const concurrency = Number(arg('concurrency', '6'))
const env = Object.fromEntries((arg('env', '') ? arg('env').split(',') : []).map((kv) => kv.split('=')))
const out = resolve(arg('out', '../artifacts/scenarios'))
mkdirSync(out, { recursive: true })

const types = { '.jpg': 'image/jpeg', '.jpeg': 'image/jpeg', '.png': 'image/png', '.gif': 'image/gif', '.webp': 'image/webp', '.avif': 'image/avif' }
const files = readdirSync(dir)
  .filter((f) => types[extname(f).toLowerCase()] && only.test(f))
  .sort()
  .map((f) => ({ name: f, type: types[extname(f).toLowerCase()], body: readFileSync(join(dir, f)) }))
const byName = new Map(files.map((f) => [f.name, f]))

function origin() {
  const srv = http.createServer((req, res) => {
    const f = byName.get(decodeURIComponent(req.url.split('?')[0].slice(1)))
    if (!f) {
      res.writeHead(404).end()
      return
    }
    const h = { 'Content-Type': f.type, 'Cache-Control': noTransform ? 'public, max-age=86400, no-transform' : 'public, max-age=3600' }
    if (!chunked) h['Content-Length'] = f.body.length
    res.writeHead(200, h)
    if (!chunked) return res.end(f.body)
    const step = 64 << 10
    let i = 0
    const next = () => {
      if (i >= f.body.length) return res.end()
      res.write(f.body.subarray(i, i + step))
      i += step
      setImmediate(next)
    }
    next()
  })
  return new Promise((r) => srv.listen(0, '127.0.0.1', () => r(srv)))
}

function fetchVia(proxyPort, url, headers) {
  return new Promise((resolve, reject) => {
    const req = http.request({ host: '127.0.0.1', port: proxyPort, path: url, method: 'GET', headers: { Host: new URL(url).host, ...headers } }, (res) => {
      let n = 0
      res.on('data', (c) => (n += c.length))
      res.on('end', () => resolve({ status: res.statusCode, bytes: n, type: res.headers['content-type'], via: res.headers['x-midopress'] }))
    })
    req.setTimeout(120000, () => req.destroy(new Error('timeout')))
    req.on('error', reject)
    req.end()
  })
}

async function pool(items, n, fn) {
  const results = new Array(items.length)
  let next = 0
  await Promise.all(
    Array.from({ length: Math.min(n, items.length) }, async () => {
      for (;;) {
        const i = next++
        if (i >= items.length) return
        results[i] = await fn(items[i], i)
      }
    }),
  )
  return results
}

async function pass(srv, port, list) {
  const t0 = Date.now()
  const rows = await pool(list, burst || concurrency, async (f) => {
    const t = Date.now()
    try {
      const r = await fetchVia(srv.proxyPort, `http://127.0.0.1:${port}/${encodeURIComponent(f.name)}`, { Accept: accept, 'Accept-Encoding': 'gzip, br' })
      return { name: f.name, original: f.body.length, ...r, ms: Date.now() - t }
    } catch (e) {
      return { name: f.name, original: f.body.length, error: String(e), ms: Date.now() - t }
    }
  })
  const sum = (k) => rows.reduce((a, r) => a + (r[k] || 0), 0)
  return {
    seconds: (Date.now() - t0) / 1000,
    original: sum('original'),
    bytes: sum('bytes'),
    changed: rows.filter((r) => r.type && r.type !== byName.get(r.name).type).length,
    errors: rows.filter((r) => r.error || r.status !== 200).length,
    rows,
  }
}

const results = []
const og = await origin()
const port = og.address().port
const list = burst ? files.slice(0, burst) : files
for (const profile of profiles) {
  const srv = await startServer(bin, { profile, env })
  const about = await srv.about()
  const run = { label, profile, bin, version: about.version, native: about.native, files: list.length, chunked, noTransform, burst, concurrency: burst || concurrency }
  process.stderr.write(`${label} ${profile} (${list.length} files${chunked ? ', chunked' : ''}${burst ? ', burst' : ''}) ... `)
  try {
    run.cold = await pass(srv, port, list)
    const afterCold = await srv.stats()
    run.coldReasons = afterCold.by_reason.filter((r) => r.kind === 'image')
    run.coldCauses = afterCold.by_cause.filter((c) => c.reason === 'busy')
    run.settled = await settle(srv, { max: 600000 })
    run.warm = await pass(srv, port, list)
    const s = await srv.stats()
    run.reasons = s.by_reason.filter((r) => r.kind === 'image')
    run.causes = s.by_cause.filter((c) => c.reason === 'busy')
    run.noTransformOverridden = metric(await srv.metrics(), 'midopress_pipeline_no_transform_overridden_total')
    run.noTransformKept = s.by_reason.filter((r) => r.reason === 'no-transform').reduce((a, r) => a + r.requests, 0)
    process.stderr.write(
      `cold ${run.cold.original} -> ${run.cold.bytes} (${((1 - run.cold.bytes / run.cold.original) * 100).toFixed(1)}%, ${run.cold.seconds.toFixed(0)}s), warm -> ${run.warm.bytes} (${((1 - run.warm.bytes / run.warm.original) * 100).toFixed(1)}%)\n`,
    )
  } catch (e) {
    run.error = String(e)
    process.stderr.write('failed: ' + e + '\n')
  } finally {
    await srv.stop()
  }
  results.push(run)
  writeFileSync(`${out}/${label}.json`, JSON.stringify(results, null, 2))
}
og.close()
