#!/usr/bin/env bash
# Runs the pgblackhole tests (pg_blackhole_linux_test.go) in Docker:
# Postgres with TLS on two networks (two addresses, as before and after a
# failover) and a test container with NET_ADMIN on both, so it can drop
# the traffic to one address with iptables.
#
#   testdata/pgblackhole/run.sh [go test -run regex]
#
# SRC      module directory to test (default: this repository)
# GO_TAG   golang image tag (default: latest)
# PG_ENV   extra env for the test process, e.g. "PG_KEEPALIVE=30s PG_TCP_USER_TIMEOUT=0"
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
SRC=${SRC:-$(cd "$here/../.." && pwd)}
GO_TAG=${GO_TAG:-latest}
RUN=${1:-'PGBlackhole|PGLongQuery'}
id=pgbh-$$
tmp=$(mktemp -d)

cleanup() {
	docker rm -f "$id-db" "$id-test" >/dev/null 2>&1 || true
	docker network rm "$id-a" "$id-b" >/dev/null 2>&1 || true
	rm -rf "$tmp"
}
trap cleanup EXIT

openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=pgbh \
	-keyout "$tmp/server.key" -out "$tmp/server.crt" >/dev/null 2>&1

docker network create "$id-a" >/dev/null
docker network create "$id-b" >/dev/null
docker run -d --name "$id-db" --network "$id-a" -e POSTGRES_PASSWORD=pgbh \
	-v "$tmp:/certs:ro" --entrypoint sh postgres:17-alpine -c '
		install -o postgres -m 600 /certs/server.key /certs/server.crt /var/lib/postgresql/ &&
		exec docker-entrypoint.sh postgres -c ssl=on \
			-c ssl_cert_file=/var/lib/postgresql/server.crt \
			-c ssl_key_file=/var/lib/postgresql/server.key' >/dev/null
docker network connect "$id-b" "$id-db"
ip_a=$(docker inspect -f "{{(index .NetworkSettings.Networks \"$id-a\").IPAddress}}" "$id-db")
ip_b=$(docker inspect -f "{{(index .NetworkSettings.Networks \"$id-b\").IPAddress}}" "$id-db")
for _ in $(seq 60); do
	docker exec "$id-db" pg_isready -q -U postgres && break
	sleep 1
done

docker build -q -t "pgbh-test:$GO_TAG" - >/dev/null <<EOF
FROM golang:$GO_TAG
RUN apt-get update -qq && apt-get install -y -qq --no-install-recommends iptables >/dev/null
EOF

envs=(-e PG_HOST=pgbh -e PG_PORT=5432 -e PG_USER=postgres -e PG_PASSWORD=pgbh
	-e PG_DATABASE=postgres -e PG_SSL=true -e PGBH_IP_A="$ip_a" -e PGBH_IP_B="$ip_b"
	-e GODEBUG=netdns=go -e GOFLAGS=-buildvcs=false)
for kv in ${PG_ENV:-}; do envs+=(-e "$kv"); done

docker create --name "$id-test" --cap-add NET_ADMIN --network "$id-a" \
	--add-host "pgbh:$ip_a" "${envs[@]}" \
	-v "$SRC:/src:ro" -v pgbh-gomod:/go/pkg/mod -v pgbh-gocache:/root/.cache/go-build \
	-w /src "pgbh-test:$GO_TAG" \
	go test -tags pgblackhole -count=1 -v -timeout 30m -run "$RUN" . >/dev/null
docker network connect "$id-b" "$id-test"
echo "db $ip_a / $ip_b, go $GO_TAG, PG_ENV='${PG_ENV:-}', run '$RUN', src $SRC" >&2
docker start -a "$id-test"
