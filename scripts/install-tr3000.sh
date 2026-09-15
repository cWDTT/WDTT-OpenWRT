#!/bin/sh
# Установка WDTT — приватный репозиторий
# Запускайте с компьютера, передавая токен на роутер по SSH

ROUTER="${1:-root@192.168.1.1}"

if [ -z "$GITHUB_TOKEN" ]; then
	echo "Укажите GITHUB_TOKEN:"
	echo "  export GITHUB_TOKEN='github_pat_...'"
	echo "  sh scripts/install-tr3000.sh root@192.168.1.1"
	exit 1
fi

DIR="$(cd "$(dirname "$0")/.." && pwd)"

echo "Copying install.sh to $ROUTER ..."
scp "$DIR/install.sh" "$ROUTER:/tmp/wdtt-install.sh"

echo "Running installer on router ..."
ssh "$ROUTER" "chmod +x /tmp/wdtt-install.sh && GITHUB_TOKEN='$GITHUB_TOKEN' sh /tmp/wdtt-install.sh"
