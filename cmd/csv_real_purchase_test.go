package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests drive the CSV purchase loop (runCSVPurchases with isDryRun=false)
// for RDS and ElastiCache. Nothing here can buy anything: the aws.Config is a
// literal whose HTTP client is awsFixtureStub (never delegates, unknown
// operations fail the test) with anonymous credentials, and no test calls
// runToolFromCSV with ActualPurchase=true.

const (
	rdsHost      = "rds.us-east-1.amazonaws.com"
	cacheHost    = "elasticache.us-west-2.amazonaws.com"
	rdsPurchase  = "PurchaseReservedDBInstancesOffering"
	cachePurch   = "PurchaseReservedCacheNodesOffering"
	rdsRespID    = "aws-ri-rds-1" // differs from any generated request id
	cacheRespID  = "aws-ri-cache-1"
	csvPurchases = `Service,Region,ResourceType,Engine,Deployment,Count,Term,PaymentOption
rds,us-east-1,db.t3.small,postgres,single-az,2,1yr,all-upfront
elasticache,us-west-2,cache.t3.micro,redis,,1,1yr,all-upfront
`
)

func queryErr(code, msg string) awsFixture {
	return awsFixture{status: http.StatusBadRequest, body: `<ErrorResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><Error>` +
		`<Type>Sender</Type><Code>` + code + `</Code><Message>` + msg + `</Message></Error><RequestId>fixture</RequestId></ErrorResponse>`}
}

func rdsFixtures() map[string]awsFixture {
	return map[string]awsFixture{
		"rds DescribeReservedDBInstancesOfferings": {body: `<DescribeReservedDBInstancesOfferingsResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/">` +
			`<DescribeReservedDBInstancesOfferingsResult><ReservedDBInstancesOfferings><ReservedDBInstancesOffering>` +
			`<ReservedDBInstancesOfferingId>rds-offering-1</ReservedDBInstancesOfferingId><DBInstanceClass>db.t3.small</DBInstanceClass>` +
			`<Duration>31536000</Duration><ProductDescription>postgresql</ProductDescription><OfferingType>All Upfront</OfferingType>` +
			`<MultiAZ>false</MultiAZ></ReservedDBInstancesOffering></ReservedDBInstancesOfferings></DescribeReservedDBInstancesOfferingsResult>` +
			`<ResponseMetadata><RequestId>fixture</RequestId></ResponseMetadata></DescribeReservedDBInstancesOfferingsResponse>`},
		"rds " + rdsPurchase: {body: `<PurchaseReservedDBInstancesOfferingResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/">` +
			`<PurchaseReservedDBInstancesOfferingResult><ReservedDBInstance><ReservedDBInstanceId>` + rdsRespID + `</ReservedDBInstanceId>` +
			`</ReservedDBInstance></PurchaseReservedDBInstancesOfferingResult><ResponseMetadata><RequestId>fixture</RequestId></ResponseMetadata>` +
			`</PurchaseReservedDBInstancesOfferingResponse>`},
	}
}

func cacheFixtures() map[string]awsFixture {
	return map[string]awsFixture{
		"elasticache DescribeReservedCacheNodesOfferings": {body: `<DescribeReservedCacheNodesOfferingsResponse xmlns="http://elasticache.amazonaws.com/doc/2015-02-02/">` +
			`<DescribeReservedCacheNodesOfferingsResult><ReservedCacheNodesOfferings><ReservedCacheNodesOffering>` +
			`<ReservedCacheNodesOfferingId>cache-offering-1</ReservedCacheNodesOfferingId><CacheNodeType>cache.t3.micro</CacheNodeType>` +
			`<Duration>31536000</Duration><ProductDescription>redis</ProductDescription><OfferingType>All Upfront</OfferingType>` +
			`</ReservedCacheNodesOffering></ReservedCacheNodesOfferings></DescribeReservedCacheNodesOfferingsResult>` +
			`<ResponseMetadata><RequestId>fixture</RequestId></ResponseMetadata></DescribeReservedCacheNodesOfferingsResponse>`},
		"elasticache " + cachePurch: {body: `<PurchaseReservedCacheNodesOfferingResponse xmlns="http://elasticache.amazonaws.com/doc/2015-02-02/">` +
			`<PurchaseReservedCacheNodesOfferingResult><ReservedCacheNode><ReservedCacheNodeId>` + cacheRespID + `</ReservedCacheNodeId>` +
			`</ReservedCacheNode></PurchaseReservedCacheNodesOfferingResult><ResponseMetadata><RequestId>fixture</RequestId></ResponseMetadata>` +
			`</PurchaseReservedCacheNodesOfferingResponse>`},
	}
}

// replayFixtures answers the existing-commitment Describe calls with one active
// RI per service bought just now: the state a second run sees after run 1.
func replayFixtures(rdsClass, cacheType string) map[string]awsFixture {
	now := time.Now().UTC().Format(time.RFC3339)
	return map[string]awsFixture{
		"rds DescribeReservedDBInstances": {body: `<DescribeReservedDBInstancesResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/">` +
			`<DescribeReservedDBInstancesResult><ReservedDBInstances><ReservedDBInstance><ReservedDBInstanceId>` + rdsRespID + `</ReservedDBInstanceId>` +
			`<DBInstanceClass>` + rdsClass + `</DBInstanceClass><ProductDescription>postgresql</ProductDescription><MultiAZ>false</MultiAZ>` +
			`<DBInstanceCount>2</DBInstanceCount><Duration>31536000</Duration><StartTime>` + now + `</StartTime><State>active</State>` +
			`</ReservedDBInstance></ReservedDBInstances></DescribeReservedDBInstancesResult>` +
			`<ResponseMetadata><RequestId>fixture</RequestId></ResponseMetadata></DescribeReservedDBInstancesResponse>`},
		"elasticache DescribeReservedCacheNodes": {body: `<DescribeReservedCacheNodesResponse xmlns="http://elasticache.amazonaws.com/doc/2015-02-02/">` +
			`<DescribeReservedCacheNodesResult><ReservedCacheNodes><ReservedCacheNode><ReservedCacheNodeId>` + cacheRespID + `</ReservedCacheNodeId>` +
			`<CacheNodeType>` + cacheType + `</CacheNodeType><ProductDescription>redis</ProductDescription><CacheNodeCount>1</CacheNodeCount>` +
			`<Duration>31536000</Duration><StartTime>` + now + `</StartTime><State>active</State></ReservedCacheNode></ReservedCacheNodes>` +
			`</DescribeReservedCacheNodesResult><ResponseMetadata><RequestId>fixture</RequestId></ResponseMetadata></DescribeReservedCacheNodesResponse>`},
	}
}

type csvPurchaseEnv struct {
	stub     *awsFixtureStub
	log      *bytes.Buffer
	denyHits *atomic.Int64
	dir      string
	run      func(report string) // buys the CSV rows against the stub, report written to the given name
}

// newCSVPurchaseEnv wires the stub with the RDS and ElastiCache offering and
// purchase fixtures. Account stays empty in the CSV; CallerAccountID is set
// because entering at runCSVPurchases skips withCallerAccount.
func newCSVPurchaseEnv(t *testing.T) *csvPurchaseEnv {
	t.Helper()
	return newCSVPurchaseEnvFor(t, csvPurchases, rdsFixtures(), cacheFixtures())
}

// newCSVPurchaseEnvFor is newCSVPurchaseEnv with its own CSV and fixtures.
func newCSVPurchaseEnvFor(t *testing.T, csv string, fixtureSets ...map[string]awsFixture) *csvPurchaseEnv {
	t.Helper()
	origCfg := toolCfg
	t.Cleanup(func() { toolCfg = origCfg })
	denyHits := isolateAWSEnv(t)
	t.Setenv("DISABLE_PURCHASE_DELAY", "true")

	awsCfg, stub := newAWSFixtureConfig(t)
	// purchasecfg.NewConfig swaps any other HTTPClient type for the real network.
	_, isStd := awsCfg.HTTPClient.(*http.Client)
	require.True(t, isStd, "the fixture transport must ride on *http.Client")
	for _, set := range fixtureSets {
		maps.Copy(stub.fixtures, set)
	}
	dir := t.TempDir()
	csvPath := writeTestRecommendationsCSV(t, csv)
	env := &csvPurchaseEnv{stub: stub, log: stub.log, denyHits: denyHits, dir: dir}
	env.run = func(report string) {
		recs, err := loadRecommendationsFromCSV(csvPath)
		require.NoError(t, err)
		cfg := Config{CallerAccountID: defaultTestAccount, CSVOutput: filepath.Join(dir, report), AuditLog: filepath.Join(dir, "audit.jsonl")}
		require.NoError(t, runCSVPurchases(context.Background(), awsCfg, recs, false, cfg, "run-"+report))
	}
	return env
}

func (e *csvPurchaseEnv) report(t *testing.T, name string) map[string][]string {
	t.Helper()
	header, rows := readPurchaseReport(t, filepath.Join(e.dir, name))
	cols := map[string][]string{}
	for _, col := range []string{"ResourceType", "CommitmentID", "Success", "Error"} {
		cols[col] = reportColumn(t, header, rows, col)
	}
	return cols
}

func (e *csvPurchaseEnv) auditStatuses(t *testing.T) []string {
	t.Helper()
	rows := auditRows(t, filepath.Join(e.dir, "audit.jsonl"))
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(row), &m))
		out = append(out, fmt.Sprint(m["status"]))
	}
	return out
}

func (e *csvPurchaseEnv) purchaseForm(t *testing.T, hostOp string) url.Values {
	t.Helper()
	e.stub.mu.Lock()
	defer e.stub.mu.Unlock()
	require.Len(t, e.stub.forms[hostOp], 1)
	return e.stub.forms[hostOp][0]
}

func firstRunRequests(rdsPurchases, cachePurchases int) map[string]int {
	return map[string]int{
		rdsHost + " DescribeReservedDBInstances":           1,
		rdsHost + " DescribeReservedDBInstancesOfferings":  1,
		rdsHost + " " + rdsPurchase:                        rdsPurchases,
		cacheHost + " DescribeReservedCacheNodes":          1,
		cacheHost + " DescribeReservedCacheNodesOfferings": 1,
		cacheHost + " " + cachePurch:                       cachePurchases,
	}
}

// TestAWSFixturesHaveNoPurchaseOps is a tripwire: the shared default fixtures
// must never answer a purchase call, so a test that forgets to install its own
// purchase fixture fails instead of "succeeding".
func TestAWSFixturesHaveNoPurchaseOps(t *testing.T) {
	for key := range awsFixtures {
		assert.NotContains(t, key, "Purchase")
	}
}

func TestCSVRealPurchase_RDSAndElastiCache(t *testing.T) {
	env := newCSVPurchaseEnv(t)
	env.run("report1.csv")

	rep := env.report(t, "report1.csv")
	assert.ElementsMatch(t, []string{"db.t3.small", "cache.t3.micro"}, rep["ResourceType"])
	assert.Equal(t, []string{"true", "true"}, rep["Success"])
	// The report id is the one AWS returned, not the generated id sent in the request.
	assert.ElementsMatch(t, []string{rdsRespID, cacheRespID}, rep["CommitmentID"])
	assert.Equal(t, []string{"", ""}, rep["Error"])

	rdsForm := env.purchaseForm(t, rdsHost+" "+rdsPurchase)
	assert.Equal(t, "rds-offering-1", rdsForm.Get("ReservedDBInstancesOfferingId"))
	assert.Equal(t, "2", rdsForm.Get("DBInstanceCount"))
	assert.NotEmpty(t, rdsForm.Get("ReservedDBInstanceId"))
	assert.NotEqual(t, rdsRespID, rdsForm.Get("ReservedDBInstanceId"))
	cacheForm := env.purchaseForm(t, cacheHost+" "+cachePurch)
	assert.Equal(t, "cache-offering-1", cacheForm.Get("ReservedCacheNodesOfferingId"))
	assert.Equal(t, "1", cacheForm.Get("CacheNodeCount"))
	assert.NotEmpty(t, cacheForm.Get("ReservedCacheNodeId"))
	assert.NotEqual(t, cacheRespID, cacheForm.Get("ReservedCacheNodeId"))

	// Purchase calls == Success=true rows, and no operation outside the six expected.
	env.stub.requireRequests(t, firstRunRequests(1, 1))
	assert.Equal(t, []string{"success", "success"}, env.auditStatuses(t))
	assert.Zero(t, env.denyHits.Load(), "nothing may reach the deny endpoint")
}

func TestCSVRealPurchase_FailedPurchaseCarriesError(t *testing.T) {
	env := newCSVPurchaseEnv(t)
	env.stub.fixtures["elasticache "+cachePurch] = queryErr("ReservedCacheNodeQuotaExceeded", "cache quota exceeded by fixture")
	env.run("report1.csv")

	rep := env.report(t, "report1.csv")
	for i, typ := range rep["ResourceType"] {
		switch typ {
		case "db.t3.small":
			assert.Equal(t, "true", rep["Success"][i])
			assert.Equal(t, rdsRespID, rep["CommitmentID"][i])
		case "cache.t3.micro":
			assert.Equal(t, "false", rep["Success"][i])
			assert.Contains(t, rep["Error"][i], "cache quota exceeded by fixture")
			assert.NotContains(t, rep["Error"][i], "account of the credentials is unknown", "must fail at the purchase, not the precondition")
			assert.NotEqual(t, cacheRespID, rep["CommitmentID"][i], "no AWS id exists for a failed purchase")
		default:
			t.Fatalf("unexpected report row %q", typ)
		}
	}
	// Both purchases were attempted; only the RDS one succeeded.
	env.stub.requireRequests(t, firstRunRequests(1, 1))
	assert.ElementsMatch(t, []string{"success", "error"}, env.auditStatuses(t))
	assert.Zero(t, env.denyHits.Load())
}

// TestCSVReplay_SecondRunBuysNothing runs the same CSV twice. Run 2 sees the
// RIs run 1 bought through the Describe fixture and must not buy again; the
// non-matching and failing variants prove the zero is caused by the match.
func TestCSVReplay_SecondRunBuysNothing(t *testing.T) {
	tests := []struct {
		name         string
		replay       map[string]awsFixture
		rdsBuys      int // purchases in run 2
		cacheBuys    int
		wantAdjusted string // dedup log line proving the rows were removed by the match
		wantRefused  bool
	}{
		{name: "matching RIs suppress the repurchase", replay: replayFixtures("db.t3.small", "cache.t3.micro"), wantAdjusted: "instances → 0 instances"},
		{name: "RIs of another class do not suppress it", replay: replayFixtures("db.m5.large", "cache.m5.large"), rdsBuys: 1, cacheBuys: 1},
		{name: "a failing check refuses to purchase", replay: map[string]awsFixture{
			"rds DescribeReservedDBInstances":        queryErr("AccessDenied", "describe denied by fixture"),
			"elasticache DescribeReservedCacheNodes": queryErr("AccessDenied", "describe denied by fixture"),
		}, wantRefused: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newCSVPurchaseEnv(t)
			env.run("report1.csv")
			env.stub.requireRequests(t, firstRunRequests(1, 1))

			for k, v := range tt.replay {
				env.stub.fixtures[k] = v
			}
			env.log.Reset()
			env.run("report2.csv")

			want := firstRunRequests(1+tt.rdsBuys, 1+tt.cacheBuys)
			want[rdsHost+" DescribeReservedDBInstances"] = 2
			want[cacheHost+" DescribeReservedCacheNodes"] = 2
			want[rdsHost+" DescribeReservedDBInstancesOfferings"] = 1 + tt.rdsBuys
			want[cacheHost+" DescribeReservedCacheNodesOfferings"] = 1 + tt.cacheBuys
			env.stub.requireRequests(t, want)

			logText := env.log.String()
			assert.NotContains(t, logText, "Could not check for existing RIs")
			assert.Equal(t, tt.wantRefused, strings.Contains(logText, "Refusing to purchase"))
			if tt.wantAdjusted != "" {
				assert.Contains(t, logText, tt.wantAdjusted, "the dedup must be what removed the rows")
			} else {
				assert.NotContains(t, logText, "Adjusted recommendations")
			}
			rows := tt.rdsBuys + tt.cacheBuys
			if rows == 0 {
				// Fully covered rows never reach the loop, so no report and no audit row.
				_, err := os.Stat(filepath.Join(env.dir, "report2.csv"))
				assert.ErrorIs(t, err, os.ErrNotExist)
			} else {
				assert.Equal(t, slices.Repeat([]string{"true"}, rows), env.report(t, "report2.csv")["Success"])
			}
			assert.Len(t, env.auditStatuses(t), 2+rows)
			assert.Zero(t, env.denyHits.Load())
		})
	}
}

// The dedupe key adds the "elasticache:" engine prefix only for ProviderAWS,
// so a CSV row must carry its provider or replays re-buy ElastiCache (#2228).
func TestLoadRecommendationsFromCSV_StampsAWSProvider(t *testing.T) {
	recs, err := loadRecommendationsFromCSV(writeTestRecommendationsCSV(t, csvPurchases))
	require.NoError(t, err)
	require.Len(t, recs, 2)
	for _, rec := range recs {
		assert.Equal(t, common.ProviderAWS, rec.Provider, rec.Service)
	}
}

const (
	memdbHost     = "memory-db.eu-west-1.amazonaws.com"
	memdbPurchase = "PurchaseReservedNodesOffering"
	memdbRespID   = "aws-ri-memdb-1"
)

const memdbCSVHeader = "Service,Region,ResourceType,Engine,Deployment,Count,Term,PaymentOption\n"

// memdbCSVWithEngine is the MemoryDB row with the given Engine cell.
func memdbCSVWithEngine(engine string) string {
	return memdbCSVHeader + "memorydb,eu-west-1,db.t4g.small," + engine + ",,3,1yr,all-upfront\n"
}

// memdbFixtures answers MemoryDB (awsJson1.1) with no existing node, one
// offering and a purchase; existing, when set, is the node a replay finds.
func memdbFixtures(existing bool) map[string]awsFixture {
	nodes := `[]`
	if existing {
		nodes = fmt.Sprintf(`[{"ReservationId":%q,"NodeType":"db.t4g.small","NodeCount":3,"Duration":31536000,"StartTime":%d,"State":"active"}]`,
			memdbRespID, time.Now().Unix())
	}
	return map[string]awsFixture{
		"memory-db DescribeReservedNodes": {body: `{"ReservedNodes":` + nodes + `}`},
		"memory-db DescribeReservedNodesOfferings": {body: `{"ReservedNodesOfferings":[{"ReservedNodesOfferingId":"memdb-offering-1",` +
			`"NodeType":"db.t4g.small","Duration":31536000,"OfferingType":"All Upfront"}]}`},
		"memory-db " + memdbPurchase: {body: `{"ReservedNode":{"ReservationId":"` + memdbRespID + `","NodeCount":3}}`},
	}
}

// The dedupe key of a CSV row only equals an existing node's ("redis") when
// Details carry that engine, whatever the CSV Engine cell says.
func TestCSVReplay_MemoryDB(t *testing.T) {
	for _, engine := range []string{"redis", "valkey", "Redis", ""} {
		t.Run("engine="+engine, func(t *testing.T) { testCSVReplayMemoryDB(t, memdbCSVWithEngine(engine)) })
	}
}

func testCSVReplayMemoryDB(t *testing.T, csv string) {
	env := newCSVPurchaseEnvFor(t, csv, memdbFixtures(false))
	env.run("report1.csv")
	assert.Equal(t, []string{"true"}, env.report(t, "report1.csv")["Success"])
	assert.Equal(t, []string{memdbRespID}, env.report(t, "report1.csv")["CommitmentID"])
	env.stub.requireRequests(t, map[string]int{
		memdbHost + " DescribeReservedNodes":          1,
		memdbHost + " DescribeReservedNodesOfferings": 1,
		memdbHost + " " + memdbPurchase:               1,
	})

	maps.Copy(env.stub.fixtures, memdbFixtures(true))
	env.log.Reset()
	env.run("report2.csv")
	env.stub.requireRequests(t, map[string]int{
		memdbHost + " DescribeReservedNodes":          2,
		memdbHost + " DescribeReservedNodesOfferings": 1,
		memdbHost + " " + memdbPurchase:               1,
	})
	assert.Contains(t, env.log.String(), "instances → 0 instances", "the dedup must be what removed the row")
	assert.NotContains(t, env.log.String(), "Could not check for existing RIs")
	_, err := os.Stat(filepath.Join(env.dir, "report2.csv"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.Zero(t, env.denyHits.Load())
}

func TestLoadRecommendationsFromCSV_MemoryDBEngineIsCanonical(t *testing.T) {
	for _, engine := range []string{"redis", "valkey", ""} {
		recs, err := loadRecommendationsFromCSV(writeTestRecommendationsCSV(t, memdbCSVWithEngine(engine)))
		require.NoError(t, err)
		require.Len(t, recs, 1)
		assert.Equal(t, &common.CacheDetails{Engine: "redis", NodeType: "db.t4g.small"}, recs[0].Details, "engine=%q", engine)
	}
}
