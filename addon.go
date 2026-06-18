package main

import "context"

// Addon — опциональная точка расширения бриджа дополнительными командами.
// В публичной сборке отсутствует (loadAddon возвращает nil); реализация
// подключается отдельной сборкой через build-tag `addon`.
type Addon interface {
	Start(ctx context.Context) error
	HandleDMCommand(ctx context.Context, userID, chatID int64, text string) (handled bool)
	// HandleDMForward вызывается, когда юзер пересылает в личку пост из канала.
	// sourceMsgID — msg_id оригинала в канале (0 если неизвестен); используется,
	// чтобы стартовать импорт с конкретного поста. Возвращает true если обработано.
	HandleDMForward(ctx context.Context, userID, dmChatID, sourceChatID int64, sourceTitle string, sourceMsgID int) (handled bool)
	// HandleCallback вызывается на нажатие inline-кнопки. Аддон проверяет, его ли
	// это callback (по своему префиксу), и возвращает true если обработал. msgID —
	// id сообщения с нажатой кнопкой (чтобы аддон мог его удалить).
	HandleCallback(ctx context.Context, userID, chatID int64, callbackID, data string, msgID int) (handled bool)
}
