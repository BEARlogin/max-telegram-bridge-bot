#!/usr/bin/env bash
set -euo pipefail

# ========================================
# Деплой Bridge (TG <-> MAX)
# Usage: ./deploy.sh [--setup]
# ========================================

HOST="85.239.52.62"
USER="root"
REMOTE_DIR="/opt/bearlogin-bridge"
SERVICE="bearlogin-bridge"
SSH_OPTS="-o StrictHostKeyChecking=accept-new -o ConnectTimeout=10"

G='\033[0;32m' R='\033[0;31m' Y='\033[1;33m' N='\033[0m'
info() { echo -e "${G}[✓]${N} $1"; }
err()  { echo -e "${R}[✗]${N} $1"; exit 1; }

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

ssh $SSH_OPTS "$USER@$HOST" 'true' 2>/dev/null || err "Не могу подключиться к $HOST"

# Сборка
info "Сборка (linux/amd64)..."
cd "$SCRIPT_DIR"
GOOS=linux GOARCH=amd64 go build -o bearlogin-bridge . || err "Ошибка сборки"

# --setup
if [[ "${1:-}" == "--setup" ]]; then
    info "Настройка systemd..."
    ssh $SSH_OPTS "$USER@$HOST" bash -s << 'SETUP'
        mkdir -p /opt/bearlogin-bridge

        cat > /etc/systemd/system/bearlogin-bridge.service << 'SVC'
[Unit]
Description=Bearlogin Bridge (TG <-> MAX)
After=network.target

[Service]
Type=simple
WorkingDirectory=/opt/bearlogin-bridge
ExecStart=/opt/bearlogin-bridge/bearlogin-bridge
EnvironmentFile=/opt/bearlogin-bridge/.env
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
SVC

        if [ ! -f /opt/bearlogin-bridge/.env ]; then
            cat > /opt/bearlogin-bridge/.env << 'ENV'
TG_TOKEN=your_telegram_bot_token
MAX_TOKEN=your_max_bot_token
ENV
            chmod 600 /opt/bearlogin-bridge/.env
            echo "⚠️  Заполните /opt/bearlogin-bridge/.env токенами!"
        fi

        systemctl daemon-reload
        systemctl enable bearlogin-bridge
SETUP
    info "Systemd настроен"
fi

# Деплой
info "Загружаю на сервер..."
ssh $SSH_OPTS "$USER@$HOST" "systemctl stop $SERVICE 2>/dev/null || true"
scp $SSH_OPTS "$SCRIPT_DIR/bearlogin-bridge" "$USER@$HOST:$REMOTE_DIR/bearlogin-bridge"
ssh $SSH_OPTS "$USER@$HOST" "chmod +x $REMOTE_DIR/bearlogin-bridge && systemctl start $SERVICE"
info "Bridge перезапущен"

ssh $SSH_OPTS "$USER@$HOST" "systemctl status $SERVICE --no-pager -l" 2>&1 | tail -5

info "Деплой завершён!"
