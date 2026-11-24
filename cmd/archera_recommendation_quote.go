package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
	OrgID, PlanID, Format, RecommendationID    string
	Provider, Service, PlanType, SourceAccount string
	OfferingID, Region, InstanceFamily         string
	Term, Payment, HourlyCommitment            string
}

var archeraRecommendationQuoteCmd = &cobra.Command{
	Use:   "archera-recommendation-quote",
	Short: "Show the read-only Archera quote for one recommendation",
	Long: `Look up the Archera quote for one recommendation, identified by explicit
flags. A figure is printed only when exactly one Archera line item matches on
every identity field; otherwise the output says "No Archera quote" and shows
no figure. Read-only and independent of --purchase; no request is made unless
it runs, and none for a recommendation the matcher does not support.

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

// archeraQuoteSeconds maps the CLI term onto the native Savings Plan seconds.
var archeraQuoteSeconds = map[string]int64{"1yr": 31536000, "3yr": 94608000}

func archeraQuoteRate(value string) (*big.Rat, error) {
	r, ok := new(big.Rat).SetString(value)
	if len(value) > archeraQuoteMaxRate || !archeraDecimalRE.MatchString(value) || !ok || r.Sign() <= 0 {
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
	var ok bool
	if id.NativeDurationSeconds, ok = archeraQuoteSeconds[o.Term]; !ok {
		return nil, errors.New("--term must be 1yr or 3yr (exact case)")
	}
	var err error
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
	out, err := archeraQuoteResult(quote, o.RecommendationID, key)
	if err != nil {
		return err
	}
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

func archeraQuoteResult(q *insurance.RecommendationQuote, recommendationID, key string) (archeraQuoteOutput, error) {
	out := archeraQuoteOutput{
		Status: string(q.Status), Reason: archeraSanitize(string(q.Reason)), RecommendationID: recommendationID,
		Note: archeraQuoteNote, NonGatingDisclosure: common.ArcheraNonGatingDisclosure,
		SponsorshipDisclosure: common.ArcheraSponsorshipDisclosure,
	}
	if q.Status == insurance.QuoteStatusQuote {
		dto, err := archeraQuoteSanitizedDTO(q.Comparison, key)
		if err != nil {
			return out, errors.New("quote output cannot be safely displayed")
		}
		out.Quote = dto
		out.Message = "One Archera line item matches this recommendation exactly."
		return out, nil
	}
	out.Message = archeraQuoteNoQuote + ": " + archeraQuoteMessages[q.Status]
	return out, nil
}

// archeraQuoteSanitizedDTO returns a copy of dto in which every string, vendor
// ids and labels included, went through archeraListingText: control characters
// removed, the key masked and a key that stays readable withheld.
func archeraQuoteSanitizedDTO(dto *insurance.RecommendationComparisonDTO, key string) (*insurance.RecommendationComparisonDTO, error) {
	raw, err := json.Marshal(dto)
	if err != nil {
		return nil, err
	}
	var out insurance.RecommendationComparisonDTO
	if unmarshalErr := json.Unmarshal(raw, &out); unmarshalErr != nil {
		return nil, unmarshalErr
	}
	archeraSanitizeStrings(reflect.ValueOf(&out).Elem(), key)
	return &out, nil
}

func archeraSanitizeStrings(v reflect.Value, key string) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			archeraSanitizeStrings(v.Elem(), key)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			archeraSanitizeStrings(v.Field(i), key)
		}
	case reflect.Slice:
		for i := range v.Len() {
			archeraSanitizeStrings(v.Index(i), key)
		}
	case reflect.String:
		v.SetString(archeraListingText(v.String(), key))
	}
}

// archeraQuoteRender renders the result and refuses it when the key can still
// be read in it. The check has no size cap: it covers the whole table text, the
// encoded JSON and every decoded JSON string and key, because JSON escapes
// < > & and U+2028/2029, which hides a key from the encoded bytes alone.
func archeraQuoteRender(out *archeraQuoteOutput, format, key string) ([]byte, error) {
	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	if archeraJSONHasKey(body, key) {
		return nil, errors.New("quote output cannot be safely displayed")
	}
	if format == "json" {
		return body, nil
	}
	table := archeraQuoteTable(out)
	if strings.Contains(archeraSkeleton(string(table)), archeraSkeleton(key)) {
		return nil, errors.New("quote output cannot be safely displayed")
	}
	return table, nil
}

func archeraJSONHasKey(encoded []byte, key string) bool {
	want := archeraSkeleton(key)
	if strings.Contains(archeraSkeleton(string(encoded)), want) {
		return true
	}
	var decoded any
	if json.Unmarshal(encoded, &decoded) != nil {
		return true
	}
	var texts []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			texts = append(texts, x)
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			for k, e := range x {
				texts = append(texts, k)
				walk(e)
			}
		}
	}
	walk(decoded)
	for _, t := range texts {
		if strings.Contains(archeraSkeleton(t), want) {
			return true
		}
	}
	return false
}

// archeraSkeleton drops every rune that can hide or space out a key: control,
// format, combining, separator and variation-selector runes, Hangul fillers
// and invalid bytes.
func archeraSkeleton(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case utf8.RuneError, 0x115F, 0x1160, 0x3164, 0xFFA0:
			continue
		}
		if unicode.In(r, unicode.Cc, unicode.Cf, unicode.Mn, unicode.Me, unicode.Zl, unicode.Zp, unicode.Zs) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
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
