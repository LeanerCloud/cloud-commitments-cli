package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issue #2174: a zonal EC2 row must keep its AvailabilityZone through the CSV
// export and --input-csv import, and a dry run must refuse the zonal rows the
// real run refuses. Every AWS call here goes to awsFixtureStub; nothing can buy.

const (
	ec2Host         = "ec2.us-east-1.amazonaws.com"
	ec2Purchase     = "PurchaseReservedInstancesOffering"
	ec2ZonalRespID  = "ec2-ri-zonal-1"
	ec2Offering1a   = "ec2-offering-1a"
	ec2Offering1b   = "ec2-offering-1b"
	ec2FixtureXMLNS = `xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"`
)

func ec2ZonalOffering(id, az string) string {
	return `<item><reservedInstancesOfferingId>` + id + `</reservedInstancesOfferingId><instanceType>m5.large</instanceType>` +
		`<availabilityZone>` + az + `</availabilityZone><duration>31536000</duration><fixedPrice>500.0</fixedPrice>` +
		`<usagePrice>0.0</usagePrice><productDescription>Linux/UNIX</productDescription><instanceTenancy>default</instanceTenancy>` +
		`<currencyCode>USD</currencyCode><offeringType>Partial Upfront</offeringType><offeringClass>convertible</offeringClass>` +
		`<scope>Availability Zone</scope><marketplace>false</marketplace></item>`
}

// ec2ZonalFixtures answers the EC2 calls of one CSV purchase: an empty
// inventory, two zonal offerings (us-east-1a listed first, so buying from it
// would mean the AZ was lost), the purchase and the post-purchase tagging.
func ec2ZonalFixtures() map[string]awsFixture {
	return map[string]awsFixture{
		"ec2 DescribeReservedInstances": {body: `<DescribeReservedInstancesResponse ` + ec2FixtureXMLNS + `><requestId>fixture</requestId>` +
			`<reservedInstancesSet/></DescribeReservedInstancesResponse>`},
		"ec2 DescribeReservedInstancesOfferings": {body: `<DescribeReservedInstancesOfferingsResponse ` + ec2FixtureXMLNS + `>` +
			`<requestId>fixture</requestId><reservedInstancesOfferingsSet>` + ec2ZonalOffering(ec2Offering1a, "us-east-1a") +
			ec2ZonalOffering(ec2Offering1b, "us-east-1b") + `</reservedInstancesOfferingsSet></DescribeReservedInstancesOfferingsResponse>`},
		"ec2 " + ec2Purchase: {body: `<PurchaseReservedInstancesOfferingResponse ` + ec2FixtureXMLNS + `><requestId>fixture</requestId>` +
			`<reservedInstancesId>` + ec2ZonalRespID + `</reservedInstancesId></PurchaseReservedInstancesOfferingResponse>`},
		"ec2 CreateTags": {body: `<CreateTagsResponse ` + ec2FixtureXMLNS + `><requestId>fixture</requestId><return>true</return></CreateTagsResponse>`},
	}
}

// exportCSV writes recs through the real report writer, the file a user would
// pass back with --input-csv, and returns its contents.
func exportCSV(t *testing.T, recs ...common.Recommendation) string {
	t.Helper()
	results := make([]common.PurchaseResult, 0, len(recs))
	for _, rec := range recs {
		results = append(results, common.PurchaseResult{Recommendation: rec, DryRun: true, Success: true, Timestamp: time.Now()})
	}
	path := filepath.Join(t.TempDir(), "export.csv")
	require.NoError(t, writeMultiServiceCSVReport(results, path))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

func TestCSVRoundTrip_ZonalEC2KeepsAvailabilityZone(t *testing.T) {
	zonal := zonalEC2Rec(ec2types.ScopeAvailabilityZone, "us-east-1b")
	zonal.EstimatedSavings = 20
	regional := zonalEC2Rec(ec2types.ScopeRegional, "")
	regional.ResourceType = "c5.large"
	regional.Details.(*common.ComputeDetails).InstanceType = "c5.large"

	recs, err := loadRecommendationsFromCSV(writeTestRecommendationsCSV(t, exportCSV(t, zonal, regional)))
	require.NoError(t, err)
	require.Len(t, recs, 2)

	byType := map[string]*common.ComputeDetails{}
	for _, rec := range recs {
		d, ok := rec.Details.(*common.ComputeDetails)
		require.True(t, ok, "%s: EC2 row must load ComputeDetails", rec.ResourceType)
		byType[rec.ResourceType] = d
	}
	assert.Equal(t, "us-east-1b", byType["m5.large"].AvailabilityZone)
	assert.Equal(t, string(ec2types.ScopeAvailabilityZone), byType["m5.large"].Scope)
	assert.Empty(t, byType["c5.large"].AvailabilityZone, "a regional row must not gain an AZ")
	assert.Equal(t, string(ec2types.ScopeRegional), byType["c5.large"].Scope)
}

// TestCSVRealPurchase_ZonalEC2BuysInItsAZ is the write -> read -> purchase
// path: the exported zonal row is loaded with --input-csv semantics and bought
// through runCSVPurchases against the stub. Before the fix the AZ was dropped
// on export and the library refused the row before any offering lookup.
func TestCSVRealPurchase_ZonalEC2BuysInItsAZ(t *testing.T) {
	env := newCSVPurchaseEnvFor(t, exportCSV(t, zonalEC2Rec(ec2types.ScopeAvailabilityZone, "us-east-1b")), ec2ZonalFixtures())
	env.run("report1.csv")

	rep := env.report(t, "report1.csv")
	assert.Equal(t, []string{"m5.large"}, rep["ResourceType"])
	assert.Equal(t, []string{"true"}, rep["Success"])
	assert.Equal(t, []string{""}, rep["Error"])
	assert.Equal(t, []string{ec2ZonalRespID}, rep["CommitmentID"])

	env.stub.mu.Lock()
	describes := env.stub.forms[ec2Host+" DescribeReservedInstancesOfferings"]
	env.stub.mu.Unlock()
	require.NotEmpty(t, describes)
	for _, form := range describes {
		assert.Equal(t, "us-east-1b", form.Get("AvailabilityZone"), "the offering lookup must be pinned to the row's AZ")
	}
	buy := env.purchaseForm(t, ec2Host+" "+ec2Purchase)
	assert.Equal(t, ec2Offering1b, buy.Get("ReservedInstancesOfferingId"), "must buy the us-east-1b offering, not the first listed")
	assert.Equal(t, "1", buy.Get("InstanceCount"))

	header, rows := readPurchaseReport(t, filepath.Join(env.dir, "report1.csv"))
	assert.Equal(t, []string{"us-east-1b"}, reportColumn(t, header, rows, "AvailabilityZone"), "the purchase report keeps the AZ too")
	assert.Zero(t, env.denyHits.Load(), "nothing may reach the deny endpoint")
}

// runCSVDryRun loads csvData and dry-runs it through runCSVPurchases against
// the stub, returning the report rows.
func runCSVDryRun(t *testing.T, csvData string) (header []string, rows [][]string, stub *awsFixtureStub) {
	t.Helper()
	origCfg := toolCfg
	t.Cleanup(func() { toolCfg = origCfg })
	isolateAWSEnv(t)
	awsCfg, stub := newAWSFixtureConfig(t)
	for k, v := range ec2ZonalFixtures() {
		if !strings.Contains(k, "Purchase") {
			stub.fixtures[k] = v
		}
	}
	recs, err := loadRecommendationsFromCSV(writeTestRecommendationsCSV(t, csvData))
	require.NoError(t, err)
	dir := t.TempDir()
	cfg := Config{CallerAccountID: defaultTestAccount, CSVOutput: filepath.Join(dir, "dry.csv"), AuditLog: filepath.Join(dir, "audit.jsonl")}
	require.NoError(t, runCSVPurchases(context.Background(), awsCfg, recs, true, cfg, "dry-run"))
	header, rows = readPurchaseReport(t, cfg.CSVOutput)
	return header, rows, stub
}

func TestCSVDryRun_RefusesZonalEC2RowsTheRealRunRefuses(t *testing.T) {
	const legacyHeader = "Service,Region,ResourceType,Engine,Count,Term,PaymentOption,Tenancy,Scope"
	tests := []struct {
		name    string
		csv     string
		wantErr string // "" means the dry run must succeed
	}{
		{
			name:    "zonal row from a CSV without the AvailabilityZone column",
			csv:     legacyHeader + "\nec2,us-east-1,m5.large,Linux/UNIX,1,1yr,partial-upfront,default,Availability Zone\n",
			wantErr: "zonal scope but no AvailabilityZone",
		},
		{
			name:    "zonal row with a blank AvailabilityZone",
			csv:     legacyHeader + ",AvailabilityZone\nec2,us-east-1,m5.large,Linux/UNIX,1,1yr,partial-upfront,default,Availability Zone,  \n",
			wantErr: "zonal scope but no AvailabilityZone",
		},
		{
			name:    "regional row that names an AZ",
			csv:     legacyHeader + ",AvailabilityZone\nec2,us-east-1,m5.large,Linux/UNIX,1,1yr,partial-upfront,default,Region,us-east-1b\n",
			wantErr: `regional scope but AvailabilityZone "us-east-1b"`,
		},
		{
			name: "zonal row with its AZ",
			csv:  legacyHeader + ",AvailabilityZone\nec2,us-east-1,m5.large,Linux/UNIX,1,1yr,partial-upfront,default,Availability Zone,us-east-1b\n",
		},
		{
			name: "regional row without an AZ",
			csv:  legacyHeader + ",AvailabilityZone\nec2,us-east-1,m5.large,Linux/UNIX,1,1yr,partial-upfront,default,Region,\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header, rows, stub := runCSVDryRun(t, tt.csv)
			require.Len(t, rows, 1)
			success := reportColumn(t, header, rows, "Success")[0]
			errCell := reportColumn(t, header, rows, "Error")[0]
			if tt.wantErr == "" {
				assert.Equal(t, "true", success)
				assert.Empty(t, errCell)
			} else {
				assert.Equal(t, "false", success, "the dry run must not report success for a row the real run refuses")
				assert.Contains(t, errCell, tt.wantErr)
			}
			stub.mu.Lock()
			defer stub.mu.Unlock()
			for k := range stub.received {
				assert.NotContains(t, k, "Offerings", "a dry run makes no offering lookup")
				assert.NotContains(t, k, ec2Purchase)
			}
		})
	}
}

// TestDetailCells_AvailabilityZone covers both ComputeDetails shapes the
// writer accepts: the parser's pointer and a plain value.
func TestDetailCells_AvailabilityZone(t *testing.T) {
	azCol := len(csvDetailColumns) - 1
	require.Equal(t, "AvailabilityZone", csvDetailColumns[azCol])
	ptr := zonalEC2Rec(ec2types.ScopeAvailabilityZone, "us-east-1b")
	val := ptr
	val.Details = *ptr.Details.(*common.ComputeDetails)
	for name, rec := range map[string]common.Recommendation{"pointer": ptr, "value": val} {
		cells := detailCells(rec)
		assert.Equal(t, "us-east-1b", cells[azCol], name)
		assert.Equal(t, string(ec2types.ScopeAvailabilityZone), cells[azCol-1], name)
	}
}

// TestValidatePurchasePreconditions_EC2AvailabilityZone pins the dry-run check
// to the library's refusal (providers/aws ec2 buildEC2OfferingQuery), including
// its legacy scope spellings.
func TestValidatePurchasePreconditions_EC2AvailabilityZone(t *testing.T) {
	withScope := func(scope, az string) common.Recommendation {
		return zonalEC2Rec(ec2types.Scope(scope), az)
	}
	ok := map[string]common.Recommendation{
		"zonal with AZ":                 withScope("Availability Zone", "us-east-1b"),
		"legacy zonal spelling with AZ": withScope("availability-zone", "us-east-1b"),
		"regional without AZ":           withScope("Region", ""),
		"regional with blank AZ":        withScope("Region", "   "),
	}
	for name, rec := range ok {
		assert.NoError(t, validatePurchasePreconditions(rec, rec.Region, defaultTestAccount), name)
	}
	bad := map[string]common.Recommendation{
		"zonal without AZ":               withScope("Availability Zone", ""),
		"zonal with blank AZ":            withScope("Availability Zone", " \t"),
		"legacy zonal spelling no AZ":    withScope("availability-zone", ""),
		"lowercase zonal spelling no AZ": withScope("availability zone", ""),
		"regional with AZ":               withScope("Region", "us-east-1b"),
		"lowercase regional with AZ":     withScope("region", "us-east-1b"),
	}
	for name, rec := range bad {
		err := validatePurchasePreconditions(rec, rec.Region, defaultTestAccount)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "AvailabilityZone", name)
	}
}
