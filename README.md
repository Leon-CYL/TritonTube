# TritonTube

## Description:

TritonTube is a distributed video-sharing platform built in Go that allows users to upload MP4 videos, which are transcoded into DASH format and distributed across multiple content nodes. Metadata such as video IDs and upload timestamps are managed using a multi-node etcd cluster to ensure high availability and consistency. The system uses gRPC for efficient server-client communication between nodes, enabling scalable and resilient coordination of video storage and metadata services. TritonTube uses practical distributed systems design with modular components and fault-tolerant architecture.

## Command

gRPC:

> go install google.golang.org/protobuf/cmd/protoc-gen-go@latest <br>

> go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest <br>

> export PATH="$PATH:$(go env GOPATH)/bin" <br>

> protoc --proto_path=proto --go_out=. --go-grpc_out=. proto/storage.proto <br>

etcd:

> brew install etcd<br>

RocksDB:

> brew install rocksdb<br>

> go get github.com/linxGnu/grocksdb@latest<br>

Storage Command(3 terminals):

1. Storage 1

   > export CGO_CFLAGS="-I/opt/homebrew/include"<br>

   > export CGO_LDFLAGS="-L/opt/homebrew/lib -lrocksdb"<br>

   > go run ./cmd/storage -port 8090 "./storage/8090"<br>

2. Storage 2

   > export CGO_CFLAGS="-I/opt/homebrew/include"<br>

   > export CGO_LDFLAGS="-L/opt/homebrew/lib -lrocksdb"<br>

   > go run ./cmd/storage -port 8091 "./storage/8091"<br>

3. Storage 3

   > export CGO_CFLAGS="-I/opt/homebrew/include"<br>

   > export CGO_LDFLAGS="-L/opt/homebrew/lib -lrocksdb"<br>

   > go run ./cmd/storage -port 8092 "./storage/8092"<br>

etcd Command(3 terminals):

1. etcd node 1

   > etcd --name node1 \
     --data-dir ./data1 \
     --initial-advertise-peer-urls http://localhost:2380 \
     --listen-peer-urls http://localhost:2380 \
     --listen-client-urls http://localhost:8093 \
     --advertise-client-urls http://localhost:8093 \
     --initial-cluster-token etcd-cluster-1 \
     --initial-cluster node1=http://localhost:2380,node2=http://localhost:2381,node3=http://localhost:2382 \
     --initial-cluster-state new
   <br>

2. etcd node 2

   > etcd --name node2 \
     --data-dir ./data2 \
     --initial-advertise-peer-urls http://localhost:2381 \
     --listen-peer-urls http://localhost:2381 \
     --listen-client-urls http://localhost:8094 \
     --advertise-client-urls http://localhost:8094 \
     --initial-cluster-token etcd-cluster-1 \
     --initial-cluster node1=http://localhost:2380,node2=http://localhost:2381,node3=http://localhost:2382 \
     --initial-cluster-state new
   <br>

3. etcd node 3
   > etcd --name node3 \
     --data-dir ./data3 \
     --initial-advertise-peer-urls http://localhost:2382 \
     --listen-peer-urls http://localhost:2382 \
     --listen-client-urls http://localhost:8095 \
     --advertise-client-urls http://localhost:8095 \
     --initial-cluster-token etcd-cluster-1 \
     --initial-cluster node1=http://localhost:2380,node2=http://localhost:2381,node3=http://localhost:2382 \
     --initial-cluster-state new
   <br>

Server Command(1 terminal):

> export CGO_CFLAGS="-I/opt/homebrew/include"<br>

> export CGO_LDFLAGS="-L/opt/homebrew/lib -lrocksdb"<br>

> go run ./cmd/web -port 3344 etcd "localhost:8093,localhost:8094,localhost:8095" nw "localhost:3343,localhost:8090,localhost:8091,localhost:8092"<br>

Storage Node Operation Command(1 terminal):

1. Add

   > go run ./cmd/admin add localhost:3343 localhost:8096

2. Remove

   > go run ./cmd/admin remove localhost:3343 localhost:8096

3. List
   > go run ./cmd/admin list localhost:3343

## Performance

Sequential for loop:

1. Write File Performance: 475 DASH files for video 1 in 29.48 second, average 61.38ms

2. Migrate File Performance: 267 DASH files for video 1 in 16.98 second, average 63.60ms

3. Read File Performance: average 58ms

ThreadPool (Write Files):

1. 32 workers:
   2025/07/15 13:45:58 Uploaded 475 DASH files for video2 in 1.211037125s
   2025/07/15 13:45:58 Average write time per file: 2.549551ms

2. 64 workers:
   2025/07/15 13:47:30 Uploaded 475 DASH files for video3 in 800.514666ms
   2025/07/15 13:47:30 Average write time per file: 1.685294ms

3. 128 workers:
   2025/07/15 13:50:42 Uploaded 475 DASH files for video4 in 5.85389025s
   2025/07/15 13:50:42 Average write time per file: 12.323979ms

Batch gRPC and RocksDB(Migrating Files):
1. Migrate File Performance: 267 DASH files for video 1 in 315 ms

## Resume

1. Parallelized write operations using goroutines with a thread pool to improve concurrency and prevent overload, reducing the upload time of 475 DASH files from 29.48 seconds to 800.51 milliseconds — achieving an approximate 97% reduction in upload time.

2. Replaced the local file system with RocksDB and implemented gRPC-based batch file transfer to minimize disk I/O during storage node migration, reducing the migration time of 267 DASH video files from 16.98 seconds to 315 milliseconds — achieving an approximate 98% reduction in transfer time.
