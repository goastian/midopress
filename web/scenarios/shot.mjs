// Screenshots of the dashboard of a throw-away MidoPress that served the files of
// a directory, to look at what the interface says about real traffic.
//   node scenarios/shot.mjs ../bin/midopress ../artifacts/corpus-images /tmp/dash
import { readdirSync, readFileSync } from 'node:fs'
import http from 'node:http'
import { extname, join, resolve } from 'node:path'
import { chromium } from '@playwright/test'
import { TOKEN, settle, startServer } from './lib.mjs'

const [bin, dir, prefix] = [resolve(process.argv[2]), resolve(process.argv[3]), process.argv[4]]
const types = { '.jpg': 'image/jpeg', '.png': 'image/png', '.gif': 'image/gif', '.webp': 'image/webp', '.avif': 'image/avif' }
const files = readdirSync(dir)
  .filter((f) => types[extname(f)])
  .map((f) => ({ f, t: types[extname(f)], b: readFileSync(join(dir, f)) }))
const og = http.createServer((req, res) => {
  const x = files.find((y) => '/' + y.f === req.url)
  if (!x) return res.writeHead(404).end()
  res.writeHead(200, { 'Content-Type': x.t, 'Content-Length': x.b.length, 'Cache-Control': 'public, max-age=60' }).end(x.b)
})
await new Promise((r) => og.listen(0, '127.0.0.1', r))
const srv = await startServer(bin, { profile: 'saver' })
for (const x of files.slice(0, 30)) {
  await new Promise((r) => {
    const q = http.request(
      { host: '127.0.0.1', port: srv.proxyPort, path: `http://127.0.0.1:${og.address().port}/${x.f}`, headers: { Accept: 'image/avif,image/webp,*/*', 'Accept-Encoding': 'gzip, br' } },
      (res) => (res.resume(), res.on('end', r)),
    )
    q.on('error', r)
    q.end()
  })
}
await settle(srv, { max: 120000 })
const admin = `http://127.0.0.1:${srv.adminPort}`
const b = await chromium.launch()
for (const [name, vp] of [['desktop', { width: 1280, height: 900 }], ['phone', { width: 390, height: 844 }]]) {
  const ctx = await b.newContext({ viewport: vp })
  const page = await ctx.newPage()
  await page.goto(admin + '/#/login')
  await page.getByLabel('Admin token').fill(TOKEN)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await page.waitForSelector('text=Overview')
  await page.waitForTimeout(2500)
  await page.screenshot({ path: `${prefix}-${name}.png`, fullPage: true })
  await ctx.close()
}
await b.close()
await srv.stop()
og.close()
