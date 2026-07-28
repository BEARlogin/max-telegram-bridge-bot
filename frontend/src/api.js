// API мини-аппа комментариев.
//
// post_id приходит во вьюху из стартового параметра мини-аппа (его кладёт бот в
// кнопку OPEN_APP под постом — payload вида "<maxChatID>:<maxMid>").
// initData (подпись юзера от MAX) шлём в заголовке X-Init-Data — бэкенд по ней
// определит, кто комментирует. TODO: достать реальный initData из MAX WebApp SDK.

// initData в location.hash: Telegram кладёт как tgWebAppData, MAX — как WebAppData.
// Один мини-апп работает и там, и там.
export function initData() {
  try {
    const h = new URLSearchParams((location.hash || '').replace(/^#/, ''))
    return h.get('tgWebAppData') || h.get('WebAppData') || ''
  } catch {
    return ''
  }
}

// База API = база мини-аппа (Vite BASE_URL = '/commenter/') + 'api'.
const API = `${import.meta.env.BASE_URL}api`

// Обычный браузер авторизуется HttpOnly-cookie, мини-апп — X-Init-Data.
// credentials явно оставляем включёнными, чтобы один API обслуживал оба режима.
function request(url, options = {}) {
  const headers = new Headers(options.headers || {})
  const init = initData()
  if (init) headers.set('X-Init-Data', init)
  return fetch(url, { ...options, headers, credentials: 'same-origin' })
}

export async function logoutCabinet() {
  const r = await request(`${API}/cabinet/logout`, { method: 'POST' })
  if (!r.ok) throw new Error('logout failed')
  return r.json()
}

export async function subscribePro() {
  const r = await fetch(`${API}/billing/subscribe`, { method: 'POST', headers: { 'X-Init-Data': initData() } })
  if (!r.ok) throw new Error('subscribe failed')
  return r.json() // { url }
}

// Активировать бесплатный триал PRO (3 дня, один раз).
export async function startTrial() {
  const r = await fetch(`${API}/billing/trial`, { method: 'POST', headers: { 'X-Init-Data': initData() } })
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'trial failed')
  return r.json()
}

// Отменить PRO-подписку (списаний больше не будет; PRO до конца оплаченного периода).
export async function cancelPro() {
  const r = await fetch(`${API}/billing/cancel`, { method: 'POST', headers: { 'X-Init-Data': initData() } })
  if (!r.ok) throw new Error('cancel failed')
  return r.json()
}

// Возобновить подписку без новой оплаты, если есть сохранённая карта.
// result: 'resumed' (мгновенно) | 'charging' (списание по карте) | 'need_card' (нужна оплата).
export async function resumePro() {
  const r = await fetch(`${API}/billing/resume`, { method: 'POST', headers: { 'X-Init-Data': initData() } })
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'resume failed')
  return r.json()
}

export async function getSettings() {
  const r = await request(`${API}/settings`)
  if (!r.ok) {
    const e = new Error('settings failed')
    e.status = r.status
    throw e
  }
  return r.json()
}

// Докупка пакета постов импорта (T-Bank). Возвращает { url } для перехода на оплату.
export async function buyPosts(posts) {
  const r = await fetch(`${API}/posts/buy`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Init-Data': initData() },
    body: JSON.stringify({ posts }),
  })
  if (!r.ok) throw new Error('buy failed')
  return r.json()
}

// Удалить связку кросспоста (только владелец).
export async function deleteCrosspost(maxChatId) {
  const r = await fetch(`${API}/crosspost/delete`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Init-Data': initData() },
    body: JSON.stringify({ max_chat_id: maxChatId }),
  })
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'delete failed')
  return r.json()
}

// Вкл/выкл комментарии под постами канала (PRO).
export async function setComments(maxChatId, enabled) {
  const r = await fetch(`${API}/crosspost/comments`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Init-Data': initData() },
    body: JSON.stringify({ max_chat_id: maxChatId, enabled }),
  })
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'save failed')
  return r.json()
}

// Сохранить замены (правила) для связки. replacements = { 'tg>max': [...], 'max>tg': [...] }.
export async function setReplacements(maxChatId, replacements) {
  const r = await fetch(`${API}/crosspost/replacements`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Init-Data': initData() },
    body: JSON.stringify({ max_chat_id: maxChatId, replacements }),
  })
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'save failed')
  return r.json()
}

// Префикс [TG]/[MAX] для bridge-группы (вкл/выкл).
export async function setGroupPrefix(tgChatId, enabled) {
  const r = await fetch(`${API}/group/prefix`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Init-Data': initData() },
    body: JSON.stringify({ tg_chat_id: tgChatId, enabled }),
  })
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'save failed')
  return r.json()
}

// Приветствие новых участников Telegram-группы (PRO).
export async function setGroupWelcome(tgChatId, enabled, text = '') {
  const r = await fetch(`${API}/group/welcome`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Init-Data': initData() },
    body: JSON.stringify({ tg_chat_id: tgChatId, enabled, text }),
  })
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'save failed')
  return r.json()
}

// Антиспам bridge-группы (PRO): вкл/выкл + настройки.
export async function setGroupAntispam(tgChatId, opts) {
  const r = await fetch(`${API}/group/antispam`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Init-Data': initData() },
    body: JSON.stringify({ tg_chat_id: tgChatId, ...opts }),
  })
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'save failed')
  return r.json()
}

// Кастомные правила антиспама группы: добавить / удалить. Возвращают обновлённый список rules.
export async function addGroupRule(tgChatId, rule) {
  const r = await fetch(`${API}/group/antispam/rule/add`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Init-Data': initData() },
    body: JSON.stringify({ tg_chat_id: tgChatId, ...rule }),
  })
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'add failed')
  return r.json()
}
export async function delGroupRule(tgChatId, rid) {
  const r = await fetch(`${API}/group/antispam/rule/del`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Init-Data': initData() },
    body: JSON.stringify({ tg_chat_id: tgChatId, rid }),
  })
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'del failed')
  return r.json()
}

// Разорвать связку bridge-группы.
export async function unbridgeGroup(tgChatId) {
  const r = await fetch(`${API}/group/unbridge`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Init-Data': initData() },
    body: JSON.stringify({ tg_chat_id: tgChatId }),
  })
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'unbridge failed')
  return r.json()
}

// Направление пересылки моста группы: both | tg>max | max>tg (PRO).
export async function setGroupDirection(tgChatId, direction) {
  const r = await fetch(`${API}/group/direction`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Init-Data': initData() },
    body: JSON.stringify({ tg_chat_id: tgChatId, direction }),
  })
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'save failed')
  return r.json()
}

// Антиспам связки (PRO): вкл/выкл + настройки (режим, дилей ссылок, порог доверия).
export async function setAntispam(maxChatId, opts) {
  const r = await fetch(`${API}/crosspost/antispam`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Init-Data': initData() },
    body: JSON.stringify({ max_chat_id: maxChatId, ...opts }),
  })
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'save failed')
  return r.json()
}

// Перепроверить права бота (после добавления его админом в группу). kind: crosspost|group.
export async function checkBotAdmin(kind, id) {
  const r = await fetch(`${API}/antispam/check`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Init-Data': initData() },
    body: JSON.stringify({ kind, id }),
  })
  if (!r.ok) throw new Error('check failed')
  return r.json() // { bot_admin, discussion_linked }
}

// Вкл/выкл синхронизацию правок для связки.
export async function setSyncEdits(maxChatId, enabled) {
  const r = await fetch(`${API}/crosspost/sync-edits`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Init-Data': initData() },
    body: JSON.stringify({ max_chat_id: maxChatId, enabled }),
  })
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'save failed')
  return r.json()
}

// Пауза/возобновление связки кросспоста (на паузе посты не пересылаются, связка цела).
export async function setCrosspostPaused(maxChatId, paused) {
  const r = await fetch(`${API}/crosspost/pause`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Init-Data': initData() },
    body: JSON.stringify({ max_chat_id: maxChatId, paused }),
  })
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'save failed')
  return r.json()
}

// Пауза/возобновление связки группы.
export async function setGroupPaused(tgChatId, paused) {
  const r = await fetch(`${API}/group/pause`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Init-Data': initData() },
    body: JSON.stringify({ tg_chat_id: tgChatId, paused }),
  })
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'save failed')
  return r.json()
}

// Привязка MAX↔TG: получить одноразовый код (вводится боту в TG как /link <код>).
export async function linkStart() {
  const r = await fetch(`${API}/link/start`, { method: 'POST', headers: { 'X-Init-Data': initData() } })
  if (!r.ok) throw new Error('link failed')
  return r.json() // { code, ttl_min }
}

// Журнал заблокированных антиспамом.
export async function getBlocks() {
  const r = await fetch(`${API}/blocks`, { headers: { 'X-Init-Data': initData() } })
  if (!r.ok) throw new Error('blocks failed')
  return r.json() // { blocks: [...] }
}

// Разбанить (TG — снять мут, MAX — вернуть участника).
export async function unbanUser(platform, chatId, userId) {
  const r = await fetch(`${API}/block/unban`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Init-Data': initData() },
    body: JSON.stringify({ platform, chat_id: chatId, user_id: userId }),
  })
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'unban failed')
  return r.json()
}

// Удалить зеркальную связку (владелец связки или донора).
export async function deleteMirror(platform, srcChat, dstChat) {
  const r = await fetch(`${API}/mirror/delete`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Init-Data': initData() },
    body: JSON.stringify({ platform, src_chat: srcChat, dst_chat: dstChat }),
  })
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'delete failed')
  return r.json()
}

export async function setVKDirection(id, direction) {
  const r = await request(`${API}/vk/direction`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ id, direction }),
  })
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'save failed')
  return r.json()
}

export async function startVKConnect() {
  const r = await request(`${API}/vk/connect`, { method: 'POST' })
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'Не удалось подключить VK')
  return r.json()
}

export async function getVKChats() {
  const r = await request(`${API}/vk/chats`, { method: 'POST' })
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'Не удалось получить беседы VK')
  return r.json()
}

export async function createVKChatBinding(accountId, peerId, platform, sourceChatId) {
  const r = await request(`${API}/vk/chat-bind`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      account_id: accountId, peer_id: peerId,
      platform, source_chat_id: sourceChatId,
    }),
  })
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'Не удалось создать связку')
  return r.json()
}

export async function setVKPaused(id, paused) {
  const r = await request(`${API}/vk/pause`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ id, paused }),
  })
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'save failed')
  return r.json()
}

export async function deleteVKBinding(id) {
  const r = await request(`${API}/vk/delete`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ id }),
  })
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'delete failed')
  return r.json()
}

// Расчёт покупки слотов (без платежа): прорейт, конец периода, будущий рекуррент.
export async function previewSlots(groups) {
  const r = await fetch(`${API}/slots/preview`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Init-Data': initData() },
    body: JSON.stringify({ groups }),
  })
  if (!r.ok) throw new Error('preview failed')
  return r.json()
}

// Докупка слотов тарифа (мосты/зеркала/каналы). Возвращает { pay_url }.
export async function buySlots(groups) {
  const r = await fetch(`${API}/slots/buy`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Init-Data': initData() },
    body: JSON.stringify({ groups }),
  })
  if (!r.ok) throw new Error('buy slots failed')
  return r.json()
}

// Уменьшение доп-слотов (без возврата; рекуррент снизится со следующего периода).
export async function reduceSlots(groups) {
  const r = await fetch(`${API}/slots/reduce`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Init-Data': initData() },
    body: JSON.stringify({ groups }),
  })
  if (!r.ok) throw new Error('reduce failed')
  return r.json()
}

// Бизнес-показатели для админ-панели (только владелец).
export async function adminStats() {
  const r = await fetch(`${API}/admin/stats`, { headers: { 'X-Init-Data': initData() } })
  if (!r.ok) throw new Error('stats failed')
  return r.json()
}

export async function adminCampaigns() {
  const r = await fetch(`${API}/admin/campaigns`, { headers: { 'X-Init-Data': initData() } })
  if (!r.ok) throw new Error('campaigns failed')
  return r.json()
}

export async function createAdminCampaign(name, source, note) {
  const r = await fetch(`${API}/admin/campaigns`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Init-Data': initData() },
    body: JSON.stringify({ name, source, note }),
  })
  const data = await r.json().catch(() => ({}))
  if (!r.ok) throw new Error(data.error || 'create campaign failed')
  return data
}

export async function setAdminCampaignActive(id, active) {
  const r = await fetch(`${API}/admin/campaigns/active`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Init-Data': initData() },
    body: JSON.stringify({ id, active }),
  })
  const data = await r.json().catch(() => ({}))
  if (!r.ok) throw new Error(data.error || 'update campaign failed')
  return data
}

export async function whoami() {
  const r = await fetch(`${API}/whoami`, { headers: { 'X-Init-Data': initData() } })
  if (!r.ok) throw new Error('whoami failed')
  return r.json()
}

export async function fetchComments(postId) {
  const r = await fetch(`${API}/comments?post_id=${encodeURIComponent(postId)}`)
  if (!r.ok) throw new Error('fetch failed')
  const j = await r.json()
  return j.comments || []
}

export async function addComment(postId, text, replyTo = 0) {
  const r = await fetch(`${API}/comments`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Init-Data': initData() },
    body: JSON.stringify({ post_id: postId, text, reply_to: replyTo }),
  })
  if (!r.ok) throw new Error('post failed')
  return r.json()
}
