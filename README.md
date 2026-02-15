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
- Кросспостинг каналов с выбором направления (`tg>max`, `max>tg`, `both`)
- Сохранение форматирования при кросспостинге (жирный, курсив, код, ссылки, зачёркнутый, подчёркнутый)
- Управление кросспостингом через inline-кнопки
- SQLite или PostgreSQL для хранения связок и маппинга сообщений

### Форматирование при кросспостинге

| Формат | TG → MAX | MAX → TG |
|--------|:--------:|:--------:|
| **Жирный** | ✅ | ✅ |
| *Курсив* | ✅ | ✅ |
| `Код` | ✅ | ✅ |
| Блок кода | ⚠️ моноширинный | ⚠️ моноширинный |
| ~~Зачёркнутый~~ | ✅ | ✅ |
| Подчёркнутый | ✅ | ✅ |
| [Ссылки](url) | ✅ | ✅ |
| Цитата | ❌ | ❌ |
| Спойлер | ❌ | — |

Цитаты и спойлеры не поддерживаются MAX Bot API.

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

### Docker Compose (с PostgreSQL)

```bash
cp .env.example .env
# Заполните TG_TOKEN и MAX_TOKEN в .env
docker compose up -d
```

PostgreSQL настраивается через `.env`:

```env
POSTGRES_USER=bridge
POSTGRES_PASSWORD=bridge
POSTGRES_DB=bridge
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
- **MAX**: через [business.max.ru](https://dev.max.ru/docs/chatbots/bots-create)

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

### 4. Кросспостинг каналов

Настройка через личные сообщения с ботами (ничего не публикуется в каналах):

1. Добавьте бота как админа в TG-канал и MAX-канал
2. Перешлите любой пост из TG-канала в **личку TG-бота** → бот покажет ID канала
3. В **личке MAX-бота** напишите `/crosspost <TG_ID>`
4. Перешлите любой пост из MAX-канала в **личку MAX-бота** → кросспостинг настроен!

По умолчанию посты идут в обе стороны. Управление:

- `/crosspost` (в личке любого бота) — список всех связок с кнопками
- Перешлите пост из связанного канала в личку бота → появятся кнопки управления (направление, удаление)

## Команды

### Группы (bridge)

| Команда | Описание |
|---------|----------|
| `/start`, `/help` | Инструкция |
| `/bridge` | Создать ключ для связки |
| `/bridge <ключ>` | Связать чат по ключу |
| `/bridge prefix on/off` | Включить/выключить префикс `[TG]`/`[MAX]` |
| `/unbridge` | Удалить связку |

### Каналы (crosspost) — через личку бота

| Команда | Где | Описание |
|---------|-----|----------|
| `/crosspost` | TG или MAX личка | Список всех связок с кнопками управления |
| `/crosspost <TG_ID>` | MAX личка | Начать настройку (затем переслать пост из MAX-канала) |
| Переслать пост из канала | TG или MAX личка | Показать ID (если не связан) или кнопки управления |

Кнопки управления позволяют менять направление (TG→MAX, MAX→TG, оба) и удалять связку.

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
