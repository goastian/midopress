(() => {
  function size(element, naturalWidth = 0, naturalHeight = 0, fit = '') {
    const style = getComputedStyle(element)
    const rect = element.getBoundingClientRect()
    const dpr = Math.max(1, devicePixelRatio)
    if (!Number.isFinite(dpr) || dpr > 4 || style.visibility === 'hidden' || style.display === 'none' || Number(style.opacity) === 0) return null
    let transformed = false
    for (let node = element, depth = 0; node && depth < 64; node = node.parentElement, depth++) {
      const s = getComputedStyle(node)
      if (s.perspective !== 'none' || s.transform.startsWith('matrix3d')) return null
      transformed ||= s.transform !== 'none'
    }
    if (style.display === 'none' || style.visibility === 'hidden' || Number(style.opacity) === 0 || rect.width < 1 || rect.height < 1 ||
        rect.right <= 0 || rect.bottom <= 0 || rect.left >= innerWidth || rect.top >= innerHeight) return null
    const padX = parseFloat(style.paddingLeft) + parseFloat(style.paddingRight) + parseFloat(style.borderLeftWidth) + parseFloat(style.borderRightWidth)
    const padY = parseFloat(style.paddingTop) + parseFloat(style.paddingBottom) + parseFloat(style.borderTopWidth) + parseFloat(style.borderBottomWidth)
    if (rect.width < 1 || rect.height < 1) return null
    const extent = Math.hypot(rect.width, rect.height)
    const width = Math.max(1, transformed ? extent : rect.width - padX)
    const height = Math.max(1, transformed ? extent : rect.height - padY)
    let rendered = width
    fit ||= style.objectFit
    if (naturalWidth > 0 && naturalHeight > 0) {
      if (fit === 'cover' || fit === 'fill') rendered = naturalWidth * Math.max(width / naturalWidth, height / naturalHeight)
      if (fit === 'contain' || fit === 'scale-down') rendered = naturalWidth * Math.min(width / naturalWidth, height / naturalHeight, fit === 'scale-down' ? 1 : Infinity)
      if (fit === 'none') rendered = naturalWidth
    } else if (fit === 'cover' || fit === 'none') return null
    return { width: Math.ceil(rendered), dpr, boxWidth: rect.width, boxHeight: rect.height }
  }
  function backgrounds(element) {
    const style = getComputedStyle(element)
    const layers = []
    const expression = /url\(\s*(?:"((?:\\.|[^"\\])*)"|'((?:\\.|[^'\\])*)'|([^)]*))\s*\)/g
    if (style.backgroundImage.includes('image-set(')) return { css: style.backgroundImage, layers: [] }
    for (const match of style.backgroundImage.matchAll(expression)) {
      const url = (match[1] ?? match[2] ?? match[3]).trim().replace(/\\(["'\\])/g, '$1')
      if (/^https?:\/\//.test(url)) layers.push({ url, token: match[0], index: layers.length })
    }
    return { css: style.backgroundImage, fit: style.backgroundSize.split(',')[0].trim(), layers }
  }
  function backgroundSize(element, naturalWidth, naturalHeight, index) {
    const style = getComputedStyle(element)
    const box = size(element, naturalWidth, naturalHeight, 'fill')
    if (!box) return null
    const values = style.backgroundSize.split(',').map(v => v.trim())
    const value = values[index % values.length]
    if (value === 'cover' || value === 'contain') return size(element, naturalWidth, naturalHeight, value)
    const parts = value.split(/\s+/)
    const dimension = (v, n) => v === 'auto' ? null : /^\d+(?:\.\d+)?px$/.test(v) ? Number.parseFloat(v) : /^\d+(?:\.\d+)?%$/.test(v) ? Number.parseFloat(v) * n / 100 : NaN
    let w = dimension(parts[0], element.clientWidth)
    let h = dimension(parts[1] ?? 'auto', element.clientHeight)
    if (Number.isNaN(w) || Number.isNaN(h)) return null
    if (w == null && h == null) { w = naturalWidth; h = naturalHeight }
    else if (w == null) w = naturalWidth * h / naturalHeight
    else if (h == null) h = naturalHeight * w / naturalWidth
    return { ...box, width: Math.ceil(Math.max(w, h * naturalWidth / naturalHeight)) }
  }
  globalThis.MidoPressMeasure = { size, backgrounds, backgroundSize }
})()
