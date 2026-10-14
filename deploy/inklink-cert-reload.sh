#!/bin/sh
set -eu
docker compose -f /opt/switchnet/docker-compose.yml exec -T nginx nginx -s reload
