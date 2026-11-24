package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/LeanerCloud/CUDly/pkg/provider"
)

const awsElastiCacheRIPurchaseName = "cudly_aws_elasticache_ri_purchase"

const awsElastiCacheRIPurchaseTitle = "Purchase AWS ElastiCache Reserved Cache Nodes"

var awsElastiCacheRIPurchaseAnnotations = purchaseAnnotations(awsElastiCacheRIPurchaseTitle)

const awsElastiCacheRIPurchaseDescription = "Purchase AWS ElastiCache Reserved Cache Nodes. THIS SPENDS REAL " +
	"MONEY when dry_run=false and confirm=true. Always call with dry_run=true first (the default) to validate " +
	"your parameters before committing; a dry_run response never contacts AWS and never spends money."

// elasticacheRIPurchaseArgs is the input schema for
// cudly_aws_elasticache_ri_purchase. engine maps onto common.CacheDetails,
// which providers/aws/services/elasticache/client.go:273-283 requires for
// the offering lookup.
type elasticacheRIPurchaseArgs struct {
	Region           string `json:"region" jsonschema:"AWS region, e.g. us-east-1"`
	NodeType         string `json:"node_type" jsonschema:"ElastiCache cache node type, e.g. cache.r6g.large"`
	Count            int    `json:"count" jsonschema:"number of cache nodes to reserve, must be > 0"`
	TermYears        int    `json:"term_years" jsonschema:"commitment length in years"`
	PaymentOption    string `json:"payment_option" jsonschema:"payment schedule"`
	Engine           string `json:"engine" jsonschema:"cache engine"`
	AWSProfile       string `json:"aws_profile,omitempty" jsonschema:"AWS named profile override (~/.aws/config); default uses ambient credentials"`
	DryRun           *bool  `json:"dry_run,omitempty" jsonschema:"preview only, no purchase; defaults to true"`
	Confirm          *bool  `json:"confirm,omitempty" jsonschema:"required (with dry_run=false) to execute a real purchase; defaults to false"`
	IdempotencyNonce string `json:"idempotency_nonce,omitempty" jsonschema:"optional; set to a fresh value to authorize a purchase that is otherwise identical to a previous one (e.g. buy 3 more RIs with the same parameters); leave empty (the default) so retries with identical parameters dedupe and never double-buy"`
}

type awsElastiCacheRIPurchaseTool struct {
	createProvider func(name string, cfg *provider.ProviderConfig) (provider.Provider, error)
}

// NewAWSElastiCacheRIPurchaseTool builds the cudly_aws_elasticache_ri_purchase tool.
func NewAWSElastiCacheRIPurchaseTool() Registration {
	return &awsElastiCacheRIPurchaseTool{createProvider: provider.CreateProvider}
}

func (t *awsElastiCacheRIPurchaseTool) Descriptor() Descriptor {
	return Descriptor{
		Name:                awsElastiCacheRIPurchaseName,
		Provider:            "aws",
		Product:             "elasticache",
		Action:              "ri_purchase",
		Description:         awsElastiCacheRIPurchaseDescription,
		Annotations:         awsElastiCacheRIPurchaseAnnotations,
		RealPurchaseEnabled: true,
		ExamplePrompts: []string{
			"Preview buying 3 cache.r6g.large redis ElastiCache RIs in us-east-1 for 1 year",
			"Buy an ElastiCache Reserved Cache Node for memcached in eu-west-1 for real",
		},
	}
}

func (t *awsElastiCacheRIPurchaseTool) Register(s *mcp.Server) error {
	schema, err := BuildInputSchema[elasticacheRIPurchaseArgs](map[string]FieldOverride{
		"term_years":     {Enum: []any{int(TermOneYear), int(TermThreeYear)}},
		"payment_option": {Enum: []any{string(PaymentOptionAllUpfront), string(PaymentOptionPartialUpfront), string(PaymentOptionNoUpfront)}},
		"engine":         {Enum: []any{string(CacheEngineRedis), string(CacheEngineMemcached)}},
		"dry_run":        {Default: true},
		"confirm":        {Default: false},
	})
	if err != nil {
		return err
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        awsElastiCacheRIPurchaseName,
		Description: awsElastiCacheRIPurchaseDescription,
		Annotations: awsElastiCacheRIPurchaseAnnotations,
		InputSchema: schema,
	}, t.handle)
	return nil
}

func (t *awsElastiCacheRIPurchaseTool) handle(ctx context.Context, _ *mcp.CallToolRequest, args elasticacheRIPurchaseArgs) (*mcp.CallToolResult, PurchaseResponse, error) {
	rec, region, dryRun, confirm, err := elasticacheRecommendationFromArgs(args)
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

// elasticacheRecommendationFromArgs validates args and builds the
// common.Recommendation to purchase, the effective region (trimmed of any
// surrounding whitespace), and the effective dry_run/confirm booleans.
func elasticacheRecommendationFromArgs(args elasticacheRIPurchaseArgs) (rec common.Recommendation, region string, dryRun, confirm bool, err error) {
	region, err = requireNonBlank("region", args.Region)
	if err != nil {
		return common.Recommendation{}, "", false, false, err
	}
	nodeType, err := requireNonBlank("node_type", args.NodeType)
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
	engine, err := ValidateCacheEngine(args.Engine)
	if err != nil {
		return common.Recommendation{}, "", false, false, err
	}

	rec = common.Recommendation{
		Provider:       common.ProviderAWS,
		Service:        common.ServiceElastiCache,
		Region:         region,
		ResourceType:   nodeType,
		Count:          args.Count,
		CommitmentType: common.CommitmentReservedInstance,
		Term:           term.RecommendationTerm(),
		PaymentOption:  string(paymentOption),
		Details: &common.CacheDetails{
			Engine:   string(engine),
			NodeType: nodeType,
		},
	}

	dryRun, confirm = ResolveDryRunConfirm(args.DryRun, args.Confirm)
	return rec, region, dryRun, confirm, nil
}

// resolveClient returns the ResolveClientFunc that ExecutePurchase invokes
// only for a real purchase. region is the effective, already-validated-and-
// trimmed region returned by elasticacheRecommendationFromArgs -- not
// args.Region -- so a real purchase never resolves the provider/service
// client against a raw, un-trimmed value.
func (t *awsElastiCacheRIPurchaseTool) resolveClient(args elasticacheRIPurchaseArgs, region string) ResolveClientFunc {
	return func(ctx context.Context) (provider.ServiceClient, error) {
		cfg := &provider.ProviderConfig{Name: string(common.ProviderAWS), AWSProfile: CredentialScope(args.AWSProfile), Region: region}
		prov, err := t.createProvider(string(common.ProviderAWS), cfg)
		if err != nil {
			return nil, err
		}
		return prov.GetServiceClient(ctx, common.ServiceElastiCache, region)
	}
}
