# MaxTelegramBridgeBot

Мост между Telegram и [MAX](https://max.ru) мессенджером. Пересылает сообщения, медиа, файлы и редактирования между связанными чатами.

**Боты:** [Telegram](https://t.me/MaxTelegramBridgeBot) | [MAX](https://max.ru/id710708943262_bot)

## Возможности

- Пересылка текстовых сообщений в обе стороны
- Пересылка медиа: фото, видео, документы, голосовые, аудио
- Поддержка ответов (reply) — сохраняется контекст
- Отслеживание редактирования сообщений
- Удаление сообщений (MAX→TG). TG→MAX удаление невозможно — [Telegram Bot API не отправляет событие удаления](https://github.com/tdlib/telegram-bot-api/issues/286)
- Настраиваемый префикс `[TG]` / `[MAX]`
- SQLite или PostgreSQL для хранения связок и маппинга сообщений

## Установка

### Из бинаря

Скачайте бинарь со [страницы релизов](https://github.com/BEARlogin/max-telegram-bridge-bot/releases) и запустите:

```bash
chmod +x max-telegram-bridge-bot
./max-telegram-bridge-bot
```

### Docker

```bash
docker run -e TG_TOKEN=your_token -e MAX_TOKEN=your_token ghcr.io/bearlogin/max-telegram-bridge-bot:latest
```

Для сохранения БД между перезапусками:

```bash
docker run -e TG_TOKEN=your_token -e MAX_TOKEN=your_token \
  -v ./data:/data -e DB_PATH=/data/bridge.db \
  ghcr.io/bearlogin/max-telegram-bridge-bot:latest
```

### Из исходников

```bash
git clone https://github.com/BEARlogin/max-telegram-bridge-bot.git
cd max-telegram-bridge-bot
go build -o max-telegram-bridge-bot .
./max-telegram-bridge-bot
```

## Быстрый старт

### 1. Создайте ботов

- **Telegram**: через [@BotFather](https://t.me/BotFather), отключите Privacy Mode (Bot Settings → Group Privacy → Turn off)
- **MAX**: через настройки платформы

### 2. Настройте и запустите

Передайте токены через переменные окружения:

```bash
TG_TOKEN=your_token MAX_TOKEN=your_token ./max-telegram-bridge-bot
```

Или через `export`:

```bash
export TG_TOKEN=your_token
export MAX_TOKEN=your_token
./max-telegram-bridge-bot
```

### 3. Свяжите чаты

1. Добавьте бота в Telegram-группу и MAX-группу
2. В MAX сделайте бота **админом** группы
3. В одном из чатов отправьте `/bridge`
4. Бот выдаст ключ — отправьте `/bridge <ключ>` в другом чате

## Команды

| Команда | Описание |
|---------|----------|
| `/start`, `/help` | Инструкция |
| `/bridge` | Создать ключ для связки |
| `/bridge <ключ>` | Связать чат по ключу |
| `/bridge prefix on/off` | Включить/выключить префикс `[TG]`/`[MAX]` |
| `/unbridge` | Удалить связку |

## Переменные окружения

| Переменная | Описание | По умолчанию |
|------------|----------|--------------|
| `TG_TOKEN` | Токен Telegram бота | — (обязательно) |
| `MAX_TOKEN` | Токен MAX бота | — (обязательно) |
| `DB_PATH` | Путь к SQLite базе | `bridge.db` |
| `DATABASE_URL` | DSN для PostgreSQL (если задана — SQLite игнорируется) | — |
| `TG_BOT_URL` | Ссылка на TG-бота (показывается в `/help`) | `https://t.me/MaxTelegramBridgeBot` |
| `MAX_BOT_URL` | Ссылка на MAX-бота (показывается в `/help`) | `https://max.ru/id710708943262_bot` |
| `WEBHOOK_URL` | Базовый URL для webhook, например `https://bridge.example.com` (если не задан — long polling). Эндпоинты: `/tg-webhook`, `/max-webhook` | — |
| `WEBHOOK_PORT` | Порт для webhook сервера | `8443` |
