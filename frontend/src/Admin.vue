<script setup>
import { ref, onMounted } from 'vue'
import { adminStats } from './api.js'

const emit = defineEmits(['back'])

const stats = ref(null)
const loading = ref(false)
const err = ref('')

async function load() {
  loading.value = true
  err.value = ''
  try {
    stats.value = await adminStats()
  } catch (e) {
    err.value = 'Не удалось загрузить статистику (нужны права админа).'
  } finally {
    loading.value = false
  }
}
onMounted(load)

function nfmt(n) { return (n ?? 0).toLocaleString('ru-RU') }
</script>

<template>
  <main class="wrap">
    <div class="top">
      <button class="back" @click="emit('back')">← Кабинет</button>
      <h1>📊 Админка</h1>
      <button class="refresh" :disabled="loading" @click="load">↻</button>
    </div>

    <p v-if="loading" class="muted">Загрузка…</p>
    <p v-else-if="err" class="muted err">{{ err }}</p>

    <template v-else-if="stats">
      <div class="grp">💰 Выручка</div>
      <div class="row"><span>Всего</span><b>{{ nfmt(stats.billing?.revenue_total_rub) }} ₽</b></div>
      <div class="row"><span>— подписки</span><b>{{ nfmt(stats.billing?.revenue_sub_rub) }} ₽</b></div>
      <div class="row"><span>— посты</span><b>{{ nfmt(stats.billing?.revenue_posts_rub) }} ₽</b></div>
      <div class="row"><span>За месяц</span><b>{{ nfmt(stats.billing?.revenue_month_rub) }} ₽</b></div>
      <div class="row"><span>MRR (≈)</span><b>{{ nfmt(stats.billing?.mrr_rub) }} ₽</b></div>
      <div class="row"><span>Платежей</span><b>{{ nfmt(stats.billing?.payments_count) }}</b></div>

      <div class="grp">⭐ Подписки</div>
      <div class="row"><span>Платящих (active)</span><b>{{ nfmt(stats.billing?.sub_active) }}</b></div>
      <div class="row"><span>Отменённых (в периоде)</span><b>{{ nfmt(stats.billing?.sub_canceled) }}</b></div>
      <div class="row"><span>Триал активен</span><b>{{ nfmt(stats.billing?.sub_trial) }}</b></div>
      <div class="row"><span>Просрочка / pending</span><b>{{ nfmt(stats.billing?.sub_past_due) }} / {{ nfmt(stats.billing?.sub_pending) }}</b></div>
      <div class="row"><span>Триалов всего</span><b>{{ nfmt(stats.billing?.trials_total) }}</b></div>

      <div class="grp">👤 Пользователи</div>
      <div class="row"><span>Всего (TG / MAX)</span><b>{{ nfmt(stats.users?.total) }} ({{ nfmt(stats.users?.tg) }}/{{ nfmt(stats.users?.max) }})</b></div>
      <div class="row"><span>Новых сегодня / 7д</span><b>{{ nfmt(stats.users?.new_today) }} / {{ nfmt(stats.users?.new_7d) }}</b></div>
      <div class="row"><span>Активных 7д</span><b>{{ nfmt(stats.users?.active_7d) }}</b></div>

      <div class="grp">📡 Контент</div>
      <div class="row"><span>Кросспостов (с комментами)</span><b>{{ nfmt(stats.content?.crossposts) }} ({{ nfmt(stats.content?.comments_enabled) }})</b></div>
      <div class="row"><span>Групп-мостов</span><b>{{ nfmt(stats.content?.bridge_groups) }}</b></div>
      <div class="row"><span>Бот в чатах (TG / MAX)</span><b>{{ nfmt(stats.content?.bot_chats_tg) }} / {{ nfmt(stats.content?.bot_chats_max) }}</b></div>

      <div class="grp">🛡 Антиспам</div>
      <div class="row"><span>Включён (TG / MAX)</span><b>{{ nfmt(stats.antispam?.enabled_tg) }} / {{ nfmt(stats.antispam?.enabled_max) }}</b></div>
      <div class="row"><span>Банов / мутов</span><b>{{ nfmt(stats.antispam?.bans) }} / {{ nfmt(stats.antispam?.mutes) }}</b></div>

      <div class="grp">📥 Импорт</div>
      <div class="row"><span>Задач выполнено</span><b>{{ nfmt(stats.import?.jobs_done) }}</b></div>
      <div class="row"><span>Постов перенесено</span><b>{{ nfmt(stats.import?.posts_imported) }}</b></div>
      <div class="row"><span>Баланс постов (всего)</span><b>{{ nfmt(stats.import?.posts_balance) }}</b></div>
    </template>
  </main>
</template>

<style scoped>
.wrap { max-width: 560px; margin: 0 auto; padding: 16px; }
.top { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; }
.top h1 { font-size: 20px; margin: 0; flex: 1; }
.back, .refresh {
  background: var(--surface); border: 1px solid var(--border); color: var(--text);
  border-radius: 10px; padding: 8px 12px; font: inherit; font-weight: 600; cursor: pointer;
}
.refresh { padding: 8px 12px; }
.muted { opacity: .7; }
.err { color: #e05656; }
.grp { font-weight: 700; font-size: 14px; margin: 18px 0 6px; }
.grp:first-of-type { margin-top: 0; }
.row { display: flex; justify-content: space-between; gap: 12px; font-size: 14px; padding: 7px 0; border-bottom: 1px solid var(--border); }
.row span { opacity: .7; }
.row b { white-space: nowrap; }
</style>
