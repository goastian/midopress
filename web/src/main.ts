import { createApp } from 'vue'
import '@fontsource/ibm-plex-sans/latin-400.css'
import '@fontsource/ibm-plex-sans/latin-500.css'
import '@fontsource/ibm-plex-sans/latin-600.css'
import '@fontsource/ibm-plex-mono/latin-400.css'
import '@fontsource/ibm-plex-mono/latin-500.css'
import './style.css'
import App from './App.vue'
import router from './router'
import { initTheme } from './composables/theme'

initTheme()
createApp(App).use(router).mount('#app')
