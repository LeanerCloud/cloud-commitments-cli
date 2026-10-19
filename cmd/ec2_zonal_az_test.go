package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ec2FormRecorder is an offline RoundTripper that records the Action and
// AvailabilityZone of every EC2 query request and answers each with a
// non-retryable 400, so no purchase can ever succeed.
type ec2FormRecorder struct {
	mu      sync.Mutex
	actions []string
	zones   []string
}

func (r *ec2FormRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.actions = append(r.actions, values.Get("Action"))
	r.zones = append(r.zones, values.Get("AvailabilityZone"))
	r.mu.Unlock()
	payload := `<Response><Errors><Error><Code>InvalidParameterValue</Code><Message>stubbed</Message></Error></Errors></Response>`
	return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"text/xml"}},
		Body: io.NopCloser(bytes.NewReader([]byte(payload))), Request: req}, nil
}

func zonalEC2Rec(scope ec2types.Scope, az string) common.Recommendation {
	rec := validEC2Rec()
	rec.Term = "1yr"
	rec.PaymentOption = "partial-upfront"
	rec.Details = &common.ComputeDetails{InstanceType: "m5.large", Platform: "Linux/UNIX", Tenancy: "default",
		Scope: string(scope), AvailabilityZone: az}
	return rec
}

func purchaseEC2WithRecorder(t *testing.T, rec common.Recommendation) (common.PurchaseResult, *ec2FormRecorder) {
	t.Helper()
	t.Setenv("DISABLE_PURCHASE_DELAY", "true")
	recorder := &ec2FormRecorder{}
	awsCfg := aws.Config{
		HTTPClient:  &http.Client{Transport: recorder},
		Credentials: aws.AnonymousCredentials{},
		Retryer:     func() aws.Retryer { return aws.NopRetryer{} },
	}
	cfg := Config{CallerAccountID: defaultTestAccount, AuditLog: filepath.Join(t.TempDir(), "audit.jsonl")}
	result, _ := purchaseSingleRec(context.Background(), awsCfg, rec, 1, false, cfg)
	return result, recorder
}

func TestPurchaseSingleRec_ZonalEC2KeepsItsAvailabilityZone(t *testing.T) {
	result, recorder := purchaseEC2WithRecorder(t, zonalEC2Rec(ec2types.ScopeAvailabilityZone, "us-east-1b"))

	require.Error(t, result.Error)
	assert.Contains(t, recorder.actions, "DescribeReservedInstancesOfferings")
	assert.NotContains(t, recorder.actions, "PurchaseReservedInstancesOffering")
	for i, action := range recorder.actions {
		if action == "DescribeReservedInstancesOfferings" {
			assert.Equal(t, "us-east-1b", recorder.zones[i], "the offering query must be pinned to the rec's AZ")
		}
	}
}

func TestPurchaseSingleRec_RefusesInconsistentEC2ScopeBeforeAnyAWSCall(t *testing.T) {
	for name, rec := range map[string]common.Recommendation{
		"zonal without AZ": zonalEC2Rec(ec2types.ScopeAvailabilityZone, ""),
		"regional with AZ": zonalEC2Rec(ec2types.ScopeRegional, "us-east-1b"),
		"zonal blank AZ":   zonalEC2Rec(ec2types.ScopeAvailabilityZone, "  "),
	} {
		t.Run(name, func(t *testing.T) {
			result, recorder := purchaseEC2WithRecorder(t, rec)

			require.Error(t, result.Error)
			assert.Contains(t, strings.ToLower(result.Error.Error()), "availability zone")
			assert.Empty(t, recorder.actions, "no AWS call may precede the refusal")
		})
	}
}
