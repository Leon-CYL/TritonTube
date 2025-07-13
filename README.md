# TritonTube

## Description:
TritonTube is a distributed video-sharing platform built in Go that allows users to upload MP4 videos, which are transcoded into DASH format and distributed across multiple content nodes. Metadata such as video IDs and upload timestamps are managed using a multi-node etcd cluster to ensure high availability and consistency. The system uses gRPC for efficient server-client communication between nodes, enabling scalable and resilient coordination of video storage and metadata services. TritonTube uses practical distributed systems design with modular components and fault-tolerant architecture.


## Command

Storage Command:
> go run ./cmd/storage -port 8090 "./storage/8090"
> go run ./cmd/storage -port 8091 "./storage/8091"
> go run ./cmd/storage -port 8092 "./storage/8092"

etcd Command:
> brew install etcd

> etcd --name node1 \
 --data-dir ./data1 \
 --initial-advertise-peer-urls http://localhost:2380 \
 --listen-peer-urls http://localhost:2380 \
 --listen-client-urls http://localhost:8093 \
 --advertise-client-urls http://localhost:8093 \
 --initial-cluster-token etcd-cluster-1 \
 --initial-cluster node1=http://localhost:2380,node2=http://localhost:2381,node3=http://localhost:2382 \
 --initial-cluster-state new


> etcd --name node2 \
 --data-dir ./data2 \
 --initial-advertise-peer-urls http://localhost:2381 \
 --listen-peer-urls http://localhost:2381 \
 --listen-client-urls http://localhost:8094 \
 --advertise-client-urls http://localhost:8094 \
 --initial-cluster-token etcd-cluster-1 \
 --initial-cluster node1=http://localhost:2380,node2=http://localhost:2381,node3=http://localhost:2382 \
 --initial-cluster-state new


> etcd --name node3 \
 --data-dir ./data3 \
 --initial-advertise-peer-urls http://localhost:2382 \
 --listen-peer-urls http://localhost:2382 \
 --listen-client-urls http://localhost:8095 \
 --advertise-client-urls http://localhost:8095 \
 --initial-cluster-token etcd-cluster-1 \
 --initial-cluster node1=http://localhost:2380,node2=http://localhost:2381,node3=http://localhost:2382 \
 --initial-cluster-state new


Server Command:

> go run ./cmd/web -port 3344 etcd "localhost:8093,localhost:8094,localhost:8095" nw "localhost:3343,localhost:8090,localhost:8091,localhost:8092"

Storage Node Operation Command:

1. Add
> go run ./cmd/admin add localhost:3343 localhost:8096

2. Remove
> go run ./cmd/admin remove localhost:3343 localhost:8096

3. List
> go run ./cmd/admin list localhost:3343


## Perfromance

Write
1. Write File Performance: 475 DASH files for video 1 in 29.48 second, average 61.38ms
2. By parallelizing the write operations using goroutines, I reduced the file upload time by approximately 90% compared to the sequential implementation.(Resume)
3. By using Goroutines to parallelize the file upload process, I was able to reduce the file uploading time from ~30 secoends to ~3 seconds of a 17 minutes long video.(Project Description)

Read
1. Read File Performance: ~58ms