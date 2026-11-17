(() => {
  if (globalThis.__midoPress) { globalThis.__midoPress.refresh(true); return }
  const api = globalThis.browser ?? globalThis.chrome
  const records = new Map()
  const visible = new Set()
  const pending = new Set()
  const own = new WeakSet()
  const stats = { optimized: 0, original: 0, failed: 0, saved: 0, active: true }
  let running = 0
  let timer
  let stopped = false
  const mark = element => { own.add(element); setTimeout(() => own.delete(element), 0) }
  function attrs(element) {
    return Object.fromEntries(['src', 'srcset', 'sizes'].map(name => [name, element.getAttribute(name)]))
  }
  function restore(record) {
    if (!record.applied) return
    mark(record.element)
    if (record.kind === 'img') {
      for (const [name, value] of Object.entries(record.attributes)) {
        if (value == null) record.element.removeAttribute(name)
        else record.element.setAttribute(name, value)
      }
      for (const source of record.sources) {
        mark(source.element)
        for (const [name, value] of Object.entries(source.attributes)) {
          if (value == null) source.element.removeAttribute(name)
          else source.element.setAttribute(name, value)
        }
      }
    } else {
      record.element.style.setProperty('background-image', record.background, record.priority)
    }
    record.applied = false
    record.key = ''
  }
  function schedule(element) {
    if (stopped || !visible.has(element)) return
    pending.add(element)
    clearTimeout(timer)
    timer = setTimeout(flush, 120)
  }
  function refresh(force) {
    for (const record of records.values()) {
      if (force === true) { record.key = ''; record.rejectedKey = '' }
      record.revision++
      record.dirty = true
      schedule(record.element)
    }
  }
  async function displayed(data) {
    const image = new Image()
    image.src = data
    await image.decode()
  }
  async function request(url, measured) {
    if (!/^https?:\/\//.test(url) || !measured || measured.width > 16384) return { code: 'unmeasurable' }
    return api.runtime.sendMessage({ type: 'optimize', image: { url, width: measured.width, dpr: measured.dpr } })
  }
  async function process(record) {
    const element = record.element
    if (!element.isConnected) { records.delete(element); visible.delete(element); return }
    if (record.dirty && record.applied) {
      restore(record)
      await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))
    }
    record.dirty = false
    const revision = record.revision
    let urls, style
    if (record.kind === 'img') {
      const current = element.currentSrc || element.src
      if (/^https?:\/\//.test(current)) {
        record.url = current
        if (element.naturalWidth) record.natural = [element.naturalWidth, element.naturalHeight]
      }
      if (!record.natural[0]) return
      const measured = MidoPressMeasure.size(element, ...record.natural)
      urls = [{ url: record.url, measured }]
    } else {
      style = MidoPressMeasure.backgrounds(element)
      urls = []
      for (const layer of style.layers) {
        let natural = record.dimensions.get(layer.url)
        if (!natural) {
          const image = new Image()
          image.src = layer.url
          try { await image.decode(); natural = [image.naturalWidth, image.naturalHeight]; record.dimensions.set(layer.url, natural) }
          catch { continue }
        }
        urls.push({ ...layer, measured: MidoPressMeasure.backgroundSize(element, ...natural, layer.index) })
      }
    }
    const key = JSON.stringify(urls.map(u => [u.url, u.measured?.width, u.measured?.dpr]))
    if (key === record.key || key === record.rejectedKey || !urls.length) return
    let css = style?.css
    let data
    for (const item of urls) {
      let result
      try { result = await request(item.url, item.measured) } catch { result = { code: 'unavailable' } }
      if (stopped || revision !== record.revision) return
      if (!result?.data) {
        if (result?.code === 'original' || result?.code === 'unmeasurable') stats.original++
        else stats.failed++
        continue
      }
      try { await displayed(result.data) } catch { stats.failed++; continue }
      if (stopped || revision !== record.revision) return
      stats.optimized++
      stats.saved += Math.max(0, result.before - result.after)
      if (record.kind === 'img') data = result.data
      else css = css.replace(item.token, `url("${result.data}")`)
    }
    record.key = key
    if (data || css && css !== style.css) {
      mark(element)
      if (record.kind === 'img') {
        for (const source of record.sources) { mark(source.element); source.element.removeAttribute('srcset') }
        element.removeAttribute('srcset')
        element.src = data
      } else {
        record.appliedCSS = css
        element.style.setProperty('background-image', css, 'important')
      }
      record.applied = true
    }
    api.storage.local.set({ ['stats:' + location.origin]: { ...stats } }).catch(() => {})
  }
  function flush() {
    while (running < 3 && pending.size && !stopped) {
      const element = pending.values().next().value
      pending.delete(element)
      const record = records.get(element)
      if (!record || record.busy) continue
      running++
      const revision = record.revision
      record.busy = true
      process(record).catch(() => { stats.failed++ }).finally(() => {
        running--; record.busy = false
        if (record.revision !== revision) schedule(element)
        if (pending.size) flush()
      })
    }
  }
  const intersection = new IntersectionObserver(entries => {
    for (const entry of entries) {
      if (entry.isIntersecting) { visible.add(entry.target); schedule(entry.target) }
      else visible.delete(entry.target)
    }
  }, { rootMargin: '100px' })
  const resize = new ResizeObserver(entries => {
    for (const entry of entries) {
      const record = records.get(entry.target)
      if (record && !own.has(entry.target)) { record.dirty = true; record.revision++; schedule(entry.target) }
    }
  })
  function add(element) {
    if (!(element instanceof Element) || records.has(element) || records.size >= 5000 || element.closest('[data-midopress-ui]')) return
    if (!['IMG', 'DIV', 'SPAN', 'SECTION', 'ARTICLE', 'A', 'FIGURE', 'HEADER', 'MAIN', 'LI', 'BODY'].includes(element.tagName)) return
    const img = element instanceof HTMLImageElement
    const record = { element, kind: img ? 'img' : 'background', revision: 0, natural: [0, 0],
      attributes: img ? attrs(element) : {}, sources: img && element.parentElement?.tagName === 'PICTURE'
        ? [...element.parentElement.querySelectorAll('source')].map(source => ({ element: source, attributes: attrs(source) })) : [],
      background: element.style.backgroundImage, priority: element.style.getPropertyPriority('background-image'), dimensions: new Map() }
    records.set(element, record)
    intersection.observe(element)
    resize.observe(element)
    if (img) {
      record.onLoad = () => { if (!record.applied) { record.key = ''; schedule(element) } }
      record.onError = () => {
        if (!record.applied) return
        record.rejectedKey = record.key
        stats.failed++
        restore(record)
      }
      element.addEventListener('load', record.onLoad)
      element.addEventListener('error', record.onError)
    }
  }
  function scan(root) {
    add(root)
    for (const element of root.querySelectorAll?.('img,div,span,section,article,a,figure,header,main,li') ?? []) add(element)
  }
  const mutations = new MutationObserver(entries => {
    let layout = false
    for (const entry of entries) {
      if (entry.type === 'childList') { for (const node of entry.addedNodes) if (node instanceof Element) scan(node); layout = true; continue }
      if (own.has(entry.target)) continue
      let record = records.get(entry.target)
      if (entry.target.tagName === 'SOURCE') record = records.get(entry.target.parentElement?.querySelector('img'))
      if (record) {
        if (record.kind === 'img' && ['src', 'srcset', 'sizes'].includes(entry.attributeName)) {
          const target = entry.target === record.element ? record : record.sources.find(s => s.element === entry.target)
          if (target) target.attributes[entry.attributeName] = entry.target.getAttribute(entry.attributeName)
        }
        if (record.kind === 'background' && entry.attributeName === 'style' && entry.target.style.backgroundImage !== record.appliedCSS) {
          record.background = entry.target.style.backgroundImage
          record.priority = entry.target.style.getPropertyPriority('background-image')
        }
        record.revision++; record.dirty = true; record.key = ''; schedule(record.element)
      }
      if (entry.attributeName === 'class' || entry.target.tagName === 'STYLE') layout = true
    }
    if (layout) refresh()
  })
  mutations.observe(document.documentElement, { subtree: true, childList: true, attributes: true,
    attributeFilter: ['src', 'srcset', 'sizes', 'style', 'class', 'media'] })
  addEventListener('resize', refresh)
  visualViewport?.addEventListener('resize', refresh)
  let density = matchMedia(`(resolution: ${devicePixelRatio}dppx)`)
  const densityChange = () => { refresh(); density = matchMedia(`(resolution: ${devicePixelRatio}dppx)`); density.addEventListener('change', densityChange, { once: true }) }
  density.addEventListener('change', densityChange, { once: true })
  globalThis.__midoPress = { refresh, stats, stop() {
    stopped = true; stats.active = false; clearTimeout(timer); mutations.disconnect(); resize.disconnect(); intersection.disconnect()
    removeEventListener('resize', refresh); visualViewport?.removeEventListener('resize', refresh); density.removeEventListener('change', densityChange)
    for (const record of records.values()) {
      if (record.onLoad) record.element.removeEventListener('load', record.onLoad)
      if (record.onError) record.element.removeEventListener('error', record.onError)
      restore(record)
    }
    pending.clear(); visible.clear(); records.clear()
    api.storage.local.set({ ['stats:' + location.origin]: { ...stats } }).catch(() => {})
    delete globalThis.__midoPress
  } }
  scan(document.documentElement)
})()
