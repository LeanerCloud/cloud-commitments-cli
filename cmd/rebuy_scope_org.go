package main

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"github.com/aws/aws-sdk-go-v2/service/organizations/types"
)

// orgDescribeAPI is the slice of the Organizations client the role check uses.
type orgDescribeAPI interface {
	DescribeOrganization(ctx context.Context, in *organizations.DescribeOrganizationInput, optFns ...func(*organizations.Options)) (*organizations.DescribeOrganizationOutput, error)
}

// rebuyOrgClient is the seam tests replace so they never reach AWS.
var rebuyOrgClient = func(cfg aws.Config) orgDescribeAPI { return organizations.NewFromConfig(cfg) }

// orgRoleKind is the credentials' account's place in AWS Organizations.
type orgRoleKind int

const (
	orgRoleStandalone orgRoleKind = iota + 1
	orgRoleManagement
	orgRoleMember
)

// orgRole classifies the caller with organizations:DescribeOrganization.
// Only AWSOrganizationsNotInUseException means standalone; an empty answer,
// an access denial or any other error cannot prove the role and is an error.
func orgRole(ctx context.Context, client orgDescribeAPI, caller string) (orgRoleKind, error) {
	out, err := client.DescribeOrganization(ctx, &organizations.DescribeOrganizationInput{})
	if err != nil {
		var notInUse *types.AWSOrganizationsNotInUseException
		if errors.As(err, &notInUse) {
			return orgRoleStandalone, nil
		}
		return 0, err
	}
	if out == nil || out.Organization == nil || aws.ToString(out.Organization.MasterAccountId) == "" {
		return 0, errors.New("the organization answer has no management account")
	}
	if aws.ToString(out.Organization.MasterAccountId) == caller {
		return orgRoleManagement, nil
	}
	return orgRoleMember, nil
}

// requireNotMember refuses a gated run from a member account: replacements
// bought centrally are invisible to it, so its expiry netting would be wrong.
// Standalone and management callers pass on to the usage probe.
func requireNotMember(ctx context.Context, client orgDescribeAPI, caller string) error {
	role, err := orgRole(ctx, client, caller)
	if err != nil {
		return refuseRebuyScope("the scope check could not run (%w); fix access or retry, grant organizations:DescribeOrganization (read-only), or omit --rebuy-window-days", err)
	}
	if role == orgRoleMember {
		return refuseRebuyScope("account %s is a member of an AWS Organization: member accounts can't run gated rebuy safely (replacements bought centrally aren't visible to this account); gated rebuy only runs from a standalone account, or a management account whose member accounts have no in-scope usage; omit --rebuy-window-days", caller)
	}
	return nil
}
