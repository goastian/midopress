// The six scenarios: a news portal, a social feed, a shop, GitHub, three
// minutes of YouTube and an HLS stream. They browse the real sites through the
// proxy, so the numbers are what those sites send on the day they are run, and
// they vary between runs: compare runs of the same day, not of different ones.
import http from 'node:http'
import { sleep } from './lib.mjs'

/** Scrolls like somebody reading, so the lazy images and the infinite feeds load. */
async function read(page, { steps = 10, pause = 900 } = {}) {
  for (let i = 0; i < steps; i++) {
    await page.mouse.wheel(0, 900)
    await sleep(pause)
  }
  await page.waitForLoadState('networkidle', { timeout: 8000 }).catch(() => {})
}

async function visit(page, url) {
  await page.goto(url, { waitUntil: 'load', timeout: 60000 }).catch((e) => {
    if (!/Timeout/.test(String(e))) throw e
  })
}

export const scenarios = {
  news: {
    title: 'News portal (eltiempo.com)',
    warm: true,
    min: { compressible: 2e6 },
    async run(page) {
      await visit(page, 'https://www.eltiempo.com/')
      await read(page, { steps: 10 })
    },
  },

  social: {
    title: 'Social feed (mastodon.social explore)',
    warm: true,
    min: { compressible: 1.5e6 },
    async run(page) {
      await visit(page, 'https://mastodon.social/explore')
      await page.waitForSelector('.status', { timeout: 25000 }).catch(() => {})
      await read(page, { steps: 12, pause: 1000 })
    },
  },

  shop: {
    title: 'Shop (exito.com)',
    warm: true,
    min: { compressible: 2e6 },
    async run(page) {
      await visit(page, 'https://www.exito.com/')
      await read(page, { steps: 10 })
    },
  },

  github: {
    title: 'GitHub (golang/go repository and a source file)',
    warm: true,
    min: { compressible: 1e6, image: 1e5 },
    async run(page) {
      await visit(page, 'https://github.com/golang/go')
      await read(page, { steps: 4 })
      await visit(page, 'https://github.com/golang/go/blob/master/src/net/http/server.go')
      await read(page, { steps: 4 })
    },
  },

  youtube: {
    title: 'YouTube, three minutes of one video',
    warm: false,
    min: { media: 1e6 },
    launch: { args: ['--autoplay-policy=no-user-gesture-required'] },
    async run(page, note) {
      await visit(page, 'https://www.youtube.com/watch?v=aqz-KE-bpKQ')
      await page.waitForSelector('video', { timeout: 30000 })
      await page.evaluate(() => document.querySelector('video')?.play()).catch(() => {})
      const t0 = Date.now()
      while (Date.now() - t0 < 180000) {
        await sleep(10000)
        const s = await page
          .evaluate(() => {
            const v = document.querySelector('video')
            const p = document.getElementById('movie_player')
            return { t: v?.currentTime ?? 0, paused: v?.paused, w: v?.videoWidth, h: v?.videoHeight, q: p?.getPlaybackQuality?.() }
          })
          .catch(() => null)
        if (s) note.samples = [...(note.samples || []), { at: Math.round((Date.now() - t0) / 1000), ...s }]
      }
    },
  },

  hls: {
    title: 'HLS stream (Big Buck Bunny, hls.js, one minute)',
    warm: false,
    min: { video: 1e6 },
    async run(page, note) {
      const html = `<!doctype html><meta charset=utf-8><video id=v controls muted autoplay playsinline width=960></video>
<script src="https://cdn.jsdelivr.net/npm/hls.js@1/dist/hls.min.js"></script>
<script>
const v = document.getElementById('v')
const hls = new Hls()
window.hls = hls
hls.loadSource('https://test-streams.mux.dev/x36xhzz/x36xhzz.m3u8')
hls.attachMedia(v)
hls.on(Hls.Events.MANIFEST_PARSED, () => v.play().catch(() => {}))
</script>`
      const srv = http.createServer((_, res) => {
        res.setHeader('Content-Type', 'text/html')
        res.end(html)
      })
      await new Promise((r) => srv.listen(0, '127.0.0.1', r))
      try {
        await visit(page, `http://127.0.0.1:${srv.address().port}/`)
        const t0 = Date.now()
        while (Date.now() - t0 < 60000) {
          await sleep(10000)
          const s = await page
            .evaluate(() => ({
              t: document.getElementById('v').currentTime,
              w: document.getElementById('v').videoWidth,
              h: document.getElementById('v').videoHeight,
              level: window.hls?.currentLevel,
              levels: window.hls?.levels?.map((l) => `${l.width}x${l.height}@${Math.round(l.bitrate / 1000)}k`),
            }))
            .catch(() => null)
          if (s) note.samples = [...(note.samples || []), { at: Math.round((Date.now() - t0) / 1000), ...s }]
        }
      } finally {
        srv.close()
      }
    },
  },
}
