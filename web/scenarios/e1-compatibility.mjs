// Runtime SRI compatibility: external JS/CSS are inserted after the HTML response.
// Every origin and proxy here is disposable; the running user proxy is untouched.
import assert from 'node:assert/strict'
import { chromium } from '@playwright/test'
import { createHash } from 'node:crypto'
import { copyFileSync, chmodSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import http from 'node:http'
import { resolve, join } from 'node:path'
import { startServer, settle } from './lib.mjs'

const arg = (key, fallback) => { const i = process.argv.indexOf('--' + key); return i < 0 ? fallback : process.argv[i + 1] }
const sourceBin = resolve(arg('bin', '../bin/midopress'))
const out = resolve(arg('out', '../artifacts/e1/sri'))
const profiles = arg('profiles', 'balanced,saver,extreme,lossless').split(',')
const executablePath = arg('chromium', undefined)
mkdirSync(out, { recursive: true })
const bin = join(out, 'midopress.snapshot')
copyFileSync(sourceBin, bin); chmodSync(bin, 0o755)
const hash = (algorithm, bytes) => createHash(algorithm).update(bytes).digest('base64')
const js = Buffer.from(('/* Runtime-inserted script: original entity bytes are covered by SRI. */\n').repeat(80) + 'window.__e1DynamicIntegrity = (function proofOfExecution() { const expected = 42; return expected; })();\n')
const css = Buffer.from(('/* Runtime-inserted stylesheet: changes must never invalidate its integrity. */\n').repeat(80) + '.e1-target { color: rgb(13, 37, 59); padding-left: 7px; }\n')
const negativeJS = Buffer.from('window.__e1NegativeExecuted = true;\n')
const integrity = { js: 'sha384-' + hash('sha384', js), css: 'sha384-' + hash('sha384', css) }
const html = Buffer.from(`<!doctype html><meta charset=utf-8><title>E1 runtime SRI fixture</title><div class=e1-target>Integrity probe</div>
<script>
window.e1 = {loaded: [], errors: [], negativeChecked: false, negativeRejected: false};
setTimeout(() => {
  const s = document.createElement('script');
  s.src = '/dynamic.js'; s.integrity = ${JSON.stringify(integrity.js)}; s.crossOrigin = 'anonymous';
  s.onload = () => e1.loaded.push('js'); s.onerror = () => e1.errors.push('js');
  const c = document.createElement('link');
  c.rel = 'stylesheet'; c.href = '/dynamic.css'; c.integrity = ${JSON.stringify(integrity.css)}; c.crossOrigin = 'anonymous';
  c.onload = () => e1.loaded.push('css'); c.onerror = () => e1.errors.push('css');
  const bad = document.createElement('script');
  bad.src = '/negative.js'; bad.integrity = ${JSON.stringify('sha384-' + Buffer.alloc(48).toString('base64'))};
  bad.onload = () => { e1.negativeChecked = true; };
  bad.onerror = () => { e1.negativeChecked = true; e1.negativeRejected = true; };
  document.head.append(s, c, bad);
}, 50);
</script>`)
const origin = http.createServer((req, res) => {
  const path = new URL(req.url, 'http://fixture').pathname
  const [type, body] = path === '/dynamic.js' ? ['application/javascript', js] : path === '/negative.js' ? ['application/javascript', negativeJS] : path === '/dynamic.css' ? ['text/css', css] : ['text/html', html]
  res.writeHead(200, { 'Content-Type': type, 'Content-Length': body.length, 'Cache-Control': 'public, max-age=3600', 'Access-Control-Allow-Origin': '*' })
  res.end(body)
})
await new Promise((resolve, reject) => { origin.once('error', reject); origin.listen(0, '127.0.0.1', resolve) })
const rows = []
const report = { schema: 1, generated: new Date().toISOString(), binary_sha256: createHash('sha256').update(readFileSync(bin)).digest('hex'), node: process.version,
  fixture: { js_sha384: integrity.js, css_sha384: integrity.css, js_bytes: js.length, css_bytes: css.length }, rows,
  scope: 'Runtime-created script/link with browser SRI verification; same-origin local public fixture; proxy conversion cache, browser cache disabled; no system trust changes' }
let failed = false
try {
  for (const profile of profiles) {
    let server, browser
    try {
      server = await startServer(bin, { profile })
      browser = await chromium.launch({ executablePath, headless: true,
        proxy: { server: server.proxyURL, bypass: '<-loopback>' }, args: ['--disable-quic', '--proxy-bypass-list=<-loopback>'] })
      const context = await browser.newContext({ serviceWorkers: 'block' })
      const about = await server.about()
      for (const phase of ['cold', 'warm']) {
        const page = await context.newPage()
        const cdp = await context.newCDPSession(page)
        await cdp.send('Network.enable'); await cdp.send('Network.setCacheDisabled', { cacheDisabled: true })
        const before = await server.stats()
        const observed = {}, pending = [], errors = []
        page.on('pageerror', e => errors.push(String(e)))
        page.on('response', response => {
          const name = new URL(response.url()).pathname
          if (name !== '/dynamic.js' && name !== '/dynamic.css') return
          pending.push((async () => {
            const body = await response.body()
            observed[name] = { status: response.status(), sha384: 'sha384-' + hash('sha384', body), entity_bytes: body.length,
              content_encoding: response.headers()['content-encoding'] || 'identity' }
          })())
        })
        const started = performance.now()
        await page.goto(`http://127.0.0.1:${origin.address().port}/?phase=${phase}`, { waitUntil: 'load', timeout: 30000 })
        await page.waitForFunction(() => window.e1?.loaded.length + window.e1?.errors.length === 2 && window.e1?.negativeChecked, null, { timeout: 15000 })
        const state = await page.evaluate(() => ({ ...window.e1, value: window.__e1DynamicIntegrity,
          negativeExecuted: window.__e1NegativeExecuted ?? false,
          color: getComputedStyle(document.querySelector('.e1-target')).color, padding: getComputedStyle(document.querySelector('.e1-target')).paddingLeft,
          dynamicScripts: [...document.scripts].filter(s => s.src).map(s => ({ path: new URL(s.src).pathname, integrity: s.integrity })) }))
        await Promise.all(pending)
        const after = await server.stats()
        const inspected = after.totals.requests - before.totals.requests
        const row = { profile, phase, ms: performance.now() - started, browser: browser.version(), state, observed, errors, inspected_requests: inspected,
          runtime: about, fallback: 'external JS/CSS entity preserved; lossless content compression allowed; runtime SRI remains valid' }
        rows.push(row)
        writeFileSync(join(out, 'results.json'), JSON.stringify(report, null, 2))
        assert.equal(state.value, 42, 'integrity-protected dynamic script must execute')
        assert.equal(state.color, 'rgb(13, 37, 59)'); assert.equal(state.padding, '7px')
        assert.deepEqual(state.errors, []); assert.deepEqual(errors, [])
        assert.equal(state.negativeRejected, true, 'wrong SRI must actually be enforced by this browser')
        assert.equal(state.negativeExecuted, false)
        assert.equal(observed['/dynamic.js']?.sha384, integrity.js)
        assert.equal(observed['/dynamic.css']?.sha384, integrity.css)
        assert.ok(inspected >= 4, 'the browser must really route its traffic through the disposable proxy')
        await page.close()
        if (phase === 'cold') assert.ok(await settle(server, { max: 60000 }), 'background work must settle before warm validation')
      }
      await context.close()
    } catch (error) {
      failed = true; rows.push({ profile, error: String(error), stack: error.stack })
    } finally {
      await browser?.close(); await server?.stop()
      writeFileSync(join(out, 'results.json'), JSON.stringify(report, null, 2))
    }
  }
} finally { await new Promise(resolve => origin.close(resolve)) }
report.complete = !failed && rows.filter(r => r.phase).length === profiles.length * 2
writeFileSync(join(out, 'results.json'), JSON.stringify(report, null, 2))
console.log(JSON.stringify({ complete: report.complete, visits: rows.filter(r => r.phase).length, file: join(out, 'results.json') }))
if (!report.complete) process.exitCode = 1
