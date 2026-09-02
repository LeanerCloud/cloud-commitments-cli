package tools

import (
	"context"
	"fmt"

	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/LeanerCloud/CUDly/pkg/provider"
)

const awsEC2RIPurchaseName = "cudly_aws_ec2_ri_purchase"

const awsEC2RIPurchaseTitle = "Purchase AWS EC2 Reserved Instances"

var awsEC2RIPurchaseAnnotations = purchaseAnnotations(awsEC2RIPurchaseTitle)

const awsEC2RIPurchaseDescription = "Purchase AWS EC2 Reserved Instances. THIS SPENDS REAL MONEY when " +
	"dry_run=false and confirm=true. Always call with dry_run=true first (the default) to validate your " +
	"parameters before committing; a dry_run response never contacts AWS and never spends money. Search first " +
	"with cudly_search_recommendations to find a region/instance_type/count worth reserving."

// ec2RIPurchaseArgs is the input schema for cudly_aws_ec2_ri_purchase. term
// and payment_option map onto common.Recommendation.Term/PaymentOption;
// platform/tenancy/scope map onto common.ComputeDetails, which
// providers/aws/services/ec2/client.go:401-424 requires (Platform empty is a
// hard error there) -- they default to the overwhelmingly common case
// (on-demand Linux, shared tenancy, region-scoped) but are always visible in
// the schema and can be overridden per call.
type ec2RIPurchaseArgs struct {
	Region           string `json:"region" jsonschema:"AWS region, e.g. us-east-1"`
	InstanceType     string `json:"instance_type" jsonschema:"EC2 instance type, e.g. m5.large"`
	Count            int    `json:"count" jsonschema:"number of instances to reserve, must be > 0"`
	TermYears        int    `json:"term_years" jsonschema:"commitment length in years"`
	PaymentOption    string `json:"payment_option" jsonschema:"payment schedule"`
	Platform         string `json:"platform,omitempty" jsonschema:"RI product description (operating system); defaults to Linux/UNIX"`
	Tenancy          string `json:"tenancy,omitempty" jsonschema:"instance tenancy; defaults to default (shared)"`
	Scope            string `json:"scope,omitempty" jsonschema:"region or availability-zone; defaults to region"`
	AWSProfile       string `json:"aws_profile,omitempty" jsonschema:"AWS named profile override (~/.aws/config); default uses ambient credentials"`
	DryRun           *bool  `json:"dry_run,omitempty" jsonschema:"preview only, no purchase; defaults to true"`
	Confirm          *bool  `json:"confirm,omitempty" jsonschema:"required (with dry_run=false) to execute a real purchase; defaults to false"`
	IdempotencyNonce string `json:"idempotency_nonce,omitempty" jsonschema:"optional; set to a fresh value to authorize a purchase that is otherwise identical to a previous one (e.g. buy 3 more RIs with the same parameters); leave empty (the default) so retries with identical parameters dedupe and never double-buy"`
}

type awsEC2RIPurchaseTool struct {
	createProvider func(name string, cfg *provider.ProviderConfig) (provider.Provider, error)
}

// NewAWSEC2RIPurchaseTool builds the cudly_aws_ec2_ri_purchase tool.
func NewAWSEC2RIPurchaseTool() Registration {
	return &awsEC2RIPurchaseTool{createProvider: provider.CreateProvider}
}

func (t *awsEC2RIPurchaseTool) Descriptor() Descriptor {
	return Descriptor{
		Name:                awsEC2RIPurchaseName,
		Provider:            "aws",
		Product:             "ec2",
		Action:              "ri_purchase",
		Description:         awsEC2RIPurchaseDescription,
		Annotations:         awsEC2RIPurchaseAnnotations,
		RealPurchaseEnabled: true,
		ExamplePrompts: []string{
			"Preview buying 3 m5.large 3-year no-upfront RIs in us-east-1",
			"Buy 2 r6g.large Reserved Instances in eu-west-1 for real, 1-year all-upfront",
		},
	}
}

func (t *awsEC2RIPurchaseTool) Register(s *mcp.Server) error {
	schema, err := BuildInputSchema[ec2RIPurchaseArgs](map[string]FieldOverride{
		"term_years":     {Enum: []any{int(TermOneYear), int(TermThreeYear)}},
		"payment_option": {Enum: []any{string(PaymentOptionAllUpfront), string(PaymentOptionPartialUpfront), string(PaymentOptionNoUpfront)}},
		"scope":          {Enum: []any{string(ScopeRegion), string(ScopeAvailabilityZone)}, Default: string(ScopeRegion)},
		"tenancy":        {Enum: []any{string(TenancyDefault), string(TenancyDedicated)}, Default: string(TenancyDefault)},
		"dry_run":        {Default: true},
		"confirm":        {Default: false},
	})
	if err != nil {
		return err
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        awsEC2RIPurchaseName,
		Description: awsEC2RIPurchaseDescription,
		Annotations: awsEC2RIPurchaseAnnotations,
		InputSchema: schema,
	}, t.handle)
	return nil
}

func (t *awsEC2RIPurchaseTool) handle(ctx context.Context, _ *mcp.CallToolRequest, args ec2RIPurchaseArgs) (*mcp.CallToolResult, PurchaseResponse, error) {
	rec, region, dryRun, confirm, err := ec2RecommendationFromArgs(args)
	if err != nil {
		return nil, PurchaseResponse{}, err
	}

	resp, err := ExecutePurchase(ctx, PurchaseRequest{
		Region:          region,
		Recommendation:  rec,
		DryRun:          dryRun,
		Confirm:         confirm,
		ResolveClient:   t.resolveClient(args, region),
		Nonce:           args.IdempotencyNonce,
		CredentialScope: requestCredentialScope(args.AWSProfile, dryRun, "AWS_PROFILE"),
	})
	if err != nil {
		return nil, PurchaseResponse{}, err
	}
	return nil, *resp, nil
}

// ec2ComputeDimensions holds the validated, possibly-defaulted EC2 RI
// dimensions that do not vary by resource/count/term -- split out of
// ec2RecommendationFromArgs to keep that function under the pre-commit
// gocyclo threshold.
type ec2ComputeDimensions struct {
	platform ec2types.RIProductDescription
	tenancy  Tenancy
	scope    Scope
}

// resolveEC2ComputeDimensions validates the optional platform/tenancy/scope
// fields, applying their documented defaults (Linux/UNIX, default tenancy,
// region scope) when the caller omits them.
func resolveEC2ComputeDimensions(args ec2RIPurchaseArgs) (ec2ComputeDimensions, error) {
	dims := ec2ComputeDimensions{
		platform: ec2types.RIProductDescriptionLinuxUnix,
		tenancy:  TenancyDefault,
		scope:    ScopeRegion,
	}
	var err error
	if args.Platform != "" {
		if dims.platform, err = ValidatePlatform(args.Platform); err != nil {
			return ec2ComputeDimensions{}, err
		}
	}
	if args.Tenancy != "" {
		if dims.tenancy, err = ValidateTenancy(args.Tenancy); err != nil {
			return ec2ComputeDimensions{}, err
		}
	}
	if args.Scope != "" {
		if dims.scope, err = ValidateScope(args.Scope); err != nil {
			return ec2ComputeDimensions{}, err
		}
	}
	return dims, nil
}

// ec2RecommendationFromArgs validates every field of args and builds the
// common.Recommendation to purchase, the effective region (trimmed of any
// surrounding whitespace), and the effective dry_run/confirm booleans.
func ec2RecommendationFromArgs(args ec2RIPurchaseArgs) (rec common.Recommendation, region string, dryRun, confirm bool, err error) {
	region, err = requireNonBlank("region", args.Region)
	if err != nil {
		return common.Recommendation{}, "", false, false, err
	}
	instanceType, err := requireNonBlank("instance_type", args.InstanceType)
	if err != nil {
		return common.Recommendation{}, "", false, false, err
	}
	if args.Count <= 0 {
		return common.Recommendation{}, "", false, false, fmt.Errorf("count must be > 0, got %d", args.Count)
	}
	term, err := ValidateTermYears(args.TermYears)
	if err != nil {
		return common.Recommendation{}, "", false, false, err
	}
	paymentOption, err := ValidatePaymentOption(args.PaymentOption)
	if err != nil {
		return common.Recommendation{}, "", false, false, err
	}
	dims, err := resolveEC2ComputeDimensions(args)
	if err != nil {
		return common.Recommendation{}, "", false, false, err
	}

	rec = common.Recommendation{
		Provider:       common.ProviderAWS,
		Service:        common.ServiceEC2,
		Region:         region,
		ResourceType:   instanceType,
		Count:          args.Count,
		CommitmentType: common.CommitmentReservedInstance,
		Term:           term.RecommendationTerm(),
		PaymentOption:  string(paymentOption),
		Details: &common.ComputeDetails{
			InstanceType: instanceType,
			Platform:     string(dims.platform),
			Tenancy:      string(dims.tenancy),
			Scope:        string(dims.scope),
		},
	}

	dryRun, confirm = ResolveDryRunConfirm(args.DryRun, args.Confirm)
	return rec, region, dryRun, confirm, nil
}

// resolveClient returns the ResolveClientFunc that ExecutePurchase invokes
// only for a real purchase, so provider/credential resolution is deferred
// until after the dry_run/confirm gate has already decided to execute.
// region is the effective, already-validated-and-trimmed region returned by
// ec2RecommendationFromArgs -- not args.Region -- so a real purchase never
// resolves the provider/service client against a raw, un-trimmed value.
func (t *awsEC2RIPurchaseTool) resolveClient(args ec2RIPurchaseArgs, region string) ResolveClientFunc {
	return func(ctx context.Context) (provider.ServiceClient, error) {
		cfg := &provider.ProviderConfig{Name: string(common.ProviderAWS), AWSProfile: CredentialScope(args.AWSProfile), Region: region}
		prov, err := t.createProvider(string(common.ProviderAWS), cfg)
		if err != nil {
			return nil, err
		}
		return prov.GetServiceClient(ctx, common.ServiceEC2, region)
	}
}
