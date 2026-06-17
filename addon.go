package main

import "context"

// Addon — опциональная точка расширения бриджа дополнительными командами.
// В публичной сборке отсутствует (loadAddon возвращает nil); реализация
// подключается отдельной сборкой через build-tag `addon`.
type Addon interface {
	Start(ctx context.Context) error
	HandleDMCommand(ctx context.Context, userID, chatID int64, text string) (handled bool)
	// HandleDMForward вызывается, когда юзер пересылает в личку пост из канала.
	// Возвращает true если сообщение обработано аддоном.
	HandleDMForward(ctx context.Context, userID, dmChatID, sourceChatID int64, sourceTitle string) (handled bool)
}
