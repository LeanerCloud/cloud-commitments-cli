package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/LeanerCloud/CUDly/pkg/provider"
)

const awsRDSRIPurchaseName = "cudly_aws_rds_ri_purchase"

const awsRDSRIPurchaseTitle = "Purchase AWS RDS Reserved Instances"

var awsRDSRIPurchaseAnnotations = purchaseAnnotations(awsRDSRIPurchaseTitle)

const awsRDSRIPurchaseDescription = "Purchase AWS RDS Reserved Instances. THIS SPENDS REAL MONEY when " +
	"dry_run=false and confirm=true. Always call with dry_run=true first (the default) to validate your " +
	"parameters before committing; a dry_run response never contacts AWS and never spends money."

// rdsRIPurchaseArgs is the input schema for cudly_aws_rds_ri_purchase.
// engine and az_config map onto common.DatabaseDetails, which
// providers/aws/services/rds/client.go:301-322 requires -- az_config in
// particular has no safe default (single-AZ and multi-AZ RIs have different
// prices and do not cover each other's demand), so unlike EC2's
// platform/tenancy/scope it is a required field here, not a defaulted one.
type rdsRIPurchaseArgs struct {
	Region           string `json:"region" jsonschema:"AWS region, e.g. us-east-1"`
	InstanceClass    string `json:"instance_class" jsonschema:"RDS DB instance class, e.g. db.r6g.large"`
	Count            int    `json:"count" jsonschema:"number of instances to reserve, must be > 0"`
	TermYears        int    `json:"term_years" jsonschema:"commitment length in years"`
	PaymentOption    string `json:"payment_option" jsonschema:"payment schedule"`
	Engine           string `json:"engine" jsonschema:"RDS database engine, e.g. mysql, postgres, mariadb, oracle-se2, sqlserver-ee"`
	AZConfig         string `json:"az_config" jsonschema:"single-az or multi-az; must match the recommendation exactly (different price, no cross-coverage)"`
	AWSProfile       string `json:"aws_profile,omitempty" jsonschema:"AWS named profile override (~/.aws/config); default uses ambient credentials"`
	DryRun           *bool  `json:"dry_run,omitempty" jsonschema:"preview only, no purchase; defaults to true"`
	Confirm          *bool  `json:"confirm,omitempty" jsonschema:"required (with dry_run=false) to execute a real purchase; defaults to false"`
	IdempotencyNonce string `json:"idempotency_nonce,omitempty" jsonschema:"optional; set to a fresh value to authorize a purchase that is otherwise identical to a previous one (e.g. buy 3 more RIs with the same parameters); leave empty (the default) so retries with identical parameters dedupe and never double-buy"`
}

type awsRDSRIPurchaseTool struct {
	createProvider func(name string, cfg *provider.ProviderConfig) (provider.Provider, error)
}

// NewAWSRDSRIPurchaseTool builds the cudly_aws_rds_ri_purchase tool.
func NewAWSRDSRIPurchaseTool() Registration {
	return &awsRDSRIPurchaseTool{createProvider: provider.CreateProvider}
}

func (t *awsRDSRIPurchaseTool) Descriptor() Descriptor {
	return Descriptor{
		Name:                awsRDSRIPurchaseName,
		Provider:            "aws",
		Product:             "rds",
		Action:              "ri_purchase",
		Description:         awsRDSRIPurchaseDescription,
		Annotations:         awsRDSRIPurchaseAnnotations,
		RealPurchaseEnabled: true,
		ExamplePrompts: []string{
			"Preview buying 2 db.r6g.large multi-az postgres RDS RIs in us-east-1 for 3 years",
			"Buy an RDS Reserved Instance for a single-az mysql db.t3.medium in eu-west-1 for real",
		},
	}
}

func (t *awsRDSRIPurchaseTool) Register(s *mcp.Server) error {
	schema, err := BuildInputSchema[rdsRIPurchaseArgs](map[string]FieldOverride{
		"term_years":     {Enum: []any{int(TermOneYear), int(TermThreeYear)}},
		"payment_option": {Enum: []any{string(PaymentOptionAllUpfront), string(PaymentOptionPartialUpfront), string(PaymentOptionNoUpfront)}},
		"az_config":      {Enum: []any{string(AZConfigSingleAZ), string(AZConfigMultiAZ)}},
		"dry_run":        {Default: true},
		"confirm":        {Default: false},
	})
	if err != nil {
		return err
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        awsRDSRIPurchaseName,
		Description: awsRDSRIPurchaseDescription,
		Annotations: awsRDSRIPurchaseAnnotations,
		InputSchema: schema,
	}, t.handle)
	return nil
}

func (t *awsRDSRIPurchaseTool) handle(ctx context.Context, _ *mcp.CallToolRequest, args rdsRIPurchaseArgs) (*mcp.CallToolResult, PurchaseResponse, error) {
	rec, region, dryRun, confirm, err := rdsRecommendationFromArgs(args)
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

// rdsRecommendationFromArgs validates args and builds the
// common.Recommendation to purchase, the effective region (trimmed of any
// surrounding whitespace), and the effective dry_run/confirm booleans.
func rdsRecommendationFromArgs(args rdsRIPurchaseArgs) (rec common.Recommendation, region string, dryRun, confirm bool, err error) {
	region, err = requireNonBlank("region", args.Region)
	if err != nil {
		return common.Recommendation{}, "", false, false, err
	}
	instanceClass, err := requireNonBlank("instance_class", args.InstanceClass)
	if err != nil {
		return common.Recommendation{}, "", false, false, err
	}
	if args.Count <= 0 {
		return common.Recommendation{}, "", false, false, fmt.Errorf("count must be > 0, got %d", args.Count)
	}
	engine, err := requireNonBlank("engine", args.Engine)
	if err != nil {
		return common.Recommendation{}, "", false, false, err
	}
	term, err := ValidateTermYears(args.TermYears)
	if err != nil {
		return common.Recommendation{}, "", false, false, err
	}
	paymentOption, err := ValidatePaymentOption(args.PaymentOption)
	if err != nil {
		return common.Recommendation{}, "", false, false, err
	}
	azConfig, err := ValidateAZConfig(args.AZConfig)
	if err != nil {
		return common.Recommendation{}, "", false, false, err
	}

	rec = common.Recommendation{
		Provider:       common.ProviderAWS,
		Service:        common.ServiceRDS,
		Region:         region,
		ResourceType:   instanceClass,
		Count:          args.Count,
		CommitmentType: common.CommitmentReservedInstance,
		Term:           term.RecommendationTerm(),
		PaymentOption:  string(paymentOption),
		Details: &common.DatabaseDetails{
			Engine:        engine,
			AZConfig:      string(azConfig),
			InstanceClass: instanceClass,
		},
	}

	dryRun, confirm = ResolveDryRunConfirm(args.DryRun, args.Confirm)
	return rec, region, dryRun, confirm, nil
}

// resolveClient returns the ResolveClientFunc that ExecutePurchase invokes
// only for a real purchase. region is the effective, already-validated-and-
// trimmed region returned by rdsRecommendationFromArgs -- not args.Region --
// so a real purchase never resolves the provider/service client against a
// raw, un-trimmed value.
func (t *awsRDSRIPurchaseTool) resolveClient(args rdsRIPurchaseArgs, region string) ResolveClientFunc {
	return func(ctx context.Context) (provider.ServiceClient, error) {
		cfg := &provider.ProviderConfig{Name: string(common.ProviderAWS), AWSProfile: CredentialScope(args.AWSProfile), Region: region}
		prov, err := t.createProvider(string(common.ProviderAWS), cfg)
		if err != nil {
			return nil, err
		}
		return prov.GetServiceClient(ctx, common.ServiceRDS, region)
	}
}
