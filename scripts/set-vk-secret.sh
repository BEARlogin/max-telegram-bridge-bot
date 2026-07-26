#!/bin/sh
set -eu

APP_DIR=${APP_DIR:-/opt/bearlogin-bridge}
ENV_FILE=$APP_DIR/.env
SERVICE_NAME=${SERVICE_NAME:-bearlogin-bridge}

if [ "$(id -u)" -ne 0 ]; then
  echo "Запустите скрипт от root: sudo $0" >&2
  exit 1
fi

if [ ! -f "$ENV_FILE" ]; then
  echo "Не найден $ENV_FILE" >&2
  exit 1
fi

if [ ! -t 0 ]; then
  echo "Секрет нужно ввести интерактивно с терминала." >&2
  exit 1
fi

printf 'Введите новый VK_CLIENT_SECRET (ввод скрыт): '
old_stty=$(stty -g)
trap 'stty "$old_stty" 2>/dev/null || true' EXIT HUP INT TERM
stty -echo
IFS= read -r vk_client_secret
stty "$old_stty"
trap - EXIT HUP INT TERM
printf '\n'

if [ -z "$vk_client_secret" ]; then
  echo "Пустой секрет не сохранён." >&2
  exit 1
fi

case "$vk_client_secret" in
  *'
'*)
    echo "Секрет не должен содержать перевод строки." >&2
    exit 1
    ;;
esac

stamp=$(TZ=Europe/Moscow date +%Y%m%d-%H%M%S)
backup_file=$ENV_FILE.bak-vk-secret-$stamp
tmp_file=$(mktemp "$APP_DIR/.env.vk-secret.XXXXXX")

cleanup() {
  rm -f "$tmp_file"
}
trap cleanup EXIT HUP INT TERM

cp -a "$ENV_FILE" "$backup_file"
awk -F= '$1 != "VK_CLIENT_SECRET" { print }' "$ENV_FILE" > "$tmp_file"
printf 'VK_CLIENT_SECRET=%s\n' "$vk_client_secret" >> "$tmp_file"
unset vk_client_secret

chmod --reference="$ENV_FILE" "$tmp_file"
chown --reference="$ENV_FILE" "$tmp_file"
mv "$tmp_file" "$ENV_FILE"
trap - EXIT HUP INT TERM

if ! systemctl restart "$SERVICE_NAME"; then
  echo "Сервис не запустился. Восстанавливаю предыдущий .env." >&2
  cp -a "$backup_file" "$ENV_FILE"
  systemctl restart "$SERVICE_NAME"
  exit 1
fi

if [ "$(systemctl is-active "$SERVICE_NAME")" != "active" ]; then
  echo "Сервис после перезапуска не активен. Восстанавливаю предыдущий .env." >&2
  cp -a "$backup_file" "$ENV_FILE"
  systemctl restart "$SERVICE_NAME"
  exit 1
fi

echo "VK_CLIENT_SECRET установлен, $SERVICE_NAME активен."
echo "Резервная копия: $backup_file"
TZ=Europe/Moscow date '+Готово: %d.%m.%Y %H:%M:%S МСК'
