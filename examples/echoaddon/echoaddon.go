// Package echoaddon — минимальный пример расширения бриджа (см. README, раздел
// «Расширения (аддоны)»). Отвечает на команду /echo в личке боту.
//
// Это standalone-пакет: он НЕ зависит от ядра бриджа и не лезет в TG/MAX API
// напрямую — все операции бриджа приходят через колбэки в Deps. Подключение к
// бриджу делается в addon_local.go (build-тег addon); образец такой склейки —
// examples/addon_local.go.example.
package echoaddon

import (
	"context"
	"strings"
)

// Deps — операции бриджа, нужные расширению. Их прокидывает addon_local.go.
// Так расширение остаётся изолированным и легко тестируется.
type Deps struct {
	// Reply отправляет текст в личку пользователю (chatID — id чата в Telegram).
	Reply func(ctx context.Context, chatID int64, text string) error
}

// Echo — пример расширения. Набор методов совпадает с интерфейсом main.Addon
// бриджа, поэтому *Echo можно вернуть из loadAddon как Addon.
type Echo struct {
	deps Deps
}

// New создаёт расширение с переданными зависимостями.
func New(deps Deps) *Echo { return &Echo{deps: deps} }

// Start — место для фоновой работы (воркеры, вебхуки). Примеру она не нужна,
// поэтому просто ждём отмены контекста.
func (e *Echo) Start(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

// HandleDMCommand вызывается на личное сообщение боту. Возвращаем true, только
// если сообщение наше (тогда ядро его дальше не обрабатывает).
func (e *Echo) HandleDMCommand(ctx context.Context, userID, chatID int64, text string) bool {
	text = strings.TrimSpace(text)
	if text != "/echo" && !strings.HasPrefix(text, "/echo ") {
		return false // не наша команда — пусть разбирается ядро
	}
	arg := strings.TrimSpace(strings.TrimPrefix(text, "/echo"))
	if arg == "" {
		arg = "Пришлите текст после команды, например: /echo привет"
	}
	if e.deps.Reply != nil {
		_ = e.deps.Reply(ctx, chatID, arg)
	}
	return true
}

// HandleCallback — нажатие inline-кнопки. msgID — id сообщения с кнопкой (можно
// удалить). Пример кнопок не использует.
func (e *Echo) HandleCallback(ctx context.Context, userID, chatID int64, callbackID, data string, msgID int) bool {
	return false
}

// HandleDMForward — пересланный в личку пост из канала. Пример его не обрабатывает.
func (e *Echo) HandleDMForward(ctx context.Context, userID, dmChatID, sourceChatID int64, sourceTitle string, sourceMsgID int) bool {
	return false
}
