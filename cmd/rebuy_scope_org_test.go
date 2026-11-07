package main

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeOrg answers DescribeOrganization from a fixed output or error.
type fakeOrg struct {
	out   *organizations.DescribeOrganizationOutput
	err   error
	calls int
}

func (f *fakeOrg) DescribeOrganization(context.Context, *organizations.DescribeOrganizationInput, ...func(*organizations.Options)) (*organizations.DescribeOrganizationOutput, error) {
	f.calls++
	return f.out, f.err
}

func orgOf(master *string) *fakeOrg {
	return &fakeOrg{out: &organizations.DescribeOrganizationOutput{Organization: &types.Organization{MasterAccountId: master}}}
}

// managementOrg is an organization whose management account is the given one.
func managementOrg(account string) *fakeOrg { return orgOf(aws.String(account)) }

// stubOrg replaces the Organizations seam for one test.
func stubOrg(t *testing.T, fake *fakeOrg) {
	t.Helper()
	orig := rebuyOrgClient
	t.Cleanup(func() { rebuyOrgClient = orig })
	rebuyOrgClient = func(aws.Config) orgDescribeAPI { return fake }
}

func TestOrgRole(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		fake    *fakeOrg
		want    orgRoleKind
		wantErr string
	}{
		"standalone":         {&fakeOrg{err: &types.AWSOrganizationsNotInUseException{}}, orgRoleStandalone, ""},
		"management":         {managementOrg("111111111111"), orgRoleManagement, ""},
		"member":             {managementOrg("999999999999"), orgRoleMember, ""},
		"access denied":      {&fakeOrg{err: &types.AccessDeniedException{}}, 0, "AccessDeniedException"},
		"other error":        {&fakeOrg{err: errors.New("throttled")}, 0, "throttled"},
		"nil organization":   {&fakeOrg{out: &organizations.DescribeOrganizationOutput{}}, 0, "no management account"},
		"empty management":   {orgOf(aws.String("")), 0, "no management account"},
		"missing management": {orgOf(nil), 0, "no management account"},
		"not-in-use by text": {&fakeOrg{err: errors.New("AWSOrganizationsNotInUseException")}, 0, "AWSOrganizationsNotInUseException"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := orgRole(ctx, tc.fake, "111111111111")
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRequireNotMember(t *testing.T) {
	ctx := context.Background()
	t.Run("standalone and management pass", func(t *testing.T) {
		require.NoError(t, requireNotMember(ctx, &fakeOrg{err: &types.AWSOrganizationsNotInUseException{}}, "111111111111"))
		require.NoError(t, requireNotMember(ctx, managementOrg("111111111111"), "111111111111"))
	})
	t.Run("member refuses with the scope text", func(t *testing.T) {
		err := requireNotMember(ctx, managementOrg("999999999999"), "111111111111")
		require.ErrorIs(t, err, errRebuyScope)
		assert.Equal(t, "account 111111111111 is a member of an AWS Organization: member accounts can't run gated rebuy safely (replacements bought centrally aren't visible to this account); gated rebuy only runs from a standalone account, or a management account whose member accounts have no in-scope usage; omit --rebuy-window-days", err.Error())
	})
	for name, fake := range map[string]*fakeOrg{
		"access denied": {err: &types.AccessDeniedException{}},
		"throttled":     {err: errors.New("throttled")},
		"empty answer":  {out: &organizations.DescribeOrganizationOutput{}},
	} {
		t.Run(name+" is a check failure", func(t *testing.T) {
			err := requireNotMember(ctx, fake, "111111111111")
			require.ErrorIs(t, err, errRebuyScope)
			assert.Contains(t, err.Error(), "the scope check could not run (")
			assert.Contains(t, err.Error(), "fix access or retry, grant organizations:DescribeOrganization (read-only), or omit --rebuy-window-days")
		})
	}
}

func TestEnforceRebuyScopeOrg(t *testing.T) {
	run := func(t *testing.T, org *fakeOrg, probe *fakeCoverage) error {
		t.Helper()
		stubCaller(t, "111111111111", nil)
		stubOrg(t, org)
		stubProbe(t, probe)
		_, err := enforceRebuyScope(context.Background(), aws.Config{}, gatedCfg(), riServices)
		return err
	}
	t.Run("member refuses before the probe", func(t *testing.T) {
		org := managementOrg("999999999999")
		err := run(t, org, &fakeCoverage{t: t, noCalls: true})
		require.ErrorIs(t, err, errRebuyScope)
		assert.Contains(t, err.Error(), "member of an AWS Organization")
		assert.Equal(t, 1, org.calls)
	})
	t.Run("an org error refuses before the probe", func(t *testing.T) {
		err := run(t, &fakeOrg{err: &types.AccessDeniedException{}}, &fakeCoverage{t: t, noCalls: true})
		require.ErrorIs(t, err, errRebuyScope)
		assert.Contains(t, err.Error(), "organizations:DescribeOrganization")
	})
	t.Run("management proceeds to the probe", func(t *testing.T) {
		probe := &fakeCoverage{t: t}
		require.NoError(t, run(t, managementOrg("111111111111"), probe))
		assert.Len(t, probe.inputs, 1)
	})
	t.Run("standalone proceeds to the probe", func(t *testing.T) {
		probe := &fakeCoverage{t: t}
		require.NoError(t, run(t, &fakeOrg{err: &types.AWSOrganizationsNotInUseException{}}, probe))
		assert.Len(t, probe.inputs, 1)
	})
	t.Run("standalone with a foreign linked account still refuses", func(t *testing.T) {
		probe := &fakeCoverage{t: t, pages: []*costexplorer.GetReservationCoverageOutput{groupsPage("", "111111111111", "222222222222")}}
		err := run(t, &fakeOrg{err: &types.AWSOrganizationsNotInUseException{}}, probe)
		require.ErrorIs(t, err, errRebuyScope)
		assert.Contains(t, err.Error(), "222222222222")
	})
}
