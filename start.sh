./nakama migrate up --database.address root@127.0.0.1:26257 && \
  exec ./nakama --name nakama1 --database.address root@127.0.0.1:26257 \
  --session.token_expiry_sec 7200 --metrics.prometheus_port 9100 \
  --logger.level DEBUG --logger.file ./data/nakama.log \
  --config ./local.yml
