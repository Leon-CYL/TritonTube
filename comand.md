go run ./cmd/web -port 3344 \
 sqlite "./metadata.db" \
 nw "localhost:3343,localhost:8090,localhost:8091,localhost:8092"

go run ./cmd/admin list localhost:3343
go run ./cmd/admin remove localhost:3343 localhost:8090
go run ./cmd/admin add localhost:3343 localhost:8090
