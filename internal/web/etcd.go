// Lab 9: Implement a distributed video metadata service

package web

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

type EtcdVideoMetadataService struct {
	etcdClient *clientv3.Client
}

var _ VideoMetadataService = (*EtcdVideoMetadataService)(nil)

func NewEtcdVideoMetadataService(nodes []string) (*EtcdVideoMetadataService, error) {
	client, err := clientv3.New(clientv3.Config{
		Endpoints: nodes,
	})

	if err != nil {
		fmt.Printf("Client Error: %v\n", err)
		return nil, err
	}

	return &EtcdVideoMetadataService{
		etcdClient: client,
	}, nil
}

func (es *EtcdVideoMetadataService) Close() error {
	return es.etcdClient.Close()
}

func (es *EtcdVideoMetadataService) Read(videoId string) (*VideoMetadata, error) {
	res, err := es.etcdClient.Get(context.Background(), videoId)

	if err != nil {
		fmt.Printf("Read Error: %v\n", err)
		return nil, err
	}

	if len(res.Kvs) == 0 {
		fmt.Printf("etcd: Key is not found: %v\n", videoId)
		return nil, nil
	}

	var metadata VideoMetadata
	err = json.Unmarshal(res.Kvs[0].Value, &metadata)
	if err != nil {
		return nil, fmt.Errorf("failed to parse metadata for %s: %w", videoId, err)
	}

	return &metadata, nil
}

func (es *EtcdVideoMetadataService) Create(videoId string, uploadedAt time.Time) error {
	metadata := VideoMetadata{
		Id:         videoId,
		UploadedAt: uploadedAt,
	}
	value, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	_, err = es.etcdClient.Put(context.Background(), videoId, string(value))
	if err != nil {
		fmt.Printf("Create Error: %v\n", err)
		return err
	}

	return nil
}

func (es *EtcdVideoMetadataService) List() ([]VideoMetadata, error) {
	res, err := es.etcdClient.Get(context.Background(), "", clientv3.WithPrefix())

	if err != nil {
		fmt.Printf("Create Error: %v\n", err)
		return nil, err
	}

	var results []VideoMetadata

	for _, kv := range res.Kvs {
		var metadata VideoMetadata
		err = json.Unmarshal(kv.Value, &metadata)

		if err != nil {
			return nil, fmt.Errorf("failed to parse metadata for %s: %w", kv.Key, err)
		}

		results = append(results, metadata)
	}
	return results, nil
}
