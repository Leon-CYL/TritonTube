# TritonTube

## Command

Storage Command:
1. Storage 1
> go run ./cmd/storage -port 8090 "./storage/8090"<br>
2. Storage 2
> go run ./cmd/storage -port 8091 "./storage/8091"<br>
3. Storage 3
> go run ./cmd/storage -port 8092 "./storage/8092"<br>

etcd Command:
> brew install etcd<br>

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

Server Command:

> go run ./cmd/web -port 3344 etcd "localhost:8093,localhost:8094,localhost:8095" nw "localhost:3343,localhost:8090,localhost:8091,localhost:8092"<br>

Storage Node Operation Command:

1. Add
> go run ./cmd/admin add localhost:3343 localhost:8090

2. Remove
> go run ./cmd/admin remove localhost:3343 localhost:8090

3. List
> go run ./cmd/admin remove localhost:3343 localhost:8090
