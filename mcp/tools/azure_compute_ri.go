package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/LeanerCloud/CUDly/pkg/provider"
)

const azureComputeRIPurchaseName = "cudly_azure_compute_ri_purchase"

const azureComputeRIPurchaseTitle = "Purchase an Azure VM Reserved Instance"

var azureComputeRIPurchaseAnnotations = purchaseAnnotations(azureComputeRIPurchaseTitle)

// azureComputeRIPurchaseDescription documents Azure's actual billing-plan
// contract: providers/azure/services/compute/client.go's buildReservationBody
// sends properties.billingPlan (armreservations.ReservationBillingPlan --
// Upfront or Monthly), so both all-upfront and no-upfront purchases are
// honored for real. Monthly costs the same total as Upfront -- Azure has no
// premium for spreading payments -- but there is no partial-upfront billing
// plan at all, so that value is rejected with an explicit error rather than
// silently purchased under a different schedule (see
// azureComputeRecommendationFromArgs).
const azureComputeRIPurchaseDescription = "Purchase an Azure VM Reserved Instance. THIS SPENDS REAL MONEY when " +
	"dry_run=false and confirm=true. Always call with dry_run=true first (the default) to validate your " +
	"parameters before committing; a dry_run response never contacts Azure and never spends money. Azure " +
	"Reserved Instances support two billing plans: all-upfront and no-upfront (billed monthly, same total " +
	"price as all-upfront -- Azure charges no premium for spreading payments). payment_option defaults to " +
	"no-upfront when omitted. Azure has no partial-upfront billing plan, so that value is rejected with an " +
	"explicit error rather than silently purchased under all-upfront or no-upfront instead."

// azureComputeRIPurchaseArgs is the input schema for
// cudly_azure_compute_ri_purchase. Unlike EC2, Azure's purchase body needs no
// Recommendation.Details -- providers/azure/services/compute/client.go's
// buildReservationBody only reads Region/ResourceType/Count/Term/PaymentOption.
type azureComputeRIPurchaseArgs struct {
	Region              string `json:"region" jsonschema:"Azure region, e.g. eastus"`
	VMSize              string `json:"vm_size" jsonschema:"Azure VM size (SKU), e.g. Standard_D2s_v3"`
	Count               int    `json:"count" jsonschema:"number of VM instances to reserve, must be > 0"`
	TermYears           int    `json:"term_years" jsonschema:"commitment length in years"`
	PaymentOption       string `json:"payment_option,omitempty" jsonschema:"payment schedule; Azure honors all-upfront and no-upfront (monthly, same total price); no partial-upfront; defaults to no-upfront"`
	AzureSubscriptionID string `json:"azure_subscription_id,omitempty" jsonschema:"Azure subscription ID override; default uses AZURE_SUBSCRIPTION_ID"`
	DryRun              *bool  `json:"dry_run,omitempty" jsonschema:"preview only, no purchase; defaults to true"`
	Confirm             *bool  `json:"confirm,omitempty" jsonschema:"required (with dry_run=false) to execute a real purchase; defaults to false"`
	IdempotencyNonce    string `json:"idempotency_nonce,omitempty" jsonschema:"optional; set to a fresh value to authorize a purchase that is otherwise identical to a previous one (e.g. buy 3 more RIs with the same parameters); leave empty (the default) so retries with identical parameters dedupe and never double-buy"`
}

type azureComputeRIPurchaseTool struct {
	createProvider func(name string, cfg *provider.ProviderConfig) (provider.Provider, error)
}

// NewAzureComputeRIPurchaseTool builds the cudly_azure_compute_ri_purchase tool.
func NewAzureComputeRIPurchaseTool() Registration {
	return &azureComputeRIPurchaseTool{createProvider: provider.CreateProvider}
}

func (t *azureComputeRIPurchaseTool) Descriptor() Descriptor {
	return Descriptor{
		Name:                azureComputeRIPurchaseName,
		Provider:            "azure",
		Product:             "compute",
		Action:              "ri_purchase",
		Description:         azureComputeRIPurchaseDescription,
		Annotations:         azureComputeRIPurchaseAnnotations,
		RealPurchaseEnabled: true,
		ExamplePrompts: []string{
			"Preview buying 2 Standard_D2s_v3 Azure VM RIs in eastus for 3 years",
			"Buy an Azure VM Reserved Instance for real in westeurope",
		},
	}
}

func (t *azureComputeRIPurchaseTool) Register(s *mcp.Server) error {
	schema, err := BuildInputSchema[azureComputeRIPurchaseArgs](map[string]FieldOverride{
		"term_years": {Enum: []any{int(TermOneYear), int(TermThreeYear)}},
		// Azure has no partial-upfront billing plan (see
		// azureComputeRIPurchaseDescription and azureComputeRecommendationFromArgs
		// below), so this tool's schema advertises only the two values Azure
		// actually honors. The runtime check in azureComputeRecommendationFromArgs
		// still rejects partial-upfront explicitly, as defense in depth for a
		// caller that bypasses the schema.
		"payment_option": {Enum: []any{string(PaymentOptionAllUpfront), string(PaymentOptionNoUpfront)}, Default: string(PaymentOptionNoUpfront)},
		"dry_run":        {Default: true},
		"confirm":        {Default: false},
	})
	if err != nil {
		return err
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        azureComputeRIPurchaseName,
		Description: azureComputeRIPurchaseDescription,
		Annotations: azureComputeRIPurchaseAnnotations,
		InputSchema: schema,
	}, t.handle)
	return nil
}

// azureCredentialScope resolves the Azure subscription a purchase is billed
// to, lower-cased. Every use of the subscription ID in this file goes through
// it -- the idempotency scope in handle AND the ProviderConfig in resolveClient
// -- so the two cannot normalize it differently. That is the same reason both
// already route through CredentialScope rather than one of them reading
// args.AzureSubscriptionID raw.
//
// Case folding is the load-bearing part. ARM subscription IDs are
// case-insensitive GUIDs and nothing in providers/azure canonicalizes them
// (resolveAzureSubscriptionID forwards the configured value verbatim into the
// request path), so "ABC12345-..." pasted out of the portal and the lower-case
// "abc12345-..." that AZURE_SUBSCRIPTION_ID normally holds address the SAME
// subscription while deriving DIFFERENT idempotency tokens. A purchase issued
// under the first spelling that times out, then retried under the second (the
// override omitted, so the value comes from the environment), therefore misses
// reservations.FindReservationOrderByIdempotencyToken -- which matches on the
// token tag across the TENANT-wide order list -- and buys a SECOND reservation.
// That is the defect family 44b6094 fixed for untrimmed input: purchase
// identity forked by a difference the provider itself does not recognize.
// Whitespace was normalized there; case was not.
//
// Deliberately Azure-only rather than folded into CredentialScope: AWS named
// profiles are case-SENSITIVE (they are section names in ~/.aws/config), so
// lower-casing one would point a real purchase at a profile that does not
// exist, or at a different one that does. GCP project IDs are lower-case by
// grammar and need no folding.
func azureCredentialScope(explicit string, envVars ...string) string {
	return strings.ToLower(CredentialScope(explicit, envVars...))
}

func azureRequestCredentialScope(explicit string, dryRun bool, envVars ...string) string {
	return strings.ToLower(requestCredentialScope(explicit, dryRun, envVars...))
}

func (t *azureComputeRIPurchaseTool) handle(ctx context.Context, _ *mcp.CallToolRequest, args azureComputeRIPurchaseArgs) (*mcp.CallToolResult, PurchaseResponse, error) {
	rec, region, dryRun, confirm, err := azureComputeRecommendationFromArgs(args)
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
		CredentialScope: azureRequestCredentialScope(args.AzureSubscriptionID, dryRun, "AZURE_SUBSCRIPTION_ID"),
	})
	if err != nil {
		return nil, PurchaseResponse{}, err
	}
	return nil, *resp, nil
}

func azureComputeRecommendationFromArgs(args azureComputeRIPurchaseArgs) (rec common.Recommendation, region string, dryRun, confirm bool, err error) {
	region, err = requireNonBlank("region", args.Region)
	if err != nil {
		return common.Recommendation{}, "", false, false, err
	}
	vmSize, err := requireNonBlank("vm_size", args.VMSize)
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

	// payment_option defaults to no-upfront (matching the CLI's --payment
	// default, cmd/main.go) when the caller omits it -- an omitted string
	// field arrives as "" and is never confused with an explicit,
	// unrecognized value (feedback_no_silent_fallbacks: the default is
	// applied here, explicitly, not fabricated deeper in the stack).
	paymentOptionStr := args.PaymentOption
	if paymentOptionStr == "" {
		paymentOptionStr = string(PaymentOptionNoUpfront)
	}
	paymentOption, err := ValidatePaymentOption(paymentOptionStr)
	if err != nil {
		return common.Recommendation{}, "", false, false, err
	}
	// Azure reservations support exactly two billing plans (Upfront,
	// Monthly -- see providers/azure/services/internal/reservations.
	// BillingPlanForPaymentOption); there is no partial-upfront at any
	// layer of Azure's API. Rejecting it here, unconditionally (not just
	// for a real purchase), means a dry_run preview never reports success
	// for a request that could never be honored for real.
	if paymentOption == PaymentOptionPartialUpfront {
		return common.Recommendation{}, "", false, false, fmt.Errorf(
			"azure reservations do not support payment_option=%q: azure billing plans are all-upfront or "+
				"no-upfront (monthly, same total price) only, with no partial-upfront option",
			paymentOption)
	}

	dryRun, confirm = ResolveDryRunConfirm(args.DryRun, args.Confirm)

	rec = common.Recommendation{
		Provider:       common.ProviderAzure,
		Service:        common.ServiceCompute,
		Region:         region,
		ResourceType:   vmSize,
		Count:          args.Count,
		CommitmentType: common.CommitmentReservedInstance,
		Term:           term.RecommendationTerm(),
		PaymentOption:  string(paymentOption),
	}

	return rec, region, dryRun, confirm, nil
}

// resolveClient returns the ResolveClientFunc that ExecutePurchase invokes
// only for a real purchase. region is the effective, already-validated-and-
// trimmed region returned by azureComputeRecommendationFromArgs -- not
// args.Region -- so a real purchase never resolves the provider/service
// client against a raw, un-trimmed value.
//
// The subscription goes through azureCredentialScope, the same function the
// idempotency scope uses, so the account the token names and the account the
// purchase authenticates against are normalized identically by construction.
// No environment variable is passed here (unlike the handler's scope) because
// an empty AzureSubscriptionID is how this config asks the provider factory to
// resolve the subscription itself; ARM treats the ID case-insensitively, so
// lower-casing it does not change which subscription is addressed.
func (t *azureComputeRIPurchaseTool) resolveClient(args azureComputeRIPurchaseArgs, region string) ResolveClientFunc {
	return func(ctx context.Context) (provider.ServiceClient, error) {
		cfg := &provider.ProviderConfig{Name: string(common.ProviderAzure), AzureSubscriptionID: azureCredentialScope(args.AzureSubscriptionID), Region: region}
		prov, err := t.createProvider(string(common.ProviderAzure), cfg)
		if err != nil {
			return nil, err
		}
		return prov.GetServiceClient(ctx, common.ServiceCompute, region)
	}
}
