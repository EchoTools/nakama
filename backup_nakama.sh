#!/bin/bash
set -euo pipefail

# Source environment variables
if [ -f .env ]; then
    set -a
    source .env
    set +a
else
    echo "Error: .env file not found"
    exit 1
fi

# Set up variables
TIMESTAMP="$(date '+%Y-%m-%dT%H-%M-%S')"
BACKUP_DIR="backups"

mkdir -p "$BACKUP_DIR"

echo "Creating backups with timestamp: ${TIMESTAMP}"

# Postgres backup - stream directly to zstd
echo "Backing up PostgreSQL..."
docker-compose exec -T postgres pg_dump -U "${POSTGRES_USER}" -d "${POSTGRES_DB}" | \
    zstd -T0 -o "${BACKUP_DIR}/nakama-pg-${TIMESTAMP}.sql.zst"

# Redis backup for db 0
echo "Backing up Redis DB 0..."
docker-compose exec -T redis redis-cli --rdb /tmp/dump-db0.rdb > /dev/null
docker-compose exec -T redis cat /tmp/dump-db0.rdb | \
    zstd -T0 -o "${BACKUP_DIR}/nakama-redis-0-${TIMESTAMP}.rdb.zst"
docker-compose exec -T redis rm /tmp/dump-db0.rdb

# Redis backup for db 1
echo "Backing up Redis DB 1..."
docker-compose exec -T redis redis-cli SELECT 1 > /dev/null
docker-compose exec -T redis redis-cli --rdb /tmp/dump-db1.rdb > /dev/null
docker-compose exec -T redis cat /tmp/dump-db1.rdb | \
    zstd -T0 -o "${BACKUP_DIR}/nakama-redis-1-${TIMESTAMP}.rdb.zst"
docker-compose exec -T redis rm /tmp/dump-db1.rdb

echo "Backup complete!"
echo "  - ${BACKUP_DIR}/nakama-pg-${TIMESTAMP}.sql.zst"
echo "  - ${BACKUP_DIR}/nakama-redis-0-${TIMESTAMP}.rdb.zst"
echo "  - ${BACKUP_DIR}/nakama-redis-1-${TIMESTAMP}.rdb.zst"
