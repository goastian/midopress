<script setup lang="ts">
// A small set of line icons, 24x24, drawn with the current colour.
defineProps<{ name: string; size?: number }>()

const paths: Record<string, string> = {
  dashboard: 'M3 13h8V3H3v10zm0 8h8v-6H3v6zm10 0h8V11h-8v10zm0-18v6h8V3h-8z',
  traffic: 'M3 12h4l3-8 4 16 3-8h4',
  devices: 'M7 2h10a2 2 0 0 1 2 2v16a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2zm5 17h.01',
  rules: 'M4 6h16M4 12h10M4 18h6m8-2 2 2 3-4',
  shield: 'M12 3 4 6v6c0 5 3.5 8 8 9 4.5-1 8-4 8-9V6l-8-3zm-3 9 2 2 4-4',
  logs: 'M5 4h14v16H5zM8 8h8M8 12h8M8 16h5',
  service: 'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zm7.4-3a7.4 7.4 0 0 0-.1-1.3l2-1.5-2-3.4-2.3.9a7.7 7.7 0 0 0-2.2-1.3L14.4 3h-4l-.4 2.4a7.7 7.7 0 0 0-2.2 1.3l-2.3-.9-2 3.4 2 1.5a7.4 7.4 0 0 0 0 2.6l-2 1.5 2 3.4 2.3-.9a7.7 7.7 0 0 0 2.2 1.3l.4 2.4h4l.4-2.4a7.7 7.7 0 0 0 2.2-1.3l2.3.9 2-3.4-2-1.5c.1-.4.1-.9.1-1.3z',
  download: 'M12 3v12m0 0 4-4m-4 4-4-4M4 19h16',
  upload: 'M12 15V3m0 0 4 4m-4-4L8 7M4 19h16',
  refresh: 'M20 11a8 8 0 1 0-2.3 5.7M20 4v7h-7',
  trash: 'M4 7h16M10 11v6m4-6v6M6 7l1 13h10l1-13M9 7V4h6v3',
  plus: 'M12 5v14M5 12h14',
  check: 'M5 13l4 4L19 7',
  x: 'M6 6l12 12M18 6 6 18',
  copy: 'M9 9h11v11H9zM5 15V4h11',
  search: 'M11 4a7 7 0 1 0 0 14 7 7 0 0 0 0-14zm9 16-4.5-4.5',
  pause: 'M8 5v14M16 5v14',
  play: 'M7 4v16l13-8z',
  eye: 'M2 12s4-7 10-7 10 7 10 7-4 7-10 7S2 12 2 12zm10 3a3 3 0 1 0 0-6 3 3 0 0 0 0 6z',
  lock: 'M6 11h12v9H6zM8 11V8a4 4 0 0 1 8 0v3',
  image: 'M4 5h16v14H4zM4 16l5-5 4 4 3-3 4 4M9 9h.01',
  text: 'M5 6h14M12 6v13M9 19h6',
  font: 'M5 19 11 5h2l6 14M7.5 14h9',
  bolt: 'M13 2 4 14h7l-1 8 9-12h-7z',
  qr: 'M4 4h6v6H4zM14 4h6v6h-6zM4 14h6v6H4zM14 14h3v3h-3zM20 14v3M14 20h6',
  external: 'M14 4h6v6M20 4l-9 9M18 14v5H5V6h5',
  menu: 'M4 7h16M4 12h16M4 17h16',
  info: 'M12 8h.01M11 12h1v5h1M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18z',
  warn: 'M12 4 2 20h20L12 4zm0 6v4m0 3h.01',
  save: 'M5 3h12l3 3v15H5zM8 3v6h8M8 21v-7h8v7',
  sun: 'M12 4V2m0 20v-2M4 12H2m20 0h-2M6 6 4.6 4.6M19.4 19.4 18 18M6 18l-1.4 1.4M19.4 4.6 18 6M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8z',
  link: 'M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1',
}
</script>

<template>
  <svg
    :width="size ?? 20"
    :height="size ?? 20"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="1.9"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
    style="flex: none"
  >
    <path :d="paths[name] ?? ''" />
  </svg>
</template>
