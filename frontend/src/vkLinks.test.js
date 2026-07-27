import test from 'node:test'
import assert from 'node:assert/strict'

import { peerFromVKLink } from './vkLinks.js'

test('parses current VK conversation URL', () => {
  assert.equal(
    peerFromVKLink('https://vk.ru/im/convo/2000000160?entrypoint=convo_invite'),
    2000000160,
  )
})

test('parses legacy VK conversation URL', () => {
  assert.equal(peerFromVKLink('https://vk.ru/im?sel=c160'), 2000000160)
})

test('accepts VK hosts only and validates chat peer range', () => {
  assert.equal(peerFromVKLink('https://example.com/im/convo/2000000160'), 0)
  assert.equal(peerFromVKLink('https://vk.ru/im/convo/160'), 0)
  assert.equal(peerFromVKLink('https://vk.ru/im/convo/3000000000'), 0)
})
