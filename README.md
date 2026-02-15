# MaxTelegramBridgeBot

Мост между Telegram и [MAX](https://max.ru) мессенджером. Пересылает сообщения, медиа, файлы и редактирования между связанными чатами.

## Возможности

- Пересылка текстовых сообщений в обе стороны
- Пересылка медиа: фото, видео, документы, голосовые, аудио
- Поддержка ответов (reply) — сохраняется контекст
- Отслеживание редактирования сообщений
- Настраиваемый префикс `[TG]` / `[MAX]`
- SQLite или PostgreSQL для хранения связок и маппинга сообщений

## Установка

### Из бинаря

Скачайте бинарь со [страницы релизов](https://github.com/BEARlogin/max-telegram-bridge-bot/releases) и запустите:

```bash
chmod +x max-telegram-bridge-bot
./max-telegram-bridge-bot
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

## Деплой

```bash
# Первый раз — настройка systemd
./deploy.sh --setup

# Обновление
./deploy.sh
```

## Переменные окружения

| Переменная | Описание | По умолчанию |
|------------|----------|--------------|
| `TG_TOKEN` | Токен Telegram бота | — (обязательно) |
| `MAX_TOKEN` | Токен MAX бота | — (обязательно) |
| `DB_PATH` | Путь к SQLite базе | `bridge.db` |
| `DATABASE_URL` | DSN для PostgreSQL (если задана — SQLite игнорируется) | — |
| `TG_BOT_URL` | Ссылка на TG-бота (показывается в `/help`) | `https://t.me/MaxTelegramBridgeBot` |
| `MAX_BOT_URL` | Ссылка на MAX-бота (показывается в `/help`) | `https://max.ru/id710708943262_bot` |
