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
	// HandleCallback вызывается на нажатие inline-кнопки. Аддон проверяет, его ли
	// это callback (по своему префиксу), и возвращает true если обработал.
	HandleCallback(ctx context.Context, userID, chatID int64, callbackID, data string) (handled bool)
}
