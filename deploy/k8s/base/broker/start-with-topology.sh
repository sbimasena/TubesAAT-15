#!/bin/sh
set -eu

# Clear an old root-owned readiness marker before dropping privileges.
rm -f /tmp/topology-ready

# Keep server and CLI cookie creation under the same user on a fresh volume.
if [ "$(id -u)" = '0' ]; then
  # Cluster members must share one Erlang cookie; derive it from the broker Secret.
  # shortcut: the cookie follows RABBITMQ_DEFAULT_PASS, so a password change needs all broker
  # pods restarted at once (kubectl delete pod -l app=rabbitmq), not a rolling restart.
  printf 'bnpb-erlang-cookie:%s' "$RABBITMQ_DEFAULT_PASS" | sha256sum | cut -c1-48 >/var/lib/rabbitmq/.erlang.cookie
  chmod 400 /var/lib/rabbitmq/.erlang.cookie
  find /var/lib/rabbitmq \! -user rabbitmq -exec chown rabbitmq '{}' +
  exec su-exec rabbitmq /bin/sh "$0" "$@"
fi

# Import after normal boot so RabbitMQ creates the environment-configured user.
# Readiness is withheld until all durable queues and bindings have been imported.
/usr/local/bin/docker-entrypoint.sh rabbitmq-server &
broker_pid=$!
trap 'kill -TERM "$broker_pid" 2>/dev/null || true' TERM INT EXIT
attempt=0
until rabbitmqctl await_startup --timeout 5 >/dev/null 2>&1; do
  kill -0 "$broker_pid"
  attempt=$((attempt + 1))
  # A restarting member waits up to 5 min for its peers before it finishes booting.
  [ "$attempt" -lt 60 ] || exit 1
  sleep 2
done
rabbitmqctl import_definitions /etc/rabbitmq/definitions.json
touch /tmp/topology-ready
wait "$broker_pid"
