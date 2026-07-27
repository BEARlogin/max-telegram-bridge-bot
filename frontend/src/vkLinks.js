const vkHosts = new Set(['vk.com', 'vk.ru', 'www.vk.com', 'www.vk.ru'])
const minChatPeerID = 2000000000
const maxChatPeerID = 2999999999

function validChatPeerID(value) {
  const peerID = Number(value)
  return Number.isSafeInteger(peerID) && peerID >= minChatPeerID && peerID <= maxChatPeerID
    ? peerID
    : 0
}

export function peerFromVKLink(raw) {
  const value = String(raw || '').trim()
  if (!value) return 0

  try {
    const url = new URL(value.includes('://') ? value : `https://${value}`)
    if (!vkHosts.has(url.hostname.toLowerCase())) return 0

    // Новый формат, который VK отдаёт из адресной строки беседы:
    // https://vk.ru/im/convo/2000000160?entrypoint=convo_invite
    const convoMatch = url.pathname.match(/^\/im\/convo\/(\d+)\/?$/i)
    if (convoMatch) return validChatPeerID(convoMatch[1])

    // Старый формат: https://vk.ru/im?sel=c160.
    const sel = url.searchParams.get('sel') || ''
    const legacyMatch = sel.match(/^c(\d+)$/i)
    if (legacyMatch) return validChatPeerID(minChatPeerID + Number(legacyMatch[1]))

    // Встречается и прямой peer_id в параметре sel.
    return validChatPeerID(sel)
  } catch {
    const legacyMatch = value.match(/^c(\d+)$/i)
    if (legacyMatch) return validChatPeerID(minChatPeerID + Number(legacyMatch[1]))
    return validChatPeerID(value)
  }
}
