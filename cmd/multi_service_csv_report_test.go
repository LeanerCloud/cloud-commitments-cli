package main

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
)

func TestWriteMultiServiceCSVReport_EmptyResults(t *testing.T) {
	tmpFile := "/tmp/test_empty_results.csv"
	defer os.Remove(tmpFile)

	err := writeMultiServiceCSVReport([]common.PurchaseResult{}, tmpFile)
	assert.NoError(t, err)

	// File should not be created for empty results
	_, err = os.Stat(tmpFile)
	assert.True(t, os.IsNotExist(err))
}

func TestWriteMultiServiceCSVReport_Success(t *testing.T) {
	tmpFile := "/tmp/test_csv_success.csv"
	defer os.Remove(tmpFile)

	results := []common.PurchaseResult{
		{
			Recommendation: common.Recommendation{
				Service:          common.ServiceRDS,
				Region:           "us-east-1",
				ResourceType:     "db.t3.small",
				Count:            2,
				Account:          "123456789012",
				AccountName:      "test-account",
				Term:             "1yr",
				PaymentOption:    "all-upfront",
				EstimatedSavings: 100.50,
			},
			Success:      true,
			CommitmentID: "test-commitment-id",
			Timestamp:    time.Now(),
		},
	}

	err := writeMultiServiceCSVReport(results, tmpFile)
	assert.NoError(t, err)

	// Verify file was created and has content
	content, err := os.ReadFile(tmpFile)
	assert.NoError(t, err)
	assert.Contains(t, string(content), "Service,Region,ResourceType")
	assert.Contains(t, string(content), "rds")
	assert.Contains(t, string(content), "us-east-1")
	assert.Contains(t, string(content), "db.t3.small")
	assert.Contains(t, string(content), "test-commitment-id")
}

func TestWriteMultiServiceCSVReport_WithError(t *testing.T) {
	tmpFile := "/tmp/test_csv_with_error.csv"
	defer os.Remove(tmpFile)

	results := []common.PurchaseResult{
		{
			Recommendation: common.Recommendation{
				Service:      common.ServiceEC2,
				Region:       "us-west-2",
				ResourceType: "t3.medium",
				Count:        1,
			},
			Success:      false,
			CommitmentID: "",
			Error:        fmt.Errorf("purchase failed: insufficient quota"),
			Timestamp:    time.Now(),
		},
	}

	err := writeMultiServiceCSVReport(results, tmpFile)
	assert.NoError(t, err)

	// Verify error is included in CSV
	content, err := os.ReadFile(tmpFile)
	assert.NoError(t, err)
	assert.Contains(t, string(content), "false")
	assert.Contains(t, string(content), "purchase failed: insufficient quota")
}

func TestWriteMultiServiceCSVReport_InvalidPath(t *testing.T) {
	// Use an invalid path (directory that doesn't exist)
	invalidPath := "/nonexistent/directory/test.csv"

	results := []common.PurchaseResult{
		{
			Recommendation: common.Recommendation{
				Service:      common.ServiceRDS,
				Region:       "us-east-1",
				ResourceType: "db.t3.small",
				Count:        1,
			},
			Success:   true,
			Timestamp: time.Now(),
		},
	}

	err := writeMultiServiceCSVReport(results, invalidPath)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create CSV file")
}

func TestWriteMultiServiceCSVReport_MultipleResults(t *testing.T) {
	tmpFile := "/tmp/test_csv_multiple.csv"
	defer os.Remove(tmpFile)

	results := []common.PurchaseResult{
		{
			Recommendation: common.Recommendation{
				Service:          common.ServiceRDS,
				Region:           "us-east-1",
				ResourceType:     "db.t3.small",
				Count:            2,
				EstimatedSavings: 150.25,
			},
			Success:      true,
			CommitmentID: "commitment-1",
			Timestamp:    time.Now(),
		},
		{
			Recommendation: common.Recommendation{
				Service:          common.ServiceElastiCache,
				Region:           "us-west-2",
				ResourceType:     "cache.t3.micro",
				Count:            3,
				EstimatedSavings: 75.50,
			},
			Success:      true,
			CommitmentID: "commitment-2",
			Timestamp:    time.Now(),
		},
	}

	err := writeMultiServiceCSVReport(results, tmpFile)
	assert.NoError(t, err)

	// Verify both results are in CSV
	content, err := os.ReadFile(tmpFile)
	assert.NoError(t, err)
	assert.Contains(t, string(content), "commitment-1")
	assert.Contains(t, string(content), "commitment-2")
	assert.Contains(t, string(content), "150.25")
	assert.Contains(t, string(content), "75.50")
}
