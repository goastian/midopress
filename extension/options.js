const api = globalThis.chrome ?? globalThis.browser
const fields = ['endpoint', 'token', 'profile']
const status = document.querySelector('#status')
const scope = document.querySelector('#scope')
function describe(token) {
  try {
    const c = JSON.parse(atob(token.split('.')[0].replace(/-/g, '+').replace(/_/g, '/')))
    scope.textContent = `Dominios: ${c.hosts.join(', ')}. Hasta ${c.max_width} CSS px, DPR ${c.max_dpr}. Caduca: ${new Date(c.expires * 1000).toLocaleString()}.`
  } catch { scope.textContent = '' }
}
api.storage.local.get('config').then(({ config }) => {
  if (config) { for (const key of fields) document.getElementById(key).value = config[key]; describe(config.token) }
})
document.querySelector('#settings').addEventListener('submit', async event => {
  event.preventDefault()
  try {
    const config = MidoPressClient.settings(Object.fromEntries(fields.map(k => [k, document.getElementById(k).value])))
    if (config.endpoint.startsWith('https:')) {
      const origins = [config.endpoint + '/*']
      if (!await api.permissions.contains({ origins }) && !await api.permissions.request({ origins })) throw new Error('No se autorizó el acceso a la API.')
    }
    await api.storage.local.set({ config })
    describe(config.token)
    status.textContent = 'Guardado. Activa la extensión en la pestaña que quieras probar.'
  } catch (error) { status.textContent = error.message }
})
document.querySelector('#clear').addEventListener('click', async () => {
  await api.storage.local.remove('config')
  document.querySelector('#token').value = ''
  scope.textContent = ''
  status.textContent = 'Credencial borrada de este navegador.'
})
