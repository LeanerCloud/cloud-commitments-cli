package main

// Command-level regression tests for cli#2132: with --target-coverage and
// --rebuy-window-days, RDS family-NU sizing must deduct RIs that expire inside
// the window from the family's existing coverage. The harness drives the real
// root command against a recorded-HTTP proxy (Cost Explorer, RDS and
// Organizations responses are synthetic fixtures), dry-run only: no purchase
// operation is permitted.
//
// Every scenario below that is not labeled "control" fails on the commit
// before the fix (the family was sized against the un-adjusted coverage).

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const rdsExpiryService = "rds-expiry"

// rdsExpiryUsage is one Cost Explorer coverage row: average running instances
// per hour of one class and the share of those hours covered by RIs.
type rdsExpiryUsage struct {
	engine, class string
	avg, pct      float64
}

// rdsExpiryRI is one DescribeReservedDBInstances entry.
type rdsExpiryRI struct {
	class, engine string
	multiAZ       bool
	count         int
	endDays       int
	// startedDaysAgo overrides the derived start (one year before endDays).
	startedDaysAgo int
}

// rdsExpiryRec is one Cost Explorer purchase recommendation row (one linked
// account, one class). The fixture prices every recommended instance at 100
// upfront, 100 monthly savings and 10 recurring, so costs scale with count.
type rdsExpiryRec struct {
	account int
	class   string
	count   int
}

type rdsExpiryScenario struct {
	usage []rdsExpiryUsage
	ris   []rdsExpiryRI
	recs  []rdsExpiryRec
	// want is the expected Count per rec after sizing (0 = the rec is absent
	// from the CSV). existing is the expected family ExistingCoverage column
	// for the surviving rows. unusable is the expected number of expiring RIs
	// reported as not applied to family sizing.
	want     []int
	existing float64
	unusable int
	// logLine, when set, must appear in the run log.
	logLine string
	// absentLogLine, when set, must NOT appear in the run log.
	absentLogLine string
	// parent documents the count the commit before the fix produced.
	parent []int
}

const rdsExpiryTarget = 80

var rdsExpiryAccountIDs = func() []string {
	ids := make([]string, 4)
	for i := range ids {
		ids[i] = strings.Repeat(strconv.Itoa(i+1), 12)
	}
	return ids
}()

func rdsExpiryCase(name string) rdsExpiryScenario {
	mysql := func(class string, avg, pct float64) rdsExpiryUsage {
		return rdsExpiryUsage{engine: "MySQL", class: class, avg: avg, pct: pct}
	}
	ri := func(class string, count, endDays int) rdsExpiryRI {
		return rdsExpiryRI{class: class, engine: "mysql", count: count, endDays: endDays}
	}
	one := func(class string, count int) []rdsExpiryRec { return []rdsExpiryRec{{0, class, count}} }
	switch name {
	case "already-at-target":
		// 80% covered by 16 large RIs, 3 of them expiring: the family looks at
		// target, so the parent drops it. After expiry it is 65% covered and
		// the 15% gap (12 NU) buys exactly the 3 expiring RIs back.
		return rdsExpiryScenario{
			usage: []rdsExpiryUsage{mysql("db.r6g.large", 20, 80)},
			ris:   []rdsExpiryRI{ri("db.r6g.large", 13, 200), ri("db.r6g.large", 3, 15)},
			recs:  one("db.r6g.large", 8), want: []int{3}, existing: 65, parent: []int{0},
		}
	case "below-target":
		// 40% covered by 8 large RIs, 2 expiring: post-expiry 30%, so the gap
		// is 50% (40 NU) = 10 large instead of the parent's 8.
		return rdsExpiryScenario{
			usage: []rdsExpiryUsage{mysql("db.r6g.large", 20, 40)},
			ris:   []rdsExpiryRI{ri("db.r6g.large", 6, 200), ri("db.r6g.large", 2, 15)},
			recs:  one("db.r6g.large", 8), want: []int{10}, existing: 30, parent: []int{8},
		}
	case "large-ri-xlarge-usage":
		// Cost Explorer reports coverage under the running size (xlarge) while
		// the expiring RIs are large: 4 large = 16 NU expire, so the family
		// drops from 80% to 60% and buys 2 xlarge (16 NU).
		return rdsExpiryScenario{
			usage: []rdsExpiryUsage{mysql("db.r6g.xlarge", 10, 80)},
			ris:   []rdsExpiryRI{ri("db.r6g.large", 12, 200), ri("db.r6g.large", 4, 15)},
			recs:  one("db.r6g.xlarge", 4), want: []int{2}, existing: 60, parent: []int{0},
		}
	case "multi-size-linked-accounts":
		// Two sizes and three linked-account rows in one family: the 16 expiring
		// NU are deducted once (family 40% -> 20%), so the 60% gap (48 NU) splits
		// 2 + 4 + 2. A double deduction would buy 2 + 5 + 2; no deduction, 1 + 2 + 1.
		return rdsExpiryScenario{
			usage: []rdsExpiryUsage{mysql("db.r6g.xlarge", 5, 40), mysql("db.r6g.large", 10, 40)},
			ris:   []rdsExpiryRI{ri("db.r6g.large", 4, 200), ri("db.r6g.large", 4, 15)},
			recs: []rdsExpiryRec{
				{0, "db.r6g.xlarge", 4}, {1, "db.r6g.large", 8}, {2, "db.r6g.xlarge", 4},
			},
			want: []int{2, 4, 2}, existing: 20, parent: []int{1, 2, 1},
		}
	case "partial-inventory":
		// Cost Explorer covers 64 NU org-wide but the single-account inventory
		// lists only the 4 expiring large RIs (16 NU); the other accounts' RIs
		// are missing. The deduction is capped at the 16 expiring NU (80% ->
		// 60%, buy 4 large), never the whole supplied-vs-covered difference.
		return rdsExpiryScenario{
			usage: []rdsExpiryUsage{mysql("db.r6g.large", 20, 80)},
			ris:   []rdsExpiryRI{ri("db.r6g.large", 4, 15)},
			recs:  one("db.r6g.large", 8), want: []int{4}, existing: 60, parent: []int{0},
		}
	case "unusable-expiring-warned":
		// Expiring RIs the family pass cannot use (multi-AZ with no coverage
		// family, and an unparsable class) must be reported, not read as "no
		// expiry". Counts are unchanged from the parent; only the warning is new.
		return rdsExpiryScenario{
			usage: []rdsExpiryUsage{mysql("db.r6g.large", 20, 40)},
			ris: []rdsExpiryRI{
				ri("db.r6g.large", 8, 200),
				{class: "db.r6g.large", engine: "mysql", multiAZ: true, count: 2, endDays: 15},
				ri("db.t3.weird", 1, 15),
			},
			recs: one("db.r6g.large", 8), want: []int{8}, existing: 40, unusable: 2, parent: []int{8},
		}
	case "control-outside-window":
		return rdsExpiryScenario{
			usage: []rdsExpiryUsage{mysql("db.r6g.large", 20, 40)},
			ris:   []rdsExpiryRI{ri("db.r6g.large", 8, 180)},
			recs:  one("db.r6g.large", 8), want: []int{8}, existing: 40, parent: []int{8},
		}
	case "control-engine-isolation":
		// The expiring RIs belong to a PostgreSQL family; the MySQL family must
		// not be touched.
		return rdsExpiryScenario{
			usage: []rdsExpiryUsage{mysql("db.r6g.large", 20, 40), {engine: "PostgreSQL", class: "db.r6g.large", avg: 20, pct: 80}},
			ris:   []rdsExpiryRI{{class: "db.r6g.large", engine: "postgres", count: 16, endDays: 15}},
			recs:  one("db.r6g.large", 8), want: []int{8}, existing: 40, parent: []int{8},
		}
	case "rerun-replacement-bought":
		// Re-run after the CLI already bought the replacements (cli#2175 gate F1).
		// Lagged coverage is 82.5% (66 NU): 12 large owned by another account (not
		// in the inventory), the 4 expiring, and part of the 4 replacements bought
		// 2 days ago (outside the 24h idempotency window). True post-expiry
		// coverage is 12 + 4 replacements = 16 large = 64 NU = 80%, so nothing is
		// bought. Before the lookback-aware call the lagged coverage lost the 16
		// expiring NU and 3 large were bought again.
		return rdsExpiryScenario{
			usage: []rdsExpiryUsage{mysql("db.r6g.large", 20, 82.5)},
			ris: []rdsExpiryRI{
				ri("db.r6g.large", 4, 15),
				{class: "db.r6g.large", engine: "mysql", count: 4, endDays: 363, startedDaysAgo: 2},
			},
			recs: one("db.r6g.large", 8), want: []int{0}, parent: []int{3},
			// Since go#533 ExpiringNU is the deduction actually applied. The
			// replacements cancel the expiring RIs here, so it is 0 and the
			// "considered against family coverage" line must not be printed.
			absentLogLine: "normalized units of RIs expiring within 30 days considered against family coverage",
		}
	case "control-replacement-outside-lookback":
		// Same inventory but the replacements started 45 days ago, outside the
		// 30-day coverage lookback, so they are already inside the coverage
		// figure and must not be credited again: 66 - 16 = 50 NU (62.5%), the
		// 17.5% gap (14 NU) buys 3 large.
		return rdsExpiryScenario{
			usage: []rdsExpiryUsage{mysql("db.r6g.large", 20, 82.5)},
			ris: []rdsExpiryRI{
				ri("db.r6g.large", 4, 15),
				{class: "db.r6g.large", engine: "mysql", count: 4, endDays: 320, startedDaysAgo: 45},
			},
			recs: one("db.r6g.large", 8), want: []int{3}, existing: 62.5, parent: []int{3},
		}
	}
	panic("unknown rds expiry scenario: " + name)
}

func TestRDSFamilyExpiryCommand(t *testing.T) {
	for _, name := range []string{
		"already-at-target", "below-target", "large-ri-xlarge-usage", "multi-size-linked-accounts",
		"partial-inventory", "unusable-expiring-warned", "control-outside-window", "control-engine-isolation",
		"rerun-replacement-bought", "control-replacement-outside-lookback",
	} {
		t.Run(name, func(t *testing.T) { runCompletenessScenario(t, rdsExpiryService, "explicit", name) })
	}
}

func rdsExpiryArgs(string) []string {
	return []string{"--payment", "partial-upfront", "--target-coverage", strconv.Itoa(rdsExpiryTarget), "--rebuy-window-days", "30"}
}

func assertRDSExpiryCSV(t *testing.T, output, name, logs string) {
	t.Helper()
	s := rdsExpiryCase(name)
	if s.logLine != "" {
		require.Contains(t, logs, s.logLine)
	}
	if s.absentLogLine != "" {
		require.NotContains(t, logs, s.absentLogLine)
	}
	data, err := os.ReadFile(output)
	if countNonZero(s.want) == 0 {
		require.ErrorIs(t, err, os.ErrNotExist, "nothing to buy must write no CSV; logs=%s", logs)
		require.Contains(t, logs, "0 recommendations selected", logs)
		return
	}
	require.NoError(t, err, logs)
	rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	require.NoError(t, err)
	t.Logf("actual synthetic RDS expiry CSV scenario=%s rows=%v", name, rows)
	for _, failure := range []string{"Failed to fetch", "incomplete AWS", "Could not fetch", "failed to fetch", "WARNING:", "catalog fetch"} {
		require.NotContains(t, logs, failure)
	}
	if s.unusable > 0 {
		require.Contains(t, logs, fmt.Sprintf("%d RDS reserved instances expiring within 30 days could not be applied to family sizing", s.unusable), logs)
	} else {
		require.NotContains(t, logs, "could not be applied to family sizing", logs)
	}
	byAccount := make(map[string]map[string]string)
	total := 0
	for _, row := range rows[1 : len(rows)-1] {
		columns := make(map[string]string)
		for i, header := range rows[0] {
			columns[header] = row[i]
		}
		require.NotContains(t, byAccount, columns["Account"], "duplicate account row")
		byAccount[columns["Account"]] = columns
	}
	for i, rec := range s.recs {
		columns, present := byAccount[rdsExpiryAccountIDs[rec.account]]
		count := s.want[i]
		if count == 0 {
			require.False(t, present, "scenario=%s rec %d must be dropped (parent behavior); rows=%v", name, i, rows)
			continue
		}
		require.True(t, present, "scenario=%s rec %d missing; rows=%v\nlogs=%s", name, i, rows, logs)
		wantSuccess, wantErr := wantPurchaseOutcome(columns["Account"])
		want := map[string]string{
			"Service": "rds", "Region": "us-east-1", "ResourceType": rec.class, "Term": "1yr", "PaymentOption": "partial-upfront",
			"Count": strconv.Itoa(count), "ExistingCoverage": fmt.Sprintf("%.1f", s.existing),
			"UpfrontPayment": fmt.Sprintf("%.2f", float64(count)*100), "EstimatedSavings": fmt.Sprintf("%.2f", float64(count)*100),
			"RecurringMonthlyCost": fmt.Sprintf("%.2f", float64(count)*10), "Success": wantSuccess, "Error": wantErr,
		}
		for field, value := range want {
			require.Equal(t, value, columns[field], "scenario=%s rec=%d field=%s (parent count %d)", name, i, field, s.parent[i])
		}
		total += count
	}
	require.Len(t, byAccount, countNonZero(s.want), "unexpected extra rows: %v", rows)
	if total > 0 {
		last := rows[len(rows)-1]
		require.Equal(t, "TOTAL", last[0])
		for i, header := range rows[0] {
			switch header {
			case "Count":
				require.Equal(t, strconv.Itoa(total), last[i])
			case "UpfrontPayment", "EstimatedSavings":
				require.Equal(t, fmt.Sprintf("%.2f", float64(total)*100), last[i])
			case "RecurringMonthlyCost":
				require.Equal(t, fmt.Sprintf("%.2f", float64(total)*10), last[i])
			}
		}
	}
}

func countNonZero(values []int) int {
	n := 0
	for _, v := range values {
		if v != 0 {
			n++
		}
	}
	return n
}

func (p *completenessProxy) respondRDSExpiry(req *http.Request, body []byte) *http.Response {
	p.mu.Lock()
	defer p.mu.Unlock()
	op := req.Header.Get("X-Amz-Target")
	values, err := url.ParseQuery(string(body))
	if op == "" {
		require.NoError(p.t, err)
		op = values.Get("Action")
	}
	p.requests[req.Host+"/"+op]++
	if req.Method != http.MethodPost || req.URL.Path != "/" || req.URL.RawQuery != "" {
		p.t.Errorf("unexpected rds expiry request %s %s", req.Method, req.URL)
	}
	status, contentType, payload := p.rdsExpiryResponse(req.Host, op, body, values)
	return &http.Response{StatusCode: status, ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Content-Type": []string{contentType}},
		Body:   io.NopCloser(strings.NewReader(payload)), ContentLength: int64(len(payload))}
}

func (p *completenessProxy) rdsExpiryResponse(host, op string, body []byte, values url.Values) (int, string, string) {
	s := rdsExpiryCase(p.details)
	switch {
	case host == "ce.us-east-1.amazonaws.com" && op == "AWSInsightsIndexService.GetReservationPurchaseRecommendation":
		var request map[string]any
		require.NoError(p.t, json.Unmarshal(body, &request))
		require.Equal(p.t, "Amazon Relational Database Service", request["Service"])
		details := make([]map[string]any, 0, len(s.recs))
		for _, rec := range s.recs {
			details = append(details, map[string]any{
				"AccountId":                              rdsExpiryAccountIDs[rec.account],
				"RecommendedNumberOfInstancesToPurchase": strconv.Itoa(rec.count), "AverageNumberOfInstancesUsedPerHour": "1",
				"UpfrontCost": strconv.Itoa(rec.count * 100), "EstimatedMonthlyOnDemandCost": strconv.Itoa(rec.count * 200),
				"EstimatedMonthlySavingsAmount": strconv.Itoa(rec.count * 100), "RecurringStandardMonthlyCost": strconv.Itoa(rec.count * 10),
				"InstanceDetails": map[string]any{"RDSInstanceDetails": map[string]string{
					"InstanceType": rec.class, "Region": "us-east-1", "DatabaseEngine": "MySQL", "DeploymentOption": "Single-AZ",
				}},
			})
		}
		encoded, err := json.Marshal(map[string]any{"Recommendations": []any{map[string]any{"RecommendationDetails": details}}})
		require.NoError(p.t, err)
		return 200, "application/x-amz-json-1.1", string(encoded)
	case host == "ce.us-east-1.amazonaws.com" && op == "AWSInsightsIndexService.GetReservationCoverage":
		return 200, "application/x-amz-json-1.1", p.rdsExpiryCoverage(s, body)
	case host == "organizations.us-east-1.amazonaws.com" && op == "AWSOrganizationsV20161128.DescribeAccount":
		var request map[string]string
		require.NoError(p.t, json.Unmarshal(body, &request))
		require.Contains(p.t, rdsExpiryAccountIDs, request["AccountId"])
		p.expiryAccounts = append(p.expiryAccounts, request["AccountId"])
		return 200, "application/x-amz-json-1.1", fmt.Sprintf(`{"Account":{"Id":%q,"Name":"rds-expiry-account"}}`, request["AccountId"])
	case host == "rds.us-east-1.amazonaws.com" && op == "DescribeReservedDBInstances":
		require.Equal(p.t, "2014-10-31", values.Get("Version"))
		return 200, "text/xml", rdsExpiryInventory(s)
	}
	p.t.Errorf("unexpected rds expiry operation %s %s", host, op)
	return 403, "text/plain", "fixture rejected operation"
}

func rdsExpiryInventory(s rdsExpiryScenario) string {
	var items strings.Builder
	for i, ri := range s.ris {
		// A one-year reservation ending endDays from now.
		start := time.Now().AddDate(0, 0, ri.endDays-365).UTC().Format(time.RFC3339)
		if ri.startedDaysAgo > 0 {
			start = time.Now().AddDate(0, 0, -ri.startedDaysAgo).UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(&items, `<ReservedDBInstance><ReservedDBInstanceId>rds-expiry-%d</ReservedDBInstanceId><DBInstanceClass>%s</DBInstanceClass><ProductDescription>%s</ProductDescription><MultiAZ>%t</MultiAZ><DBInstanceCount>%d</DBInstanceCount><State>active</State><StartTime>%s</StartTime><Duration>31536000</Duration></ReservedDBInstance>`,
			i, ri.class, ri.engine, ri.multiAZ, ri.count, start)
	}
	return `<DescribeReservedDBInstancesResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><DescribeReservedDBInstancesResult><ReservedDBInstances>` +
		items.String() + `</ReservedDBInstances></DescribeReservedDBInstancesResult></DescribeReservedDBInstancesResponse>`
}

func (p *completenessProxy) rdsExpiryCoverage(s rdsExpiryScenario, body []byte) string {
	var request struct {
		GroupBy []map[string]string
		Filter  struct {
			And []struct {
				Dimensions struct {
					Key    string
					Values []string
				}
			}
		}
	}
	require.NoError(p.t, json.Unmarshal(body, &request))
	filters := make(map[string][]string)
	for _, expression := range request.Filter.And {
		filters[expression.Dimensions.Key] = expression.Dimensions.Values
	}
	require.Len(p.t, filters["SERVICE"], 1)
	service, engine := filters["SERVICE"][0], ""
	key := service
	if service == "Amazon Relational Database Service" {
		require.Len(p.t, filters["DATABASE_ENGINE"], 1)
		engine = filters["DATABASE_ENGINE"][0]
		key += "/" + engine
	}
	p.expiryCoverage = append(p.expiryCoverage, key)
	if engine == "" {
		return `{"CoveragesByTime":[]}`
	}
	var groups []any
	for _, usage := range s.usage {
		if usage.engine != engine {
			continue
		}
		groups = append(groups, map[string]any{
			"Attributes": map[string]string{"instanceType": usage.class, "deploymentOption": "Single-AZ"},
			"Coverage": map[string]any{"CoverageHours": map[string]string{
				"CoverageHoursPercentage": strconv.FormatFloat(usage.pct, 'g', -1, 64),
				"TotalRunningHours":       strconv.FormatFloat(usage.avg*720, 'g', -1, 64),
			}},
		})
	}
	if groups == nil {
		return `{"CoveragesByTime":[]}`
	}
	payload, err := json.Marshal(map[string]any{"CoveragesByTime": []any{map[string]any{"Groups": groups}}})
	require.NoError(p.t, err)
	return string(payload)
}

func (p *completenessProxy) assertRDSExpiryRequests(t *testing.T) {
	t.Helper()
	p.handlers.Wait()
	for op, n := range p.requests {
		require.NotContains(t, op, "PurchaseReservedDBInstancesOffering", "dry run must not purchase")
		t.Logf("rds expiry op %s x%d", op, n)
	}
	require.Equal(t, 1, p.requests["ce.us-east-1.amazonaws.com/AWSInsightsIndexService.GetReservationPurchaseRecommendation"])
	require.Equal(t, 12, p.requests["ce.us-east-1.amazonaws.com/AWSInsightsIndexService.GetReservationCoverage"])
	require.Positive(t, p.requests["rds.us-east-1.amazonaws.com/DescribeReservedDBInstances"])
}
