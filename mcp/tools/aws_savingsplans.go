package tools

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	spTypes "github.com/aws/aws-sdk-go-v2/service/savingsplans/types"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/LeanerCloud/CUDly/pkg/provider"
	"github.com/LeanerCloud/CUDly/providers/aws/services/savingsplans"
)

const awsSavingsPlansPurchaseName = "cudly_aws_savingsplans_purchase"

const awsSavingsPlansPurchaseTitle = "Purchase an AWS Savings Plan"

var awsSavingsPlansPurchaseAnnotations = purchaseAnnotations(awsSavingsPlansPurchaseTitle)

const awsSavingsPlansPurchaseDescription = "Purchase an AWS Savings Plan (Compute, EC2Instance, SageMaker, or " +
	"Database). THIS SPENDS REAL MONEY when dry_run=false and confirm=true. Always call with dry_run=true " +
	"first (the default) to validate your parameters before committing; a dry_run response never contacts AWS " +
	"and never spends money. Unlike RI purchases this is dollar-denominated: you specify hourly_commitment " +
	"(USD/hour), not an instance count. CAVEAT: sp_type=Database only supports term_years=1 and " +
	"payment_option=no-upfront; AWS does not offer a 3-year Database Savings Plan or all-upfront/" +
	"partial-upfront billing for it."

// savingsPlansAccountLevelRegion is the region used to resolve the account-
// level Savings Plans service client when the caller omits region -- Compute,
// SageMaker, and Database plans are global, and cmd/multi_service_helpers.go
// already establishes this same "single query, us-east-1" convention for
// account-level Savings Plans recommendations.
const savingsPlansAccountLevelRegion = "us-east-1"

// savingsPlansPurchaseArgs is the input schema for
// cudly_aws_savingsplans_purchase. instance_family and region are only
// meaningful for EC2Instance plans (common.SavingsPlanDetails); Compute,
// SageMaker, and Database plans are family-agnostic and account-level.
type savingsPlansPurchaseArgs struct {
	SPType           string  `json:"sp_type" jsonschema:"AWS Savings Plans type"`
	HourlyCommitment float64 `json:"hourly_commitment" jsonschema:"USD/hour commitment amount, must be > 0"`
	TermYears        int     `json:"term_years" jsonschema:"commitment length in years"`
	PaymentOption    string  `json:"payment_option" jsonschema:"payment schedule"`
	InstanceFamily   string  `json:"instance_family,omitempty" jsonschema:"EC2 instance family, e.g. m5; only meaningful for sp_type=EC2Instance"`
	Region           string  `json:"region,omitempty" jsonschema:"AWS region; required for sp_type=EC2Instance, ignored for account-level plan types"`
	AWSProfile       string  `json:"aws_profile,omitempty" jsonschema:"AWS named profile override (~/.aws/config); default uses ambient credentials"`
	DryRun           *bool   `json:"dry_run,omitempty" jsonschema:"preview only, no purchase; defaults to true"`
	Confirm          *bool   `json:"confirm,omitempty" jsonschema:"required (with dry_run=false) to execute a real purchase; defaults to false"`
	IdempotencyNonce string  `json:"idempotency_nonce,omitempty" jsonschema:"optional; set to a fresh value to authorize a purchase that is otherwise identical to a previous one (e.g. buy 3 more RIs with the same parameters); leave empty (the default) so retries with identical parameters dedupe and never double-buy"`
}

type awsSavingsPlansPurchaseTool struct {
	createProvider func(name string, cfg *provider.ProviderConfig) (provider.Provider, error)
}

// NewAWSSavingsPlansPurchaseTool builds the cudly_aws_savingsplans_purchase tool.
func NewAWSSavingsPlansPurchaseTool() Registration {
	return &awsSavingsPlansPurchaseTool{createProvider: provider.CreateProvider}
}

func (t *awsSavingsPlansPurchaseTool) Descriptor() Descriptor {
	return Descriptor{
		Name:                awsSavingsPlansPurchaseName,
		Provider:            "aws",
		Product:             "savingsplans",
		Action:              "purchase",
		Description:         awsSavingsPlansPurchaseDescription,
		Annotations:         awsSavingsPlansPurchaseAnnotations,
		RealPurchaseEnabled: true,
		ExamplePrompts: []string{
			"Preview a $10/hour Compute Savings Plan, 3-year no-upfront",
			"Buy a $5/hour EC2Instance Savings Plan for the m5 family in us-east-1 for real",
		},
	}
}

func (t *awsSavingsPlansPurchaseTool) Register(s *mcp.Server) error {
	schema, err := BuildInputSchema[savingsPlansPurchaseArgs](map[string]FieldOverride{
		"sp_type": {Enum: []any{
			string(SPTypeCompute), string(SPTypeEC2Instance), string(SPTypeSageMaker), string(SPTypeDatabase),
		}},
		"term_years":     {Enum: []any{int(TermOneYear), int(TermThreeYear)}},
		"payment_option": {Enum: []any{string(PaymentOptionAllUpfront), string(PaymentOptionPartialUpfront), string(PaymentOptionNoUpfront)}},
		"dry_run":        {Default: true},
		"confirm":        {Default: false},
	})
	if err != nil {
		return err
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        awsSavingsPlansPurchaseName,
		Description: awsSavingsPlansPurchaseDescription,
		Annotations: awsSavingsPlansPurchaseAnnotations,
		InputSchema: schema,
	}, t.handle)
	return nil
}

func (t *awsSavingsPlansPurchaseTool) handle(ctx context.Context, _ *mcp.CallToolRequest, args savingsPlansPurchaseArgs) (*mcp.CallToolResult, PurchaseResponse, error) {
	rec, region, dryRun, confirm, err := savingsPlanRecommendationFromArgs(args)
	if err != nil {
		return nil, PurchaseResponse{}, err
	}

	resp, err := ExecutePurchase(ctx, PurchaseRequest{
		Region:          region,
		Recommendation:  rec,
		DryRun:          dryRun,
		Confirm:         confirm,
		ResolveClient:   t.resolveClient(args, region, rec.Service),
		Nonce:           args.IdempotencyNonce,
		CredentialScope: requestCredentialScope(args.AWSProfile, dryRun, "AWS_PROFILE"),
	})
	if err != nil {
		return nil, PurchaseResponse{}, err
	}
	return nil, *resp, nil
}

// validateSavingsPlanArgs validates every field of args that does not
// depend on the effective region, returning the typed sp_type, term, and
// payment_option. Split out of savingsPlanRecommendationFromArgs so that
// function's cyclomatic complexity stays under the repo's gocyclo gate as
// validation branches (e.g. validateDatabaseSPConstraints) are added.
func validateSavingsPlanArgs(args savingsPlansPurchaseArgs) (spType SPType, term TermYears, paymentOption PaymentOption, err error) {
	// Named commitErr rather than err: this function's err is a named return,
	// so `err :=` trips govet's shadow check while `err =` trips gocritic's
	// sloppyReassign. A distinct name satisfies both.
	if commitErr := validateHourlyCommitment(args.HourlyCommitment); commitErr != nil {
		return "", 0, "", commitErr
	}
	spType, err = ValidateSPType(args.SPType)
	if err != nil {
		return "", 0, "", err
	}
	term, err = ValidateTermYears(args.TermYears)
	if err != nil {
		return "", 0, "", err
	}
	paymentOption, err = ValidatePaymentOption(args.PaymentOption)
	if err != nil {
		return "", 0, "", err
	}
	if spType == SPTypeEC2Instance && strings.TrimSpace(args.Region) == "" {
		return "", 0, "", fmt.Errorf("region is required for sp_type=%s", SPTypeEC2Instance)
	}
	// instance_family is the filter that stops DescribeSavingsPlansOfferings
	// from resolving to an arbitrary EC2Instance offering across every family
	// in the region. providers/aws/services/savingsplans/client.go's
	// lookupEC2OfferingIDStrict does fail loud when the resulting offerings
	// span more than one family, but that is defense in depth at the API
	// boundary; requiring the family here, at the tool boundary, catches the
	// missing value before a real purchase attempt is even made.
	if spType == SPTypeEC2Instance && strings.TrimSpace(args.InstanceFamily) == "" {
		return "", 0, "", fmt.Errorf("instance_family is required for sp_type=%s", SPTypeEC2Instance)
	}
	if err := validateDatabaseSPConstraints(spType, term, paymentOption); err != nil {
		return "", 0, "", err
	}
	return spType, term, paymentOption, nil
}

// savingsPlanRecommendationFromArgs validates args and builds the
// common.Recommendation to purchase, the effective region to resolve the
// service client against, and the effective dry_run/confirm booleans.
func savingsPlanRecommendationFromArgs(args savingsPlansPurchaseArgs) (rec common.Recommendation, region string, dryRun, confirm bool, err error) {
	spType, term, paymentOption, err := validateSavingsPlanArgs(args)
	if err != nil {
		return common.Recommendation{}, "", false, false, err
	}

	// Trim once and use the trimmed value everywhere region/instance_family
	// are stored or forwarded (resolved region, rec.Region, Details.Region,
	// Details.InstanceFamily): validateSavingsPlanArgs above only trims for
	// the blank-check, so a caller-supplied " us-east-1 " would otherwise
	// flow raw into ProviderConfig/GetServiceClient and the real
	// DescribeSavingsPlansOfferings lookup for an EC2Instance purchase.
	trimmedRegion := strings.TrimSpace(args.Region)
	trimmedInstanceFamily := strings.TrimSpace(args.InstanceFamily)

	// Account-level plan types (Compute/SageMaker/Database) ignore region
	// entirely, so pin the canonical one rather than honoring whatever the
	// caller passed. region flows into rec.Region and from there into
	// idempotencyKeyFor (mcp/tools/purchase.go), so letting an IGNORED input
	// through would fork purchase identity: buying a $10/hr Compute SP once
	// without region and then re-issuing the identical call with
	// region="eu-west-1" (a self-correction, or a retry that fills the field
	// in) derives two different tokens, Savings Plans' ClientToken dedupe
	// misses, and a second plan is bought. Only EC2Instance plans are
	// genuinely region-scoped, and validateSavingsPlanArgs already requires a
	// non-blank region for those.
	region = savingsPlansAccountLevelRegion
	if spType == SPTypeEC2Instance {
		region = trimmedRegion
	}

	// Resolve the precise per-plan-type ServiceType (e.g.
	// ServiceSavingsPlansCompute) rather than the ServiceSavingsPlansAll
	// umbrella sentinel, so GetServiceClient returns a client scoped to
	// spType: providers/aws/services/savingsplans/client.go's
	// resolveSPPlanType then rejects a mismatched Details.PlanType instead of
	// silently buying whatever plan type happens to be in Details (defense in
	// depth on top of the ValidateSPType check above).
	service := savingsplans.ServiceTypeForPlanType(spTypes.SavingsPlanType(spType))

	details := &common.SavingsPlanDetails{
		PlanType:         string(spType),
		HourlyCommitment: args.HourlyCommitment,
	}
	// InstanceFamily and Region are only meaningful for EC2Instance plans
	// (common.SavingsPlanDetails documents both as "only populated for
	// EC2Instance"); leaving them unset for Compute/SageMaker/Database keeps
	// that contract instead of leaking a caller-supplied region/family into
	// an account-level, family-agnostic plan's Details.
	if spType == SPTypeEC2Instance {
		details.InstanceFamily = trimmedInstanceFamily
		details.Region = trimmedRegion
	}

	rec = common.Recommendation{
		Provider:       common.ProviderAWS,
		Service:        service,
		Region:         region,
		CommitmentType: common.CommitmentSavingsPlan,
		Term:           term.RecommendationTerm(),
		PaymentOption:  string(paymentOption),
		Details:        details,
	}

	dryRun, confirm = ResolveDryRunConfirm(args.DryRun, args.Confirm)
	return rec, region, dryRun, confirm, nil
}

// commitmentFormat is the exact format providers/aws/services/savingsplans/
// client.go renders CreateSavingsPlanInput.Commitment with. Duplicated here
// deliberately: validateHourlyCommitment's job is to prove the caller's
// amount survives THAT rendering unchanged, so it must compare against the
// same format string rather than an independently-chosen precision.
const commitmentFormat = "%.2f"

// commitmentEpsilon is the relative tolerance for "the rendered amount is
// the same money as the requested amount". A decimal literal such as 0.07
// is not exactly representable in float64 (0.07*100 is 7.000000000000001),
// so an exact equality test would reject legitimate whole-cent values;
// anything genuinely finer than a cent differs from its rendering by at
// least ~0.001, orders of magnitude above this bound.
const commitmentEpsilon = 1e-9

// validateHourlyCommitment rejects a commitment AWS cannot bill exactly.
// The Savings Plans client renders the amount with %.2f, so anything finer
// than a cent is silently rounded on the way to CreateSavingsPlan: a
// requested $0.004/hour becomes "0.00" and a requested $10.005/hour becomes
// "10.01", in both cases committing real money to a figure the caller never
// asked for. Rounding a money value behind the caller's back is exactly the
// silent coercion this path must not do (feedback_no_silent_fallbacks), so a
// sub-cent value is an explicit error naming the amount AWS would actually
// have charged.
//
// This also keeps the idempotency key honest. idempotencyKeyFor folds in the
// full-precision HourlyCommitment, so without this two requests AWS would
// bill identically (10.001 and 10.004, both "10.00") would derive different
// tokens and could purchase twice.
func validateHourlyCommitment(hourlyCommitment float64) error {
	if math.IsNaN(hourlyCommitment) || math.IsInf(hourlyCommitment, 0) {
		return fmt.Errorf("hourly_commitment must be a finite number, got %v", hourlyCommitment)
	}
	if hourlyCommitment <= 0 {
		return fmt.Errorf("hourly_commitment must be > 0, got %v", hourlyCommitment)
	}
	rendered := fmt.Sprintf(commitmentFormat, hourlyCommitment)
	billed, err := strconv.ParseFloat(rendered, 64)
	if err != nil {
		// Unreachable: hourlyCommitment is finite here (NaN/Inf rejected
		// above), and %.2f on a finite float64 always renders as a plain
		// decimal string that strconv.ParseFloat accepts.
		return fmt.Errorf("hourly_commitment %v is not a billable amount: %w", hourlyCommitment, err)
	}
	if math.Abs(hourlyCommitment-billed) > commitmentEpsilon*math.Max(1, hourlyCommitment) {
		return fmt.Errorf(
			"hourly_commitment %v is finer than one cent: AWS bills a Savings Plan commitment to two decimal "+
				"places, so this would be silently charged as %s/hour instead; pass an amount in whole cents",
			hourlyCommitment, rendered)
	}
	return nil
}

// validateDatabaseSPConstraints rejects a Database Savings Plan request
// AWS's purchase API would itself reject: per AWS's Database Savings Plans
// announcement (aws.amazon.com/about-aws/whats-new/2025/12/database-savings-plans-savings),
// Database Savings Plans support only a one-year term billed no-upfront --
// unlike Compute, EC2Instance, and SageMaker plans, there is no three-year
// term and no all-upfront/partial-upfront option. Failing loud here, before
// building the recommendation, surfaces AWS's real constraint instead of
// letting a real purchase reach AWS only to be rejected there.
func validateDatabaseSPConstraints(spType SPType, term TermYears, paymentOption PaymentOption) error {
	if spType != SPTypeDatabase {
		return nil
	}
	if term != TermOneYear {
		return fmt.Errorf("sp_type=%s only supports a %d-year term (got term_years=%d): "+
			"AWS Database Savings Plans do not offer a %d-year term",
			SPTypeDatabase, TermOneYear, term, TermThreeYear)
	}
	if paymentOption != PaymentOptionNoUpfront {
		return fmt.Errorf("sp_type=%s only supports payment_option=%s (got %q): "+
			"AWS Database Savings Plans do not offer all-upfront or partial-upfront billing",
			SPTypeDatabase, PaymentOptionNoUpfront, paymentOption)
	}
	return nil
}

func (t *awsSavingsPlansPurchaseTool) resolveClient(args savingsPlansPurchaseArgs, region string, service common.ServiceType) ResolveClientFunc {
	return func(ctx context.Context) (provider.ServiceClient, error) {
		cfg := &provider.ProviderConfig{Name: string(common.ProviderAWS), AWSProfile: CredentialScope(args.AWSProfile), Region: region}
		prov, err := t.createProvider(string(common.ProviderAWS), cfg)
		if err != nil {
			return nil, err
		}
		return prov.GetServiceClient(ctx, service, region)
	}
}
