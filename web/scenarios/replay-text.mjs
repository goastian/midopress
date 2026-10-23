// Serves the text responses recorded by record-text.mjs as they came (same
// bytes, same Content-Encoding and Cache-Control) and fetches them through a
// throw-away MidoPress with the headers of a browser, cold and then warm.
//
//   node scenarios/replay-text.mjs --bin ../bin/midopress --dir ../artifacts/text-wire --label text-new
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import http from 'node:http'
import { join, resolve } from 'node:path'
import { settle, startServer } from './lib.mjs'

const arg = (name, def) => {
  const i = process.argv.indexOf('--' + name)
  return i > 0 ? process.argv[i + 1] : def
}
const bin = resolve(arg('bin', '../bin/midopress'))
const dir = resolve(arg('dir', '../artifacts/text-wire'))
const label = arg('label', 'text')
const profiles = arg('profiles', 'balanced,saver,extreme').split(',')
const env = Object.fromEntries((arg('env', '') ? arg('env').split(',') : []).map((kv) => kv.split('=')))
const extra = arg('args', '') ? arg('args').split(' ') : []
const out = resolve(arg('out', '../artifacts/scenarios'))
mkdirSync(out, { recursive: true })

const index = JSON.parse(readFileSync(join(dir, 'index.json'), 'utf8')).filter((e) => existsSync(join(dir, e.id + '.wire')))
const byId = new Map(index.map((e) => [e.id, { ...e, body: readFileSync(join(dir, e.id + '.wire')) }]))
const og = http.createServer((req, res) => {
  const f = byId.get(req.url.slice(1))
  if (!f) return res.writeHead(404).end()
  const h = { 'Content-Type': f.type, 'Content-Length': f.body.length, 'Cache-Control': f.cache || 'no-cache' }
  if (f.encoding !== 'identity') h['Content-Encoding'] = f.encoding
  res.writeHead(200, h).end(f.body)
})
await new Promise((r) => og.listen(0, '127.0.0.1', r))
const port = og.address().port

function get(proxyPort, id) {
  return new Promise((resolve) => {
    const req = http.request(
      { host: '127.0.0.1', port: proxyPort, path: `http://127.0.0.1:${port}/${id}`, headers: { Host: `127.0.0.1:${port}`, 'Accept-Encoding': 'gzip, deflate, br, zstd' } },
      (res) => {
        let n = 0
        res.on('data', (c) => (n += c.length))
        res.on('end', () => resolve({ id, bytes: n, encoding: res.headers['content-encoding'] ?? 'identity' }))
      },
    )
    req.on('error', () => resolve({ id, bytes: 0, error: true }))
    req.end()
  })
}

async function pass(srv) {
  const rows = []
  for (let i = 0; i < index.length; i += 8) rows.push(...(await Promise.all(index.slice(i, i + 8).map((e) => get(srv.proxyPort, e.id)))))
  const sum = (f) => rows.filter(f).reduce((a, r) => a + r.bytes, 0)
  const wire = index.reduce((a, e) => a + e.wire, 0)
  return { wire, bytes: sum(() => true), rows }
}

const results = []
for (const profile of profiles) {
  const srv = await startServer(bin, { profile, env, args: extra })
  const about = await srv.about()
  const run = { label, profile, bin, version: about.version, native: about.native, files: index.length }
  process.stderr.write(`${label} ${profile} (${index.length} responses) ... `)
  try {
    run.cold = await pass(srv)
    const cold = await srv.stats()
    run.coldReasons = cold.by_reason
    run.settled = await settle(srv, { max: 600000 })
    run.warm = await pass(srv)
    process.stderr.write(
      `wire ${run.cold.wire} -> cold ${run.cold.bytes} (${((1 - run.cold.bytes / run.cold.wire) * 100).toFixed(1)}%), warm ${run.warm.bytes} (${((1 - run.warm.bytes / run.warm.wire) * 100).toFixed(1)}%)\n`,
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
