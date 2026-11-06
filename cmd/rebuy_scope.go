package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/aws/aws-sdk-go-v2/aws"
)

// errRebuyScope marks a gated --rebuy-window-days run that cannot prove the
// credentials' account owns all the in-scope usage: either another account's
// usage is in scope, or the check itself could not run. Both refuse the run.
var errRebuyScope = errors.New("rebuy scope")

// rebuyScopeError carries the operator-facing text unchanged (the exit message
// supplies the "Cannot verify the rebuy scope" prefix) while matching
// errRebuyScope under errors.Is.
type rebuyScopeError struct{ err error }

func (e rebuyScopeError) Error() string        { return e.err.Error() }
func (e rebuyScopeError) Unwrap() error        { return e.err }
func (e rebuyScopeError) Is(target error) bool { return target == errRebuyScope }

func refuseRebuyScope(format string, args ...any) error {
	return rebuyScopeError{fmt.Errorf(format, args...)}
}

// rebuyScopeRemedy ends every refusal that names another account's usage.
const rebuyScopeRemedy = "gated rebuy needs a standalone account, or a management account whose member accounts have no in-scope usage; otherwise omit --rebuy-window-days"

// scopeCheckFailure is the refusal for a check that could not run, as opposed
// to one that ran and found another account's usage.
func scopeCheckFailure(cause error) error {
	return refuseRebuyScope("the scope check could not run (%w); fix access or retry, or omit --rebuy-window-days", cause)
}

// rebuyGated reports whether --rebuy-window-days is enforced for the service:
// it adjusts RI sizing only, and only with a coverage target.
func rebuyGated(cfg Config, service common.ServiceType) bool {
	return cfg.RebuyWindowDays > 0 && cfg.TargetCoverage > 0 && !common.IsSavingsPlan(service)
}

// rebuyScopeActive reports whether any selected service is gated.
func rebuyScopeActive(cfg Config, services []common.ServiceType) bool {
	for _, service := range services {
		if rebuyGated(cfg, service) {
			return true
		}
	}
	return false
}

// enforceRebuyScope resolves the credentials' account when the rebuy gate
// holds, refuses a member account, refuses if the scope probe finds usage in
// another account, and returns cfg carrying the account for the
// per-recommendation scope check. An unresolvable or empty account refuses.
func enforceRebuyScope(ctx context.Context, awsCfg aws.Config, cfg Config, services []common.ServiceType) (Config, error) {
	if !rebuyScopeActive(cfg, services) {
		return cfg, nil
	}
	account, err := callerAccountFetcher(ctx, awsCfg)
	if err != nil {
		return cfg, scopeCheckFailure(fmt.Errorf("cannot determine the AWS account of the credentials: %w", err))
	}
	out := cfg
	out.CallerAccountID = account
	if out.CallerAccountID == "" {
		return cfg, scopeCheckFailure(errors.New("the AWS account of the credentials is unknown"))
	}
	if orgErr := requireNotMember(ctx, rebuyOrgClient(awsCfg), out.CallerAccountID); orgErr != nil {
		return cfg, orgErr
	}
	if probeErr := probeRebuyScope(ctx, rebuyCoverageClient(awsCfg), out, services); probeErr != nil {
		return cfg, probeErr
	}
	return out, nil
}

// mustEnforceRebuyScope is enforceRebuyScope for the main pipeline; it exits
// through the same route as fetch errors.
func mustEnforceRebuyScope(ctx context.Context, awsCfg aws.Config, cfg Config, services []common.ServiceType) Config {
	out, err := enforceRebuyScope(ctx, awsCfg, cfg, services)
	if err != nil {
		exitIfInterrupted(ctx, "checking the rebuy scope")
		exitOnFetchError(err)
	}
	return out
}

// requireRecPreconditions runs the checks every fetched batch must pass
// before filtering: the scope check first, then the RDS inventory check.
func requireRecPreconditions(recs []common.Recommendation, cfg Config, data engineVersionData) error {
	if err := requireRebuyRecScope(recs, cfg); err != nil {
		return err
	}
	return requireRegionInventory(recs, cfg, data)
}

// requireRebuyRecScope refuses a gated run when an RI recommendation belongs to
// an account other than the credentials'. The existing-commitment inventory
// reads only the credentials' own account, so expiry netting against another
// account's demand would be wrong. A recommendation with no account passes
// here; the scope probe covers it.
func requireRebuyRecScope(recs []common.Recommendation, cfg Config) error {
	for i := range recs {
		if !rebuyGated(cfg, recs[i].Service) {
			continue
		}
		err := checkPurchaseAccount(recs[i], cfg.CallerAccountID)
		if err == nil {
			continue
		}
		if cfg.CallerAccountID == "" {
			return scopeCheckFailure(err)
		}
		return refuseRebuyScope("%s recommendation for account %s in %s, but the credentials are for account %s; %s",
			recs[i].Service, recs[i].Account, recs[i].Region, cfg.CallerAccountID, rebuyScopeRemedy)
	}
	return nil
}

// fetchRebuyInventory reads the owned commitments the sizing step deducts
// expiring RIs from. Under the gate a missing client or a failed read refuses:
// sizing without the inventory would silently skip the expiry netting.
// Savings Plans and a gate that is off keep the warning and continue with no
// inventory.
func fetchRebuyInventory(ctx context.Context, client provider.ServiceClient, service common.ServiceType, cfg Config) ([]common.Commitment, error) {
	if cfg.RebuyWindowDays <= 0 {
		return nil, nil
	}
	gated := rebuyGated(cfg, service)
	if client == nil {
		if gated {
			return nil, scopeCheckFailure(fmt.Errorf("no %s client to read existing commitments", service))
		}
		return nil, nil
	}
	commits, err := client.GetExistingCommitments(ctx)
	if err == nil {
		return commits, nil
	}
	if gated {
		return nil, scopeCheckFailure(fmt.Errorf("reading existing %s commitments: %w", service, err))
	}
	AppLogger.Printf("  ⚠️  Could not fetch existing commitments for expiry check: %v\n", err)
	return nil, nil
}
