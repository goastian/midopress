// Records the text responses of some real pages exactly as the origins send
// them: the bytes on the wire (still brotli, gzip or zstd) with the headers that
// matter. replay-text.mjs serves them back, so that two binaries can be compared
// on the same real bytes, which the scenarios of real sites can not do.
//
//   node scenarios/record-text.mjs ../artifacts/text-wire
import { createHash } from 'node:crypto'
import { mkdirSync, writeFileSync } from 'node:fs'
import https from 'node:https'
import { resolve } from 'node:path'
import { chromium } from '@playwright/test'
import { sleep } from './lib.mjs'

const out = resolve(process.argv[2] ?? '../artifacts/text-wire')
mkdirSync(out, { recursive: true })
const pages = [
  'https://www.eltiempo.com/', 'https://www.bbc.com/news', 'https://github.com/golang/go',
  'https://www.youtube.com/watch?v=aqz-KE-bpKQ', 'https://mastodon.social/explore', 'https://www.exito.com/',
  'https://en.wikipedia.org/wiki/HTTP', 'https://stackoverflow.com/questions',
]
const textual = /^(text\/|application\/(javascript|x-javascript|json|xml)|application\/.*\+(json|xml))/

function raw(url, headers) {
  return new Promise((resolve, reject) => {
    const req = https.get(url, { headers, timeout: 20000 }, (res) => {
      const chunks = []
      res.on('data', (c) => chunks.push(c))
      res.on('end', () => resolve({ status: res.statusCode, headers: res.headers, body: Buffer.concat(chunks) }))
    })
    req.on('timeout', () => req.destroy(new Error('timeout')))
    req.on('error', reject)
  })
}

const seen = new Set()
const index = []
const browser = await chromium.launch()
for (const url of pages) {
  const page = await (await browser.newContext({ viewport: { width: 1280, height: 800 } })).newPage()
  const found = []
  page.on('response', (res) => {
    const type = (res.headers()['content-type'] ?? '').split(';')[0].trim().toLowerCase()
    if (res.status() === 200 && res.request().method() === 'GET' && textual.test(type)) found.push({ url: res.url(), headers: res.request().headers() })
  })
  await page.goto(url, { waitUntil: 'load', timeout: 60000 }).catch(() => {})
  for (let i = 0; i < 6; i++) {
    await page.mouse.wheel(0, 900)
    await sleep(500)
  }
  await sleep(1500)
  await page.context().close()
  let n = 0
  for (const f of found) {
    if (seen.has(f.url)) continue
    seen.add(f.url)
    const h = { ...f.headers }
    for (const k of Object.keys(h)) if (/^(cookie|range|if-|:)/i.test(k)) delete h[k]
    h['accept-encoding'] = 'gzip, deflate, br, zstd' // the network stack adds it, so the page does not show it
    try {
      const r = await raw(f.url, h)
      const type = (r.headers['content-type'] ?? '').split(';')[0].trim().toLowerCase()
      if (r.status !== 200 || !textual.test(type) || r.body.length < 200) continue
      const id = createHash('sha1').update(f.url).digest('hex').slice(0, 12)
      writeFileSync(`${out}/${id}.wire`, r.body)
      index.push({
        id, page: new URL(url).hostname, host: new URL(f.url).hostname, type, encoding: r.headers['content-encoding'] ?? 'identity',
        cache: r.headers['cache-control'] ?? '', wire: r.body.length,
      })
      n++
    } catch {}
  }
  process.stderr.write(`${url}: ${n} responses\n`)
}
await browser.close()
writeFileSync(`${out}/index.json`, JSON.stringify(index, null, 1))
const by = {}
for (const e of index) (by[e.encoding] ??= { n: 0, bytes: 0 }), (by[e.encoding].n++, (by[e.encoding].bytes += e.wire))
process.stderr.write(JSON.stringify(by) + '\n')
