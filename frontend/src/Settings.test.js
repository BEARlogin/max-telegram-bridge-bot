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
})
