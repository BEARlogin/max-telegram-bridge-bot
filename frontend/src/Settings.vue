<script setup>
// Кабинет мини-аппа: управление связками (комменты, синк правок, замены, удаление),
// баланс импорта/докупка, PRO. Авторизация — по подписи initData.
import { ref, onMounted, onBeforeUnmount, reactive, computed } from 'vue'
import {
  getSettings, subscribePro, cancelPro, resumePro, startTrial, buyPosts,
  deleteCrosspost, setComments, setReplacements, setSyncEdits,
  setGroupPrefix, setGroupWelcome, unbridgeGroup, setAntispam, setGroupAntispam, linkStart,
  setCrosspostPaused, setGroupPaused, setGroupDirection,
  getBlocks, unbanUser, checkBotAdmin, initData,
  addGroupRule, delGroupRule, deleteMirror, buySlots, previewSlots, reduceSlots,
  logoutCabinet, startVKConnect, setVKDirection, setVKPaused, deleteVKBinding,
  getVKChats, createVKChatBinding,
} from './api.js'

const loading = ref(true)
const error = ref('')
const noAuth = ref(false)
const me = ref(null)
const isPro = ref(false)
const subStatus = ref('') // '' | active | canceled | trial | past_due
const subUntil = ref(0)
const trialUsed = ref(false)
const recurrentConsent = ref(false) // согласие на регулярные списания (обязательно для рекуррента T-Bank)
const cardPan = ref('')
const hasRebill = ref(false)
const isAdmin = ref(false)
const emit = defineEmits(['admin'])
const proOpen = ref(false)
const tab = ref('channels') // вкладки: channels | groups
const crossposts = ref([])
const groups = ref([])
const blocks = ref([])
const mirrors = ref([])
const vkBindings = ref([])
const vkCommunities = ref([])
const vkSources = ref([])
const vkBusy = reactive({})
const vkConfirm = reactive({})
const vkConnecting = ref(false)
const vkWizardOpen = ref(false)
const vkChats = ref([])
const vkChatsLoading = ref(false)
const vkChatsLoaded = ref(false)
const vkChatsFailed = ref([])
const vkChatLink = ref('')
const vkSelectedChat = ref('')
const vkSelectedSource = ref('')
const vkWizardError = ref('')
const vkCreating = ref(false)
const slots = ref(null) // { used, base, extra, limit }
const mirrorBusy = reactive({})
const slotsBuying = ref(false)
// Покупка слотов: промежуточный экран (кол-во, прорейт, согласие на рост рекуррента).
const slotsOpen = ref(false)
const slotQty = ref(1)
const slotPreview = ref(null) // { amount_kopecks, paid_until, slot_price_kopecks, next_recurrent_kopecks }
const slotPreviewErr = ref('')
const slotConsent = ref(false)
// Уменьшение слотов: отдельная мини-панель (без возврата, рекуррент со след. периода).
const slotReduceOpen = ref(false)
const slotReduceQty = ref(1)
const slotReducing = ref(false)
const blockBusy = reactive({})
const groupUi = reactive({}) // { [tg_chat_id]: { busy, confirm } }
const importBalance = ref(0)
const proPriceKopecks = ref(99000)
const slotPriceKopecks = ref(9900)
const postPackages = ref([])
const subscribing = ref(false)
const buying = ref(false)
const toast = ref('')
// Инструкция «как связать канал/группу» (линковка — через бота)
const linkOpen = ref(false)
const grpHelp = ref(false)
// Привязка MAX↔TG аккаунта (баланс постов/PRO — по TG-id)
const acctPlat = ref('')
const accLinked = ref(true)
const accCode = ref('')
const accBusy = ref(false)
const tgBotUrl = 'https://t.me/MaxTelegramBridgeBot'
const maxBotUrl = 'https://max.ru/id710708943262_4_bot'
// Транзиентное состояние карточек (open/busy/confirm/edit) — вне объектов API.
const ui = reactive({})
const browserMode = !initData()
const loggingOut = ref(false)
const cabinetSections = [
  { id: 'overview', path: '', label: 'Обзор' },
  { id: 'channels', path: 'channels', label: 'Каналы' },
  { id: 'groups', path: 'groups', label: 'Группы' },
  { id: 'mirrors', path: 'mirrors', label: 'Зеркала' },
  { id: 'vk', path: 'vk', label: 'VK' },
  { id: 'moderation', path: 'moderation', label: 'Модерация' },
  { id: 'billing', path: 'billing', label: 'Тариф и слоты' },
  { id: 'import', path: 'import', label: 'Импорт истории' },
]
function sectionFromLocation() {
  if (!browserMode) return 'overview'
  const prefix = '/cabinet/'
  const path = location.pathname === '/cabinet' ? '' :
    (location.pathname.startsWith(prefix) ? location.pathname.slice(prefix.length).split('/')[0] : '')
  return cabinetSections.find(x => x.path === path)?.id || 'overview'
}
const currentSection = ref(sectionFromLocation())
const currentSectionLabel = computed(() => cabinetSections.find(x => x.id === currentSection.value)?.label || 'Обзор')
const totalConnections = computed(() => crossposts.value.length + groups.value.length + mirrors.value.length + vkBindings.value.length)
const pausedConnections = computed(() =>
  crossposts.value.filter(x => x.paused).length + groups.value.filter(x => x.paused).length +
  vkBindings.value.filter(x => x.paused).length)
const freeSlots = computed(() => slots.value ? Math.max(0, slots.value.limit - slots.value.used) : 0)
const attentionCount = computed(() => pausedConnections.value + blocks.value.length + (!accLinked.value ? 1 : 0))

const platform = (() => {
  const h = new URLSearchParams((location.hash || '').replace(/^#/, ''))
  if (h.get('tgWebAppData')) return 'Telegram'
  if (h.get('WebAppData')) return 'MAX'
  return '—'
})()

function flash(msg) {
  toast.value = msg
  setTimeout(() => { if (toast.value === msg) toast.value = '' }, 3500)
}

function cpKey(c) { return c.tg_chat_id + '-' + c.max_chat_id }
function uiOf(c) {
  const k = cpKey(c)
  if (!ui[k]) ui[k] = { open: false, busy: false, confirm: false, repl: null, dirty: false, sec: {} }
  return ui[k]
}
function dirLabel(d) {
  return d === 'max>tg' ? 'MAX → TG' : d === 'tg>max' ? 'TG → MAX' : 'TG ↔ MAX'
}

async function load() {
  try {
    const s = await getSettings()
    me.value = s.user
    isPro.value = !!s.pro
    subStatus.value = s.sub_status || ''
    subUntil.value = s.sub_until || 0
    trialUsed.value = !!s.trial_used
    cardPan.value = s.card_pan || ''
    hasRebill.value = !!s.has_rebill
    isAdmin.value = !!s.admin
    crossposts.value = s.crossposts || []
    groups.value = s.groups || []
    mirrors.value = s.mirrors || []
    vkBindings.value = s.vk_bindings || []
    vkCommunities.value = s.vk_communities || []
    vkSources.value = s.vk_sources || []
    slots.value = s.slots || null
    importBalance.value = s.import_balance || 0
    proPriceKopecks.value = s.pro_price_kopecks || 99000
    slotPriceKopecks.value = s.slot_price_kopecks || 9900
    postPackages.value = s.post_packages || []
    acctPlat.value = s.platform || ''
    accLinked.value = s.linked !== false
    error.value = ''
    getBlocks().then(b => { blocks.value = b.blocks || [] }).catch(() => {})
  } catch (e) {
    if (browserMode && e?.status === 401) noAuth.value = true
    else error.value = 'Не удалось загрузить данные. Проверьте соединение и попробуйте ещё раз.'
  } finally {
    loading.value = false
  }
}

function onCabinetPopState() {
  currentSection.value = sectionFromLocation()
  closeAll()
}

onMounted(() => {
  if (browserMode) window.addEventListener('popstate', onCabinetPopState)
  load()
})
onBeforeUnmount(() => window.removeEventListener('popstate', onCabinetPopState))

function navigate(section) {
  const target = cabinetSections.find(x => x.id === section) || cabinetSections[0]
  currentSection.value = target.id
  closeAll()
  if (browserMode) {
    const next = `/cabinet/${target.path ? target.path : ''}`
    if (location.pathname !== next) history.pushState({}, '', next)
    window.scrollTo({ top: 0, behavior: 'auto' })
  } else if (['channels', 'groups', 'mirrors', 'blocks'].includes(section)) {
    tab.value = section
  }
}

function onMobileSection(event) {
  navigate(event.target.value)
}

async function logout() {
  if (loggingOut.value) return
  loggingOut.value = true
  try {
    await logoutCabinet()
    noAuth.value = true
    me.value = null
  } catch {
    flash('Не удалось выйти')
  } finally {
    loggingOut.value = false
  }
}

function vkSource(v) {
  return v.source_title || `${v.source_platform.toUpperCase()} ${v.source_chat_id}`
}
function vkTarget(v) {
  if (v.kind === 'community_wall') return `Публикации сообщества VK ${v.community_id}`
  if (v.kind === 'profile_wall') return v.title || 'Стена профиля VK'
  if (v.kind === 'board_topic') return v.title || 'Обсуждение VK'
  return v.title || `Беседа сообщества VK ${v.community_id}`
}
function vkDirection(v) {
  const source = v.source_platform.toUpperCase()
  if (v.direction === 'source>vk') return `${source} → VK`
  if (v.direction === 'vk>source') return `VK → ${source}`
  return `${source} ↔ VK`
}
async function connectVK() {
  if (vkConnecting.value) return
  vkConnecting.value = true
  try {
    const result = await startVKConnect()
    if (!result.url) throw new Error('VK не вернул ссылку авторизации')
    location.assign(result.url)
  } catch (e) {
    flash(e.message || 'Не удалось начать подключение VK')
    vkConnecting.value = false
  }
}
function vkChatKey(chat) {
  return `${chat.account_id}:${chat.peer_id}`
}
function vkSourceKey(source) {
  return `${source.platform}:${source.chat_id}`
}
const selectedVKChat = computed(() => vkChats.value.find(x => vkChatKey(x) === vkSelectedChat.value) || null)
const selectedVKSource = computed(() => vkSources.value.find(x => vkSourceKey(x) === vkSelectedSource.value) || null)
function peerFromVKLink(raw) {
  const value = String(raw || '').trim()
  if (!value) return 0
  try {
    const url = new URL(value.includes('://') ? value : `https://${value}`)
    if (!['vk.com', 'vk.ru', 'www.vk.com', 'www.vk.ru'].includes(url.hostname.toLowerCase())) return 0
    const sel = url.searchParams.get('sel') || ''
    const match = sel.match(/^c(\d+)$/i)
    if (!match) return 0
    return 2000000000 + Number(match[1])
  } catch {
    const match = value.match(/^c(\d+)$/i)
    return match ? 2000000000 + Number(match[1]) : 0
  }
}
function findVKChatByLink(showError = true) {
  const peer = peerFromVKLink(vkChatLink.value)
  if (!peer) {
    if (showError) vkWizardError.value = 'Вставьте ссылку вида https://vk.ru/im?sel=c160'
    return false
  }
  const found = vkChats.value.find(x => Number(x.peer_id) === peer)
  if (!found) {
    if (showError) vkWizardError.value = 'Эта беседа пока не видна сообществу. Добавьте сообщество в беседу, отправьте там сообщение и обновите список.'
    return false
  }
  vkSelectedChat.value = vkChatKey(found)
  vkWizardError.value = ''
  return true
}
async function loadVKChats() {
  if (vkChatsLoading.value) return
  vkChatsLoading.value = true
  vkWizardError.value = ''
  try {
    const result = await getVKChats()
    vkChats.value = result.chats || []
    vkChatsFailed.value = result.failed_communities || []
    vkChatsLoaded.value = true
    if (vkChatLink.value) findVKChatByLink(false)
  } catch (e) {
    vkWizardError.value = e.message || 'Не удалось получить беседы VK'
  } finally {
    vkChatsLoading.value = false
  }
}
async function openVKWizard() {
  vkWizardOpen.value = true
  vkWizardError.value = ''
  if (!vkChatsLoaded.value) await loadVKChats()
}
function closeVKWizard() {
  if (vkCreating.value) return
  vkWizardOpen.value = false
  vkWizardError.value = ''
}
async function createVKChat() {
  if (vkCreating.value) return
  if (!selectedVKChat.value) {
    vkWizardError.value = 'Выберите беседу VK.'
    return
  }
  if (!selectedVKSource.value) {
    vkWizardError.value = 'Выберите группу Telegram или MAX.'
    return
  }
  vkCreating.value = true
  vkWizardError.value = ''
  try {
    await createVKChatBinding(
      selectedVKChat.value.account_id, selectedVKChat.value.peer_id,
      selectedVKSource.value.platform, selectedVKSource.value.chat_id,
    )
    vkWizardOpen.value = false
    vkSelectedChat.value = ''
    vkSelectedSource.value = ''
    await load()
    flash('Беседа VK связана. Направление: в обе стороны')
  } catch (e) {
    vkWizardError.value = e.message || 'Не удалось создать связку'
  } finally {
    vkCreating.value = false
  }
}
async function changeVKDirection(v, direction) {
  if (vkBusy[v.id]) return
  vkBusy[v.id] = true
  try {
    await setVKDirection(v.id, direction)
    v.direction = direction
    flash('Направление сохранено')
  } catch (e) { flash(e.message || 'Не удалось сохранить') }
  finally { vkBusy[v.id] = false }
}
async function toggleVKPause(v) {
  if (vkBusy[v.id]) return
  vkBusy[v.id] = true
  try {
    await setVKPaused(v.id, !v.paused)
    v.paused = !v.paused
    flash(v.paused ? 'VK-связка на паузе' : 'VK-связка работает')
  } catch (e) { flash(e.message || 'Не удалось сохранить') }
  finally { vkBusy[v.id] = false }
}
async function removeVK(v) {
  if (vkBusy[v.id]) return
  vkBusy[v.id] = true
  try {
    await deleteVKBinding(v.id)
    vkBindings.value = vkBindings.value.filter(x => x.id !== v.id)
    flash('VK-связка удалена')
  } catch (e) { flash(e.message || 'Не удалось удалить') }
  finally { vkBusy[v.id] = false; vkConfirm[v.id] = false }
}

// Master-detail: открытая карточка = «экран настроек» одного объекта. anyOpen прячет
// список/шапку/табы, closeAll — кнопка «Назад». Карточки single-open (раскрытие одной
// закрывает остальные), чтобы на экране всегда был ровно один объект.
const anyOpen = computed(() => {
  for (const k in ui) if (ui[k] && ui[k].open) return true
  for (const k in groupUi) if (groupUi[k] && groupUi[k].open) return true
  return false
})
function closeAll() {
  for (const k in ui) ui[k].open = false
  for (const k in groupUi) groupUi[k].open = false
}

// Авто-сохранение политики антиспама (единый UX: тогглы и так сохраняются сразу,
// теперь и форма — без кнопки «Сохранить»). Debounce + детект изменений: шлём только
// если что-то реально поменялось (иначе тапы внутри панели не дёргают сеть/тост).
const asSaveTimers = {}
const asLastSaved = {}
function scheduleSaveC(c) {
  if (!c.antispam) return
  const k = 'c' + cpKey(c)
  clearTimeout(asSaveTimers[k])
  asSaveTimers[k] = setTimeout(async () => {
    const payload = { enabled: true, ...asOf(c) }
    const snap = JSON.stringify(payload)
    if (snap === asLastSaved[k]) return
    try { await setAntispam(c.max_chat_id, payload); asLastSaved[k] = snap; flash('Сохранено ✓') }
    catch (e) { flash(e.message || 'Ошибка') }
  }, 700)
}
function scheduleSaveG(g) {
  if (!g.antispam) return
  const k = 'g' + g.tg_chat_id
  clearTimeout(asSaveTimers[k])
  asSaveTimers[k] = setTimeout(async () => {
    const payload = { enabled: true, ...gAsOf(g) }
    const snap = JSON.stringify(payload)
    if (snap === asLastSaved[k]) return
    try { await setGroupAntispam(g.tg_chat_id, payload); asLastSaved[k] = snap; flash('Сохранено ✓') }
    catch (e) { flash(e.message || 'Ошибка') }
  }, 700)
}
function toggleCard(c) {
  const u = uiOf(c)
  const willOpen = !u.open
  if (willOpen) closeAll()
  u.open = willOpen
  if (willOpen) asLastSaved['c' + cpKey(c)] = JSON.stringify({ enabled: true, ...asOf(c) })
  if (u.open && !u.repl) {
    // готовим редактируемую копию замен
    const r = c.replacements || {}
    const norm = (arr) => [...(arr || [])].map((x) => ({ from: x.from || '', to: x.to || '', target: x.target === 'links' ? 'links' : 'all', regex: !!x.regex }))
    u.repl = { tgmax: norm(r['tg>max']), maxtg: norm(r['max>tg']) }
    u.dirty = false
  }
}

function gUi(g) {
  if (!groupUi[g.tg_chat_id]) {
    groupUi[g.tg_chat_id] = {
      busy: false,
      confirm: false,
      open: false,
      panel: g.standalone ? 'antispam' : 'bridge',
      sec: {},
    }
  }
  return groupUi[g.tg_chat_id]
}
function toggleGroupCard(g) {
  const u = gUi(g)
  const willOpen = !u.open
  if (willOpen) closeAll()
  u.open = willOpen
  if (willOpen && u.welcomeText === undefined) u.welcomeText = g.welcome_text || ''
  if (willOpen) asLastSaved['g' + g.tg_chat_id] = JSON.stringify({ enabled: true, ...gAsOf(g) })
}
async function saveGroupWelcome(g) {
  const u = gUi(g)
  if (u.busy) return
  if (!isPro.value) { flash('Приветствие — PRO-функция'); return }
  const text = String(u.welcomeText || '').trim()
  if (!text) { flash('Введите текст приветствия'); return }
  if ([...text].length > 1000) { flash('Приветствие: максимум 1000 символов'); return }
  u.busy = true
  try {
    const r = await setGroupWelcome(g.tg_chat_id, true, text)
    g.welcome_text = r.welcome_text || ''
    u.welcomeText = g.welcome_text
    flash('Приветствие сохранено')
  } catch (e) { flash(e.message || 'Не удалось сохранить приветствие') }
  finally { u.busy = false }
}
async function disableGroupWelcome(g) {
  const u = gUi(g)
  if (u.busy) return
  u.busy = true
  try {
    await setGroupWelcome(g.tg_chat_id, false, '')
    g.welcome_text = ''
    u.welcomeText = ''
    flash('Приветствие выключено')
  } catch (e) { flash(e.message || 'Не удалось выключить приветствие') }
  finally { u.busy = false }
}
async function toggleGroupPrefix(g) {
  const u = gUi(g)
  if (u.busy) return
  u.busy = true
  const next = !g.prefix
  try { await setGroupPrefix(g.tg_chat_id, next); g.prefix = next }
  catch (e) { flash(e.message || 'Не удалось сохранить') }
  finally { u.busy = false }
}
async function toggleGroupPause(g) {
  const u = gUi(g)
  if (u.busy) return
  u.busy = true
  const next = !g.paused
  try { await setGroupPaused(g.tg_chat_id, next); g.paused = next; flash(next ? 'Связка на паузе' : 'Связка возобновлена') }
  catch (e) { flash(e.message || 'Не удалось сохранить') }
  finally { u.busy = false }
}
async function setGroupDir(g, dir) {
  if ((g.direction || 'both') === dir) return
  if (!isPro.value) { flash('Направление пересылки — PRO-функция'); return }
  const u = gUi(g)
  if (u.busy) return
  u.busy = true
  const prev = g.direction
  g.direction = dir
  try { await setGroupDirection(g.tg_chat_id, dir); flash('Направление обновлено') }
  catch (e) { g.direction = prev; flash(e.message || 'Не удалось сохранить') }
  finally { u.busy = false }
}
async function doUnbridge(g) {
  const u = gUi(g)
  if (u.busy) return
  u.busy = true
  try {
    await unbridgeGroup(g.tg_chat_id)
    groups.value = groups.value.filter(x => x.tg_chat_id !== g.tg_chat_id)
    flash('Связка группы разорвана')
  } catch (e) { flash(e.message || 'Не удалось разорвать') }
  finally { u.busy = false; u.confirm = false }
}
// Форма нового правила (на группу) + операции.
function gRuleForm(g) {
  const u = gUi(g)
  if (!u.rule) u.rule = { descr: '', keywords: '', action: 'mute', warns: 0 }
  return u.rule
}
async function addAsRule(g) {
  const f = gRuleForm(g)
  if (!f.descr.trim()) { flash('Опишите правило'); return }
  try {
    const r = await addGroupRule(g.tg_chat_id, { descr: f.descr, keywords: f.keywords, action: f.action, warns: Number(f.warns) || 0 })
    g.rules = r.rules || []
    f.descr = ''; f.keywords = ''; f.warns = 0
    flash('Правило добавлено ✓')
  } catch (e) { flash(e.message) }
}
async function delAsRule(g, rid) {
  try { const r = await delGroupRule(g.tg_chat_id, rid); g.rules = r.rules || [] } catch (e) { flash(e.message) }
}

function gAsOf(g) {
  const u = gUi(g)
  if (!u.as) u.as = { mode: g.antispam_mode || 'enforce', allow_links: !!g.allow_links, link_delay_h: 24, trust_msgs: 3, strike_limit: g.strike_limit || 2, ban_after: g.ban_after || 3, action: g.action || 'mute', mute_minutes: g.mute_minutes || 60, warn: !!g.warn, notify: g.notify || 'ban', captcha: !!g.captcha, antiraid: !!g.antiraid, profile_guard: !!g.profile_guard, block_words: g.block_words || '', block_cats: g.block_cats || '', del_service: !!g.del_service, tone: g.tone || 'strict' }
  return u.as
}
async function toggleGroupAntispam(g) {
  const u = gUi(g)
  if (u.busy) return
  if (!isPro.value) { flash('Антиспам — PRO-функция'); return }
  u.busy = true
  const next = !g.antispam
  try {
    const r = await setGroupAntispam(g.tg_chat_id, { enabled: next, ...gAsOf(g) })
    g.antispam = !!r.ok
    g.bot_admin = r.bot_admin
    if (next && r.bot_admin === false) {
      flash('⚠️ Антиспам включён, но бот не админ в группе — добавьте его админом, иначе модерация не сработает.')
    } else {
      flash(g.antispam ? 'Антиспам в группе включён' : 'Антиспам выключен')
    }
  } catch (e) { flash(e.message || 'Ошибка') } finally { u.busy = false }
}
async function saveGroupAntispam(g) {
  const u = gUi(g)
  if (u.busy || !g.antispam) return
  u.busy = true
  try {
    await setGroupAntispam(g.tg_chat_id, { enabled: true, ...gAsOf(g) })
    flash('Настройки антиспама сохранены')
  } catch (e) { flash(e.message || 'Ошибка') } finally { u.busy = false }
}
// Удаление системных сообщений — отдельная фича (не антиспам). Сохраняем, не меняя
// состояние антиспама (enabled: g.antispam).
async function toggleGroupDelService(g) {
  const u = gUi(g)
  if (u.busy) return
  if (!isPro.value) { flash('Эта функция доступна на PRO'); return }
  const as = gAsOf(g)
  const next = !as.del_service
  u.busy = true
  try {
    as.del_service = next
    await setGroupAntispam(g.tg_chat_id, { enabled: !!g.antispam, ...as })
    g.del_service = next
    flash(next ? 'Удаление системных сообщений включено' : 'Удаление системных выключено')
  } catch (e) { as.del_service = !next; flash(e.message || 'Ошибка') } finally { u.busy = false }
}

// Категории запрета (галочки) — block_cats хранится csv-строкой в объекте настроек.
const blockCatList = [
  { key: 'crypto', label: 'Крипта / обмен (USDT и т.п.)' },
  { key: 'work', label: 'Работа / заработок' },
  { key: 'betting', label: 'Ставки / казино' },
  { key: 'sale', label: 'Продажа / скупка' },
]
function hasCat(as, key) {
  return (as.block_cats || '').split(',').map(s => s.trim()).includes(key)
}
function toggleCat(as, key) {
  const set = new Set((as.block_cats || '').split(',').map(s => s.trim()).filter(Boolean))
  if (set.has(key)) set.delete(key); else set.add(key)
  as.block_cats = [...set].join(',')
}
// Свёрнутые секции настроек антиспама (прогрессивное раскрытие): u — ui-объект (uiOf/gUi).
function secOpen(u, name) { return !!(u.sec && u.sec[name]) }
function secToggle(u, name) { if (!u.sec) u.sec = {}; u.sec[name] = !u.sec[name] }

function bKey(b) { return b.platform + b.chat_id + '_' + b.user_id }

// Зеркала: удаление связки (владелец) + докупка слота тарифа.
async function onDeleteMirror(m) {
  const k = m.platform + m.src_chat + '-' + m.dst_chat
  if (mirrorBusy[k]) return
  mirrorBusy[k] = true
  try {
    await deleteMirror(m.platform, m.src_chat, m.dst_chat)
    mirrors.value = mirrors.value.filter(x => !(x.platform === m.platform && x.src_chat === m.src_chat && x.dst_chat === m.dst_chat))
    flash('Зеркало удалено')
  } catch (e) {
    flash('Не удалось удалить: ' + (e.message || e))
  } finally {
    mirrorBusy[k] = false
  }
}

function rub(kopecks) { return (Math.round(kopecks) / 100).toFixed(0) + ' ₽' }
function dateStr(unix) { return new Date(unix * 1000).toLocaleDateString('ru-RU') }

async function openSlotsPanel() {
  slotsOpen.value = !slotsOpen.value
  slotReduceOpen.value = false
  if (slotsOpen.value) { slotQty.value = 1; slotConsent.value = false; await refreshSlotPreview() }
}
function openReducePanel() {
  slotReduceOpen.value = !slotReduceOpen.value
  slotsOpen.value = false
  slotReduceQty.value = 1
}
function reduceQtyDelta(d) {
  const max = slots.value ? Math.max(1, slots.value.extra) : 1
  slotReduceQty.value = Math.min(max, Math.max(1, slotReduceQty.value + d))
}
async function onReduceSlots() {
  if (slotReducing.value) return
  slotReducing.value = true
  try {
    const r = await reduceSlots(slotReduceQty.value)
    if (r.ok) {
      flash('Слоты уменьшены — списание снизится со следующего продления')
      slotReduceOpen.value = false
      await load() // обновить счётчик слотов
    } else {
      flash(r.error || 'Не удалось уменьшить')
    }
  } catch (e) {
    flash('Ошибка: ' + (e.message || e))
  } finally {
    slotReducing.value = false
  }
}
async function refreshSlotPreview() {
  slotPreview.value = null
  slotPreviewErr.value = ''
  try {
    const r = await previewSlots(slotQty.value)
    if (r.ok) slotPreview.value = r
    else slotPreviewErr.value = r.error || 'Расчёт недоступен'
  } catch { slotPreviewErr.value = 'Расчёт недоступен' }
}
function slotQtyDelta(d) {
  const n = Math.min(50, Math.max(1, slotQty.value + d))
  if (n !== slotQty.value) { slotQty.value = n; refreshSlotPreview() }
}
async function onBuySlot() {
  if (slotsBuying.value || !slotConsent.value) return
  slotsBuying.value = true
  try {
    const r = await buySlots(slotQty.value)
    if (r.ok && r.pay_url) {
      window.open(r.pay_url, '_blank')
      flash('Откройте ссылку оплаты — слоты начислятся после платежа')
      slotsOpen.value = false
    } else {
      flash(r.error || 'Не удалось создать оплату')
    }
  } catch (e) {
    flash('Ошибка: ' + (e.message || e))
  } finally {
    slotsBuying.value = false
  }
}
async function doUnban(b) {
  const k = bKey(b)
  if (blockBusy[k]) return
  blockBusy[k] = true
  try {
    await unbanUser(b.platform, b.chat_id, b.user_id)
    blocks.value = blocks.value.filter(x => bKey(x) !== k)
	    flash(b.action === 'kick' ? 'Запись удалена' : (b.action === 'ban' ? 'Пользователь разбанен' : 'Мут снят'))
	  } catch (e) { flash(e.message || 'Не удалось отменить ограничение') } finally { blockBusy[k] = false }
}

async function recheckCp(c) {
  try {
    const r = await checkBotAdmin('crosspost', c.max_chat_id)
    c.bot_admin = r.bot_admin
    flash(r.bot_admin ? '✅ Бот админ — модерация работает' : '❌ Бот всё ещё не админ группы обсуждения')
  } catch { flash('Не удалось проверить') }
}
async function recheckGroup(g) {
  try {
    const r = await checkBotAdmin('group', g.tg_chat_id)
    g.bot_admin = r.bot_admin
    flash(r.bot_admin ? '✅ Бот админ — модерация работает' : '❌ Бот всё ещё не админ группы')
  } catch { flash('Не удалось проверить') }
}

async function getLinkCode() {
  if (accBusy.value) return
  accBusy.value = true
  try { const r = await linkStart(); accCode.value = r.code }
  catch { flash('Не удалось получить код') }
  finally { accBusy.value = false }
}

async function upgrade() {
  if (subscribing.value) return
  subscribing.value = true
  try { const { url } = await subscribePro(); if (url) location.href = url }
  catch { flash('Не удалось создать платёж') }
  finally { subscribing.value = false }
}
async function tryTrial() {
  if (subscribing.value) return
  subscribing.value = true
  try {
    await startTrial()
    await load()
    flash('PRO-триал активирован на 7 дней. Для общего PRO в Telegram и MAX свяжите аккаунты через /link в MAX-боте.')
  } catch (e) {
    flash(e.message || 'Не удалось активировать триал')
  } finally {
    subscribing.value = false
  }
}

async function cancelSub() {
  if (subscribing.value) return
  subscribing.value = true
  try {
    await cancelPro()
    subStatus.value = 'canceled'
    flash('Подписка отменена. Списаний больше не будет.')
  } catch {
    flash('Не удалось отменить')
  } finally {
    subscribing.value = false
  }
}

// Возобновить подписку. Если карта привязана — без новой оплаты (мгновенно или списанием
// по сохранённой карте). Если карты нет (need_card) — отправляем на полную оплату.
async function resumeSub() {
  if (subscribing.value) return
  subscribing.value = true
  try {
    const { result } = await resumePro()
    if (result === 'need_card') { await upgrade(); return }
    await load()
    flash(result === 'charging' ? 'Списываем по сохранённой карте — PRO активируется через минуту 💳' : 'Подписка возобновлена 🎉')
  } catch (e) {
    flash(e.message || 'Не удалось возобновить')
  } finally {
    subscribing.value = false
  }
}

function cardLast4(pan) {
  const d = String(pan || '').replace(/[^0-9]/g, '')
  return d.slice(-4)
}


function fmtDate(unix) {
  if (!unix) return ''
  return new Date(unix * 1000).toLocaleDateString('ru-RU', { day: '2-digit', month: '2-digit', year: 'numeric' })
}

// reasonLabel — превращает машинный код причины бана в понятную человеку фразу.
// Формат причины: "<префикс>: <код>,<код>" либо просто "<код>,<код>" либо готовая фраза.
const REASON_PREFIX = {
  'join-profile-channel': 'приманка в профиле (описание канала)',
  'join-profile': 'имя/юзернейм-приманка',
}
const REASON_CODE = {
  'phone': 'номер телефона',
  'bot-promo': 'ссылка на бота',
  'shortener': 'сокращённая ссылка',
  'contact-funnel': '«пишите менеджеру/организатору»',
  'funnel': 'предложение скинуть инфопродукт',
  'freebie': 'бесплатная раздача инфопродукта',
  'begging': 'сбор денег/попрошайничество',
  'link-new': 'ссылка от новичка',
  'external-reply': 'цитата чужого канала',
  'invisible': 'невидимые символы',
  'rtl': 'переворот текста (RTL)',
  'mixed-script': 'смешанные алфавиты',
  'spacing': 'разрядка слов',
  'digit-in-word': 'цифры внутри слов',
  'emoji-letters': 'слово из эмодзи',
  'caps': 'сплошной капс',
  'emoji-flood': 'много эмодзи',
  'emoji-junk': 'эмодзи без текста',
  'repeat': 'повтор символов',
  'kw-hard': 'стоп-слово',
  'kw-soft': 'стоп-слово',
  'kw-custom': 'стоп-слово чата',
  'spam': 'спам',
}
function mapCode(code) {
  const c = code.trim()
  return REASON_CODE[c] || c
}
function reasonLabel(reason) {
  if (!reason) return ''
  const r = reason.trim()
  const ci = r.indexOf(':')
  if (ci > 0) {
    const prefix = r.slice(0, ci).trim()
    const rest = r.slice(ci + 1).trim()
    const head = REASON_PREFIX[prefix]
    if (head) {
      // Остаток может быть списком кодов или уже готовой фразой.
      const parts = rest.split(',').map(mapCode)
      const known = rest.split(',').every(c => REASON_CODE[c.trim()])
      return known ? head + ': ' + parts.join(', ') : head + ': ' + rest
    }
  }
  // Без известного префикса: если это список кодов — маппим, иначе показываем как есть.
  if (/^[a-z0-9,\- ]+$/i.test(r) && r.split(',').some(c => REASON_CODE[c.trim()])) {
    return r.split(',').map(mapCode).join(', ')
  }
  return r
}

async function buy(posts) {
  if (buying.value) return
  buying.value = true
  try { const { url } = await buyPosts(posts); if (url) location.href = url }
  catch { flash('Не удалось создать платёж') }
  finally { buying.value = false }
}

async function toggleComments(c) {
  const u = uiOf(c)
  if (u.busy) return
  u.busy = true
  try {
    const r = await setComments(c.max_chat_id, !c.comments_enabled)
    c.comments_enabled = !!r.enabled
    flash(c.comments_enabled ? 'Комментарии включены' : 'Комментарии выключены')
  } catch (e) { flash(e.message || 'Ошибка') } finally { u.busy = false }
}
async function toggleSync(c) {
  const u = uiOf(c)
  if (u.busy) return
  u.busy = true
  try {
    const r = await setSyncEdits(c.max_chat_id, !c.sync_edits)
    c.sync_edits = !!r.enabled
    flash(c.sync_edits ? 'Синхронизация правок включена' : 'Выключена')
  } catch (e) { flash(e.message || 'Ошибка') } finally { u.busy = false }
}
async function togglePause(c) {
  const u = uiOf(c)
  if (u.busy) return
  u.busy = true
  try {
    const r = await setCrosspostPaused(c.max_chat_id, !c.paused)
    c.paused = !!r.paused
    flash(c.paused ? 'Связка на паузе — посты не пересылаются' : 'Связка возобновлена')
  } catch (e) { flash(e.message || 'Ошибка') } finally { u.busy = false }
}
// Настройки антиспама держим в ui-объекте связки (с дефолтами).
function asOf(c) {
  const u = uiOf(c)
  if (!u.as) u.as = { mode: c.antispam_mode || 'enforce', allow_links: !!c.allow_links, link_delay_h: 24, trust_msgs: 3, strike_limit: c.strike_limit || 2, ban_after: c.ban_after || 3, action: c.action || 'mute', mute_minutes: c.mute_minutes || 60, warn: !!c.warn, notify: c.notify || 'ban', captcha: !!c.captcha, antiraid: !!c.antiraid, profile_guard: !!c.profile_guard, block_words: c.block_words || '', block_cats: c.block_cats || '', del_service: !!c.del_service }
  return u.as
}
async function toggleAntispam(c) {
  const u = uiOf(c)
  if (u.busy) return
  if (!isPro.value) { flash('Антиспам — PRO-функция'); return }
  u.busy = true
  const next = !c.antispam
  try {
    const r = await setAntispam(c.max_chat_id, { enabled: next, ...asOf(c) })
    c.antispam = !!r.ok
    c.bot_admin = r.bot_admin
    if (next && !r.discussion_linked) {
      flash('Антиспам в комментариях включён. У канала нет группы обсуждения — модерация TG-группы не активна.')
    } else if (next && r.bot_admin === false) {
      flash('⚠️ Антиспам включён, но бот не админ в группе обсуждения — добавьте его админом, иначе модерация не сработает.')
    } else {
      flash(c.antispam ? 'Антиспам включён' : 'Антиспам выключен')
    }
  } catch (e) { flash(e.message || 'Ошибка') } finally { u.busy = false }
}
async function saveAntispam(c) {
  const u = uiOf(c)
  if (u.busy || !c.antispam) return
  u.busy = true
  try {
    await setAntispam(c.max_chat_id, { enabled: true, ...asOf(c) })
    flash('Настройки антиспама сохранены')
  } catch (e) { flash(e.message || 'Ошибка') } finally { u.busy = false }
}
// Удаление системных сообщений — отдельная фича (не антиспам). Сохраняем, не меняя
// состояние антиспама (enabled: c.antispam).
async function toggleDelService(c) {
  const u = uiOf(c)
  if (u.busy) return
  if (!isPro.value) { flash('Эта функция доступна на PRO'); return }
  const as = asOf(c)
  const next = !as.del_service
  u.busy = true
  try {
    as.del_service = next
    await setAntispam(c.max_chat_id, { enabled: !!c.antispam, ...as })
    c.del_service = next
    flash(next ? 'Удаление системных сообщений включено' : 'Удаление системных выключено')
  } catch (e) { as.del_service = !next; flash(e.message || 'Ошибка') } finally { u.busy = false }
}
async function removeCp(c) {
  const u = uiOf(c)
  if (u.busy) return
  u.busy = true
  try {
    await deleteCrosspost(c.max_chat_id)
    crossposts.value = crossposts.value.filter(x => cpKey(x) !== cpKey(c))
    flash('Связка удалена')
  } catch (e) { u.busy = false; u.confirm = false; flash(e.message || 'Не удалось удалить') }
}

function addRule(u, dir) { u.repl[dir].push({ from: '', to: '', target: 'all', regex: false }); u.dirty = true }
function delRule(u, dir, i) { u.repl[dir].splice(i, 1); u.dirty = true }
async function saveRepl(c) {
  const u = uiOf(c)
  if (u.busy) return
  u.busy = true
  try {
    const payload = {
      'tg>max': u.repl.tgmax.filter(r => r.from.trim()),
      'max>tg': u.repl.maxtg.filter(r => r.from.trim()),
    }
    const res = await setReplacements(c.max_chat_id, payload)
    c.replacements = res.replacements || payload
    c.has_replacements = (payload['tg>max'].length + payload['max>tg'].length) > 0
    u.dirty = false
    flash('Замены сохранены')
  } catch (e) { flash(e.message || 'Не удалось сохранить') } finally { u.busy = false }
}
</script>

<template>
  <main class="wrap" :class="{ 'cabinet-desktop': browserMode, 'detail-open': anyOpen, 'auth-required': noAuth }">
    <aside v-if="browserMode && !noAuth" class="cabinet-sidebar" aria-label="Разделы кабинета">
      <a class="brand" href="/cabinet/" aria-label="Мост — главная кабинета">
        <span class="brand-mark" aria-hidden="true">М</span>
        <span><b>Мост</b><small>Кабинет</small></span>
      </a>
      <nav class="side-nav">
        <a v-for="item in cabinetSections" :key="item.id"
          :href="'/cabinet/' + item.path"
          :class="{ active: currentSection === item.id }"
          :aria-current="currentSection === item.id ? 'page' : undefined"
          @click.prevent="navigate(item.id)">{{ item.label }}</a>
      </nav>
      <div class="side-bottom">
        <a href="https://t.me/+0ucbOj4wBwQzMWNi" target="_blank" rel="noopener">Поддержка</a>
        <button :disabled="loggingOut" @click="logout">{{ loggingOut ? 'Выходим…' : 'Выйти' }}</button>
      </div>
    </aside>

    <section class="cabinet-content">
    <header v-if="!noAuth" id="overview" class="top">
      <div>
        <p v-if="browserMode" class="eyebrow">Рабочее пространство</p>
        <h1>{{ browserMode ? currentSectionLabel : 'Кабинет' }}</h1>
      </div>
      <button v-if="!loading && !noAuth && !error" class="icon-btn" title="Обновить" @click="load">↻</button>
    </header>

    <p v-if="loading" class="muted">Загрузка…</p>
    <div v-else-if="noAuth" class="login-card">
      <span class="login-mark" aria-hidden="true">М</span>
      <p class="eyebrow">Без паролей</p>
      <h2>Войдите через бота</h2>
      <p class="muted">Отправьте боту команду <code>/cabinet</code>. Он пришлёт личную одноразовую ссылку — откройте её на этом компьютере.</p>
      <div class="login-actions">
        <a class="btn accent" :href="tgBotUrl" target="_blank" rel="noopener">Открыть Telegram-бота</a>
        <a class="btn ghost" :href="maxBotUrl" target="_blank" rel="noopener">Открыть MAX-бота</a>
      </div>
      <p class="security-note">Ссылка действует 10 минут и срабатывает один раз. Пароль придумывать и хранить не нужно.</p>
    </div>
    <p v-else-if="error" class="center error">{{ error }}</p>

    <template v-else>
      <label v-if="browserMode && !anyOpen" class="mobile-section-picker">
        <span>Раздел</span>
        <select :value="currentSection" @change="onMobileSection">
          <option v-for="item in cabinetSections" :key="item.id" :value="item.id">{{ item.label }}</option>
        </select>
      </label>

      <!-- Назад к списку (когда открыт «экран» одного объекта) -->
      <button v-if="anyOpen" class="back-bar" @click="closeAll">← Назад к списку</button>

      <div v-if="me" v-show="!anyOpen" class="whoami">👤 <b>{{ me.name }}</b> · id {{ me.id }} · {{ browserMode ? acctPlat.toUpperCase() : platform }}</div>

      <section v-if="browserMode" v-show="!anyOpen && currentSection === 'overview'" class="overview-grid" aria-label="Состояние кабинета">
        <article><span>Связок работает</span><strong>{{ totalConnections - pausedConnections }}</strong><small>из {{ totalConnections }}</small></article>
        <article><span>Свободных слотов</span><strong>{{ freeSlots }}</strong><small>можно подключить сейчас</small></article>
        <article><span>На паузе</span><strong>{{ pausedConnections }}</strong><small>{{ pausedConnections ? 'проверьте связки' : 'всё активно' }}</small></article>
        <article :class="{ attention: attentionCount }"><span>Требует внимания</span><strong>{{ attentionCount }}</strong><small>{{ attentionCount ? 'есть действия' : 'всё спокойно' }}</small></article>
      </section>

      <section v-if="browserMode" v-show="!anyOpen && currentSection === 'overview'" class="focus-card">
        <div>
          <p class="eyebrow">Следующий шаг</p>
          <h2>{{ attentionCount ? 'Проверьте то, что требует внимания' : 'Все основные связки под контролем' }}</h2>
          <p class="muted">{{ attentionCount ? 'Паузы и события модерации собраны в соответствующих разделах.' : 'Можно добавить новую связку или спокойно закрыть кабинет.' }}</p>
        </div>
        <button class="btn accent" @click="navigate(attentionCount ? 'moderation' : 'channels')">
          {{ attentionCount ? 'Открыть события' : 'Перейти к связкам' }}
        </button>
      </section>

      <!-- Вход в админку (только владелец) — отдельный экран -->
      <button v-if="isAdmin" v-show="!anyOpen && (!browserMode || currentSection === 'overview')" class="btn ghost full admin-link" @click="emit('admin')">📊 Админка — статистика</button>

      <!-- Бесплатный тариф: яркий CTA-блок (сразу видны кнопки, без раскрытия) -->
      <div id="billing" v-if="!isPro" v-show="!anyOpen && (!browserMode || currentSection === 'billing')" class="cta section-anchor">
        <div class="cta-title">🚀 Откройте PRO</div>
        <div class="cta-sub">5 слотов на мосты, зеркала и каналы (с докупкой), комментарии под постами, антиспам в группах, без подписи бота.</div>
        <div class="consent-terms" style="margin-bottom:10px">
          ℹ️ PRO активируется на аккаунте оплаты. Чтобы подписка, слоты и баланс были общими в Telegram и MAX, после оплаты отправьте <b>/link</b> в личке MAX-бота и привяжите Telegram.
        </div>
        <button v-if="!trialUsed" class="btn accent full" :disabled="subscribing" @click="tryTrial">
          {{ subscribing ? '…' : '🎁 Попробовать 7 дней бесплатно' }}
        </button>
        <div class="consent">
          <div class="consent-terms">Подписка <b>PRO — {{ rub(proPriceKopecks) }}/мес</b>. Регулярное автосписание раз в 30 дней до отмены.</div>
          <label class="consent-check">
            <input type="checkbox" v-model="recurrentConsent" />
            <span>Согласен на регулярные списания {{ rub(proPriceKopecks) }} раз в 30 дней. Отменить можно в любой момент в этом кабинете.</span>
          </label>
          <div class="consent-contact">Возврат, отмена, вопросы оплаты — <a href="https://t.me/+0ucbOj4wBwQzMWNi">группа поддержки</a></div>
        </div>
        <button class="btn full" :class="trialUsed ? 'accent' : 'ghost-light'" :disabled="subscribing || !recurrentConsent" @click="upgrade" style="margin-top:8px">
          {{ subscribing ? '…' : 'Оформить PRO — ' + rub(proPriceKopecks) + '/мес' }}
        </button>
      </div>

      <!-- PRO активен: раскрывающееся управление подпиской -->
      <div id="billing" v-else v-show="!anyOpen && (!browserMode || currentSection === 'billing')" class="pro-banner active section-anchor">
        <button class="pro-head" @click="proOpen = !proOpen">
          <div>
            <b>⭐ PRO активен</b>
            <div class="muted small">
              <template v-if="subStatus === 'trial'">Триал · до {{ fmtDate(subUntil) }}</template>
              <template v-else-if="subStatus === 'canceled'">Отменена · PRO до {{ fmtDate(subUntil) }}</template>
              <template v-else-if="subStatus === 'active'">Активна · продление {{ fmtDate(subUntil) }}</template>
              <template v-else>Активен</template>
            </div>
          </div>
          <span class="chev">{{ proOpen ? '▾' : '▸' }}</span>
        </button>
        <div v-if="proOpen" class="pro-body">
          <div class="muted small" style="margin-bottom:10px">
            ℹ️ PRO привязан к аккаунту оплаты. Для общего PRO в Telegram и MAX отправьте <b>/link</b> в личке MAX-бота и привяжите Telegram.
          </div>
          <!-- Привязанная карта + замена (полная оплата новой картой) -->
          <div v-if="cardPan" class="card-row">
            <span class="muted small">💳 Карта •••• {{ cardLast4(cardPan) }}</span>
            <button class="btn ghost sm" :disabled="subscribing || !recurrentConsent" @click="upgrade">Заменить</button>
          </div>
          <!-- Согласие на рекуррент — обязательно перед оформлением/заменой карты (не для отмены). -->
          <div v-if="subStatus === 'trial' || cardPan" class="consent">
            <label class="consent-check">
              <input type="checkbox" v-model="recurrentConsent" />
              <span>Согласен на регулярные списания {{ rub(proPriceKopecks) }} раз в 30 дней до отмены (в кабинете).</span>
            </label>
            <div class="consent-contact">Возврат/отмена/вопросы — <a href="https://t.me/+0ucbOj4wBwQzMWNi">группа поддержки</a></div>
          </div>
          <!-- active: отменить. trial: оформить полную (карты ещё нет). canceled: возобновить без оплаты. -->
          <button v-if="subStatus === 'active'" class="btn ghost full" :disabled="subscribing" @click="cancelSub">
            {{ subscribing ? '…' : 'Отменить подписку' }}
          </button>
          <button v-else-if="subStatus === 'trial'" class="btn accent full" :disabled="subscribing || !recurrentConsent" @click="upgrade">
            {{ subscribing ? '…' : 'Оформить полную PRO — ' + rub(proPriceKopecks) + '/мес' }}
          </button>
          <button v-else class="btn accent full" :disabled="subscribing" @click="resumeSub">
            {{ subscribing ? '…' : 'Возобновить подписку' }}
          </button>
        </div>
      </div>

      <!-- Привязка MAX↔TG (баланс постов/PRO по TG-аккаунту) -->
      <div v-if="acctPlat === 'max' && !accLinked" v-show="!anyOpen && (!browserMode || currentSection === 'overview')" class="help-card acc-card">
        <div class="help-head" style="cursor:default">
          <span class="help-q">🔗</span>
          <span class="help-title">Telegram-аккаунт не привязан</span>
        </div>
        <div class="help-body">
          <p class="muted small" style="margin:0 0 10px">
            Баланс постов и PRO привязаны к вашему Telegram-аккаунту. Свяжите его — и они появятся здесь, в MAX.
          </p>
          <template v-if="!accCode">
            <button class="btn accent full" :disabled="accBusy" @click="getLinkCode">{{ accBusy ? '…' : 'Привязать Telegram' }}</button>
          </template>
          <template v-else>
            <p class="muted small" style="margin:0 0 6px">Откройте Telegram-бота и отправьте сообщение:</p>
            <div class="acc-code">/link {{ accCode }}</div>
            <a class="lk-bot" :href="tgBotUrl" target="_blank" rel="noopener" style="margin-top:8px">Открыть Telegram-бота</a>
            <p class="muted" style="font-size:12px;margin-top:8px">После привязки нажмите ↻ (обновить). Код действует 10 минут.</p>
          </template>
        </div>
      </div>

      <div id="connections" class="tabs section-anchor" v-show="!anyOpen && !browserMode">
        <button type="button" :class="{ active: tab === 'channels' }" @click="tab = 'channels'">Каналы ({{ crossposts.length }})</button>
        <button type="button" :class="{ active: tab === 'groups' }" @click="tab = 'groups'">Группы ({{ groups.length }})</button>
        <button type="button" :class="{ active: tab === 'mirrors' }" @click="tab = 'mirrors'">Зеркала ({{ mirrors.length }})</button>
        <button type="button" :class="{ active: tab === 'blocks' }" @click="tab = 'blocks'">Баны ({{ blocks.length }})</button>
      </div>

      <!-- Слоты тарифа: общий счётчик мостов/зеркал/каналов + докупка -->
      <div class="help-card" v-show="!anyOpen && slots && (!browserMode || currentSection === 'billing')">
        <div class="help-body" style="display:flex;align-items:center;gap:10px;justify-content:space-between;padding:10px 12px">
          <span class="muted small">🧩 Слоты: <b>{{ slots.used }}</b> из <b>{{ slots.limit }}</b> (база {{ slots.base }} + докуплено {{ slots.extra }})</span>
          <span style="display:flex;gap:6px">
            <button v-if="isPro && slots.extra > 0" class="btn ghost" @click="openReducePanel">{{ slotReduceOpen ? 'Скрыть' : '−' }}</button>
            <button v-if="isPro" class="btn ghost" @click="openSlotsPanel">{{ slotsOpen ? 'Скрыть' : '+ слот (' + rub(slotPriceKopecks) + '/мес)' }}</button>
          </span>
        </div>
        <!-- Уменьшение слотов: без возврата, рекуррент снизится со следующего периода -->
        <div v-if="slotReduceOpen" class="help-body" style="padding:0 12px 12px">
          <div style="display:flex;align-items:center;gap:12px;margin-bottom:8px">
            <span class="muted small">Убрать слотов:</span>
            <button class="btn ghost" style="min-width:36px" @click="reduceQtyDelta(-1)">−</button>
            <b>{{ slotReduceQty }}</b>
            <button class="btn ghost" style="min-width:36px" @click="reduceQtyDelta(1)">+</button>
            <span class="muted small">из {{ slots.extra }} докупленных</span>
          </div>
          <p class="muted small" style="margin:4px 0">
            Возврата за текущий оплаченный период нет — слоты работают до его конца.
            Со следующего продления списание уменьшится на {{ rub(slotReduceQty * slotPriceKopecks) }}/мес.
            Если занятых связок останется больше лимита, существующие продолжат работать,
            но новые подключить будет нельзя.
          </p>
          <button class="btn danger full" :disabled="slotReducing" @click="onReduceSlots">
            {{ slotReducing ? '…' : 'Уменьшить на ' + slotReduceQty }}
          </button>
        </div>
        <!-- Промежуточный экран покупки: кол-во, прорейт, будущий рекуррент, согласие -->
        <div v-if="slotsOpen" class="help-body" style="padding:0 12px 12px">
          <div style="display:flex;align-items:center;gap:12px;margin-bottom:8px">
            <span class="muted small">Слотов:</span>
            <button class="btn ghost" style="min-width:36px" @click="slotQtyDelta(-1)">−</button>
            <b>{{ slotQty }}</b>
            <button class="btn ghost" style="min-width:36px" @click="slotQtyDelta(1)">+</button>
          </div>
          <p v-if="slotPreviewErr" class="muted small" style="color:#c00">{{ slotPreviewErr }}</p>
          <template v-else-if="slotPreview">
            <p class="muted small" style="margin:4px 0">
              Сейчас к оплате по ссылке: <b>{{ rub(slotPreview.amount_kopecks) }}</b> — прорейт за остаток
              оплаченного периода (до {{ dateStr(slotPreview.paid_until) }}). Полная цена слота —
              {{ rub(slotPreview.slot_price_kopecks) }}/мес, сейчас вы платите только за оставшиеся дни.
            </p>
            <p class="muted small" style="margin:4px 0">
              Со следующего продления подписка станет <b>{{ rub(slotPreview.next_recurrent_kopecks) }}/мес</b>
              (PRO + все доп-слоты). Уменьшить слоты: /slots off в личке бота — рекуррент снизится со следующего периода.
            </p>
            <label class="consent-check" style="margin:8px 0">
              <input type="checkbox" v-model="slotConsent" />
              <span>Согласен, что после покупки регулярное списание составит {{ rub(slotPreview.next_recurrent_kopecks) }} раз в 30 дней до отмены подписки или уменьшения слотов.</span>
            </label>
            <button class="btn accent full" :disabled="slotsBuying || !slotConsent" @click="onBuySlot">
              {{ slotsBuying ? '…' : 'Оплатить ' + rub(slotPreview.amount_kopecks) }}
            </button>
            <div class="consent-contact" style="margin-top:6px">Возврат, отмена, вопросы оплаты — <a href="https://t.me/+0ucbOj4wBwQzMWNi">группа поддержки</a></div>
          </template>
          <p v-else class="muted small">Расчёт…</p>
        </div>
      </div>
      <div v-show="(!browserMode && tab === 'channels') || (browserMode && currentSection === 'channels')">
      <p class="muted small" style="margin-top:-4px" v-show="!anyOpen">
        <template v-if="isPro">Тариф PRO — 5 слотов на мосты, зеркала и каналы; доп-слоты — /slots в личке бота.</template>
        <template v-else>Бесплатно — 1 канал, PRO — 5 слотов (мосты, зеркала, каналы) с докупкой. Новый канал добавится, только если есть свободный слот (удалите лишние связки или оформите PRO).</template>
      </p>

      <!-- Как связать канал — инструкция (линковка идёт через бота) -->
      <div class="help-card" v-show="!anyOpen">
        <button class="help-head" @click="linkOpen = !linkOpen">
          <span class="help-q">?</span>
          <span class="help-title">Как добавить новый канал?</span>
          <span class="chev">{{ linkOpen ? '▾' : '▸' }}</span>
        </button>
        <div v-if="linkOpen" class="help-body">
          <ol class="lk-steps">
            <li>Добавьте бота <b>админом</b> (с правом постинга) в оба канала:
              <div class="lk-links">
                <a class="lk-bot" :href="tgBotUrl" target="_blank" rel="noopener">TG-бот</a>
                <a class="lk-bot" :href="maxBotUrl" target="_blank" rel="noopener">MAX-бот</a>
              </div>
            </li>
            <li><b>Перешлите любой пост</b> из своего TG-канала в личку TG-бота — он пришлёт ID канала и готовую команду.</li>
            <li>Откройте личку MAX-бота, отправьте <code>/crosspost &lt;ID&gt;</code>, затем <b>перешлите пост из MAX-канала</b> туда же.</li>
            <li>Готово — связка появится в этом списке. Тут же настроите замены, комментарии и синхронизацию.</li>
          </ol>
        </div>
      </div>

      <p v-if="!crossposts.length" v-show="!anyOpen" class="muted small">Связок кросспоста нет.</p>

      <div v-for="c in crossposts" :key="cpKey(c)" v-show="!anyOpen || uiOf(c).open" class="card">
        <button class="card-head" @click="toggleCard(c)">
          <span class="row-icon">🔗</span>
          <span class="body">
            <span class="name">{{ c.title || ('Канал ' + c.tg_chat_id) }}</span>
            <span class="muted small">
              {{ dirLabel(c.direction) }}
              <template v-if="c.comments_enabled"> · 💬</template>
              <template v-if="c.has_replacements"> · замены</template>
              <template v-if="c.sync_edits"> · синк</template>
              <template v-if="c.antispam"> · 🛡</template>
            </span>
          </span>
          <span class="chev">{{ uiOf(c).open ? '▾' : '▸' }}</span>
        </button>

        <div v-if="uiOf(c).open" class="card-body">
          <!-- Тумблеры -->
          <div class="toggle-row" @click="toggleComments(c)">
            <span class="tg-label">💬 Комментарии <span class="muted small" v-if="!isPro">(бесплатно на 1 канале)</span>
              <span class="tg-desc muted">В MAX нет нативных комментариев под постами. Бот добавляет под каждый пост кнопку «Комментарии», открывающую мини-апп с обсуждением (синк с группой обсуждения TG в обе стороны). Бесплатно — на 1 канале; на остальных — PRO.</span>
            </span>
            <span class="switch" :class="{ on: c.comments_enabled }"></span>
          </div>
          <div class="toggle-row" @click="toggleSync(c)">
            <span class="tg-label">✏️ Синхронизировать правки
              <span class="tg-desc muted">Отредактируете или удалите пост в исходном канале — бот так же поправит/удалит его копию в связанном канале.</span>
            </span>
            <span class="switch" :class="{ on: c.sync_edits }"></span>
          </div>
          <div class="toggle-row" @click="togglePause(c)">
            <span class="tg-label">⏸ Пауза связки
              <span class="tg-desc muted">Временно остановить пересылку постов (связка не удаляется). Включите обратно в любой момент.</span>
            </span>
            <span class="switch" :class="{ on: c.paused }"></span>
          </div>
          <div class="toggle-row" @click="toggleAntispam(c)">
            <span class="tg-label">🛡 Антиспам <span class="muted small" v-if="!isPro">(PRO)</span>
              <span class="tg-desc muted">Чистит спам в комментариях мини-аппа и (если у канала привязана группа обсуждения) в самой группе: удаление + мут повторных, новички не постят ссылки заданное время. ⚠️ Для модерации группы обсуждения добавьте бота в неё <b>администратором</b> (с правами удалять сообщения и банить).</span>
            </span>
            <span class="switch" :class="{ on: c.antispam }"></span>
          </div>

          <!-- Настройки антиспама (авто-сохранение по изменению) -->
          <div v-if="c.antispam" class="as-settings" @change="scheduleSaveC(c)" @click="scheduleSaveC(c)">
            <div v-if="c.bot_admin === false" class="as-warn">⚠️ Бот не админ в группе обсуждения — добавьте его туда администратором (с правами удалять сообщения и банить), иначе модерация не работает.
              <button class="btn ghost sm" style="margin-top:6px" @click="recheckCp(c)">Проверить</button>
            </div>
            <div class="repl-dir">Политика антиспама</div>
            <div class="as-row">
              <span class="muted">Режим:</span>
              <button type="button" :class="{ sel: asOf(c).mode === 'enforce' }" @click="asOf(c).mode='enforce'">удалять</button>
              <button type="button" :class="{ sel: asOf(c).mode === 'observe' }" @click="asOf(c).mode='observe'">репорт</button>
              <button type="button" :class="{ sel: asOf(c).mode === 'debug' }" @click="asOf(c).mode='debug'">🐞 тест</button>
            </div>
            <p v-if="asOf(c).mode === 'enforce'" class="repl-hint muted" style="margin-top:0">
              <b>Удалять</b> — боевой режим: бот сам удаляет спам и наказывает нарушителей
              (мут/бан по настройкам ниже). Включайте, когда настроили и доверяете фильтру.
            </p>
            <p v-if="asOf(c).mode === 'observe'" class="repl-hint muted" style="margin-top:0">
              <b>Репорт</b> — наблюдение: ничего <b>не удаляет</b> и не банит, только присылает вам
              в ЛС уведомление о подозрительных сообщениях. Удобно посмотреть, что бот считает
              спамом, прежде чем включать «удалять».
            </p>
            <p v-if="asOf(c).mode === 'debug'" class="repl-hint muted" style="margin-top:0">
              <b>🐞 Тест</b> — калибровка: никого не баним и комменты не режем, бот шлёт вам в ЛС
              подробный разбор каждого подозрительного сообщения (score, сигналы, что было бы сделано).
            </p>
            <button type="button" class="as-sec-head" @click="secToggle(uiOf(c),'adv')">⚙️ Расширенные настройки <span class="chev">{{ secOpen(uiOf(c),'adv') ? '▾' : '▸' }}</span></button>
            <div v-show="secOpen(uiOf(c),'adv')">
            <label class="as-check">
              <input type="checkbox" v-model="asOf(c).allow_links" />
              <span>Разрешать ссылки участникам. Остальные антиспам-проверки продолжат работать.</span>
            </label>
            <label v-if="!asOf(c).allow_links" class="as-field">Новичкам нельзя ссылки, часов
              <input type="number" min="0" max="720" v-model.number="asOf(c).link_delay_h" />
            </label>
            <label class="as-field">«Доверенный» после N сообщений
              <input type="number" min="0" max="100" v-model.number="asOf(c).trust_msgs" />
            </label>
            <div class="as-row">
              <span class="muted">Наказание:</span>
              <button type="button" :class="{ sel: asOf(c).action === 'mute' }" @click="asOf(c).action='mute'">мут</button>
              <button type="button" :class="{ sel: asOf(c).action === 'ban' }" @click="asOf(c).action='ban'">бан</button>
              <button type="button" :class="{ sel: asOf(c).action === 'mute_then_ban' }" @click="asOf(c).action='mute_then_ban'">мут → бан</button>
            </div>
            <label v-if="asOf(c).action !== 'ban'" class="as-field">Мут на сколько минут
              <input type="number" min="1" max="43200" v-model.number="asOf(c).mute_minutes" />
            </label>
            <label class="as-field">{{ asOf(c).action === 'ban' ? 'Бан' : 'Мут' }} после N нарушений (1 = сразу)
              <input type="number" min="1" max="10" v-model.number="asOf(c).strike_limit" />
            </label>
            <label v-if="asOf(c).action === 'mute_then_ban'" class="as-field">Бан после M нарушений (M больше N)
              <input type="number" min="2" max="20" v-model.number="asOf(c).ban_after" />
            </label>
            <label class="as-check">
              <input type="checkbox" v-model="asOf(c).warn" />
              <span>Предупреждать нарушителя в чате (удаляем сообщение и пишем «нарушаете»), пока не дошло до наказания</span>
            </label>
			<label class="as-check">
			  <input type="checkbox" v-model="asOf(c).captcha" />
			  <span>🤖 Капча на входе (TG): новый участник мьютится до нажатия «Я не бот», не нажал — кик</span>
			</label>
			<label class="as-check">
			  <input type="checkbox" v-model="asOf(c).profile_guard" />
				  <span>Проверять профиль при входе (TG): подозрительный профиль получает выбранное выше наказание; в «репорт»/«тест» — только уведомление</span>
			</label>
            <label class="as-check">
              <input type="checkbox" v-model="asOf(c).antiraid" />
              <span>🛡 Анти-рейд (TG): при массовом входе — новичков молча мьютим на случайное время (1–6 ч), чтобы не было синхронного спама</span>
            </label>
            <div class="repl-dir" style="margin-top:10px">🚫 Запрещённый контент (наказание — по политике выше)</div>
            <div class="as-cats">
              <label v-for="cat in blockCatList" :key="cat.key" class="as-check">
                <input type="checkbox" :checked="hasCat(asOf(c), cat.key)" @change="toggleCat(asOf(c), cat.key)" />
                <span>{{ cat.label }}</span>
              </label>
            </div>
            <label class="as-field as-field-col">Свои запрещённые слова/фразы (по одному в строке или через запятую)
              <textarea v-model="asOf(c).block_words" rows="3" placeholder="usdt
обмен крипты
заработок"></textarea>
            </label>
            <div class="as-row">
              <span class="muted">Уведомлять в ЛС:</span>
              <button type="button" :class="{ sel: asOf(c).notify === 'off' }" @click="asOf(c).notify='off'">выкл</button>
              <button type="button" :class="{ sel: asOf(c).notify === 'ban' }" @click="asOf(c).notify='ban'">о банах</button>
              <button type="button" :class="{ sel: asOf(c).notify === 'all' }" @click="asOf(c).notify='all'">обо всём</button>
            </div>
            <p class="repl-hint muted">«мут → бан» — мут после N нарушений, бан после M. Уведомления и предупреждения троттлятся (не чаще раза в 30 сек на чат), чтобы налёт ботов не засыпал. «репорт» — шлём владельцу в ЛС, ничего не удаляя.</p>
            </div>
            <p class="repl-hint muted" style="text-align:center;margin:6px 0 0">✓ Изменения сохраняются автоматически</p>
          </div>
          <div class="toggle-row" @click="toggleDelService(c)">
            <span class="tg-label">🧹 Удалять системные сообщения
              <span class="tg-desc muted">Авто-удаление служебных сообщений в группе обсуждения: вошёл / вышел / сменил название / закрепил. Нужны права админа на удаление.</span>
            </span>
            <span class="switch" :class="{ on: asOf(c).del_service }"></span>
          </div>

          <!-- Замены -->
          <div class="repl">
            <div class="repl-h">🔁 Автозамены текста</div>
            <p class="repl-intro muted">
              Бот автоматически меняет текст постов при пересылке. Например: заменить ссылку на ваш
              Telegram-канал на ссылку MAX, убрать лишние упоминания или поправить подпись.
            </p>
            <template v-for="(dir, key) in { 'tgmax': 'Посты TG → MAX', 'maxtg': 'Посты MAX → TG' }" :key="key">
              <div class="repl-dir">{{ dir }}</div>
              <div v-if="!uiOf(c).repl[key].length" class="repl-empty muted">
                Правил нет.
                <a class="repl-ex" @click="addRule(uiOf(c), key); uiOf(c).repl[key].at(-1).from='наш телеграм'; uiOf(c).repl[key].at(-1).to='наш канал в MAX'">
                  добавить пример
                </a>
              </div>
              <div v-for="(rule, i) in uiOf(c).repl[key]" :key="i" class="repl-rule">
                <div class="repl-fields">
                  <label>Найти<input v-model="rule.from" :placeholder="rule.regex ? 'регулярка, напр. \\d{4}' : 'что заменить'" @input="uiOf(c).dirty = true" /></label>
                  <label>Заменить на<input v-model="rule.to" :placeholder="rule.regex ? 'замена, можно $1 $2' : 'оставьте пустым, чтобы удалить'" @input="uiOf(c).dirty = true" /></label>
                  <div class="repl-scope">
                    <span class="muted">Где:</span>
                    <button type="button" :class="{ sel: rule.target !== 'links' }" @click="rule.target='all'; uiOf(c).dirty=true">весь текст</button>
                    <button type="button" :class="{ sel: rule.target === 'links' }" @click="rule.target='links'; uiOf(c).dirty=true">только ссылки</button>
                  </div>
                  <div class="repl-scope">
                    <span class="muted">Как:</span>
                    <button type="button" :class="{ sel: !rule.regex }" @click="rule.regex=false; uiOf(c).dirty=true">обычный текст</button>
                    <button type="button" :class="{ sel: rule.regex }" @click="rule.regex=true; uiOf(c).dirty=true">регулярка</button>
                  </div>
                </div>
                <button class="x" title="Удалить правило" @click="delRule(uiOf(c), key, i)">✕</button>
              </div>
              <button class="add" @click="addRule(uiOf(c), key)">+ добавить правило</button>
            </template>
            <p class="repl-hint muted">
              💡 «весь текст» — меняет везде; «только ссылки» — трогает лишь URL/@упоминания.
              Пустое «Заменить на» = удалить найденное.<br>
              🔣 «регулярка» — поиск по шаблону (синтаксис RE2/Go): <code>\d+</code> цифры,
              <code>(сайт)\.ru</code> с группами, в замене — <code>$1</code>. Невалидную регулярку сохранить нельзя.
            </p>
            <button class="btn accent full" :disabled="uiOf(c).busy || !uiOf(c).dirty" @click="saveRepl(c)">
              {{ uiOf(c).busy ? '…' : (uiOf(c).dirty ? 'Сохранить замены' : '✓ Сохранено') }}
            </button>
          </div>

          <!-- Удаление -->
          <div class="danger-zone">
            <template v-if="!uiOf(c).confirm">
              <button class="btn danger full" @click="uiOf(c).confirm = true">🗑 Удалить связку</button>
            </template>
            <template v-else>
              <span class="muted small">Точно удалить кросспост?</span>
              <button class="btn danger sm" :disabled="uiOf(c).busy" @click="removeCp(c)">{{ uiOf(c).busy ? '…' : 'Да' }}</button>
              <button class="btn ghost sm" :disabled="uiOf(c).busy" @click="uiOf(c).confirm = false">Отмена</button>
            </template>
          </div>
        </div>
      </div>

      </div>
      <div v-show="(!browserMode && tab === 'groups') || (browserMode && currentSection === 'groups')">
      <p class="muted small" style="margin-top:-4px" v-show="!anyOpen">Зеркало сообщений между TG-группой и MAX-чатом (в обе стороны).</p>

      <div class="help-card" v-show="!anyOpen">
        <button class="help-head" @click="grpHelp = !grpHelp">
          <span class="help-q">?</span>
          <span class="help-title">Как связать группу?</span>
          <span class="chev">{{ grpHelp ? '▾' : '▸' }}</span>
        </button>
        <div v-if="grpHelp" class="help-body">
          <ol class="lk-steps">
            <li>Добавьте бота в обе группы и сделайте <b>админом</b> (в TG — если это супергруппа с темами/форум):
              <div class="lk-links">
                <a class="lk-bot" :href="tgBotUrl" target="_blank" rel="noopener">TG-бот</a>
                <a class="lk-bot" :href="maxBotUrl" target="_blank" rel="noopener">MAX-бот</a>
              </div>
            </li>
            <li>В одной из групп отправьте <code>/bridge</code> — бот выдаст ключ.</li>
            <li>В другой группе отправьте <code>/bridge &lt;ключ&gt;</code> → готово, группа появится здесь.</li>
          </ol>
          <p class="lk-note muted">
            Группа уже связана, но её здесь нет? Отправьте в этой группе команду
            <code>/bridge_update</code> — и она появится в кабинете.
          </p>
          <p class="lk-note muted">
            <b>Нужен только антиспам, без моста?</b> Добавьте бота админом в любую TG-группу
            (с правами удалять и банить) — она появится здесь как «антиспам без моста».
            Или включите прямо в группе командой <code>/antispam on</code>.
          </p>
        </div>
      </div>

      <p v-if="!groups.length" v-show="!anyOpen" class="muted small">
        Групп пока нет. Свяжите группы по инструкции выше, либо добавьте бота админом
        в любую TG-группу — она появится здесь для антиспама (без моста).
      </p>
      <div v-for="g in groups" :key="g.tg_chat_id" v-show="!anyOpen || gUi(g).open" class="card">
        <button class="card-head" @click="toggleGroupCard(g)">
          <span class="row-icon">{{ g.standalone ? '🛡' : '🔗' }}</span>
          <span class="body">
            <span class="name" v-if="g.standalone">{{ g.tg_title || ('TG ' + g.tg_chat_id) }}</span>
            <span class="name" v-else>{{ g.tg_title || ('TG ' + g.tg_chat_id) }} ↔ {{ g.max_title || ('MAX ' + g.max_chat_id) }}</span>
            <span class="muted small">{{ g.standalone ? 'TG-группа · антиспам без моста' : 'TG-группа ↔ MAX-чат' }}<template v-if="!g.standalone && g.direction && g.direction !== 'both'"> · {{ dirLabel(g.direction) }}</template><template v-if="g.antispam"> · 🛡</template></span>
          </span>
          <span class="chev">{{ gUi(g).open ? '▾' : '▸' }}</span>
        </button>
        <div v-if="gUi(g).open" class="card-body">
          <nav class="group-settings-nav" aria-label="Разделы настроек группы">
            <button
              v-if="!g.standalone"
              type="button"
              :class="{ active: gUi(g).panel === 'bridge' }"
              @click="gUi(g).panel = 'bridge'"
            >
              <span>↔</span> Мост
            </button>
            <button
              type="button"
              :class="{ active: gUi(g).panel === 'welcome' }"
              @click="gUi(g).panel = 'welcome'"
            >
              <span>👋</span> Приветствие
            </button>
            <button
              type="button"
              :class="{ active: gUi(g).panel === 'antispam' }"
              @click="gUi(g).panel = 'antispam'"
            >
              <span>🛡</span> Антиспам
              <i :class="{ on: g.antispam }">{{ g.antispam ? 'вкл' : 'выкл' }}</i>
            </button>
            <button
              type="button"
              :class="{ active: gUi(g).panel === 'other' }"
              @click="gUi(g).panel = 'other'"
            >
              <span>•••</span> Прочее
            </button>
          </nav>

          <div class="group-panel-intro">
            <template v-if="gUi(g).panel === 'bridge'">
              <b>Пересылка между Telegram и MAX</b>
              <span>Направление, подпись источника и пауза.</span>
            </template>
            <template v-else-if="gUi(g).panel === 'welcome'">
              <b>Приветствие новых участников</b>
              <span>Один текст и понятные подстановки — без лишних настроек.</span>
            </template>
            <template v-else-if="gUi(g).panel === 'antispam'">
              <b>Защита от спама</b>
              <span>Сначала выберите режим. Тонкая настройка спрятана ниже.</span>
            </template>
            <template v-else>
              <b>Дополнительные действия</b>
              <span>Системные сообщения и управление связкой.</span>
            </template>
          </div>

          <div v-if="!g.standalone" v-show="gUi(g).panel === 'bridge'" class="toggle-row" @click="toggleGroupPrefix(g)">
            <span class="tg-label">🏷 Префикс [TG]/[MAX]
              <span class="tg-desc muted">Добавлять метку источника перед каждым пересланным сообщением.</span>
            </span>
            <span class="switch" :class="{ on: g.prefix }"></span>
          </div>
          <div v-if="!g.standalone" v-show="gUi(g).panel === 'bridge'" class="toggle-row" @click="toggleGroupPause(g)">
            <span class="tg-label">⏸ Пауза связки
              <span class="tg-desc muted">Временно остановить зеркалирование сообщений (в обе стороны). Связка не удаляется.</span>
            </span>
            <span class="switch" :class="{ on: g.paused }"></span>
          </div>
          <div v-if="!g.standalone" v-show="gUi(g).panel === 'bridge'" class="as-settings">
            <div class="repl-dir">↔️ Направление пересылки <span class="muted small" v-if="!isPro">(PRO)</span></div>
            <div class="as-row">
              <button type="button" :class="{ sel: (g.direction || 'both') === 'both' }" @click="setGroupDir(g, 'both')">TG ↔ MAX</button>
              <button type="button" :class="{ sel: (g.direction || 'both') === 'tg>max' }" @click="setGroupDir(g, 'tg>max')">TG → MAX</button>
              <button type="button" :class="{ sel: (g.direction || 'both') === 'max>tg' }" @click="setGroupDir(g, 'max>tg')">MAX → TG</button>
            </div>
            <p class="repl-hint muted" style="margin-top:0">Односторонний режим (напр. TG → MAX) удобен, когда несколько TG-групп сливаются в одну MAX-группу.</p>
          </div>
          <div v-show="gUi(g).panel === 'welcome'" class="as-settings welcome-settings">
            <div class="repl-dir">👋 Приветствие новичков <span class="muted small" v-if="!isPro">(PRO)</span></div>
            <label class="as-field as-field-col">Текст приветствия
              <textarea
                v-model="gUi(g).welcomeText"
                rows="4"
                maxlength="1000"
                placeholder="Добро пожаловать, {name}!"
              ></textarea>
            </label>
            <p class="repl-hint muted">{{ g.standalone ? 'Отправляется новым участникам этой Telegram-группы.' : 'Отправляется новым участникам и в Telegram-, и в MAX-группе.' }}</p>
            <p class="repl-hint muted">Подстановки: <code>{name}</code> — имя, <code>{username}</code> — username, <code>{id}</code> — ID участника.</p>
            <div class="welcome-actions">
              <button type="button" class="btn accent sm" :disabled="gUi(g).busy" @click="saveGroupWelcome(g)">
                {{ gUi(g).busy ? 'Сохраняю…' : (g.welcome_text ? 'Сохранить изменения' : 'Включить приветствие') }}
              </button>
              <button v-if="g.welcome_text" type="button" class="btn ghost sm" :disabled="gUi(g).busy" @click="disableGroupWelcome(g)">Выключить</button>
            </div>
          </div>
          <div v-show="gUi(g).panel === 'antispam'" class="toggle-row antispam-master" @click="toggleGroupAntispam(g)">
            <span class="tg-label">🛡 Антиспам <span class="muted small" v-if="!isPro">(PRO)</span>
              <span class="tg-desc muted">Чистит спам в группе: удаление + мут повторных, новички не постят ссылки заданное время. Ловит обфускацию (эмодзи-слова, разрядка, гомоглифы) + LLM на смысл. ⚠️ Бот должен быть <b>администратором</b> группы (с правами удалять сообщения и банить).</span>
            </span>
            <span class="switch" :class="{ on: g.antispam }"></span>
          </div>
          <div v-if="g.antispam" v-show="gUi(g).panel === 'antispam'" class="as-settings" @change="scheduleSaveG(g)" @click="scheduleSaveG(g)">
            <div v-if="g.bot_admin === false" class="as-warn">⚠️ Бот не админ в этой группе — добавьте его администратором (с правами удалять сообщения и банить), иначе модерация не работает.
              <button class="btn ghost sm" style="margin-top:6px" @click="recheckGroup(g)">Проверить</button>
            </div>
            <div class="repl-dir">Политика антиспама</div>
            <div class="as-row">
              <span class="muted">Режим:</span>
              <button type="button" :class="{ sel: gAsOf(g).mode === 'enforce' }" @click="gAsOf(g).mode='enforce'">удалять</button>
              <button type="button" :class="{ sel: gAsOf(g).mode === 'observe' }" @click="gAsOf(g).mode='observe'">репорт</button>
              <button type="button" :class="{ sel: gAsOf(g).mode === 'debug' }" @click="gAsOf(g).mode='debug'">🐞 тест</button>
            </div>
            <p v-if="gAsOf(g).mode === 'debug'" class="repl-hint muted" style="margin-top:0">
              Тест: никого не баним, бот шлёт разбор каждого подозрительного сообщения прямо в группу (score, сигналы, что было бы сделано).
            </p>
            <button type="button" class="as-sec-head" @click="secToggle(gUi(g),'adv')">⚙️ Расширенные настройки <span class="chev">{{ secOpen(gUi(g),'adv') ? '▾' : '▸' }}</span></button>
            <div v-show="secOpen(gUi(g),'adv')">
            <label class="as-check">
              <input type="checkbox" v-model="gAsOf(g).allow_links" />
              <span>Разрешать ссылки участникам. Остальные антиспам-проверки продолжат работать.</span>
            </label>
            <label v-if="!gAsOf(g).allow_links" class="as-field">Новичкам нельзя ссылки, часов
              <input type="number" min="0" max="720" v-model.number="gAsOf(g).link_delay_h" />
            </label>
            <label class="as-field">«Доверенный» после N сообщений
              <input type="number" min="0" max="100" v-model.number="gAsOf(g).trust_msgs" />
            </label>
            <div class="as-row">
              <span class="muted">Наказание:</span>
              <button type="button" :class="{ sel: gAsOf(g).action === 'mute' }" @click="gAsOf(g).action='mute'">мут</button>
              <button type="button" :class="{ sel: gAsOf(g).action === 'ban' }" @click="gAsOf(g).action='ban'">бан</button>
              <button type="button" :class="{ sel: gAsOf(g).action === 'mute_then_ban' }" @click="gAsOf(g).action='mute_then_ban'">мут → бан</button>
            </div>
            <div class="as-row">
              <span class="muted">Тон уведомлений в чате:</span>
              <button type="button" :class="{ sel: gAsOf(g).tone === 'strict' }" @click="gAsOf(g).tone='strict'">строгий</button>
              <button type="button" :class="{ sel: gAsOf(g).tone === 'friendly' }" @click="gAsOf(g).tone='friendly'">мягкий</button>
            </div>
            <label v-if="gAsOf(g).action !== 'ban'" class="as-field">Мут на сколько минут
              <input type="number" min="1" max="43200" v-model.number="gAsOf(g).mute_minutes" />
            </label>
            <label class="as-field">{{ gAsOf(g).action === 'ban' ? 'Бан' : 'Мут' }} после N нарушений (1 = сразу)
              <input type="number" min="1" max="10" v-model.number="gAsOf(g).strike_limit" />
            </label>
            <label v-if="gAsOf(g).action === 'mute_then_ban'" class="as-field">Бан после M нарушений (M больше N)
              <input type="number" min="2" max="20" v-model.number="gAsOf(g).ban_after" />
            </label>
            <label class="as-check">
              <input type="checkbox" v-model="gAsOf(g).warn" />
              <span>Предупреждать нарушителя в чате (удаляем сообщение и пишем «нарушаете»), пока не дошло до наказания</span>
            </label>
			<label class="as-check">
			  <input type="checkbox" v-model="gAsOf(g).captcha" />
			  <span>🤖 Капча на входе (TG): новый участник мьютится до нажатия «Я не бот», не нажал — кик</span>
			</label>
			<label class="as-check">
			  <input type="checkbox" v-model="gAsOf(g).profile_guard" />
			  <span>Проверять профиль при входе (TG): подозрительный профиль получает выбранное выше наказание; в «репорт»/«тест» — только уведомление</span>
			</label>
            <label class="as-check">
              <input type="checkbox" v-model="gAsOf(g).antiraid" />
              <span>🛡 Анти-рейд (TG): при массовом входе — новичков молча мьютим на случайное время (1–6 ч), чтобы не было синхронного спама</span>
            </label>
            <div class="repl-dir" style="margin-top:10px">🚫 Запрещённый контент (наказание — по политике выше)</div>
            <div class="as-cats">
              <label v-for="cat in blockCatList" :key="cat.key" class="as-check">
                <input type="checkbox" :checked="hasCat(gAsOf(g), cat.key)" @change="toggleCat(gAsOf(g), cat.key)" />
                <span>{{ cat.label }}</span>
              </label>
            </div>
            <label class="as-field as-field-col">Свои запрещённые слова/фразы (по одному в строке или через запятую)
              <textarea v-model="gAsOf(g).block_words" rows="3" placeholder="usdt
обмен крипты
заработок"></textarea>
            </label>
            <div class="as-row">
              <span class="muted">Уведомлять в ЛС:</span>
              <button type="button" :class="{ sel: gAsOf(g).notify === 'off' }" @click="gAsOf(g).notify='off'">выкл</button>
              <button type="button" :class="{ sel: gAsOf(g).notify === 'ban' }" @click="gAsOf(g).notify='ban'">о банах</button>
              <button type="button" :class="{ sel: gAsOf(g).notify === 'all' }" @click="gAsOf(g).notify='all'">обо всём</button>
            </div>
            <p class="repl-hint muted">«мут → бан» — мут после N нарушений, бан после M. Уведомления и предупреждения троттлятся (не чаще раза в 30 сек на чат).</p>
            <div class="repl-dir" style="margin-top:10px">📋 Кастомные правила</div>
            <div v-if="(g.rules || []).length" class="as-rules">
              <div v-for="r in g.rules" :key="r.rid" class="as-rule-row">
                <span class="small">{{ r.descr }} · {{ r.keywords || 'семантика' }} · {{ r.action }} · warns:{{ r.warns }}</span>
                <button type="button" class="btn ghost sm" @click="delAsRule(g, r.rid)">🗑</button>
              </div>
            </div>
            <p v-else class="repl-hint muted">Правил нет. Описание обязательно; слова опционально (пусто = ловит по смыслу через AI).</p>
            <div class="as-rule-add">
              <input v-model="gRuleForm(g).descr" placeholder="Описание (напр. мат, реклама казино)" />
              <input v-model="gRuleForm(g).keywords" placeholder="Слова через запятую (опционально)" />
              <div class="as-row">
                <span class="muted">Действие:</span>
                <button type="button" :class="{ sel: gRuleForm(g).action === 'delete' }" @click="gRuleForm(g).action='delete'">удалить</button>
                <button type="button" :class="{ sel: gRuleForm(g).action === 'mute' }" @click="gRuleForm(g).action='mute'">мут</button>
                <button type="button" :class="{ sel: gRuleForm(g).action === 'ban' }" @click="gRuleForm(g).action='ban'">бан</button>
              </div>
              <label class="as-field">Предупреждений до наказания (0 = сразу)
                <input type="number" min="0" max="10" v-model.number="gRuleForm(g).warns" />
              </label>
              <button type="button" class="btn accent sm" @click="addAsRule(g)">➕ Добавить правило</button>
            </div>
            </div>
            <p class="repl-hint muted" style="text-align:center;margin:6px 0 0">✓ Изменения сохраняются автоматически</p>
          </div>
          <div v-show="gUi(g).panel === 'other'" class="toggle-row" @click="toggleGroupDelService(g)">
            <span class="tg-label">🧹 Удалять системные сообщения
              <span class="tg-desc muted">Авто-удаление служебных сообщений в группе: вошёл / вышел / сменил название / закрепил. Нужны права админа на удаление.</span>
            </span>
            <span class="switch" :class="{ on: gAsOf(g).del_service }"></span>
          </div>
          <div v-if="!g.standalone" v-show="gUi(g).panel === 'other'" class="danger-zone">
            <template v-if="!gUi(g).confirm">
              <button class="btn danger full" @click="gUi(g).confirm = true">🗑 Разорвать связку</button>
            </template>
            <template v-else>
              <span class="muted small">Точно разорвать мост группы?</span>
              <button class="btn danger sm" :disabled="gUi(g).busy" @click="doUnbridge(g)">{{ gUi(g).busy ? '…' : 'Да' }}</button>
              <button class="btn ghost sm" :disabled="gUi(g).busy" @click="gUi(g).confirm = false">Отмена</button>
            </template>
          </div>
        </div>
      </div>

      </div>

      <div v-show="((!browserMode && tab === 'mirrors') || (browserMode && currentSection === 'mirrors')) && !anyOpen">
      <h2>Зеркала <span class="muted">({{ mirrors.length }})</span></h2>
      <p class="muted small" style="margin-top:-4px">Односторонние копии постов канала в группы: MAX-канал → MAX-группы и TG-канал → TG-группы (без плашки «переслано»).</p>
      <p v-if="!mirrors.length" class="muted small">
        Пока пусто. Подключение: в личке бота отправьте <b>/mirror</b> и перешлите пост канала-донора,
        затем в каждой группе-приёмнике — <b>/mirror &lt;id канала&gt;</b> (бот должен быть админом).
      </p>
      <div v-for="m in mirrors" :key="m.platform + m.src_chat + '-' + m.dst_chat" class="card">
        <div class="card-head static">
          <div>
            <div><b>{{ m.platform === 'tg' ? 'TG' : 'MAX' }}</b> · {{ m.src_title || m.src_chat }} → {{ m.dst_title || m.dst_chat }}</div>
            <div class="muted small">{{ m.src_chat }} → {{ m.dst_chat }}</div>
          </div>
          <button v-if="m.owned" class="btn danger" :disabled="mirrorBusy[m.platform + m.src_chat + '-' + m.dst_chat]"
            @click="onDeleteMirror(m)">Удалить</button>
        </div>
      </div>
      </div>

      <div v-show="browserMode && currentSection === 'vk' && !anyOpen">
        <div class="page-intro">
          <div>
            <h2>VK-связки <span class="muted">({{ vkBindings.length }})</span></h2>
            <p class="muted small">Кросспостинг публикаций и мосты бесед между VK, Telegram и MAX.</p>
          </div>
          <div class="page-actions">
            <button v-if="vkCommunities.length" class="btn ghost page-action" @click="openVKWizard">
              Связать беседу
            </button>
            <button class="btn accent page-action" :disabled="vkConnecting" @click="connectVK">
              {{ vkConnecting ? 'Открываем VK…' : 'Подключить VK' }}
            </button>
          </div>
        </div>
        <div v-if="vkCommunities.length" class="vk-community-list">
          <span class="muted small">Подключённые сообщества:</span>
          <span v-for="id in vkCommunities" :key="id" class="vk-community">VK {{ id }}</span>
        </div>
        <section v-if="vkWizardOpen" class="vk-wizard" aria-labelledby="vk-wizard-title">
          <div class="vk-wizard-head">
            <div>
              <h3 id="vk-wizard-title">Связать беседу VK</h3>
              <p class="muted small">Сообщения и ответы будут передаваться между беседой VK и выбранной группой.</p>
            </div>
            <button class="vk-close" type="button" aria-label="Закрыть" :disabled="vkCreating" @click="closeVKWizard">
              <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18"/></svg>
            </button>
          </div>

          <div class="vk-step">
            <span class="vk-step-number">1</span>
            <div class="vk-step-body">
              <h4>Выберите беседу VK</h4>
              <p class="muted small">
                Сначала добавьте одно из подключённых сообществ участником беседы и отправьте в беседу любое сообщение.
              </p>
              <label class="vk-field">
                <span>Ссылка на беседу</span>
                <div class="vk-link-row">
                  <input v-model.trim="vkChatLink" type="url" inputmode="url"
                    placeholder="https://vk.ru/im?sel=c160" @keyup.enter="findVKChatByLink()" />
                  <button class="btn ghost" type="button" :disabled="vkChatsLoading" @click="findVKChatByLink()">Найти</button>
                </div>
              </label>
              <div class="vk-list-head">
                <span class="muted small">Доступные беседы</span>
                <button class="text-button" type="button" :disabled="vkChatsLoading" @click="loadVKChats">
                  {{ vkChatsLoading ? 'Обновляем…' : 'Обновить список' }}
                </button>
              </div>
              <div v-if="vkChatsLoading && !vkChatsLoaded" class="vk-loading">Получаем беседы из VK…</div>
              <div v-else-if="vkChatsLoaded && !vkChats.length" class="vk-empty">
                Беседы не найдены. Проверьте, что сообщество добавлено в беседу и в ней уже появилось новое сообщение.
              </div>
              <div v-else class="vk-choice-list">
                <button v-for="chat in vkChats" :key="vkChatKey(chat)" type="button"
                  class="vk-choice" :class="{ selected: vkSelectedChat === vkChatKey(chat) }"
                  :aria-pressed="vkSelectedChat === vkChatKey(chat)"
                  @click="vkSelectedChat = vkChatKey(chat); vkWizardError = ''">
                  <span class="vk-radio" aria-hidden="true"></span>
                  <span>
                    <b>{{ chat.title }}</b>
                    <small>Сообщество VK {{ chat.community_id }}</small>
                  </span>
                </button>
              </div>
              <p v-if="vkChatsFailed.length" class="vk-warning small">
                Не удалось прочитать беседы сообществ: {{ vkChatsFailed.join(', ') }}. Переподключите их к VK.
              </p>
            </div>
          </div>

          <div class="vk-step">
            <span class="vk-step-number">2</span>
            <div class="vk-step-body">
              <h4>Куда передавать сообщения</h4>
              <p class="muted small">Показываются группы, которыми вы управляете. Направление можно изменить после подключения.</p>
              <div v-if="!vkSources.length" class="vk-empty">
                Подходящих групп пока нет. Сначала добавьте Bridge Bot в группу Telegram или MAX и настройте мост группы.
              </div>
              <div v-else class="vk-choice-list">
                <button v-for="source in vkSources" :key="vkSourceKey(source)" type="button"
                  class="vk-choice" :class="{ selected: vkSelectedSource === vkSourceKey(source) }"
                  :aria-pressed="vkSelectedSource === vkSourceKey(source)"
                  @click="vkSelectedSource = vkSourceKey(source); vkWizardError = ''">
                  <span class="vk-radio" aria-hidden="true"></span>
                  <span>
                    <b>{{ source.title }}</b>
                    <small>{{ source.platform.toUpperCase() }}</small>
                  </span>
                </button>
              </div>
            </div>
          </div>

          <p v-if="vkWizardError" class="vk-form-error" role="alert">{{ vkWizardError }}</p>
          <div class="vk-wizard-footer">
            <span class="muted small">Связка занимает 1 слот PRO.</span>
            <button class="btn accent" type="button"
              :disabled="vkCreating || !selectedVKChat || !selectedVKSource" @click="createVKChat">
              {{ vkCreating ? 'Подключаем…' : 'Связать в обе стороны' }}
            </button>
          </div>
        </section>
        <div v-if="!vkCommunities.length" class="empty-card">
          <h3>VK ещё не подключён</h3>
          <p class="muted small">Выберите сообщество, которым управляете, и подтвердите разрешения VK. Команды в боте не нужны.</p>
          <button class="btn accent" :disabled="vkConnecting" @click="connectVK">
            {{ vkConnecting ? 'Открываем VK…' : 'Выбрать сообщество VK' }}
          </button>
        </div>
        <p v-else-if="!vkBindings.length" class="empty-card muted small">
          Сообщество подключено, но связок пока нет. Отправьте боту <code>/vk</code> и выберите, что связать.
        </p>
        <article v-for="v in vkBindings" :key="v.id" class="card vk-card">
          <div class="vk-card-head">
            <div class="body">
              <b>{{ vkSource(v) }} → {{ vkTarget(v) }}</b>
              <span class="muted small">{{ vkDirection(v) }}<template v-if="v.paused"> · на паузе</template></span>
            </div>
            <span class="status-pill" :class="{ paused: v.paused }">{{ v.paused ? 'Пауза' : 'Работает' }}</span>
          </div>
          <div class="vk-card-body">
            <div class="repl-dir">Направление</div>
            <div class="as-row vk-directions">
              <button :disabled="vkBusy[v.id]" :class="{ sel: v.direction === 'source>vk' }" @click="changeVKDirection(v, 'source>vk')">{{ v.source_platform.toUpperCase() }} → VK</button>
              <button :disabled="vkBusy[v.id]" :class="{ sel: v.direction === 'vk>source' }" @click="changeVKDirection(v, 'vk>source')">VK → {{ v.source_platform.toUpperCase() }}</button>
              <button :disabled="vkBusy[v.id]" :class="{ sel: v.direction === 'both' }" @click="changeVKDirection(v, 'both')">Оба</button>
            </div>
            <div class="vk-actions">
              <button class="btn ghost sm" :disabled="vkBusy[v.id]" @click="toggleVKPause(v)">{{ v.paused ? 'Возобновить' : 'Поставить на паузу' }}</button>
              <template v-if="!vkConfirm[v.id]">
                <button class="btn danger sm" @click="vkConfirm[v.id] = true">Удалить</button>
              </template>
              <template v-else>
                <span class="muted small">Удалить связку?</span>
                <button class="btn danger sm" :disabled="vkBusy[v.id]" @click="removeVK(v)">Да, удалить</button>
                <button class="btn ghost sm" @click="vkConfirm[v.id] = false">Отмена</button>
              </template>
            </div>
          </div>
        </article>
      </div>

      <div v-show="((!browserMode && tab === 'blocks') || (browserMode && currentSection === 'moderation')) && !anyOpen">
      <h2>Заблокированные <span class="muted">({{ blocks.length }})</span></h2>
      <p class="muted small" style="margin-top:-4px">Кого антиспам замьютил или забанил во всех ваших каналах и группах. Можно вернуть.</p>
      <p v-if="!blocks.length" class="muted small">Пока пусто — здесь появятся те, кого антиспам наказал в режиме «удалять» (в «репорт»/«тест» баны не выдаются).</p>
      <div v-for="b in blocks" :key="bKey(b)" class="card">
        <div class="card-head static">
			  <span class="row-icon">{{ b.action === 'ban' ? '⛔' : (b.action === 'kick' ? '↪' : '🔇') }}</span>
          <span class="body">
            <span class="name">{{ b.title || (b.platform.toUpperCase() + ' ' + b.chat_id) }}</span>
            <span class="muted small">
			      {{ b.action === 'ban' ? 'бан' : (b.action === 'kick' ? 'удалён' : 'мут') }} · {{ b.platform.toUpperCase() }} · юзер {{ b.user_id }} · {{ fmtDate(b.at) }}
              <template v-if="b.reason"> · {{ reasonLabel(b.reason) }}</template>
            </span>
            <span v-if="b.text" class="block-text"><b>Снимок профиля:</b>
{{ b.text }}</span>
          </span>
          <button class="btn ghost sm" :disabled="blockBusy[bKey(b)]" @click="doUnban(b)">
			    {{ blockBusy[bKey(b)] ? '…' : (b.action === 'kick' ? 'Убрать запись' : (b.action === 'ban' ? 'Разбанить' : 'Снять мут')) }}
          </button>
        </div>
      </div>
      </div>

      <div id="import" v-show="!anyOpen && (!browserMode || currentSection === 'import')" class="section-anchor">
      <h2>Импорт истории</h2>
      <div class="import-card">
        <div>Баланс постов: <b>{{ importBalance }}</b></div>
        <div style="display:flex;gap:6px;flex-wrap:wrap;justify-content:flex-end">
          <button v-for="p in postPackages" :key="p.Posts" class="btn accent sm" :disabled="buying" @click="buy(p.Posts)">
            {{ buying ? '…' : p.Posts + ' — ' + rub(p.Amount) }}
          </button>
        </div>
      </div>
      <p class="muted" style="font-size:12px;margin-top:6px">
        Посты тратятся при переносе истории канала в MAX. Антиспам в группах — команда <code>/antispam on</code> (PRO).
      </p>
      </div>
    </template>

    <p v-if="toast" class="toast">{{ toast }}</p>
    </section>
  </main>
</template>

<style scoped>
.top { display: flex; align-items: center; justify-content: space-between; }
.back-bar { display: flex; align-items: center; gap: 6px; width: 100%; background: none; border: 0; color: var(--accent); font: inherit; font-weight: 600; font-size: 15px; cursor: pointer; padding: 6px 0 12px; min-height: 44px; }
.icon-btn { background: none; border: 0; font-size: 20px; cursor: pointer; color: var(--text-muted); min-width: 44px; min-height: 44px; display: inline-flex; align-items: center; justify-content: center; }
.whoami { font-size: 13px; color: var(--text-muted); margin-bottom: 12px; }
.small { font-size: 13px; }
h2 { font-size: 15px; margin: 20px 0 8px; }
.muted { color: var(--text-muted); }

.cta { padding: 18px 16px; border-radius: 14px; background: linear-gradient(135deg, var(--accent), #6d5cff); color: #fff; }
.cta-title { font-size: 18px; font-weight: 700; }
.cta-sub { font-size: 13px; opacity: .9; margin: 4px 0 14px; }
.cta .btn.accent { background: #fff; color: var(--accent); }
.btn.ghost-light { background: rgba(255,255,255,.15); color: #fff; }
.pro-banner { border-radius: 12px; background: var(--surface); border: 1px solid var(--border); overflow: hidden; }
.pro-banner.active { border-color: var(--accent); }
.pro-head { width: 100%; display: flex; align-items: center; gap: 12px; padding: 14px; background: none; border: 0; cursor: pointer; text-align: left; color: var(--text); }
.pro-head > div { flex: 1; min-width: 0; }
.pro-body { padding: 0 14px 14px; }

.btn { border: 0; border-radius: 10px; cursor: pointer; font-weight: 600; font-size: 14px; }
.btn.accent { background: var(--accent); color: #fff; }
.btn.danger { background: transparent; color: #e5484d; border: 1px solid rgba(229,72,77,.4); }
.btn.ghost { background: var(--surface); color: var(--text); border: 1px solid var(--border); }
.btn.sm { padding: 9px 14px; min-height: 40px; flex: 0 0 auto; }
.btn.full { width: 100%; padding: 11px; margin-top: 8px; }
.btn:disabled { opacity: .5; cursor: default; }

.card { border: 1px solid var(--border); border-radius: 12px; margin-bottom: 10px; overflow: hidden; }
.card-head { width: 100%; display: flex; align-items: center; gap: 10px; padding: 12px; background: var(--surface); border: 0; cursor: pointer; text-align: left; }
.card-head.static { cursor: default; }
.row-icon { flex: 0 0 auto; font-size: 18px; }
.card-head .body { flex: 1; display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.card-head .name { color: var(--text); font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.chev { color: var(--text-muted); flex: 0 0 auto; }
.card-body { padding: 12px; border-top: 1px solid var(--border); }

.toggle-row { display: flex; align-items: flex-start; justify-content: space-between; gap: 14px; padding: 14px 0; cursor: pointer; color: var(--text); border-bottom: 1px solid var(--border); }
.toggle-row:last-child { border-bottom: 0; }
.tg-label { display: flex; flex-direction: column; gap: 3px; font-weight: 600; }
.tg-desc { font-size: 12.5px; line-height: 1.45; font-weight: 400; margin-top: 1px; }
.toggle-row .switch { margin-top: 2px; }
.switch { width: 40px; height: 24px; border-radius: 12px; background: var(--border); position: relative; transition: background .15s; flex: 0 0 auto; }
.switch::after { content: ''; position: absolute; top: 2px; left: 2px; width: 20px; height: 20px; border-radius: 50%; background: #fff; transition: left .15s; }
.switch.on { background: var(--accent); }
.switch.on::after { left: 18px; }

.repl { margin-top: 10px; padding-top: 10px; border-top: 1px solid var(--border); }
.repl-h { font-weight: 600; margin-bottom: 4px; }
.repl-intro { font-size: 12px; line-height: 1.45; margin: 0 0 8px; }
.repl-dir { font-size: 12px; color: var(--text-muted); margin: 12px 0 6px; text-transform: uppercase; letter-spacing: .03em; font-weight: 600; }
.as-sec-head { display: flex; align-items: center; justify-content: space-between; width: 100%; background: none; border: none; padding: 10px 0; margin: 6px 0 2px; font-size: 14px; font-weight: 600; color: var(--text); cursor: pointer; border-top: 1px solid var(--border, #e2e0d8); }
.as-sec-head .chev { font-size: 12px; }
.repl-empty { font-size: 13px; margin: 4px 0 8px; }
.repl-ex { color: var(--accent); cursor: pointer; font-weight: 600; }
.repl-rule { display: flex; align-items: flex-start; gap: 8px; margin-bottom: 10px; padding: 10px; border: 1px solid var(--border); border-radius: 10px; background: var(--surface); }
.repl-fields { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 8px; }
.repl-fields label { display: flex; flex-direction: column; gap: 3px; font-size: 11px; color: var(--text-muted); }
.repl-fields input { padding: 10px 11px; min-height: 44px; border-radius: 8px; border: 1px solid var(--border); background: var(--bg); color: var(--text); font-size: 16px; }
.repl-scope { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; font-size: 12px; }
.repl-scope button { padding: 8px 12px; min-height: 36px; border-radius: 14px; border: 1px solid var(--border); background: var(--bg); color: var(--text-muted); font-size: 13px; cursor: pointer; }
.repl-scope button.sel { background: var(--accent); color: #fff; border-color: var(--accent); }
.repl-rule .x { flex: 0 0 auto; background: none; border: 0; color: #e5484d; font-size: 16px; cursor: pointer; padding: 2px 4px; }
.repl-hint { font-size: 12px; line-height: 1.45; margin: 4px 0 10px; }
.block-text { display: block; margin-top: 5px; font-size: 12px; line-height: 1.4; opacity: 0.9; overflow-wrap: anywhere; white-space: pre-wrap; }
.card-row { display: flex; align-items: center; justify-content: space-between; gap: 10px; margin-bottom: 10px; }
.admin-link { margin: 10px 0; }
.as-check { display: flex; gap: 10px; align-items: flex-start; margin: 12px 0; font-size: 14px; line-height: 1.45; cursor: pointer; min-height: 28px; }
.as-check input { margin-top: 2px; flex: 0 0 auto; width: 20px; height: 20px; }
.add { background: none; border: 1px dashed var(--border); color: var(--text-muted); border-radius: 8px; padding: 10px 14px; min-height: 40px; cursor: pointer; font-size: 14px; }

.danger-zone { margin-top: 12px; padding-top: 12px; border-top: 1px solid var(--border); display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }

.tabs { display: flex; gap: 8px; margin: 18px 0 12px; }
.tabs button { flex: 1; padding: 11px 8px; min-height: 44px; border-radius: 10px; border: 1px solid var(--border); background: var(--surface); color: var(--text-muted); font: inherit; font-weight: 600; cursor: pointer; }
.tabs button.active { background: var(--accent, #2563eb); color: #fff; border-color: transparent; }
.as-warn { color: #f85149; font-size: 12px; line-height: 1.4; margin: 6px 0 0; }
.help-card { border: 1px dashed var(--border); border-radius: 12px; background: transparent; margin-bottom: 10px; }
.acc-card { border: 1px solid var(--accent, #2563eb); border-style: solid; }
.acc-code { font-family: ui-monospace, monospace; font-size: 18px; font-weight: 700; letter-spacing: 1px; background: var(--bg); border: 1px solid var(--border); border-radius: 8px; padding: 10px 12px; text-align: center; user-select: all; }
.help-head { width: 100%; display: flex; align-items: center; gap: 10px; padding: 11px 14px; background: none; border: none; cursor: pointer; color: var(--text-muted); font: inherit; }
.help-q { flex: 0 0 auto; width: 20px; height: 20px; border-radius: 50%; background: var(--border); color: var(--text); font-size: 13px; font-weight: 700; display: flex; align-items: center; justify-content: center; }
.help-title { flex: 1; text-align: left; font-size: 14px; font-weight: 500; }
.help-body { padding: 0 14px 14px; }
.lk-steps { margin: 0; padding-left: 20px; display: flex; flex-direction: column; gap: 10px; font-size: 13px; line-height: 1.45; }
.lk-steps code { background: var(--bg); border: 1px solid var(--border); border-radius: 5px; padding: 1px 5px; font-size: 12px; }
.as-settings { margin: 10px 0 16px; padding: 14px; border: 1px solid var(--border); border-radius: 12px; background: var(--surface); }
.as-settings .repl-dir:first-child { margin-top: 0; }
.group-settings-nav { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 8px; margin-bottom: 14px; }
.group-settings-nav button { position: relative; display: flex; align-items: center; justify-content: center; gap: 7px; min-height: 48px; padding: 9px 10px; border: 1px solid var(--border); border-radius: 10px; background: var(--surface); color: var(--text-muted); font: inherit; font-size: 13px; font-weight: 700; cursor: pointer; }
.group-settings-nav button:hover { border-color: color-mix(in srgb, var(--accent) 45%, var(--border)); color: var(--text); }
.group-settings-nav button:focus-visible { outline: 3px solid color-mix(in srgb, var(--accent) 30%, transparent); outline-offset: 2px; }
.group-settings-nav button.active { border-color: var(--accent); background: color-mix(in srgb, var(--accent) 8%, var(--surface)); color: var(--text); box-shadow: inset 0 0 0 1px var(--accent); }
.group-settings-nav button > span { font-size: 16px; line-height: 1; }
.group-settings-nav i { padding: 2px 5px; border-radius: 999px; background: var(--border); color: var(--text-muted); font-size: 9px; font-style: normal; line-height: 1.2; text-transform: uppercase; }
.group-settings-nav i.on { background: #dcfce7; color: #166534; }
.group-panel-intro { display: grid; gap: 3px; margin: 0 0 12px; padding: 0 2px; }
.group-panel-intro b { font-size: 15px; }
.group-panel-intro span { color: var(--text-muted); font-size: 12px; line-height: 1.4; }
.antispam-master { border: 1px solid color-mix(in srgb, var(--accent) 20%, var(--border)); border-radius: 12px; padding: 14px; background: color-mix(in srgb, var(--accent) 4%, var(--surface)); }
/* Segmented control: подпись над контролом, кнопки — нейтральные плашки;
   выбранная = белая с акцентной рамкой (БЕЗ синей заливки), чтобы не было «каши». */
.as-row { display: flex; align-items: center; gap: 8px; margin: 14px 0; flex-wrap: wrap; }
.as-row > .muted { flex: 1 0 100%; margin-bottom: 4px; }
.as-row button { flex: 1; min-width: 64px; min-height: 44px; padding: 9px 10px; border-radius: 9px; border: 1px solid var(--border); background: var(--bg); color: var(--text-muted); font-size: 14px; font-weight: 600; cursor: pointer; transition: border-color .12s, color .12s; }
.as-row button.sel { color: var(--text); border-color: var(--accent); box-shadow: inset 0 0 0 1px var(--accent); font-weight: 700; }
.as-field { display: flex; align-items: center; justify-content: space-between; gap: 10px; font-size: 14px; margin: 10px 0; min-height: 44px; }
.as-field input { width: 96px; min-height: 44px; padding: 9px 11px; border-radius: 8px; border: 1px solid var(--border); background: var(--bg); color: var(--text); text-align: right; font-size: 16px; }
.as-field-col { flex-direction: column; align-items: stretch; }
.as-field-col textarea { width: 100%; min-height: 64px; margin-top: 4px; padding: 9px 11px; border-radius: 8px; border: 1px solid var(--border); background: var(--bg); color: var(--text); font: inherit; font-size: 16px; resize: vertical; box-sizing: border-box; text-align: left; }
.welcome-actions { display: flex; gap: 8px; flex-wrap: wrap; }
.lk-note { font-size: 12px; line-height: 1.45; margin: 12px 0 0; padding-top: 10px; border-top: 1px solid var(--border); }
.lk-note code { background: var(--bg); border: 1px solid var(--border); border-radius: 5px; padding: 1px 5px; }
.lk-links { display: flex; gap: 8px; margin-top: 6px; }
.lk-bot { flex: 1; text-align: center; padding: 8px 10px; border-radius: 8px; background: var(--accent, #2563eb); color: #fff; text-decoration: none; font-weight: 600; font-size: 13px; }
.import-card { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 14px; border-radius: 12px; background: var(--surface); border: 1px solid var(--border); }
code { background: var(--surface); padding: 1px 5px; border-radius: 5px; font-size: 12px; }

.toast { position: fixed; left: 50%; bottom: 20px; transform: translateX(-50%); background: var(--text); color: var(--bg); padding: 10px 16px; border-radius: 10px; font-size: 14px; box-shadow: 0 4px 20px rgba(0,0,0,.3); max-width: 90%; text-align: center; }

.cabinet-content { min-width: 0; }
.cabinet-sidebar { display: none; }
.eyebrow { margin: 0 0 3px; color: var(--text-muted); font-size: 12px; font-weight: 700; letter-spacing: .06em; text-transform: uppercase; }
.overview-grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; margin: 12px 0; }
.overview-grid article { padding: 16px; border: 1px solid var(--border); border-radius: 14px; background: var(--surface); display: grid; gap: 3px; }
.overview-grid article > span { color: var(--text-muted); font-size: 12px; font-weight: 600; }
.overview-grid strong { font-size: 28px; line-height: 1.1; }
.overview-grid small { color: var(--text-muted); font-size: 12px; }
.overview-grid .attention strong { color: #b45309; }
.focus-card { display: flex; align-items: center; justify-content: space-between; gap: 24px; padding: 18px; margin: 12px 0 20px; border: 1px solid color-mix(in srgb, var(--accent) 25%, var(--border)); border-radius: 14px; background: color-mix(in srgb, var(--accent) 5%, var(--bg)); }
.focus-card h2 { margin: 2px 0 4px; font-size: 18px; }
.focus-card p:last-child { margin: 0; font-size: 13px; }
.focus-card .btn { min-height: 44px; padding: 0 18px; white-space: nowrap; }
.section-anchor { scroll-margin-top: 20px; }
.login-card { max-width: 520px; margin: 12vh auto 0; padding: 32px; border: 1px solid var(--border); border-radius: 20px; background: var(--bg); box-shadow: 0 18px 50px rgba(15,23,42,.08); }
.login-mark, .brand-mark { display: grid; place-items: center; color: #fff; background: var(--accent); font-weight: 800; }
.login-mark { width: 48px; height: 48px; border-radius: 14px; margin-bottom: 20px; }
.login-card h2 { margin: 0 0 8px; font-size: 24px; }
.login-actions { display: flex; gap: 10px; margin-top: 22px; }
.login-actions .btn { flex: 1; min-height: 46px; display: grid; place-items: center; padding: 0 16px; text-decoration: none; }
.security-note { margin: 18px 0 0; padding-top: 16px; border-top: 1px solid var(--border); color: var(--text-muted); font-size: 12px; }
.page-intro { display: flex; align-items: flex-start; justify-content: space-between; gap: 20px; margin-bottom: 16px; }
.page-intro h2 { margin: 0 0 3px; font-size: 18px; }
.page-intro p { margin: 0; }
.page-actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; }
.page-action { min-height: 44px; padding: 0 16px; display: inline-flex; align-items: center; text-decoration: none; white-space: nowrap; }
.vk-community-list { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; margin-bottom: 14px; }
.vk-community { padding: 5px 9px; border-radius: 999px; background: #eef2ff; color: #3730a3; font-size: 12px; font-weight: 700; }
.vk-wizard { margin: 14px 0 18px; border: 1px solid var(--border); border-radius: 16px; background: var(--surface); overflow: hidden; }
.vk-wizard-head { display: flex; justify-content: space-between; align-items: flex-start; gap: 16px; padding: 18px; border-bottom: 1px solid var(--border); }
.vk-wizard-head h3 { margin: 0 0 4px; font-size: 17px; }
.vk-wizard-head p { margin: 0; }
.vk-close { width: 44px; height: 44px; display: grid; place-items: center; flex: 0 0 auto; border: 1px solid var(--border); border-radius: 12px; background: var(--bg); color: var(--text); cursor: pointer; }
.vk-close svg { width: 20px; height: 20px; fill: none; stroke: currentColor; stroke-width: 2; stroke-linecap: round; }
.vk-close:focus-visible, .vk-choice:focus-visible, .text-button:focus-visible { outline: 3px solid rgba(80, 110, 255, .28); outline-offset: 2px; }
.vk-step { display: grid; grid-template-columns: 32px minmax(0, 1fr); gap: 12px; padding: 18px; border-bottom: 1px solid var(--border); }
.vk-step-number { width: 30px; height: 30px; display: grid; place-items: center; border-radius: 50%; background: var(--accent); color: #fff; font-weight: 800; font-size: 13px; }
.vk-step-body { min-width: 0; }
.vk-step-body h4 { margin: 3px 0 5px; font-size: 15px; }
.vk-step-body > p { margin: 0 0 12px; }
.vk-field { display: grid; gap: 6px; margin: 12px 0; font-size: 13px; font-weight: 700; }
.vk-link-row { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 8px; }
.vk-link-row input { width: 100%; min-height: 44px; box-sizing: border-box; border: 1px solid var(--border); border-radius: 11px; padding: 0 12px; background: var(--bg); color: var(--text); font: inherit; }
.vk-link-row input:focus { border-color: var(--accent); outline: 3px solid rgba(80, 110, 255, .18); }
.vk-list-head { display: flex; justify-content: space-between; align-items: center; gap: 12px; margin: 14px 0 7px; }
.text-button { min-height: 44px; border: 0; padding: 0 6px; background: transparent; color: var(--accent); font: inherit; font-weight: 700; cursor: pointer; }
.text-button:disabled { cursor: default; opacity: .55; }
.vk-choice-list { display: grid; gap: 8px; max-height: 280px; overflow: auto; padding: 2px; }
.vk-choice { min-height: 54px; display: grid; grid-template-columns: 20px minmax(0, 1fr); align-items: center; gap: 10px; width: 100%; border: 1px solid var(--border); border-radius: 12px; padding: 10px 12px; background: var(--bg); color: var(--text); text-align: left; font: inherit; cursor: pointer; transition: border-color .18s ease, background .18s ease; }
.vk-choice:hover { border-color: var(--accent); }
.vk-choice.selected { border-color: var(--accent); background: rgba(80, 110, 255, .08); }
.vk-choice b, .vk-choice small { display: block; overflow-wrap: anywhere; }
.vk-choice small { margin-top: 3px; color: var(--text-muted); font-size: 12px; }
.vk-radio { width: 18px; height: 18px; box-sizing: border-box; border: 2px solid var(--border); border-radius: 50%; box-shadow: inset 0 0 0 4px var(--bg); }
.vk-choice.selected .vk-radio { border-color: var(--accent); background: var(--accent); }
.vk-empty, .vk-loading { padding: 13px; border: 1px dashed var(--border); border-radius: 11px; color: var(--text-muted); font-size: 13px; line-height: 1.45; }
.vk-warning { margin: 10px 0 0; color: #9a6700; }
.vk-form-error { margin: 14px 18px 0; padding: 11px 12px; border-radius: 11px; background: rgba(205, 64, 64, .1); color: #b43c3c; font-size: 13px; line-height: 1.4; }
.vk-wizard-footer { display: flex; align-items: center; justify-content: space-between; gap: 14px; padding: 16px 18px; }
.vk-wizard-footer .btn { min-height: 44px; }
.empty-card { padding: 20px; border: 1px dashed var(--border); border-radius: 14px; background: var(--bg); }
.empty-card h3 { margin: 0 0 6px; font-size: 16px; }
.empty-card .btn { min-height: 44px; margin-top: 10px; padding: 0 16px; display: inline-flex; align-items: center; text-decoration: none; }
.vk-card-head { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 14px; background: var(--surface); }
.vk-card-head .body { display: grid; gap: 3px; }
.status-pill { padding: 5px 9px; border-radius: 999px; background: #dcfce7; color: #166534; font-size: 12px; font-weight: 700; }
.status-pill.paused { background: #fef3c7; color: #92400e; }
.vk-card-body { padding: 14px; border-top: 1px solid var(--border); }
.vk-directions { margin-top: 6px; }
.vk-actions { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; margin-top: 12px; padding-top: 12px; border-top: 1px solid var(--border); }

@media (min-width: 960px) {
  .cabinet-desktop.wrap {
    --bg: #fff; --surface: #f8fafc; --text: #0f172a; --text-muted: #64748b;
    --accent: #2563eb; --border: #e2e8f0; --danger: #dc2626;
    max-width: none; min-height: 100dvh; margin: 0; padding: 0; display: grid;
    grid-template-columns: 244px minmax(0, 1fr); background: #f8fafc; color-scheme: light;
  }
  .cabinet-desktop.auth-required { grid-template-columns: minmax(0, 1fr); }
  .cabinet-desktop.auth-required .cabinet-content { width: min(720px, calc(100vw - 48px)); }
  .cabinet-desktop .cabinet-sidebar { position: sticky; top: 0; height: 100dvh; padding: 24px 16px; display: flex; flex-direction: column; border-right: 1px solid #e2e8f0; background: #fff; }
  .brand { display: flex; align-items: center; gap: 11px; padding: 0 8px 24px; color: #0f172a; text-decoration: none; }
  .brand-mark { width: 36px; height: 36px; border-radius: 10px; }
  .brand span:last-child { display: grid; line-height: 1.2; }
  .brand small { color: #64748b; font-size: 11px; font-weight: 500; }
  .side-nav { display: grid; gap: 4px; }
.side-nav a, .side-bottom button, .side-bottom a { min-height: 44px; padding: 0 12px; border: 0; border-radius: 10px; background: transparent; color: #475569; font: inherit; font-size: 14px; font-weight: 600; text-align: left; cursor: pointer; text-decoration: none; display: flex; align-items: center; }
.side-nav a:hover, .side-bottom button:hover, .side-bottom a:hover { background: #f1f5f9; color: #0f172a; }
.side-nav a.active { background: #eff6ff; color: #1d4ed8; }
  .side-bottom { margin-top: auto; display: grid; gap: 4px; padding-top: 16px; border-top: 1px solid #e2e8f0; }
  .cabinet-desktop .cabinet-content { width: min(1080px, calc(100vw - 292px)); margin: 0 auto; padding: 30px 24px 64px; }
  .cabinet-desktop .top h1 { margin: 0; font-size: 26px; }
  .cabinet-desktop .whoami { margin-top: 6px; }
  .cabinet-desktop .tabs { max-width: 720px; }
  .cabinet-desktop .card, .cabinet-desktop .help-card, .cabinet-desktop .pro-banner, .cabinet-desktop .cta, .cabinet-desktop .import-card { background-color: #fff; }
  .cabinet-desktop .cta { color: var(--text); background: #fff; border: 1px solid var(--border); }
  .cabinet-desktop .cta .cta-sub { color: var(--text-muted); opacity: 1; }
  .cabinet-desktop .cta .btn.accent { color: #fff; background: var(--accent); }
  .cabinet-desktop .cta .btn.ghost-light { color: #1d4ed8; background: #eff6ff; }
  .cabinet-desktop.detail-open .cabinet-content { max-width: 840px; }
}

.mobile-section-picker { display: none; }

@media (max-width: 959px) {
  .mobile-section-picker { display: grid; gap: 5px; margin: 2px 0 16px; color: var(--text-muted); font-size: 12px; font-weight: 600; }
  .mobile-section-picker select { width: 100%; min-height: 46px; padding: 0 12px; border: 1px solid var(--border); border-radius: 10px; background: var(--surface); color: var(--text); font: inherit; font-size: 15px; }
}

@media (max-width: 760px) {
  .overview-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .group-settings-nav { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .group-settings-nav button { justify-content: flex-start; padding-inline: 13px; }
  .focus-card { align-items: stretch; flex-direction: column; gap: 12px; }
  .login-card { margin-top: 5vh; padding: 22px; }
  .login-actions { flex-direction: column; }
  .page-intro { display: grid; }
  .page-actions { justify-content: stretch; }
  .page-actions .btn { flex: 1 1 145px; justify-content: center; }
  .vk-step { grid-template-columns: 28px minmax(0, 1fr); padding: 14px; gap: 9px; }
  .vk-link-row { grid-template-columns: 1fr; }
  .vk-link-row .btn { width: 100%; min-height: 44px; justify-content: center; }
  .vk-wizard-footer { align-items: stretch; flex-direction: column; }
  .vk-wizard-footer .btn { width: 100%; justify-content: center; }
}
</style>
