(() => {
  function settings(value) {
    const url = new URL(value.endpoint)
    if (url.username || url.password || url.search || url.hash || !['', '/'].includes(url.pathname) ||
        url.protocol !== 'https:' && !(url.protocol === 'http:' && ['127.0.0.1', 'localhost'].includes(url.hostname))) throw new Error('Use an HTTPS API origin or localhost.')
    const token = String(value.token ?? '').trim()
    if (!/^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]{43}$/.test(token)) throw new Error('Use a limited extension credential from Images, rather than an admin token.')
    if (!/^[a-z0-9_-]{1,64}$/.test(value.profile)) throw new Error('Choose a profile.')
    return { endpoint: url.origin, token, profile: value.profile }
  }
  function publicURL(value) {
    const u = new URL(value)
    if (!['http:', 'https:'].includes(u.protocol) || u.username || u.password || value.length > 8192) throw new Error('Unsupported image URL.')
    return u.href
  }
  async function optimize(config, input, fetcher = fetch) {
    const source = publicURL(input.url)
    const width = Math.ceil(Number(input.width))
    const dpr = Math.ceil(Number(input.dpr) * 4) / 4
    if (!Number.isFinite(width) || width < 1 || width > 16384 || !Number.isFinite(dpr) || dpr < 1 || dpr > 4) return { code: 'invalid_size' }
    const signal = AbortSignal.timeout(20000)
    const signed = await fetcher(config.endpoint + '/img/sign', { method: 'POST', signal,
      credentials: 'omit', referrerPolicy: 'no-referrer', redirect: 'error', cache: 'no-store',
      headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + config.token },
      body: JSON.stringify({ url: source, width, dpr, profile: config.profile, format: 'auto', version: '1' }) })
    if (!signed.ok) return { code: 'sign_' + signed.status }
    const grant = await signed.json()
    const target = new URL(grant.optimized_url)
    if (target.origin !== config.endpoint || target.pathname !== '/img') return { code: 'invalid_signer' }
    const response = await fetcher(target.href, { signal, credentials: 'omit', referrerPolicy: 'no-referrer',
      redirect: 'error', cache: 'no-store', headers: { Accept: 'image/avif,image/webp,image/png,image/jpeg,image/gif' } })
    if (!response.ok) return { code: 'image_' + response.status }
    const before = Number(response.headers.get('X-MidoPress-Original-Bytes'))
    const after = Number(response.headers.get('X-MidoPress-Result-Bytes'))
    const mime = response.headers.get('Content-Type')?.split(';')[0]
    if (!before || !after || after >= before || response.headers.get('X-MidoPress-Decision') === 'original') {
      await response.body?.cancel()
      return { code: 'original' }
    }
    if (!/^image\/(avif|webp|png|jpeg|gif)$/.test(mime ?? '') || after > 8 * 1024 * 1024) {
      await response.body?.cancel()
      return { code: 'unsupported_result' }
    }
    const chunks = []
    let size = 0
    const reader = response.body.getReader()
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      size += value.length
      if (size > 8 * 1024 * 1024) { await reader.cancel(); return { code: 'byte_limit' } }
      chunks.push(value)
    }
    if (size !== after) return { code: 'incomplete_result' }
    const data = new Uint8Array(size)
    let at = 0
    for (const chunk of chunks) { data.set(chunk, at); at += chunk.length }
    let text = ''
    for (let i = 0; i < data.length; i += 16384) text += String.fromCharCode(...data.subarray(i, i + 16384))
    return { code: 'optimized', data: `data:${mime};base64,${btoa(text)}`, mime, before, after, width, dpr,
      backend: response.headers.get('X-MidoPress-Backend'), metric: response.headers.get('X-MidoPress-Metric'),
      measure: response.headers.get('X-MidoPress-Measure') }
  }
  globalThis.MidoPressClient = { settings, publicURL, optimize }
})()
