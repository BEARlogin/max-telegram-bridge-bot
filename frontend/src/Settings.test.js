import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./Settings.vue', import.meta.url), 'utf8')

test('renders only community-visible VK conversations and keeps selection errors in step 2', () => {
  const picker = source.indexOf('v-for="chat in visibleVKChats"')
  const inlineError = source.indexOf('v-if="vkLinkError"')
  const nextStep = source.indexOf('<span class="vk-step-number">3</span>')

  assert.ok(picker >= 0, 'filtered VK conversation picker is missing')
  assert.ok(inlineError > picker, 'selection error must follow the related picker')
  assert.ok(inlineError < nextStep, 'link error must stay inside step 2')
  assert.match(source.slice(picker, inlineError + 200), /role="alert"/)
  assert.doesNotMatch(source, /vkChatLink|findVKChatByLink/)
  assert.match(source, /availableAccountID \|\| healthyCommunity\?\.account_id/)
})

test('mini app uses an app-bar drawer and keeps billing off working screens', () => {
  assert.match(source, /class="mini-menu-button"/)
  assert.match(source, /aria-controls="mini-navigation-drawer"/)
  assert.match(source, /id="mini-navigation-drawer"/)
  assert.match(source, /const miniSectionGroups = computed/)
  assert.match(source, /\{ id: 'vk', label: 'VK'/)
  assert.match(source, /\{ id: 'billing', label: 'Тариф и слоты'/)
  assert.match(source, /\{ id: 'import', label: 'Импорт истории'/)
  assert.match(source, /\{ id: 'account', label: 'Профиль и поддержка'/)
  assert.match(source, /!browserMode && tab === 'billing'/)
  assert.match(source, /!browserMode && tab === 'import'/)
  assert.match(source, /!browserMode && tab === 'vk'/)
  assert.match(source, /class="mini-pro-cta"/)
  assert.match(source, /!isPro \|\| subStatus === 'trial'/)
  assert.match(source, /!isPro && !trialUsed/)
  assert.match(source, /7 дней бесплатно/)
  assert.match(source, /selectMiniSection\('billing'\)/)
  assert.match(source, /v-if="me && browserMode"/)
  assert.doesNotMatch(source, /\(!browserMode \|\| currentSection === 'billing'\)/)
  assert.doesNotMatch(source, /class="mini-nav/)
})

test('occupied slots are available only inside billing and collapsed by default', () => {
  assert.match(source, /const slotUsageOpen = ref\(false\)/)
  assert.match(source, /<ol v-if="slotUsageOpen" id="slot-usage-list">/)
  assert.match(source, /v-if="slots && \(\(browserMode && currentSection === 'billing'\) \|\| \(!browserMode && tab === 'billing'\)\)"/)
})

test('payment waits for a direct user click before opening an external browser', () => {
  assert.match(source, /const paymentUrl = ref\(''\)/)
  assert.match(source, /@click="openPreparedPayment"/)
  assert.match(source, /preparePayment\(url, 'Оплата PRO/)
  assert.match(source, /if \(result === 'need_card'\) \{\s*const \{ url \} = await subscribePro\(\)/)
  assert.doesNotMatch(source, /location\.href = url/)
})
