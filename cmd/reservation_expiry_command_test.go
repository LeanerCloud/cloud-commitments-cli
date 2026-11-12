package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type reservationExpiryScenario struct {
	rows, expiry, endDays, count      int
	average, demand, target, existing float64
	missing, filtered                 bool
}

func reservationExpiryCase(name string) reservationExpiryScenario {
	s := reservationExpiryScenario{rows: 3, expiry: 18, endDays: 15, count: 12, average: 30, demand: 90, target: 80, existing: 40}
	switch name {
	case "pool-demand":
	case "outside-window":
		s.endDays, s.count, s.existing = 180, 6, 60
	case "missing-demand-two-each-six-total":
		s.average, s.demand, s.count, s.existing, s.missing = 10, 0, 2, 60, true
	case "filtered-subset":
		s.count, s.filtered = 18, true
	case "exact-boundary", "below-boundary":
		s.rows, s.expiry, s.count, s.average, s.demand, s.existing = 2, 2, 4, 15, 30, 160.0/3
		if name == "below-boundary" {
			s.target, s.count = math.Nextafter(80, 0), 3
		}
	default:
		panic("unknown expiry scenario: " + name)
	}
	return s
}

func TestReservationExpiryCommand(t *testing.T) {
	for _, name := range []string{"pool-demand", "outside-window", "missing-demand-two-each-six-total", "filtered-subset", "exact-boundary", "below-boundary"} {
		t.Run(name, func(t *testing.T) { runCompletenessScenario(t, "ec2", "explicit", name) })
	}
}

func reservationExpiryArgs(name string) []string {
	s := reservationExpiryCase(name)
	args := []string{"--payment", "partial-upfront", "--target-coverage", strconv.FormatFloat(s.target, 'g', -1, 64), "--rebuy-window-days", "30"}
	if s.filtered {
		args = append(args, "--exclude-accounts", "expiry-account-3")
	}
	return args
}

func assertReservationExpiryCSV(t *testing.T, output, name, logs string) {
	t.Helper()
	s := reservationExpiryCase(name)
	data, err := os.ReadFile(output)
	require.NoError(t, err, logs)
	rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	require.NoError(t, err)
	t.Logf("actual synthetic expiry CSV scenario=%s rows=%v", name, rows)
	warnings := 0
	if s.missing {
		warnings = 1
		require.Contains(t, logs, "3 recommendations because pool demand was unavailable; coverage was left unchanged")
	}
	require.Equal(t, warnings, strings.Count(logs, "Skipped expiry adjustment for"), logs)
	for _, failure := range []string{"Failed to fetch", "incomplete AWS", "Could not fetch", "failed to fetch", "WARNING:", "no signal", "catalog fetch"} {
		require.NotContains(t, logs, failure)
	}
	count := s.rows
	avg := s.average
	if s.filtered {
		count--
		avg = s.demand / float64(count)
	}
	require.Len(t, rows, count+2, logs)
	require.NotContains(t, rows[0], "ExistingCoveragePercentExact")
	seen := make(map[string]bool)
	for _, row := range rows[1 : len(rows)-1] {
		columns := make(map[string]string)
		for i, header := range rows[0] {
			columns[header] = row[i]
		}
		account := columns["Account"]
		require.False(t, seen[account], "duplicate account %s", account)
		seen[account] = true
		index := slices.Index([]string{"111111111111", "222222222222", "333333333333"}[:count], account)
		require.NotEqual(t, -1, index, "unexpected account %s", account)
		want := map[string]string{
			"Service": "ec2", "Region": "us-east-1", "ResourceType": "m5.large", "Term": "1yr", "PaymentOption": "partial-upfront",
			"Count": strconv.Itoa(s.count), "RecommendedCount": "30", "AccountName": fmt.Sprintf("expiry-account-%d", index+1),
			"Instances": fmt.Sprintf("%.1f", avg), "CoveredInstances": fmt.Sprintf("%.1f", avg*s.existing/100),
			"ExistingCoverage": fmt.Sprintf("%.1f", s.existing), "ProjectedCoverage": fmt.Sprintf("%.1f", s.existing+float64(s.count)*100/avg),
			"UpfrontPayment": fmt.Sprintf("%.2f", float64(s.count)*100), "EstimatedSavings": fmt.Sprintf("%.2f", float64(s.count)*100),
			"RecurringMonthlyCost": fmt.Sprintf("%.2f", float64(s.count)*10), "Success": "true", "Error": "",
		}
		for field, value := range want {
			require.Equal(t, value, columns[field], "scenario=%s account=%s field=%s", name, account, field)
		}
	}
	total := make(map[string]string)
	for i, header := range rows[0] {
		total[header] = rows[len(rows)-1][i]
	}
	require.Equal(t, "TOTAL", rows[len(rows)-1][0])
	require.Equal(t, strconv.Itoa(count*s.count), total["Count"])
	for _, field := range []string{"UpfrontPayment", "EstimatedSavings"} {
		require.Equal(t, fmt.Sprintf("%.2f", float64(count*s.count)*100), total[field])
	}
	require.Equal(t, fmt.Sprintf("%.2f", float64(count*s.count)*10), total["RecurringMonthlyCost"])
}

func (p *completenessProxy) respondReservationExpiry(req *http.Request, body []byte) *http.Response {
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
		p.t.Errorf("unexpected expiry request %s %s", req.Method, req.URL)
	}
	status, contentType, payload := p.reservationExpiryResponse(req.Host, op, body, values)
	return &http.Response{StatusCode: status, ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Content-Type": []string{contentType}},
		Body:   io.NopCloser(strings.NewReader(payload)), ContentLength: int64(len(payload))}
}

func (p *completenessProxy) reservationExpiryResponse(host, op string, body []byte, values url.Values) (int, string, string) {
	s := reservationExpiryCase(p.details)
	if host == "ce.us-east-1.amazonaws.com" {
		switch op {
		case "AWSInsightsIndexService.GetReservationPurchaseRecommendation":
			var request map[string]any
			require.NoError(p.t, json.Unmarshal(body, &request))
			require.Equal(p.t, map[string]any{"Service": "Amazon Elastic Compute Cloud - Compute", "AccountScope": "LINKED", "TermInYears": "ONE_YEAR", "PaymentOption": "PARTIAL_UPFRONT", "LookbackPeriodInDays": "SEVEN_DAYS"}, request)
			var details []map[string]any
			for i := 0; i < s.rows; i++ {
				details = append(details, map[string]any{
					"AccountId":                              []string{"111111111111", "222222222222", "333333333333"}[i],
					"RecommendedNumberOfInstancesToPurchase": "30", "AverageNumberOfInstancesUsedPerHour": strconv.FormatFloat(s.average, 'g', -1, 64),
					"UpfrontCost": "3000", "EstimatedMonthlyOnDemandCost": "6000", "EstimatedMonthlySavingsAmount": "3000", "RecurringStandardMonthlyCost": "300",
					"InstanceDetails": map[string]any{"EC2InstanceDetails": map[string]string{"InstanceType": "m5.large", "Region": "us-east-1", "Platform": "Linux/UNIX", "Tenancy": "Shared"}},
				})
			}
			encoded, err := json.Marshal(map[string]any{"Recommendations": []any{map[string]any{"RecommendationDetails": details}}})
			require.NoError(p.t, err)
			return 200, "application/x-amz-json-1.1", string(encoded)
		case "AWSInsightsIndexService.GetReservationCoverage":
			return 200, "application/x-amz-json-1.1", p.reservationExpiryCoverage(body)
		}
	}
	if host == "organizations.us-east-1.amazonaws.com" && op == "AWSOrganizationsV20161128.DescribeAccount" {
		var request map[string]string
		require.NoError(p.t, json.Unmarshal(body, &request))
		index := slices.Index([]string{"111111111111", "222222222222", "333333333333"}[:s.rows], request["AccountId"])
		require.NotEqual(p.t, -1, index)
		require.Len(p.t, request, 1)
		p.expiryAccounts = append(p.expiryAccounts, request["AccountId"])
		return 200, "application/x-amz-json-1.1", fmt.Sprintf(`{"Account":{"Id":%q,"Name":%q}}`, request["AccountId"], fmt.Sprintf("expiry-account-%d", index+1))
	}
	if host == "ec2.us-east-1.amazonaws.com" {
		want := url.Values{"Action": {op}, "Version": {"2016-11-15"}}
		if op == "DescribeReservedInstances" {
			want.Set("Filter.1.Name", "state")
			for i, state := range []string{"active", "payment-pending", "queued"} {
				want.Set(fmt.Sprintf("Filter.1.Value.%d", i+1), state)
			}
		}
		require.Equal(p.t, want, values)
		switch op {
		case "DescribeRegions":
			return 200, "text/xml", `<DescribeRegionsResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><regionInfo><item><regionName>us-east-1</regionName></item></regionInfo></DescribeRegionsResponse>`
		case "DescribeInstanceTypes":
			return 200, "text/xml", `<DescribeInstanceTypesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><instanceTypeSet><item><instanceType>m5.large</instanceType><vCpuInfo><defaultVCpus>2</defaultVCpus></vCpuInfo><memoryInfo><sizeInMiB>8192</sizeInMiB></memoryInfo></item></instanceTypeSet></DescribeInstanceTypesResponse>`
		case "DescribeReservedInstances":
			return 200, "text/xml", fmt.Sprintf(`<DescribeReservedInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><reservedInstancesSet><item><reservedInstancesId>expiry-fixture</reservedInstancesId><instanceType>m5.large</instanceType><instanceCount>%d</instanceCount><state>active</state><start>%s</start><end>%s</end></item></reservedInstancesSet></DescribeReservedInstancesResponse>`, s.expiry, time.Now().AddDate(-1, 0, 0).UTC().Format(time.RFC3339), time.Now().AddDate(0, 0, s.endDays).UTC().Format(time.RFC3339))
		}
	}
	if host == "rds.us-east-1.amazonaws.com" && (op == "DescribeDBInstances" || op == "DescribeDBMajorEngineVersions") {
		want := url.Values{"Action": {op}, "Version": {"2014-10-31"}}
		element := "DBInstances"
		if op == "DescribeDBMajorEngineVersions" {
			engine := values.Get("Engine")
			want.Set("Engine", engine)
			p.engines = append(p.engines, engine)
			element = "DBMajorEngineVersions"
		}
		require.Equal(p.t, want, values)
		return 200, "text/xml", fmt.Sprintf(`<%sResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><%sResult><%s/></%sResult></%sResponse>`, op, op, element, op, op)
	}
	p.t.Errorf("unexpected expiry operation %s %s", host, op)
	return 403, "text/plain", "fixture rejected operation"
}

func (p *completenessProxy) reservationExpiryCoverage(body []byte) string {
	var request struct {
		TimePeriod map[string]string
		GroupBy    []map[string]string
		Filter     struct {
			And []struct {
				Dimensions struct {
					Key    string
					Values []string
				}
			}
		}
		Metrics       []string
		NextPageToken string
	}
	require.NoError(p.t, json.Unmarshal(body, &request))
	require.Empty(p.t, request.NextPageToken)
	require.Equal(p.t, []string{"Hour"}, request.Metrics)
	start, err := time.Parse(time.DateOnly, request.TimePeriod["Start"])
	require.NoError(p.t, err)
	end, err := time.Parse(time.DateOnly, request.TimePeriod["End"])
	require.NoError(p.t, err)
	require.Equal(p.t, 30*24*time.Hour, end.Sub(start))
	filters := make(map[string][]string)
	for _, expression := range request.Filter.And {
		require.NotContains(p.t, filters, expression.Dimensions.Key)
		filters[expression.Dimensions.Key] = expression.Dimensions.Values
	}
	require.Equal(p.t, []string{"us-east-1"}, filters["REGION"])
	require.Len(p.t, filters["SERVICE"], 1)
	service := filters["SERVICE"][0]
	key := service
	groups := []map[string]string{{"Type": "DIMENSION", "Key": "INSTANCE_TYPE"}}
	if service == "Amazon Relational Database Service" {
		require.Len(p.t, filters, 3)
		require.Len(p.t, filters["DATABASE_ENGINE"], 1)
		key += "/" + filters["DATABASE_ENGINE"][0]
		groups = append(groups, map[string]string{"Type": "DIMENSION", "Key": "DEPLOYMENT_OPTION"})
	} else {
		require.Len(p.t, filters, 2)
	}
	require.Equal(p.t, groups, request.GroupBy)
	p.expiryCoverage = append(p.expiryCoverage, key)
	if service != "Amazon Elastic Compute Cloud - Compute" {
		return `{"CoveragesByTime":[]}`
	}
	s := reservationExpiryCase(p.details)
	hours := map[string]string{"CoverageHoursPercentage": "60"}
	if !s.missing {
		hours["TotalRunningHours"] = strconv.FormatFloat(s.demand*720, 'g', -1, 64)
	}
	payload, err := json.Marshal(map[string]any{"CoveragesByTime": []any{map[string]any{"Groups": []any{map[string]any{
		"Attributes": map[string]string{"instanceType": "m5.large"}, "Coverage": map[string]any{"CoverageHours": hours},
	}}}}})
	require.NoError(p.t, err)
	return string(payload)
}

func (p *completenessProxy) assertReservationExpiryRequests(t *testing.T) {
	t.Helper()
	s := reservationExpiryCase(p.details)
	want := map[string]int{
		"ce.us-east-1.amazonaws.com/AWSInsightsIndexService.GetReservationPurchaseRecommendation": 1,
		"ce.us-east-1.amazonaws.com/AWSInsightsIndexService.GetReservationCoverage":               12,
		"organizations.us-east-1.amazonaws.com/AWSOrganizationsV20161128.DescribeAccount":         s.rows,
		"ec2.us-east-1.amazonaws.com/DescribeRegions":                                             1, "ec2.us-east-1.amazonaws.com/DescribeInstanceTypes": 1,
		"ec2.us-east-1.amazonaws.com/DescribeReservedInstances": 2,
		"rds.us-east-1.amazonaws.com/DescribeDBInstances":       1, "rds.us-east-1.amazonaws.com/DescribeDBMajorEngineVersions": 4,
	}
	require.Equal(t, want, p.requests, "no other operation, including purchases, is permitted")
	wantCoverage := make([]string, 0, 12)
	wantCoverage = append(wantCoverage, "Amazon Elastic Compute Cloud - Compute", "Amazon ElastiCache", "Amazon OpenSearch Service", "Amazon Redshift", "Amazon MemoryDB")
	for _, engine := range []string{"MySQL", "PostgreSQL", "MariaDB", "Oracle", "SQL Server", "Aurora MySQL", "Aurora PostgreSQL"} {
		wantCoverage = append(wantCoverage, "Amazon Relational Database Service/"+engine)
	}
	require.ElementsMatch(t, wantCoverage, p.expiryCoverage)
	require.ElementsMatch(t, []string{"111111111111", "222222222222", "333333333333"}[:s.rows], p.expiryAccounts)
	require.ElementsMatch(t, []string{"mysql", "postgres", "aurora-mysql", "aurora-postgresql"}, p.engines)
	t.Logf("actual root command, SDK and CSV; synthetic expiry operations=%v coverage=%v accounts=%v", p.requests, p.expiryCoverage, p.expiryAccounts)
}
