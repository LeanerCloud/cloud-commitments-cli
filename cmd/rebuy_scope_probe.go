package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/recommendations"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
)

// coverageAPI is the slice of the Cost Explorer client the scope probe uses.
type coverageAPI interface {
	GetReservationCoverage(ctx context.Context, in *costexplorer.GetReservationCoverageInput, optFns ...func(*costexplorer.Options)) (*costexplorer.GetReservationCoverageOutput, error)
}

// rebuyCoverageClient is the seam tests replace so they never reach AWS.
var rebuyCoverageClient = func(cfg aws.Config) coverageAPI { return costexplorer.NewFromConfig(cfg) }

// maxProbePages bounds the probe's paging; exceeding it refuses.
const maxProbePages = 100

// rebuyCoverageService maps an RI service to Cost Explorer's SERVICE dimension
// value. It mirrors the unexported constants in cloud-commitments-go
// providers/aws/recommendations/coverage.go, so the probe sees exactly the pool
// the coverage map sizes against. An unmapped service has no safe filter, so
// the probe refuses instead of widening it.
func rebuyCoverageService(service common.ServiceType) (string, bool) {
	switch service {
	case common.ServiceEC2:
		return "Amazon Elastic Compute Cloud - Compute", true
	case common.ServiceRDS:
		return "Amazon Relational Database Service", true
	case common.ServiceElastiCache:
		return "Amazon ElastiCache", true
	case common.ServiceOpenSearch:
		return "Amazon OpenSearch Service", true
	case common.ServiceRedshift:
		return "Amazon Redshift", true
	case common.ServiceMemoryDB:
		return "Amazon MemoryDB", true
	}
	return "", false
}

// probeRebuyScope runs the per-account usage probe for every gated service.
func probeRebuyScope(ctx context.Context, client coverageAPI, cfg Config, services []common.ServiceType) error {
	for _, service := range services {
		if !rebuyGated(cfg, service) {
			continue
		}
		if err := probeServiceAccounts(ctx, client, service, cfg); err != nil {
			return err
		}
	}
	return nil
}

// probeServiceAccounts asks Cost Explorer which accounts have usage in the
// same pool the recommendations and coverage map size (the longer of their
// two windows, same SERVICE and, when --regions
// is set, REGION filters), grouped by LINKED_ACCOUNT. It refuses on the first
// account other than the credentials'. Without --regions there is no REGION
// filter: that is broader, so it can only refuse more. A member account sees
// only itself and a standalone account sees no other accounts, so both pass.
func probeServiceAccounts(ctx context.Context, client coverageAPI, service common.ServiceType, cfg Config) error {
	ceService, ok := rebuyCoverageService(service)
	if !ok {
		return refuseRebuyScope("no Cost Explorer service mapping for %s, so its usage scope cannot be checked; omit --rebuy-window-days", service)
	}
	input, err := rebuyProbeInput(ceService, cfg)
	if err != nil {
		return err
	}
	for range maxProbePages {
		out, err := client.GetReservationCoverage(ctx, input)
		if err != nil {
			return probeCallFailure(err)
		}
		if groupErr := requireOwnAccountGroups(out, service, cfg.CallerAccountID); groupErr != nil {
			return groupErr
		}
		if aws.ToString(out.NextPageToken) == "" {
			return nil
		}
		next := *input
		next.NextPageToken = out.NextPageToken
		input = &next
	}
	return refuseRebuyScope("the scope check could not run (more than %d pages of %s usage groups); omit --rebuy-window-days", maxProbePages, service)
}

// recLookbackDays maps --rec-lookback-period to days. An empty value is the
// pipeline's default; any other unrecognized value cannot be sized, so the
// probe refuses rather than guess a window.
func recLookbackDays(period string) (int, error) {
	if period == "" {
		period = recommendations.DefaultRecLookbackPeriod
	}
	switch period {
	case "7d":
		return 7, nil
	case "30d":
		return 30, nil
	case "60d":
		return 60, nil
	}
	return 0, refuseRebuyScope("cannot read --rec-lookback-period %q, so the scope window is unknown; use 7d, 30d or 60d, or omit --rebuy-window-days", period)
}

// rebuyProbeInput builds the probe request: the larger of the coverage and
// recommendation lookback windows, so an account that contributed usage to
// either the recommendations or the coverage demand is seen (Metrics "Hour" as
// the coverage map), SERVICE and optional REGION filters.
// Cost Explorer rejects an And with fewer than two operands, so SERVICE alone
// is a bare dimension, as in cloud-commitments-go's CE filter builders.
func rebuyProbeInput(ceService string, cfg Config) (*costexplorer.GetReservationCoverageInput, error) {
	recDays, err := recLookbackDays(cfg.RecLookbackPeriod)
	if err != nil {
		return nil, err
	}
	end := time.Now().UTC()
	start := end.AddDate(0, 0, -max(coverageLookbackDays(cfg), recDays))
	filter := &types.Expression{Dimensions: &types.DimensionValues{Key: types.DimensionService, Values: []string{ceService}}}
	if len(cfg.Regions) > 0 {
		filter = &types.Expression{And: []types.Expression{
			*filter,
			{Dimensions: &types.DimensionValues{Key: types.DimensionRegion, Values: cfg.Regions}},
		}}
	}
	return &costexplorer.GetReservationCoverageInput{
		TimePeriod: &types.DateInterval{Start: aws.String(start.Format(time.DateOnly)), End: aws.String(end.Format(time.DateOnly))},
		GroupBy:    []types.GroupDefinition{{Type: types.GroupDefinitionTypeDimension, Key: aws.String(string(types.DimensionLinkedAccount))}},
		Filter:     filter,
		Metrics:    []string{"Hour"},
	}, nil
}

// requireOwnAccountGroups refuses when a page names an account other than the
// caller, or a group whose account cannot be read.
func requireOwnAccountGroups(out *costexplorer.GetReservationCoverageOutput, service common.ServiceType, caller string) error {
	for _, byTime := range out.CoveragesByTime {
		for _, group := range byTime.Groups {
			account, err := groupAccount(group.Attributes)
			if err != nil {
				return scopeCheckFailure(err)
			}
			if account != caller {
				return refuseRebuyScope("account %s has in-scope %s usage, but the credentials are for account %s; %s", account, service, caller, rebuyScopeRemedy)
			}
		}
	}
	return nil
}

// groupAccount reads the linked account from a coverage group's attributes.
// Cost Explorer returns attribute keys in camelCase ("linkedAccount") although
// GroupBy takes LINKED_ACCOUNT, so keys compare with underscores removed and
// case folded; the value is used exactly as returned. A missing or empty
// value, or two keys that normalize alike, cannot be trusted and fail closed.
// Source of truth: the AWS Cost Explorer API reference for
// ReservationCoverageGroup (Attributes); no real-account response verified.
func groupAccount(attrs map[string]string) (string, error) {
	var account string
	matches := 0
	for key, value := range attrs {
		if strings.ToLower(strings.ReplaceAll(key, "_", "")) == "linkedaccount" {
			account = value
			matches++
		}
	}
	switch {
	case matches == 0:
		return "", errors.New("a coverage group has no linked-account attribute")
	case matches > 1:
		return "", errors.New("a coverage group has duplicate linked-account attributes")
	case account == "":
		return "", errors.New("a coverage group has an empty linked-account attribute")
	}
	return account, nil
}

// probeCallFailure is the refusal for a failed Cost Explorer call. An access
// denial adds the usual fix.
func probeCallFailure(err error) error {
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) && coded.ErrorCode() == "AccessDeniedException" {
		return refuseRebuyScope("the scope check could not run (%w); fix access or retry, grant ce:GetReservationCoverage or enable Cost Explorer access for this account, or omit --rebuy-window-days", err)
	}
	return scopeCheckFailure(err)
}
