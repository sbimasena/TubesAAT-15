#!/bin/sh
set -eu

# Import after normal boot so RabbitMQ creates the environment-configured user.
# Readiness is withheld until all durable queues and bindings have been imported.
rm -f /tmp/topology-ready
/usr/local/bin/docker-entrypoint.sh rabbitmq-server &
broker_pid=$!
trap 'kill -TERM "$broker_pid" 2>/dev/null || true' TERM INT EXIT
attempt=0
until rabbitmqctl await_startup --timeout 5 >/dev/null 2>&1; do
  kill -0 "$broker_pid"
  attempt=$((attempt + 1))
  [ "$attempt" -lt 30 ] || exit 1
  sleep 2
done
rabbitmqctl import_definitions /etc/rabbitmq/definitions.json
touch /tmp/topology-ready
wait "$broker_pid"
