package main

// Command-level tests for cli#2131: a gated rebuy run (--rebuy-window-days and
// --target-coverage, RI services) refuses, exit 1 and no CSV, when it cannot
// prove the credentials' account owns the in-scope usage. The caller account
// is 111111111111 (TestMain stubs STS in the child process). Every gated
// scenario answers DescribeOrganization as a management account (caller)
// unless it names another.
//
// Scenario dispositions (router answers are the proxy's existing ones):
//
//	file  scenario                              disposition
//	ec2   pool-demand                           reshape: 1 row (caller), average=demand=90, count 12 -> 36
//	ec2   outside-window                        reshape: 1 row, count 6 -> 18
//	ec2   replaced-inside-lookback              reshape: 1 row, count 6 -> 18
//	ec2   missing-demand-two-each-six-total     reshape + rename missing-demand-two-total: 1 row, count 2, warning "1 recommendations"
//	ec2   filtered-subset                       now refuses (c), renamed exclude-others-refuses: 3 rows, exclude accounts 2,3 (kills (c)-after-filter)
//	ec2   exact-boundary                        reshape: 1 row, average 30, count 4 -> 8
//	ec2   below-boundary                        reshape: 1 row, average 30, count 3 -> 7
//	ec2   inventory-error (new)                 refuses (d): 1 row, DescribeReservedInstances answers 403
//	rds   already-at-target                     keep (rec account 111)
//	rds   below-target                          keep
//	rds   large-ri-xlarge-usage                 keep
//	rds   multi-size-linked-accounts            now refuses (c): accounts 111/222/333
//	rds   partial-inventory                     now refuses (b): the only rec is the caller, the router reports account 999999999999
//	ec2   member-account-refuses (new)          refuses (e): 1 row, DescribeOrganization names another management account
//	ec2   foreign-usage-default-region (new)    refuses (b) in default region mode: 1 caller row, router reports 999999999999
//	rds   unusable-expiring-warned              keep
//	rds   control-outside-window                keep
//	rds   control-engine-isolation              keep
//	rds   rerun-replacement-bought              keep (rec account 111)
//	rds   control-replacement-outside-lookback  keep

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// orgDescribeKey is the request-count key of the organization role check.
const orgDescribeKey = "organizations.us-east-1.amazonaws.com/AWSOrganizationsV20161128.DescribeOrganization"

// describeOrganization answers the role check: the organization is managed by
// master, or by the caller (111111111111) when master is empty. It reports
// ok=false for any other operation. The caller holds p.mu.
func (p *completenessProxy) describeOrganization(host, op, master string) (string, bool) {
	if host != "organizations.us-east-1.amazonaws.com" || op != "AWSOrganizationsV20161128.DescribeOrganization" {
		return "", false
	}
	if master == "" {
		master = "111111111111"
	}
	return fmt.Sprintf(`{"Organization":{"Id":"o-fixture","MasterAccountId":%q}}`, master), true
}

func TestRebuyScopeRefusalCommand(t *testing.T) {
	for _, tc := range []struct {
		service, name, regionMode string
		wantText                  []string
		requests                  map[string]int
	}{
		{"ec2", "exclude-others-refuses", "explicit", []string{"Cannot verify the rebuy scope", "222222222222", "ec2"}, map[string]int{"ce.us-east-1.amazonaws.com/AWSInsightsIndexService.GetReservationPurchaseRecommendation": 1, "ce.us-east-1.amazonaws.com/AWSInsightsIndexService.GetReservationCoverage": 13, orgDescribeKey: 1, "organizations.us-east-1.amazonaws.com/AWSOrganizationsV20161128.DescribeAccount": 3, "ec2.us-east-1.amazonaws.com/DescribeInstanceTypes": 1}},
		{"ec2", "inventory-error", "explicit", []string{"Cannot verify the rebuy scope", "fixture denied reserved instances", "ec2"}, map[string]int{"ce.us-east-1.amazonaws.com/AWSInsightsIndexService.GetReservationPurchaseRecommendation": 1, "ce.us-east-1.amazonaws.com/AWSInsightsIndexService.GetReservationCoverage": 13, orgDescribeKey: 1, "organizations.us-east-1.amazonaws.com/AWSOrganizationsV20161128.DescribeAccount": 1, "ec2.us-east-1.amazonaws.com/DescribeInstanceTypes": 1, "ec2.us-east-1.amazonaws.com/DescribeReservedInstances": 1}},
		{rdsExpiryService, "multi-size-linked-accounts", "explicit", []string{"Cannot verify the rebuy scope", "222222222222", "rds"}, map[string]int{"ce.us-east-1.amazonaws.com/AWSInsightsIndexService.GetReservationPurchaseRecommendation": 1, "ce.us-east-1.amazonaws.com/AWSInsightsIndexService.GetReservationCoverage": 13, orgDescribeKey: 1, "organizations.us-east-1.amazonaws.com/AWSOrganizationsV20161128.DescribeAccount": 3}},
		// (b): the caller owns every rec row; the probe alone sees the foreign account.
		// The probe runs before everything else: no coverage-map request
		// (INSTANCE_TYPE), no recommendations, no DescribeRegions, no inventory.
		{rdsExpiryService, "partial-inventory", "explicit", []string{"Cannot verify the rebuy scope", "999999999999", "rds"}, map[string]int{
			orgDescribeKey: 1, "ce.us-east-1.amazonaws.com/AWSInsightsIndexService.GetReservationCoverage": 1}},
		{"ec2", "foreign-usage-default-region", "default", []string{"Cannot verify the rebuy scope", "999999999999", "ec2"}, map[string]int{
			orgDescribeKey: 1, "ce.us-east-1.amazonaws.com/AWSInsightsIndexService.GetReservationCoverage": 1}},
		// A member account is refused after the role check and before any Cost Explorer request.
		{"ec2", "member-account-refuses", "default", []string{"Cannot verify the rebuy scope", "member of an AWS Organization", "111111111111"}, map[string]int{
			orgDescribeKey: 1}},
	} {
		t.Run(tc.service+"/"+tc.name, func(t *testing.T) {
			fixture, output, stdout, stderr, runErr := startCompletenessChild(t, tc.service, tc.regionMode, tc.name)
			logs := stdout + stderr
			var exitErr *exec.ExitError
			require.ErrorAs(t, runErr, &exitErr, logs)
			require.Equal(t, 1, exitErr.ExitCode(), logs)
			for _, text := range tc.wantText {
				require.Contains(t, logs, text)
			}
			require.NoFileExists(t, output)
			fixture.handlers.Wait()
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			require.Equal(t, tc.requests, fixture.requests, "no other operation, including purchases, is permitted")
			wantProbes := tc.requests["ce.us-east-1.amazonaws.com/AWSInsightsIndexService.GetReservationCoverage"]
			require.Equal(t, wantProbes-len(fixture.expiryCoverage), fixture.linkedAccountCalls, "coverage requests are the probe plus the coverage map")
			if tc.name == "partial-inventory" || tc.name == "foreign-usage-default-region" {
				require.Empty(t, fixture.expiryCoverage, "the probe must refuse before the coverage map is built")
			}
		})
	}
}

// linkedAccountCoverage is the shared LINKED_ACCOUNT router: it answers the
// scope probe (GroupBy LINKED_ACCOUNT) and reports ok=false for any other
// coverage request. It pins the probe's request shape: the 30-day window,
// Metrics Hour, one page, the SERVICE filter of the run's service, and the
// REGION filter only in explicit region mode. The caller (111111111111) is
// always reported; foreign, when set, is reported too. The caller holds p.mu.
func (p *completenessProxy) linkedAccountCoverage(body []byte, ceService, foreign string) (string, bool) {
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
	if len(request.GroupBy) == 0 || request.GroupBy[0]["Key"] != "LINKED_ACCOUNT" {
		return "", false
	}
	p.linkedAccountCalls++
	require.Equal(p.t, []map[string]string{{"Type": "DIMENSION", "Key": "LINKED_ACCOUNT"}}, request.GroupBy)
	require.Equal(p.t, []string{"Hour"}, request.Metrics)
	require.Empty(p.t, request.NextPageToken)
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
	want := map[string][]string{"SERVICE": {ceService}}
	if p.regions == "explicit" {
		want["REGION"] = []string{"us-east-1"}
	}
	require.Equal(p.t, want, filters)
	groups := []any{map[string]any{"Attributes": map[string]string{"linkedAccount": "111111111111"}}}
	if foreign != "" {
		groups = append(groups, map[string]any{"Attributes": map[string]string{"linkedAccount": foreign}})
	}
	payload, err := json.Marshal(map[string]any{"CoveragesByTime": []any{map[string]any{"Groups": groups}}})
	require.NoError(p.t, err)
	return string(payload), true
}
