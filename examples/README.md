# Пример расширения (аддона)

Минимальный аддон, отвечающий на команду `/echo` в личке боту. Показывает, как
устроено расширение бриджа (см. раздел «Расширения (аддоны)» в корневом README).

## Файлы

- [`echoaddon/echoaddon.go`](echoaddon/echoaddon.go) — сам аддон. Standalone-пакет:
  реализует методы интерфейса `Addon`, в TG/MAX напрямую не лезет — все операции
  бриджа приходят через колбэки в `Deps`.
- [`addon_local.go.example`](addon_local.go.example) — образец склейки с бриджом
  (функция `loadAddon`). Собирается только с `-tags addon`.

## Запуск

```bash
# Из корня проекта
cp examples/addon_local.go.example addon_local.go   # addon_local.go в .gitignore
go build -tags addon -o bridge .
./bridge
```

После запуска напишите боту в личку `/echo привет` — он ответит `привет`.

## Что дальше

Замените `echoaddon` на свой пакет, реализующий интерфейс `Addon`
(`Start`, `HandleDMCommand`, `HandleCallback`, `HandleDMForward`), и пробросьте в
него нужные операции бриджа через свой `Deps`. Команды меню добавляются через
`b.extraCommands` — ядро покажет их, не зная семантики.
