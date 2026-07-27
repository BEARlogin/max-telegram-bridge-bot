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
function dt(ts) {
  if (!ts) return 'не было'
  return new Intl.DateTimeFormat('ru-RU', {
    timeZone: 'Europe/Moscow', day: '2-digit', month: '2-digit', year: 'numeric',
    hour: '2-digit', minute: '2-digit',
  }).format(new Date(ts * 1000)) + ' МСК'
}
function owner(a) {
  if (a.owner_username) return `@${a.owner_username}`
  return a.owner_name || `ID ${a.owner_id}`
}
function direction(v) {
  const source = (v.source_platform || '').toUpperCase()
  if (v.direction === 'source>vk') return `${source} → VK`
  if (v.direction === 'vk>source') return `VK → ${source}`
  return `${source} ↔ VK`
}
function endpoint(v) {
  if (v.endpoint_kind === 'community_wall') return `Сообщество VK ${v.community_id}`
  return v.endpoint_title || `${v.endpoint_kind} ${v.community_id}`
}
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
      <div class="grp">Выручка</div>
      <div class="row"><span>Всего</span><b>{{ nfmt(stats.billing?.revenue_total_rub) }} ₽</b></div>
      <div class="row"><span>— подписки</span><b>{{ nfmt(stats.billing?.revenue_sub_rub) }} ₽</b></div>
      <div class="row"><span>— посты</span><b>{{ nfmt(stats.billing?.revenue_posts_rub) }} ₽</b></div>
      <div class="row"><span>— дополнительные слоты</span><b>{{ nfmt(stats.billing?.revenue_mirror_rub) }} ₽</b></div>
      <div class="row"><span>За месяц</span><b>{{ nfmt(stats.billing?.revenue_month_rub) }} ₽</b></div>
      <div class="row"><span>MRR автопродлений</span><b>{{ nfmt(stats.billing?.mrr_rub) }} ₽</b></div>
      <div class="row"><span>Платежей</span><b>{{ nfmt(stats.billing?.payments_count) }}</b></div>

      <div class="grp">Подписки</div>
      <div class="row"><span>Платящих сейчас</span><b>{{ nfmt(stats.billing?.pro_paying) }}</b></div>
      <div class="row"><span>Автопродление включено</span><b>{{ nfmt(stats.billing?.sub_renewing) }}</b></div>
      <div class="row"><span>Активных записей (включая промо)</span><b>{{ nfmt(stats.billing?.sub_active) }}</b></div>
      <div class="row"><span>Отменённых (в периоде)</span><b>{{ nfmt(stats.billing?.sub_canceled) }}</b></div>
      <div class="row"><span>Триал активен</span><b>{{ nfmt(stats.billing?.sub_trial) }}</b></div>
      <div class="row"><span>Просрочка / pending</span><b>{{ nfmt(stats.billing?.sub_past_due) }} / {{ nfmt(stats.billing?.sub_pending) }}</b></div>
      <div class="row"><span>Пользовались триалом когда-либо</span><b>{{ nfmt(stats.billing?.trials_total) }}</b></div>
      <div class="row"><span>Платили когда-либо</span><b>{{ nfmt(stats.billing?.paid_users_total) }}</b></div>

      <div class="grp">Пользователи</div>
      <div class="row"><span>Аккаунтов бота (TG / MAX)</span><b>{{ nfmt(stats.users?.total) }} ({{ nfmt(stats.users?.tg) }}/{{ nfmt(stats.users?.max) }})</b></div>
      <div class="row"><span>Связано аккаунтов TG ↔ MAX</span><b>{{ nfmt(stats.billing?.account_links) }}</b></div>
      <div class="row"><span>Новых сегодня / 7д</span><b>{{ nfmt(stats.users?.new_today) }} / {{ nfmt(stats.users?.new_7d) }}</b></div>
      <div class="row"><span>Активных 7д</span><b>{{ nfmt(stats.users?.active_7d) }}</b></div>

      <div class="grp">Контент</div>
      <div class="row"><span>Кросспостов (с комментами)</span><b>{{ nfmt(stats.content?.crossposts) }} ({{ nfmt(stats.content?.comments_enabled) }})</b></div>
      <div class="row"><span>Групп-мостов</span><b>{{ nfmt(stats.content?.bridge_groups) }}</b></div>
      <div class="row"><span>Известных чатов (TG / MAX)</span><b>{{ nfmt(stats.content?.bot_chats_tg) }} / {{ nfmt(stats.content?.bot_chats_max) }}</b></div>

      <div class="grp">Антиспам</div>
      <div class="row"><span>Включён (TG / MAX)</span><b>{{ nfmt(stats.antispam?.enabled_tg) }} / {{ nfmt(stats.antispam?.enabled_max) }}</b></div>
      <div class="row"><span>Банов / мутов</span><b>{{ nfmt(stats.antispam?.bans) }} / {{ nfmt(stats.antispam?.mutes) }}</b></div>

      <div class="grp">Импорт</div>
      <div class="row"><span>Задач выполнено</span><b>{{ nfmt(stats.import?.jobs_done) }}</b></div>
      <div class="row"><span>Постов перенесено</span><b>{{ nfmt(stats.import?.posts_imported) }}</b></div>
      <div class="row"><span>Баланс постов (всего)</span><b>{{ nfmt(stats.import?.posts_balance) }}</b></div>

      <section class="vk-section">
        <div class="grp">VK</div>
        <p v-if="!stats.vk?.available" class="muted">База VK недоступна.</p>
        <template v-else>
          <div class="metric-grid">
            <article><span>Владельцев</span><b>{{ nfmt(stats.vk.owners_total) }}</b></article>
            <article><span>Сообществ подключено</span><b>{{ nfmt(stats.vk.accounts_enabled) }}</b></article>
            <article><span>Рабочих связок</span><b>{{ nfmt(stats.vk.active_bindings) }}</b></article>
            <article :class="{ danger: stats.vk.queue_pending }"><span>В очереди с ошибкой</span><b>{{ nfmt(stats.vk.queue_pending) }}</b></article>
          </div>
          <div class="row"><span>Сообществ без связки</span><b>{{ nfmt(stats.vk.accounts_without_binding) }}</b></div>
          <div class="row"><span>Связок всего / на паузе</span><b>{{ nfmt(stats.vk.bindings_total) }} / {{ nfmt(stats.vk.paused_bindings) }}</b></div>
          <div class="row"><span>Успешных доставок в VK / из VK</span><b>{{ nfmt(stats.vk.deliveries_to_vk) }} / {{ nfmt(stats.vk.deliveries_from_vk) }}</b></div>
          <div class="row"><span>Последняя успешная доставка</span><b>{{ dt(stats.vk.last_delivery_at) }}</b></div>

          <h2>Кто подключил VK</h2>
          <div v-if="!stats.vk.accounts?.length" class="empty">Пока никто.</div>
          <article v-for="a in stats.vk.accounts" :key="`${a.owner_id}:${a.community_id}`" class="detail">
            <div class="detail-head">
              <strong>{{ owner(a) }}</strong>
              <span :class="['status', a.enabled ? 'ok' : 'off']">{{ a.enabled ? 'подключено' : 'отключено' }}</span>
            </div>
            <div>Сообщество VK {{ a.community_id }}</div>
            <small>{{ nfmt(a.bindings) }} связок · подключено {{ dt(a.connected_at) }}</small>
          </article>

          <h2>Связки и доставка</h2>
          <div v-if="!stats.vk.bindings?.length" class="empty">Рабочих связок пока нет.</div>
          <article v-for="v in stats.vk.bindings" :key="v.id" class="detail">
            <div class="detail-head">
              <strong>{{ endpoint(v) }}</strong>
              <span :class="['status', v.paused ? 'off' : (v.queue_pending ? 'bad' : 'ok')]">
                {{ v.paused ? 'пауза' : (v.queue_pending ? 'ошибка' : 'работает') }}
              </span>
            </div>
            <div>{{ v.source_title || `${v.source_platform.toUpperCase()} ${v.source_chat_id}` }} · {{ direction(v) }}</div>
            <small>Владелец: {{ v.owner_username ? `@${v.owner_username}` : `ID ${v.owner_id}` }}</small>
            <small>Доставок: {{ nfmt(v.deliveries) }} · последняя {{ dt(v.last_delivery_at) }}</small>
            <small v-if="v.queue_pending" class="error-line">
              Очередь: {{ nfmt(v.queue_pending) }}, попыток: {{ nfmt(v.queue_attempts) }}, обновлено {{ dt(v.last_queue_at) }}
            </small>
            <small v-if="v.last_error" class="error-line">{{ v.last_error }}</small>
          </article>
        </template>
      </section>
    </template>
  </main>
</template>

<style scoped>
.wrap { max-width: 760px; margin: 0 auto; padding: 16px; }
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
.vk-section { margin-top: 22px; }
.metric-grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 8px; margin: 10px 0 14px; }
.metric-grid article { border: 1px solid var(--border); border-radius: 12px; padding: 12px; background: var(--surface); }
.metric-grid span { display: block; min-height: 34px; font-size: 12px; opacity: .7; }
.metric-grid b { display: block; margin-top: 5px; font-size: 22px; }
.metric-grid .danger { border-color: #d65959; }
h2 { margin: 20px 0 8px; font-size: 15px; }
.detail { border: 1px solid var(--border); border-radius: 12px; padding: 12px; margin: 8px 0; background: var(--surface); font-size: 14px; }
.detail-head { display: flex; justify-content: space-between; gap: 10px; align-items: center; margin-bottom: 7px; }
.detail small { display: block; margin-top: 5px; opacity: .7; overflow-wrap: anywhere; }
.status { border-radius: 999px; padding: 3px 8px; font-size: 11px; white-space: nowrap; }
.status.ok { color: #207d49; background: rgba(45, 178, 102, .14); }
.status.off { color: #786a2c; background: rgba(205, 172, 42, .15); }
.status.bad { color: #a43838; background: rgba(214, 89, 89, .15); }
.error-line { color: #b64242; opacity: 1 !important; }
.empty { border: 1px dashed var(--border); border-radius: 12px; padding: 14px; opacity: .7; }
@media (max-width: 620px) {
  .metric-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .row { align-items: flex-start; }
  .row b { white-space: normal; text-align: right; }
}
</style>
