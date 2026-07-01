<script setup>
import { ref } from 'vue'
import Comments from './Comments.vue'
import Settings from './Settings.vue'
import Admin from './Admin.vue'

// Launch-параметр мини-аппа. MAX отдаёт initData (схема Telegram WebApp) в hash
// как WebAppData → внутри него start_param = payload кнопки (наш post_id).
function launchParam() {
  const q = new URLSearchParams(location.search)
  const h = new URLSearchParams((location.hash || '').replace(/^#/, ''))
  const wad = h.get('tgWebAppData') || h.get('WebAppData')
  if (wad) {
    const sp = new URLSearchParams(wad).get('start_param')
    if (sp) return sp
  }
  const keys = ['post_id', 'startapp', 'start', 'start_param', 'startParam', 'payload', 'tgWebAppStartParam']
  for (const k of keys) {
    const v = q.get(k) || h.get(k)
    if (v) return v
  }
  return ''
}

const param = launchParam()
const wantDebug = new URLSearchParams(location.search).get('debug') === '1'
// Роутинг: пост-id → комменты; иначе ("settings" или пусто) → настройки (по умолчанию).
// Диагностика launch-параметра — только по ?debug=1.
let mode = (param && param !== 'settings') ? 'comments' : 'settings'
if (param === 'admin') mode = 'admin' // отдельный роут админки (start_param=admin)
if (wantDebug) mode = 'unknown'

// Клиентская навигация между экранами (SPA): кабинет ⇄ админка.
const route = ref(mode)
function nav(r) { route.value = r }

const debug = ref('')
if (mode === 'unknown') {
  const globals = ['WebApp', 'Telegram', 'max', 'MAX', 'maxApp', 'vk', 'webApp', 'MiniApp', 'oneme']
    .filter((g) => typeof window[g] !== 'undefined')
  let init = ''
  try { init = JSON.stringify(window.Telegram?.WebApp?.initDataUnsafe || window.WebApp?.initData || {}) } catch {}
  debug.value =
    'href: ' + location.href +
    '\nsearch: ' + (location.search || '∅') +
    '\nhash: ' + (location.hash || '∅') +
    '\nreferrer: ' + (document.referrer || '∅') +
    '\nwindow globals: ' + (globals.join(', ') || '∅') +
    '\ninitData: ' + (init || '∅')
}
</script>

<template>
  <Comments v-if="route === 'comments'" :post-id="param" />
  <Admin v-else-if="route === 'admin'" @back="nav('settings')" />
  <Settings v-else-if="route === 'settings'" @admin="nav('admin')" />
  <main v-else class="wrap">
    <h1>Открыто без контекста</h1>
    <p class="muted">Не передан параметр запуска (пост для комментариев или «settings»).</p>
    <pre style="text-align:left;white-space:pre-wrap;word-break:break-all;font-size:12px;background:var(--surface);padding:10px;border-radius:8px;">{{ debug }}</pre>
  </main>
</template>
