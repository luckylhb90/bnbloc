# Replication recipe: a local primary and read replica

A primary and one replica of MySQL 8.4 on one Docker network, for trying the
read-replica routing (`BNBLOC_MYSQL_REPLICA_DSN`) and for the replica integration
tests. Production topology is your operator's; this is the shape the platform
expects. Replace the passwords before using it anywhere shared.

## Start both servers

```sh
docker network create bnbloc-repl
docker run -d --name bnbloc-primary --network bnbloc-repl -p 13306:3306 \
  -e MYSQL_ROOT_PASSWORD=root-pass -e MYSQL_DATABASE=bnbloc \
  -e MYSQL_USER=bnbloc -e MYSQL_PASSWORD=bnbloc-pass \
  mysql:8.4 --server-id=1 --log-bin=mysql-bin --binlog-format=ROW \
  --gtid-mode=ON --enforce-gtid-consistency=ON \
  --character-set-server=utf8mb4 --collation-server=utf8mb4_0900_ai_ci

docker run -d --name bnbloc-replica --network bnbloc-repl -p 13307:3306 \
  -e MYSQL_ROOT_PASSWORD=root-pass -e MYSQL_DATABASE=bnbloc \
  -e MYSQL_USER=bnbloc -e MYSQL_PASSWORD=bnbloc-pass \
  mysql:8.4 --server-id=2 --log-bin=mysql-bin --binlog-format=ROW \
  --gtid-mode=ON --enforce-gtid-consistency=ON \
  --character-set-server=utf8mb4 --collation-server=utf8mb4_0900_ai_ci
```

Do not add `--read-only` to the replica's start command yet. The image's init
script writes to the replica while it starts, and `super_read_only` refuses that
write (`ERROR 1290`). Set read-only after replication is configured, below.

Wait for both to answer `mysqladmin ping`.

## Start replication from a clean history

Each container's init script ran its own statements, so their GTID histories
differ. Reset both binary logs and GTIDs after init, so the replica starts from an
empty history and receives only what the application writes from now on:

```sh
docker exec bnbloc-primary mysql -uroot -proot-pass -e "RESET BINARY LOGS AND GTIDS;"
docker exec bnbloc-primary mysql -uroot -proot-pass -e \
  "CREATE USER 'repl'@'%' IDENTIFIED BY 'repl-pass'; GRANT REPLICATION SLAVE ON *.* TO 'repl'@'%';"

docker exec bnbloc-replica mysql -uroot -proot-pass -e "RESET BINARY LOGS AND GTIDS;"
docker exec bnbloc-replica mysql -uroot -proot-pass -e "CHANGE REPLICATION SOURCE TO \
  SOURCE_HOST='bnbloc-primary', SOURCE_USER='repl', SOURCE_PASSWORD='repl-pass', \
  SOURCE_AUTO_POSITION=1, GET_SOURCE_PUBLIC_KEY=1; START REPLICA;"
```

Make the replica read-only (persisted, so a restart keeps it):

```sh
docker exec bnbloc-replica mysql -uroot -proot-pass -e \
  "SET PERSIST read_only=ON; SET PERSIST super_read_only=ON;"
```

## Verify

```sh
docker exec bnbloc-replica mysql -uroot -proot-pass -e "SHOW REPLICA STATUS\G" \
  | grep -E "Replica_(IO|SQL)_Running|Seconds_Behind_Source"
# Expect: Replica_IO_Running: Yes, Replica_SQL_Running: Yes, Seconds_Behind_Source: 0

docker exec bnbloc-primary mysql -uroot -proot-pass -e \
  "CREATE DATABASE IF NOT EXISTS probe; CREATE TABLE probe.t (id INT PRIMARY KEY); INSERT INTO probe.t VALUES (1);"
docker exec bnbloc-replica mysql -uroot -proot-pass -N -e "SELECT COUNT(*) FROM probe.t"   # 1
docker exec bnbloc-replica mysql -uroot -proot-pass -e "INSERT INTO probe.t VALUES (2)"    # ERROR 1290: read-only
docker exec bnbloc-primary mysql -uroot -proot-pass -e "DROP DATABASE probe"
```

## Point the platform at it

```sh
export BNBLOC_MYSQL_DSN='bnbloc:bnbloc-pass@tcp(127.0.0.1:13306)/bnbloc?parseTime=true'
export BNBLOC_MYSQL_REPLICA_DSN='bnbloc:bnbloc-pass@tcp(127.0.0.1:13307)/bnbloc?parseTime=true'
```

The application user has the same grants on the replica because the image created
it there at init. Replication carries the schema the platform applies on the
primary; the replica never runs migrations.

## Run the replica integration tests

Point BNBLOC_TEST_MYSQL_DSN at the primary test DSN (its value is kept out of this doc), then:

```sh
BNBLOC_TEST_MYSQL_REPLICA_ADDR='127.0.0.1:13307' \
  go test ./internal/store/ -run 'Replica|Routing|Stats|Writes' -count=1 -v
```

The tests set a short `SOURCE_DELAY` on the replica to show a bounded stale read
and the fallback past the bound, then restore it. They take about 25 seconds.

## Tear down

```sh
docker rm -f bnbloc-primary bnbloc-replica && docker network rm bnbloc-repl
```
