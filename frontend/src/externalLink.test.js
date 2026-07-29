import test from 'node:test'
import assert from 'node:assert/strict'
import { openExternalLink } from './externalLink.js'

test('opens payment through MAX bridge when available', () => {
  let opened = ''
  const host = {
    location: { href: 'https://maxtelegrambridge.ru/commenter/' },
    WebApp: { openLink: url => { opened = url } },
  }
  assert.equal(openExternalLink('https://pay.tbank.ru/test', 'MAX', host), 'max')
  assert.equal(opened, 'https://pay.tbank.ru/test')
})

test('opens payment through Telegram bridge when available', () => {
  let opened = ''
  const host = {
    location: { href: 'https://maxtelegrambridge.ru/commenter/' },
    Telegram: { WebApp: { openLink: url => { opened = url } } },
  }
  assert.equal(openExternalLink('https://pay.tbank.ru/test', 'Telegram', host), 'telegram')
  assert.equal(opened, 'https://pay.tbank.ru/test')
})

test('falls back to a new browser tab and rejects non-HTTPS links', () => {
  let opened = ''
  const host = {
    location: { href: 'https://maxtelegrambridge.ru/commenter/' },
    open: url => { opened = url; return {} },
  }
  assert.equal(openExternalLink('https://pay.tbank.ru/test', '—', host), 'window')
  assert.equal(opened, 'https://pay.tbank.ru/test')
  assert.throws(() => openExternalLink('javascript:alert(1)', '—', host), /HTTPS/)
})
