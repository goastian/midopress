// Generated pictures for the flows and the QR placeholder of the devices.

import { createHash } from 'node:crypto'

const digest = (text) => createHash('sha256').update(text).digest()

const kb = (n) => (n >= 1048576 ? `${(n / 1048576).toFixed(1)} MB` : `${Math.max(1, Math.round(n / 1024))} KB`)

/**
 * A deterministic picture drawn from the id. The original is crisp with fine
 * detail; the transformed one is softer, a little less saturated and carries
 * its own size and codec label.
 */
export function pictureSvg({ id, width, height, bytes, codec, transformed }) {
  const h = digest(id)
  const hue = Math.round((h[0] / 255) * 360)
  const hue2 = (hue + 40 + (h[1] % 80)) % 360
  const sat = transformed ? 55 : 68
  let shapes = ''
  for (let i = 0; i < 7; i++) {
    const cx = Math.round((h[2 + i * 3] / 255) * width)
    const cy = Math.round((h[3 + i * 3] / 255) * height)
    const r = Math.round(Math.min(width, height) * (0.08 + (h[4 + i * 3] / 255) * 0.22))
    const col = `hsl(${(hue + i * 37) % 360} ${sat}% ${40 + (i % 3) * 12}%)`
    shapes += i % 2 === 0
      ? `<circle cx="${cx}" cy="${cy}" r="${r}" fill="${col}" fill-opacity=".85"/>`
      : `<rect x="${cx - r}" y="${cy - r / 2}" width="${r * 2}" height="${r}" fill="${col}" fill-opacity=".8"/>`
  }
  const step = Math.max(8, Math.round(width / 80))
  let lines = ''
  for (let x = 0; x < width; x += step) lines += `M${x} 0V${height}`
  const label = transformed
    ? `TRANSFORMED  ${(codec || 'result').toUpperCase()}  ${width}x${height}  ${kb(bytes)}`
    : `ORIGINAL  ${width}x${height}  ${kb(bytes)}`
  const fs = Math.max(14, Math.round(width / 38))
  return `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}" viewBox="0 0 ${width} ${height}">
<defs>
<linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="hsl(${hue} ${sat}% 28%)"/><stop offset="1" stop-color="hsl(${hue2} ${sat}% 52%)"/></linearGradient>
<filter id="soft"><feGaussianBlur stdDeviation="${(width / 900 + 0.6).toFixed(2)}"/></filter>
</defs>
<rect width="100%" height="100%" fill="url(#g)"/>
<g${transformed ? ' filter="url(#soft)"' : ''}>${shapes}<path d="${lines}" stroke="#fff" stroke-opacity=".07" stroke-width="1" fill="none"/></g>
<rect y="${height - fs * 2}" width="100%" height="${fs * 2}" fill="#000" fill-opacity=".55"/>
<text x="${fs}" y="${height - fs * 0.7}" font-family="monospace" font-size="${fs}" fill="#fff">${label}</text>
</svg>
`
}

/**
 * Not a real QR code: a deterministic pseudo QR (three finder patterns and
 * modules drawn from a hash chain of the text). It looks right in the
 * interface but a phone will not read it.
 */
export function qrPlaceholderSvg(text) {
  const n = 29
  const grid = Array.from({ length: n }, () => new Array(n).fill(false))
  let block = digest(text)
  let k = 0
  for (let y = 0; y < n; y++) {
    for (let x = 0; x < n; x++) {
      if (k >= block.length * 8) {
        block = digest(block)
        k = 0
      }
      grid[y][x] = ((block[k >> 3] >> (k & 7)) & 1) === 1
      k++
    }
  }
  const finder = (ox, oy) => {
    for (let y = -1; y <= 7; y++) {
      for (let x = -1; x <= 7; x++) {
        const gx = ox + x
        const gy = oy + y
        if (gx < 0 || gy < 0 || gx >= n || gy >= n) continue
        const ring = x === 0 || x === 6 || y === 0 || y === 6
        const core = x >= 2 && x <= 4 && y >= 2 && y <= 4
        grid[gy][gx] = x >= 0 && x <= 6 && y >= 0 && y <= 6 && (ring || core)
      }
    }
  }
  finder(0, 0)
  finder(n - 7, 0)
  finder(0, n - 7)
  let path = ''
  for (let y = 0; y < n; y++) for (let x = 0; x < n; x++) if (grid[y][x]) path += `M${x + 2} ${y + 2}h1v1h-1z`
  const size = n + 4
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${size} ${size}" width="240" height="240" shape-rendering="crispEdges"><rect width="${size}" height="${size}" fill="#fff"/><path d="${path}" fill="#000"/></svg>`
}
