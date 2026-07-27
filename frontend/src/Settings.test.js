import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./Settings.vue', import.meta.url), 'utf8')

test('renders VK conversation-link errors next to the link field', () => {
  const field = source.indexOf('placeholder="https://vk.ru/im/convo/2000000160"')
  const inlineError = source.indexOf('id="vk-link-error"')
  const nextStep = source.indexOf('<span class="vk-step-number">3</span>')

  assert.ok(field >= 0, 'VK link field is missing')
  assert.ok(inlineError > field, 'link error must follow the related field')
  assert.ok(inlineError < nextStep, 'link error must stay inside step 2')
  assert.match(source.slice(field, inlineError), /aria-invalid/)
  assert.match(source.slice(field, inlineError + 200), /role="alert"/)
})
