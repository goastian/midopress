const api = globalThis.chrome ?? globalThis.browser
const status = document.querySelector('#status')
async function tab() {
  const [current] = await api.tabs.query({ active: true, currentWindow: true })
  if (!current?.id || !/^https?:/.test(current.url ?? '')) throw new Error('Abre una página HTTP o HTTPS.')
  const url = new URL(current.url)
  if (['127.0.0.1', 'localhost'].includes(url.hostname) && ['9997', '9998'].includes(url.port)) throw new Error('Activa la extensión en una página de contenido, fuera del panel.')
  return current
}
document.querySelector('#activate').addEventListener('click', async () => {
  try {
    if (!(await api.storage.local.get('config')).config) { await api.runtime.openOptionsPage(); return }
    const current = await tab()
    await api.scripting.executeScript({ target: { tabId: current.id }, files: ['measure.js', 'content.js'] })
    status.textContent = 'Activa. Observando imágenes visibles y cambios de layout.'
  } catch (error) { status.textContent = error.message }
})
document.querySelector('#original').addEventListener('click', async () => {
  try {
    const current = await tab()
    await api.scripting.executeScript({ target: { tabId: current.id }, func: () => globalThis.__midoPress?.stop() })
    status.textContent = 'Se restauraron las imágenes originales.'
  } catch (error) { status.textContent = error.message }
})
document.querySelector('#options').addEventListener('click', () => api.runtime.openOptionsPage())
tab().then(async current => {
  const key = 'stats:' + new URL(current.url).origin
  const value = (await api.storage.local.get(key))[key]
  if (value) document.querySelector('#stats').textContent = `${value.optimized} optimizadas · ${(value.saved / 1000).toFixed(1)} KB ahorrados en variantes · ${value.failed} alternativas originales por error.`
}).catch(() => {})
