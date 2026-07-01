<script setup>
import { ref, computed, onMounted, onUnmounted, nextTick } from 'vue'
import { fetchComments, addComment } from './api.js'

const endEl = ref(null)
function nearBottom() {
  return window.innerHeight + window.scrollY >= document.body.scrollHeight - 120
}
function scrollToBottom(smooth = true) {
  nextTick(() => endEl.value?.scrollIntoView({ behavior: smooth ? 'smooth' : 'auto', block: 'end' }))
}

const props = defineProps({ postId: { type: String, required: true } })

const comments = ref([])
const text = ref('')
const loading = ref(true)
const sending = ref(false)
const error = ref('')
const replyTarget = ref(null) // комментарий, на который отвечаем
const inputEl = ref(null)

// id → коммент (для показа цитаты родителя у ответов)
const byId = computed(() => {
  const m = {}
  for (const c of comments.value) m[c.id] = c
  return m
})

function fmtTime(unix) {
  return new Date(unix * 1000).toLocaleString('ru-RU', { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' })
}
function initials(name) {
  const n = (name || 'Г').trim()
  const parts = n.split(/\s+/)
  return (parts[0][0] + (parts[1]?.[0] || '')).toUpperCase()
}
function avatarColor(name) {
  let h = 0
  for (const ch of (name || 'Г')) h = (h * 31 + ch.charCodeAt(0)) % 360
  return `hsl(${h} 55% 45%)`
}
function snippet(s, n = 60) {
  s = (s || '').replace(/\s+/g, ' ').trim()
  return s.length > n ? s.slice(0, n) + '…' : s
}

function startReply(c) {
  replyTarget.value = c
  inputEl.value?.focus()
}
function cancelReply() {
  replyTarget.value = null
}

async function load() {
  try {
    comments.value = await fetchComments(props.postId)
  } catch {
    error.value = 'Не удалось загрузить комментарии'
  } finally {
    loading.value = false
  }
}

// poll — подтягиваем новые комменты (синканутые из TG и т.п.) без сброса ввода.
// Добавляем только те, которых ещё нет (по id) — без мигания и дублей.
async function poll() {
  try {
    const fresh = await fetchComments(props.postId)
    const known = new Set(comments.value.map((c) => c.id))
    const added = fresh.filter((c) => !known.has(c.id))
    if (added.length) {
      const wasBottom = nearBottom()
      comments.value.push(...added)
      if (wasBottom) scrollToBottom() // не дёргаем, если юзер листает историю
    }
  } catch {
    /* тихо — следующий тик попробует снова */
  }
}

let pollTimer = null

async function send() {
  const t = text.value.trim()
  if (!t || sending.value) return
  sending.value = true
  error.value = ''
  try {
    const c = await addComment(props.postId, t, replyTarget.value?.id || 0)
    comments.value.push(c)
    text.value = ''
    replyTarget.value = null
    scrollToBottom()
  } catch {
    error.value = 'Не удалось отправить'
  } finally {
    sending.value = false
  }
}

onMounted(async () => {
  await load()
  scrollToBottom(false)
  pollTimer = setInterval(poll, 5000)
})
onUnmounted(() => {
  if (pollTimer) clearInterval(pollTimer)
})
</script>

<template>
  <main class="wrap">
    <h1>Комментарии</h1>

    <div v-if="loading">
      <div class="sk" v-for="i in 3" :key="i">
        <div class="c a"></div>
        <div style="flex:1"><div class="c l1"></div><div class="c l2"></div></div>
      </div>
    </div>

    <p v-else-if="error && !comments.length" class="center error">{{ error }}</p>

    <ul v-else class="list">
      <li v-if="comments.length === 0" class="center muted">Пока нет комментариев.<br />Будьте первым!</li>
      <li v-for="c in comments" :key="c.id" class="item">
        <div class="avatar" :style="{ background: avatarColor(c.author) }">{{ initials(c.author) }}</div>
        <div class="body">
          <div class="meta">
            <span class="name">{{ c.author || 'Гость' }}</span>
            <span class="src" v-if="c.source === 'tg'">TG</span>
            <span class="time">{{ fmtTime(c.created_at) }}</span>
          </div>
          <div v-if="c.reply_to && byId[c.reply_to]" class="quote">
            ↩ {{ byId[c.reply_to].author || 'Гость' }}: {{ snippet(byId[c.reply_to].text) }}
          </div>
          <div class="text">{{ c.text }}</div>
          <button class="reply-btn" @click="startReply(c)">Ответить</button>
        </div>
      </li>
    </ul>
    <div ref="endEl" class="end-anchor"></div>

    <div class="composer-wrap">
      <div v-if="replyTarget" class="reply-bar">
        <span class="muted">↩ Ответ <b>{{ replyTarget.author || 'Гость' }}</b>: {{ snippet(replyTarget.text, 40) }}</span>
        <button class="x" @click="cancelReply" aria-label="Отменить ответ">✕</button>
      </div>
      <form class="composer" @submit.prevent="send">
        <textarea ref="inputEl" v-model="text" rows="1" :placeholder="replyTarget ? 'Ваш ответ…' : 'Написать комментарий…'" aria-label="Текст комментария" @keydown.enter.exact.prevent="send" />
        <button class="send" :disabled="sending || !text.trim()" aria-label="Отправить">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
            <path d="M22 2 11 13" /><path d="M22 2 15 22 11 13 2 9 22 2Z" />
          </svg>
        </button>
      </form>
      <p class="powered">
        Комментарии работают на
        <a href="https://t.me/MaxTelegramBridgeBot" target="_blank" rel="noopener">MaxTelegramBridge</a>
      </p>
    </div>
  </main>
</template>

<style scoped>
.quote {
  margin-top: 4px; padding: 4px 8px; border-left: 2px solid var(--accent);
  background: var(--surface); border-radius: 6px; font-size: 13px; color: var(--text-muted);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.reply-btn {
  margin-top: 4px; padding: 0; border: 0; background: none; color: var(--accent);
  font-size: 13px; font-weight: 600; cursor: pointer;
}
/* отступ снизу под фиксированный композер, чтобы последний коммент не прятался */
.end-anchor { height: 84px; }
.composer-wrap {
  position: fixed; left: 0; right: 0; bottom: 0; background: var(--bg); border-top: 1px solid var(--border);
}
.reply-bar {
  display: flex; align-items: center; gap: 8px; padding: 6px 16px 0;
  font-size: 13px;
}
.powered { margin: 0; padding: 2px 16px 6px; text-align: center; font-size: 11px; color: var(--text-muted); }
.powered a { color: var(--text-muted); text-decoration: underline; }
.reply-bar > span { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.reply-bar .x { border: 0; background: none; color: var(--text-muted); font-size: 16px; cursor: pointer; padding: 0 4px; }
/* композер внутри обёртки — убираем фикс из глобального, тут он в потоке */
.composer-wrap .composer { position: static; border-top: 0; }
</style>
