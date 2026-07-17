package main

import (
	"context"
	"time"

	maxbot "github.com/max-messenger/max-bot-api-client-go"
)

// --- Дуал-бот MAX: основной (новый) + резервный (старый) ---
// Цель: обслуживать чаты, где есть только старый бот (новый туда не добавлен), и не
// дублировать в чатах, где сидят оба. Маршрутизация отправки — по членству бота в чате
// (кэш), дедуп входящих — по mid (оба бота присылают одно и то же сообщение).

// dualEnabled — поднят ли второй (старый) бот.
func (b *Bridge) dualEnabled() bool {
	return b.maxApiOld != nil && b.cfg.MaxTokenOld != ""
}

// maxTokenFor возвращает токен бота, которым нужно слать в этот MAX-чат: по умолчанию
// основной (новый); если нового бота в чате НЕТ, а старый есть — токен старого.
// Результат кэшируется (членство дёргаем один раз на чат).
func (b *Bridge) maxTokenFor(ctx context.Context, chatID int64) string {
	if !b.dualEnabled() {
		return b.cfg.MaxToken
	}
	b.maxBotMu.Lock()
	if t, ok := b.maxBotCache[chatID]; ok {
		b.maxBotMu.Unlock()
		return t
	}
	b.maxBotMu.Unlock()

	// Приоритет СТАРОМУ боту: он сидит почти везде (все каналы + большинство групп),
	// так что где есть оба — работаем старым (по запросу). Новый — только там, где
	// старого нет (чаты, откуда старого удалили) + горячий резерв.
	tok := b.cfg.MaxToken
	// На старого — только если он АДМИН (член-без-прав слать/модерировать не может).
	if m, err := b.maxApiOld.Chats.GetChatMembership(ctx, chatID); err == nil && m.IsAdmin {
		tok = b.cfg.MaxTokenOld // старый админ в чате — шлём старым
	}
	// Иначе остаётся новый (дефолт): чаты new-only (старого снесли) + где старый не админ.
	b.maxBotMu.Lock()
	b.maxBotCache[chatID] = tok
	b.maxBotMu.Unlock()
	return tok
}

// maxClientFor — SDK-клиент бота, которым нужно слать в этот чат (для команд/ответов/
// карточек). Та же маршрутизация, что и maxTokenFor (приоритет старому где он есть).
func (b *Bridge) maxClientFor(ctx context.Context, chatID int64) *maxbot.Api {
	if !b.dualEnabled() {
		return b.maxApi
	}
	// Токен из ctx (бот-источник апдейта в MAX-цикле / выбранный бот релея) приоритетнее —
	// гарантирует ответ ТЕМ ЖЕ ботом, что получил сообщение (критично для ЛС, где членство
	// не определить). Если в ctx нет — по членству.
	tok := b.maxTokenCtx(ctx)
	if tok == "" {
		tok = b.maxTokenFor(ctx, chatID)
	}
	if tok == b.cfg.MaxTokenOld {
		return b.maxApiOld
	}
	return b.maxApi
}

// altMaxToken — «другой» бот относительно данного токена (для fallback при chat.not.found).
// "" — если дуал выключен или токен неизвестен.
func (b *Bridge) altMaxToken(tok string) string {
	if !b.dualEnabled() {
		return ""
	}
	if tok == b.cfg.MaxTokenOld {
		return b.cfg.MaxToken
	}
	return b.cfg.MaxTokenOld
}

// markChatBot прайм кэша: запомнить, что в чате есть бот с данным токеном (например,
// когда апдейт пришёл через его вебхук, или отправка прошла успешно).
func (b *Bridge) markChatBot(chatID int64, token string) {
	if !b.dualEnabled() || token == "" {
		return
	}
	b.maxBotMu.Lock()
	b.maxBotCache[chatID] = token
	b.maxBotMu.Unlock()
}

// invalidateChatBot сбрасывает кэш бота для чата (например, send упал — пересчитаем).
func (b *Bridge) invalidateChatBot(chatID int64) {
	b.maxBotMu.Lock()
	delete(b.maxBotCache, chatID)
	b.maxBotMu.Unlock()
}

// maxDupMid — дедуп входящих MAX-сообщений по mid: при дуал-режиме один и тот же mid
// приходит от ОБОИХ ботов (если оба в чате). true ⇒ уже видели, пропускаем.
func (b *Bridge) maxDupMid(mid string) bool {
	if mid == "" || !b.dualEnabled() {
		return false
	}
	now := time.Now().Unix()
	b.maxSeenMu.Lock()
	defer b.maxSeenMu.Unlock()
	if _, ok := b.maxSeenMid[mid]; ok {
		return true
	}
	b.maxSeenMid[mid] = now
	if len(b.maxSeenMid) > 5000 { // редкая TTL-очистка (старше 10 мин)
		for k, t := range b.maxSeenMid {
			if now-t > 600 {
				delete(b.maxSeenMid, k)
			}
		}
	}
	return false
}

// maxTokenCtxKey — ключ контекста для проброса выбранного MAX-токена в аплоады без
// изменения десятков сигнатур. forwardTgToMax кладёт сюда токен бота, который в чате;
// customUpload*/sendMaxChunk его читают, чтобы заливать/слать тем же ботом.
type maxTokenCtxKeyT struct{}

func (b *Bridge) withMaxToken(ctx context.Context, token string) context.Context {
	if token == "" {
		return ctx
	}
	return context.WithValue(ctx, maxTokenCtxKeyT{}, token)
}

// maxTokenCtx достаёт MAX-токен из контекста ("" если не задан).
func (b *Bridge) maxTokenCtx(ctx context.Context) string {
	if v, ok := ctx.Value(maxTokenCtxKeyT{}).(string); ok {
		return v
	}
	return ""
}

// maxTokenForSend — токен для отправки/аплоада: из контекста (если релей его задал),
// иначе по членству (maxTokenFor). Обеспечивает один бот на весь релей.
func (b *Bridge) maxTokenForSend(ctx context.Context, chatID int64) string {
	if t := b.maxTokenCtx(ctx); t != "" {
		return t
	}
	return b.maxTokenFor(ctx, chatID)
}

// maxTokenCtxOr — токен из контекста (если релей задал), иначе основной. Для аплоадов
// (у них нет chatID — берут токен релея из ctx, чтобы заливать тем же ботом, что и шлёт).
func (b *Bridge) maxTokenCtxOr(ctx context.Context) string {
	if t := b.maxTokenCtx(ctx); t != "" {
		return t
	}
	return b.cfg.MaxToken
}

// isSelfMaxBot — сообщение от любого из НАШИХ MAX-ботов (основного или старого).
// Нужен дедуп/фильтр: старый бот видит сообщения нового (и наоборот) в общих чатах.
func (b *Bridge) isSelfMaxBot(senderUID int64) bool {
	return senderUID != 0 && (senderUID == b.maxBotUID || (b.maxOldUID != 0 && senderUID == b.maxOldUID))
}
