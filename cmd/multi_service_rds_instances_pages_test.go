package main

import (
	"context"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsrds "github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loopingRDSInstancesClient always returns a next marker.
type loopingRDSInstancesClient struct {
	calls  int
	onCall func()
}

func (c *loopingRDSInstancesClient) DescribeDBInstances(_ context.Context, _ *awsrds.DescribeDBInstancesInput, _ ...func(*awsrds.Options)) (*awsrds.DescribeDBInstancesOutput, error) {
	c.calls++
	if c.onCall != nil {
		c.onCall()
	}
	return &awsrds.DescribeDBInstancesOutput{Marker: aws.String("next")}, nil
}

func TestQueryRDSInstancesPaged_PageCapErrors(t *testing.T) {
	client := &loopingRDSInstancesClient{}
	var mu sync.Mutex
	err := queryRDSInstancesPaged(context.Background(), client, "eu-west-1", map[string][]InstanceEngineVersion{}, &mu)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "eu-west-1")
	assert.Equal(t, maxInstancePages, client.calls)
}

func TestQueryRDSInstancesPaged_CanceledContextStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &loopingRDSInstancesClient{onCall: cancel}
	var mu sync.Mutex
	err := queryRDSInstancesPaged(ctx, client, "eu-west-1", map[string][]InstanceEngineVersion{}, &mu)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, client.calls)
}
