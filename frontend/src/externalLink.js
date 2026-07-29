// Платёж открываем только из отдельного пользовательского клика.
// MAX и Telegram блокируют вызов внешнего браузера, если openLink случился
// после await/fetch и уже не считается прямой реакцией на нажатие.
export function openExternalLink(url, platform, host = window) {
  const parsed = new URL(url, host.location?.href || 'https://maxtelegrambridge.ru/')
  if (parsed.protocol !== 'https:') throw new Error('Разрешены только HTTPS-ссылки')

  if (platform === 'MAX') {
    const maxWebApp = host.WebApp
    if (maxWebApp && typeof maxWebApp.openLink === 'function') {
      maxWebApp.openLink(parsed.href)
      return 'max'
    }
  }

  if (platform === 'Telegram') {
    const telegramWebApp = host.Telegram?.WebApp
    if (telegramWebApp && typeof telegramWebApp.openLink === 'function') {
      telegramWebApp.openLink(parsed.href)
      return 'telegram'
    }
  }

  const popup = typeof host.open === 'function'
    ? host.open(parsed.href, '_blank', 'noopener,noreferrer')
    : null
  if (popup) return 'window'

  host.location?.assign?.(parsed.href)
  return 'location'
}
