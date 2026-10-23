// Loads the pages of the scenarios straight from the sites, without the proxy,
// and saves every text response with how it travelled: the encoding the origin
// chose and its size on the wire, and the decoded body. It is the evidence for
// what recompressing the text that origins already compress could save.
//
//   node scenarios/measure-text.mjs ../artifacts/text-corpus
import { createHash } from 'node:crypto'
import { mkdirSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { chromium } from '@playwright/test'
import { sleep } from './lib.mjs'

const out = resolve(process.argv[2] ?? '../artifacts/text-corpus')
mkdirSync(out, { recursive: true })
const pages = [
  'https://www.eltiempo.com/', 'https://www.bbc.com/news', 'https://github.com/golang/go',
  'https://www.youtube.com/watch?v=aqz-KE-bpKQ', 'https://mastodon.social/explore', 'https://www.mercadolibre.com.co/',
  'https://en.wikipedia.org/wiki/HTTP', 'https://www.nytimes.com/', 'https://www.reddit.com/', 'https://stackoverflow.com/questions',
]
const textual = /^(text\/|application\/(javascript|x-javascript|json|xml)|application\/.*\+(json|xml))/

const index = []
const browser = await chromium.launch()
for (const url of pages) {
  const page = await (await browser.newContext({ viewport: { width: 1280, height: 800 } })).newPage()
  const pending = []
  page.on('response', (res) => {
    pending.push(
      (async () => {
        const h = res.headers()
        const type = (h['content-type'] ?? '').split(';')[0].trim().toLowerCase()
        if (!textual.test(type) || res.status() !== 200) return
        const sizes = await res.request().sizes().catch(() => null)
        const body = await res.body().catch(() => null)
        if (!body || !sizes) return
        const id = createHash('sha1').update(res.url()).digest('hex').slice(0, 12)
        writeFileSync(`${out}/${id}.body`, body)
        index.push({
          id, page: new URL(url).hostname, host: new URL(res.url()).hostname, type, encoding: h['content-encoding'] ?? 'identity',
          wire: sizes.responseBodySize, decoded: body.length, cache: h['cache-control'] ?? '',
        })
      })(),
    )
  })
  await page.goto(url, { waitUntil: 'load', timeout: 60000 }).catch(() => {})
  for (let i = 0; i < 6; i++) {
    await page.mouse.wheel(0, 900)
    await sleep(600)
  }
  await sleep(2000)
  await Promise.allSettled(pending)
  await page.context().close()
  process.stderr.write(`${url}: ${index.filter((e) => e.page === new URL(url).hostname).length} text responses\n`)
}
await browser.close()
writeFileSync(`${out}/index.json`, JSON.stringify(index, null, 1))
