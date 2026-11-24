package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/insurance"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

const (
	archeraQuoteNote = "Read-only. A quote exists only when exactly one Archera line item matches the recommendation on every identity field; " +
		"otherwise no figure is shown. Figures are exact vendor decimals, not a bindable quote or a purchase; no currency is supplied."
	archeraQuoteNoQuote = "No Archera quote"
	archeraQuoteMaxText = 128
	archeraQuoteMaxRate = 40
)

var archeraQuoteOpts struct {
	OrgID, PlanID, Format           string
	RecommendationID                string
	Provider, Service, PlanType     string
	SourceAccount, OfferingID       string
	Region, InstanceFamily          string
	Term, Payment, HourlyCommitment string
}

var archeraRecommendationQuoteCmd = &cobra.Command{
	Use:   "archera-recommendation-quote",
	Short: "Show the read-only Archera quote for one recommendation",
	Long: `Look up the Archera quote that belongs to one recommendation, identified by
explicit flags. A figure is printed only when exactly one Archera line item
matches the recommendation on every identity field; otherwise the output says
"No Archera quote" and shows no figure.

This command is read-only: it never purchases, binds or submits anything, and
it is independent of --purchase. It makes no Archera request unless it is run,
and none for a recommendation the matcher does not support.

Configuration (default off; there is no flag for the API key):
  ARCHERA_API_KEY   Archera API key (environment only)
  ARCHERA_ORG_ID    Archera organization UUID (or --org-id)
  ARCHERA_PLAN_ID   Archera commitment plan UUID (or --plan-id)`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runArcheraRecommendationQuote,
}

func init() {
	rootCmd.AddCommand(archeraRecommendationQuoteCmd)
	f := archeraRecommendationQuoteCmd.Flags()
	o := &archeraQuoteOpts
	f.StringVar(&o.OrgID, "org-id", "", "Archera organization UUID (default: $"+archeraEnvOrgID+")")
	f.StringVar(&o.PlanID, "plan-id", "", "Archera commitment plan UUID (default: $"+archeraEnvPlanID+")")
	f.StringVar(&o.Format, "format", "table", "Output format: table or json")
	f.StringVar(&o.RecommendationID, "recommendation-id", "", "Recommendation UUID (required)")
	f.StringVar(&o.Provider, "provider", "", "Cloud provider of the recommendation, such as aws (required)")
	f.StringVar(&o.Service, "service", "", "Recommendation service, such as savings-plans-ec2instance (required)")
	f.StringVar(&o.PlanType, "plan-type", "", "Savings Plan type, such as EC2Instance (required)")
	f.StringVar(&o.SourceAccount, "source-account", "", "Source cloud account ID (required)")
	f.StringVar(&o.OfferingID, "offering-id", "", "Cloud provider offering ID (required)")
	f.StringVar(&o.Region, "region", "", "Recommendation region (required)")
	f.StringVar(&o.InstanceFamily, "instance-family", "", "Instance family, such as m5 (required)")
	f.StringVar(&o.Term, "term", "", "Commitment term: 1yr or 3yr (required)")
	f.StringVar(&o.Payment, "payment", "", "Payment option: no_upfront, partial_upfront or all_upfront (required)")
	f.StringVar(&o.HourlyCommitment, "hourly-commitment", "", "Hourly commitment as a plain decimal string, such as 1.25 (required)")
}

var archeraDecimalRE = regexp.MustCompile(`^(?:0|[1-9]\d*)(?:\.\d+)?$`)

// archeraQuoteText checks a free-form identity flag. The message names the flag
// and never echoes the value.
func archeraQuoteText(flag, value string) (string, error) {
	if value == "" || len(value) > archeraQuoteMaxText || archeraSanitizeN(value, archeraQuoteMaxText) != value || strings.TrimSpace(value) != value {
		return "", fmt.Errorf("--%s is required and must be printable text of at most %d bytes", flag, archeraQuoteMaxText)
	}
	return value, nil
}

func archeraQuoteUUID(flag, value string) error {
	if len(value) != 36 {
		return fmt.Errorf("--%s must be a canonical UUID", flag)
	}
	if _, err := uuid.Parse(value); err != nil {
		return fmt.Errorf("--%s must be a canonical UUID", flag)
	}
	return nil
}

// archeraQuoteDuration maps the CLI term onto the native Savings Plan seconds.
func archeraQuoteDuration(term string) (int64, error) {
	switch term {
	case "1yr":
		return 31536000, nil
	case "3yr":
		return 94608000, nil
	}
	return 0, errors.New("--term must be 1yr or 3yr (exact case)")
}

func archeraQuoteRate(value string) (*big.Rat, error) {
	if len(value) > archeraQuoteMaxRate || !archeraDecimalRE.MatchString(value) {
		return nil, errors.New("--hourly-commitment must be a plain positive decimal string such as 1.25")
	}
	r, ok := new(big.Rat).SetString(value)
	if !ok || r.Sign() <= 0 {
		return nil, errors.New("--hourly-commitment must be a plain positive decimal string such as 1.25")
	}
	return r, nil
}

// archeraQuoteIdentity validates every flag before any vendor call.
func archeraQuoteIdentity() (*insurance.RecommendationIdentity, error) {
	o := archeraQuoteOpts
	if err := archeraQuoteUUID("recommendation-id", o.RecommendationID); err != nil {
		return nil, err
	}
	id := &insurance.RecommendationIdentity{}
	fields := []struct {
		dst         *string
		flag, value string
	}{
		{&id.Provider, "provider", o.Provider}, {&id.Service, "service", o.Service},
		{&id.PlanType, "plan-type", o.PlanType}, {&id.SourceAccountID, "source-account", o.SourceAccount},
		{&id.OfferingID, "offering-id", o.OfferingID}, {&id.Region, "region", o.Region},
		{&id.InstanceFamily, "instance-family", o.InstanceFamily},
	}
	for _, f := range fields {
		v, err := archeraQuoteText(f.flag, f.value)
		if err != nil {
			return nil, err
		}
		*f.dst = v
	}
	switch o.Payment {
	case "no_upfront", "partial_upfront", "all_upfront":
		id.Payment = o.Payment
	default:
		return nil, errors.New("--payment must be no_upfront, partial_upfront or all_upfront (exact case)")
	}
	var err error
	if id.NativeDurationSeconds, err = archeraQuoteDuration(o.Term); err != nil {
		return nil, err
	}
	if id.HourlyCommitment, err = archeraQuoteRate(o.HourlyCommitment); err != nil {
		return nil, err
	}
	return id, nil
}

type archeraQuoteOutput struct {
	Quote                 *insurance.RecommendationComparisonDTO `json:"quote"`
	Status                string                                 `json:"status"`
	Reason                string                                 `json:"reason"`
	RecommendationID      string                                 `json:"recommendation_id"`
	Message               string                                 `json:"message"`
	Note                  string                                 `json:"note"`
	NonGatingDisclosure   string                                 `json:"non_gating_disclosure"`
	SponsorshipDisclosure string                                 `json:"sponsorship_disclosure"`
}

func runArcheraRecommendationQuote(cmd *cobra.Command, _ []string) error {
	o := archeraQuoteOpts
	if o.Format != "table" && o.Format != "json" {
		return errors.New("--format must be table or json")
	}
	identity, err := archeraQuoteIdentity()
	if err != nil {
		return err
	}
	key, orgID, planID, err := archeraSettings(o.OrgID, o.PlanID)
	if err != nil {
		return err
	}
	if planErr := archeraQuoteUUID("plan-id", planID); planErr != nil {
		return planErr
	}
	client, err := insurance.NewClient(insurance.Config{APIKey: insurance.NewSecret(key), OrgID: orgID}, archeraHTTPClient)
	if err != nil {
		return archeraSafeError(err, key)
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
	defer cancel()
	quote, err := insurance.QuoteRecommendation(ctx, client, planID, o.RecommendationID, identity)
	if err != nil {
		return archeraQuoteError(err, key)
	}
	out := archeraQuoteResult(quote, o.RecommendationID)
	body, err := archeraQuoteRender(&out, o.Format, key)
	if err != nil {
		return err
	}
	_, err = cmd.OutOrStdout().Write(append(body, '\n'))
	return err
}

// archeraQuoteError prints a QuoteError from its fixed fields only: the library
// drops the vendor cause, so nothing vendor-controlled can reach the terminal.
func archeraQuoteError(err error, key string) error {
	var qe *insurance.QuoteError
	if !errors.As(err, &qe) {
		return archeraSafeError(err, key)
	}
	msg := fmt.Sprintf("archera recommendation quote failed: %s at %s", qe.Kind, qe.Stage)
	if qe.Status != 0 {
		msg += fmt.Sprintf(" (HTTP %d)", qe.Status)
	}
	if qe.RetryAfter > 0 {
		msg += fmt.Sprintf(" (retry after %s; not retried automatically)", qe.RetryAfter)
	}
	return archeraSafeError(errors.New(msg), key)
}

var archeraQuoteMessages = map[insurance.RecommendationQuoteStatus]string{
	insurance.QuoteStatusUnavailable: "No exact Archera line item could be proven for this recommendation.",
	insurance.QuoteStatusAmbiguous:   "More than one Archera line item matches this recommendation, so none is shown.",
	insurance.QuoteStatusIneligible:  "This recommendation is not a product the Archera matcher supports; Archera was not called.",
}

func archeraQuoteResult(q *insurance.RecommendationQuote, recommendationID string) archeraQuoteOutput {
	out := archeraQuoteOutput{
		Status: string(q.Status), Reason: archeraSanitize(string(q.Reason)), RecommendationID: recommendationID,
		Note: archeraQuoteNote, NonGatingDisclosure: common.ArcheraNonGatingDisclosure,
		SponsorshipDisclosure: common.ArcheraSponsorshipDisclosure,
	}
	if q.Status == insurance.QuoteStatusQuote {
		out.Quote = q.Comparison
		out.Message = "One Archera line item matches this recommendation exactly."
		return out
	}
	out.Message = archeraQuoteNoQuote + ": " + archeraQuoteMessages[q.Status]
	return out
}

func archeraQuoteRender(out *archeraQuoteOutput, format, key string) ([]byte, error) {
	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	if archeraKeyReadable(string(body), key) {
		return nil, errors.New("quote output cannot be safely displayed")
	}
	if format == "json" {
		return body, nil
	}
	return archeraQuoteTable(out), nil
}

func archeraQuoteTable(out *archeraQuoteOutput) []byte {
	var b bytes.Buffer
	p := func(format string, args ...any) { _, _ = fmt.Fprintf(&b, format+"\n", args...) }
	p("Archera recommendation quote (read-only)")
	p("Recommendation: %s", out.RecommendationID)
	if out.Reason == "" {
		p("Status: %s", out.Status)
	} else {
		p("Status: %s (%s)", out.Status, out.Reason)
	}
	p("%s", out.Message)
	if out.Quote != nil {
		p("Economic status: %s; amount provenance: %s; comparable delta: %s",
			out.Quote.EconomicStatus, out.Quote.AmountProvenance, archeraStr(out.Quote.ComparableDelta))
		archeraQuoteOfferLines(p, "Current", out.Quote.Current)
		for i := range out.Quote.Candidates {
			c := &out.Quote.Candidates[i]
			if c.Figures == nil {
				p("Alternative %s: no figures (%s)", c.OfferID, c.Reason)
				continue
			}
			archeraQuoteOfferLines(p, "Alternative ("+string(c.Status)+")", c.Figures)
		}
		p("Fetched at: %s", out.Quote.FetchedAt)
		p("Currency: %s", out.Quote.CurrencyNote)
	}
	p("%s", out.Note)
	p("%s", out.NonGatingDisclosure)
	p("%s", out.SponsorshipDisclosure)
	return bytes.TrimRight(b.Bytes(), "\n")
}

func archeraQuoteOfferLines(p func(string, ...any), label string, o *insurance.RecommendationOfferDTO) {
	if o == nil {
		p("%s: unknown", label)
		return
	}
	m := o.Monthly
	p("%s: offer %s, %s %s, term %s, payment %s, hourly commitment %s, upfront %s",
		label, o.OfferID, o.Provider, o.CommitmentType, archeraStr(o.EffectiveContractTerm), archeraStr(o.PaymentOption),
		archeraStr(o.HourlySelectedCommitment), archeraStr(o.UpfrontCost))
	p("  monthly (730-hour rate): commitment cost %s, cloud provider cost %s, premium %s, gross savings %s, net savings %s, covered on-demand %s",
		archeraStr(m.CommitmentCostTotal), archeraStr(m.CloudProviderCost), archeraStr(m.Premium),
		archeraStr(m.GrossSavings), archeraStr(m.NetSavings), archeraStr(m.CoveredOnDemandCost))
}
