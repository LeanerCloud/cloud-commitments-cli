package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/insurance"
)

const (
	archeraTitle        = "Archera commitment plan comparison (read-only)"
	archeraMonthlyBasis = "730-hour monthly rate"
	archeraUpfrontBasis = "one-time"
	archeraPlanWideNote = "This comparison covers the whole Archera plan. It is not mapped onto CUDly recommendation rows or recommendation CSV output."
	archeraCaveat       = "A plan comparison is a hypothetical rollup, never a bindable insurance quote, and never a purchase. " +
		"An insured target of 100% is a requested target subject to Archera underwriting allowances, not a guarantee. " +
		"The API exposes no per-line product support or allowance verdict, so product support and allowance are shown as unknown unless Archera's documentation says otherwise."
	archeraPremiumNote  = "Totals include the Archera premium. The API reports no currency, so amounts are unlabelled."
	archeraMaxFieldSize = 256
	archeraMaxMoneyPrec = 100
)

// archeraSanitize strips control characters (ESC, C1 and the rest of
// unicode.IsControl) from a vendor string and caps it at archeraMaxFieldSize
// bytes on a rune boundary, so it is safe to print to a terminal.
func archeraSanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > archeraMaxFieldSize {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

func archeraSanitizePtr(s *string) *string {
	if s == nil {
		return nil
	}
	v := archeraSanitize(*s)
	return &v
}

// archeraMoney renders an exact decimal using the smallest precision at which
// FloatString round-trips to the same Rat. No float64 and no division. nil is
// unknown and returns nil so JSON gets null.
func archeraMoney(r *big.Rat) *string {
	if r == nil {
		return nil
	}
	for p := 0; p <= archeraMaxMoneyPrec; p++ {
		s := r.FloatString(p)
		if back, ok := new(big.Rat).SetString(s); ok && back.Cmp(r) == 0 {
			return &s
		}
	}
	s := r.String()
	return &s
}

type archeraFinancialsDTO struct {
	CommitmentCostTotal *string `json:"commitment_cost_total"`
	CloudProviderCost   *string `json:"cloud_provider_cost"`
	Premium             *string `json:"premium"`
	GrossSavings        *string `json:"gross_savings"`
	NetSavings          *string `json:"net_savings"`
	CoveredOnDemandCost *string `json:"covered_on_demand_cost"`
}

type archeraTotalsDTO struct {
	Monthly     archeraFinancialsDTO `json:"monthly_rate_730h"`
	UpfrontCost *string              `json:"upfront_cost_one_time"`
}

type archeraLineItemDTO struct {
	LineItemID           string  `json:"line_item_id"`
	ActualTerm           *string `json:"actual_term"`
	ActualPaymentOption  *string `json:"actual_payment_option"`
	ActualCommitmentType *string `json:"actual_commitment_type"`
	Reason               string  `json:"reason"`
}

type archeraHypotheticalDTO struct {
	ContractTerm               *string              `json:"contract_term"`
	PaymentOption              string               `json:"payment_option"`
	Totals                     archeraTotalsDTO     `json:"totals"`
	DeltaMonthlyNetSavings     *string              `json:"delta_monthly_net_savings"`
	DeltaMonthlyCommitmentCost *string              `json:"delta_monthly_commitment_cost"`
	DeltaUpfrontCost           *string              `json:"delta_upfront_cost"`
	LineItems                  []archeraLineItemDTO `json:"line_items"`
}

type archeraProductSupportDTO struct {
	Status   string `json:"status"`
	Source   string `json:"source,omitempty"`
	Evidence string `json:"evidence,omitempty"`
}

type archeraDeltaDTO struct {
	MonthlyNetSavings *string `json:"monthly_net_savings"`
	UpfrontCost       *string `json:"upfront_cost"`
	DiscountRate      *string `json:"discount_rate"`
	BreakevenDays     *string `json:"breakeven_days"`
}

type archeraOfferDTO struct {
	IsCurrent             bool                     `json:"is_current"`
	OfferID               string                   `json:"offer_id"`
	CommitmentType        string                   `json:"commitment_type"`
	Provider              string                   `json:"provider"`
	Region                *string                  `json:"region"`
	ContractTerm          *string                  `json:"contract_term"`
	PaymentOption         *string                  `json:"payment_option"`
	LeaseAttached         bool                     `json:"lease_attached"`
	ArcheraOfferName      *string                  `json:"archera_offer_name"`
	DiscountRate          *string                  `json:"discount_rate"`
	BreakevenDays         *string                  `json:"breakeven_days"`
	Totals                archeraTotalsDTO         `json:"totals"`
	DeltaVsCurrent        archeraDeltaDTO          `json:"delta_vs_current"`
	ArcheraProductSupport archeraProductSupportDTO `json:"archera_product_support"`
}

type archeraRowDTO struct {
	LineItemID string            `json:"line_item_id"`
	Current    archeraOfferDTO   `json:"current"`
	Candidates []archeraOfferDTO `json:"candidates"`
}

// archeraComparisonDTO is the CLI's stable JSON contract. Money values are
// exact decimal strings; null means unknown, never zero.
type archeraComparisonDTO struct {
	Title                 string                   `json:"title"`
	PlanID                string                   `json:"plan_id"`
	FetchedAt             string                   `json:"fetched_at"`
	Currency              *string                  `json:"currency"`
	PremiumIncluded       bool                     `json:"premium_included"`
	MonthlyBasis          string                   `json:"monthly_basis"`
	UpfrontBasis          string                   `json:"upfront_basis"`
	PlanWide              string                   `json:"plan_wide_note"`
	Caveat                string                   `json:"caveat"`
	Current               archeraTotalsDTO         `json:"current_totals"`
	Hypotheticals         []archeraHypotheticalDTO `json:"hypothetical_totals"`
	Rows                  []archeraRowDTO          `json:"rows"`
	NonGatingDisclosure   string                   `json:"non_gating_disclosure"`
	SponsorshipDisclosure string                   `json:"sponsorship_disclosure"`
}

func archeraFinancials(f insurance.Financials) archeraFinancialsDTO {
	return archeraFinancialsDTO{
		CommitmentCostTotal: archeraMoney(f.CommitmentCostTotal),
		CloudProviderCost:   archeraMoney(f.CloudProviderCost),
		Premium:             archeraMoney(f.Premium),
		GrossSavings:        archeraMoney(f.GrossSavings),
		NetSavings:          archeraMoney(f.NetSavings),
		CoveredOnDemandCost: archeraMoney(f.CoveredOnDemandCost),
	}
}

func archeraTotals(t insurance.Totals) archeraTotalsDTO {
	return archeraTotalsDTO{Monthly: archeraFinancials(t.Monthly), UpfrontCost: archeraMoney(t.UpfrontCost)}
}

func archeraPayment(p *insurance.PaymentOption) *string {
	if p == nil {
		return nil
	}
	s := archeraSanitize(string(*p))
	return &s
}

func archeraOffer(e insurance.OfferEntry) archeraOfferDTO {
	support := insurance.AssessProductSupport(e.Provider, e.CommitmentType)
	ps := archeraProductSupportDTO{Status: string(support.Status)}
	if support.Status == insurance.ProductSupportSupported {
		ps.Source, ps.Evidence = support.Source, support.Evidence
	}
	return archeraOfferDTO{
		IsCurrent:        e.IsCurrent,
		OfferID:          archeraSanitize(e.OfferID),
		CommitmentType:   archeraSanitize(e.CommitmentType),
		Provider:         archeraSanitize(string(e.Provider)),
		Region:           archeraSanitizePtr(e.Region),
		ContractTerm:     archeraSanitizePtr(e.ContractTerm),
		PaymentOption:    archeraPayment(e.PaymentOption),
		LeaseAttached:    e.LeaseBacked(),
		ArcheraOfferName: archeraSanitizePtr(e.GuaranteedDisplayName),
		DiscountRate:     archeraMoney(e.DiscountRate),
		BreakevenDays:    archeraMoney(e.BreakevenDays),
		Totals:           archeraTotalsDTO{Monthly: archeraFinancials(e.Monthly), UpfrontCost: archeraMoney(e.UpfrontCost)},
		DeltaVsCurrent: archeraDeltaDTO{
			MonthlyNetSavings: archeraMoney(e.Delta.MonthlyNetSavings),
			UpfrontCost:       archeraMoney(e.Delta.UpfrontCost),
			DiscountRate:      archeraMoney(e.Delta.DiscountRate),
			BreakevenDays:     archeraMoney(e.Delta.BreakevenDays),
		},
		ArcheraProductSupport: ps,
	}
}

func buildArcheraDTO(c *insurance.Comparison) archeraComparisonDTO {
	dto := archeraComparisonDTO{
		Title:                 archeraTitle,
		PlanID:                archeraSanitize(c.PlanID),
		FetchedAt:             c.FetchedAt.UTC().Format("2006-01-02T15:04:05Z"),
		Currency:              archeraSanitizePtr(c.Currency),
		PremiumIncluded:       true,
		MonthlyBasis:          archeraMonthlyBasis,
		UpfrontBasis:          archeraUpfrontBasis,
		PlanWide:              archeraPlanWideNote,
		Caveat:                archeraCaveat,
		Current:               archeraTotals(c.Current),
		Hypotheticals:         []archeraHypotheticalDTO{},
		Rows:                  []archeraRowDTO{},
		NonGatingDisclosure:   common.ArcheraNonGatingDisclosure,
		SponsorshipDisclosure: common.ArcheraSponsorshipDisclosure,
	}
	for i := range c.Hypotheticals {
		h := &c.Hypotheticals[i]
		hd := archeraHypotheticalDTO{
			ContractTerm:               archeraSanitizePtr(h.ContractTerm),
			PaymentOption:              archeraSanitize(string(h.PaymentOption)),
			Totals:                     archeraTotals(h.Totals),
			DeltaMonthlyNetSavings:     archeraMoney(h.DeltaMonthlyNetSavings),
			DeltaMonthlyCommitmentCost: archeraMoney(h.DeltaMonthlyCommitmentCost),
			DeltaUpfrontCost:           archeraMoney(h.DeltaUpfrontCost),
			LineItems:                  []archeraLineItemDTO{},
		}
		for _, li := range h.LineItems {
			hd.LineItems = append(hd.LineItems, archeraLineItemDTO{
				LineItemID:           archeraSanitize(li.LineItemID),
				ActualTerm:           archeraSanitizePtr(li.ActualTerm),
				ActualPaymentOption:  archeraPayment(li.ActualPaymentOption),
				ActualCommitmentType: archeraSanitizePtr(li.ActualCommitmentType),
				Reason:               archeraSanitize(string(li.Reason)),
			})
		}
		dto.Hypotheticals = append(dto.Hypotheticals, hd)
	}
	for i := range c.Rows {
		r := &c.Rows[i]
		rd := archeraRowDTO{LineItemID: archeraSanitize(r.LineItemID), Current: archeraOffer(r.Current), Candidates: []archeraOfferDTO{}}
		for j := range r.Candidates {
			rd.Candidates = append(rd.Candidates, archeraOffer(r.Candidates[j]))
		}
		dto.Rows = append(dto.Rows, rd)
	}
	return dto
}

func renderArcheraJSON(w io.Writer, dto archeraComparisonDTO) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(dto)
}

func archeraStr(s *string) string {
	if s == nil {
		return "unknown"
	}
	return *s
}

func archeraTotalsLine(t archeraTotalsDTO) string {
	m := t.Monthly
	return fmt.Sprintf("commitment cost %s, cloud provider cost %s, premium %s, gross savings %s, net savings %s, covered on-demand %s (%s); upfront %s (%s)",
		archeraStr(m.CommitmentCostTotal), archeraStr(m.CloudProviderCost), archeraStr(m.Premium), archeraStr(m.GrossSavings),
		archeraStr(m.NetSavings), archeraStr(m.CoveredOnDemandCost), archeraMonthlyBasis, archeraStr(t.UpfrontCost), archeraUpfrontBasis)
}

func renderArcheraTable(w io.Writer, d archeraComparisonDTO) error {
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	p("%s", d.Title)
	p("Plan: %s   Fetched: %s", d.PlanID, d.FetchedAt)
	p("%s", archeraPremiumNote)
	p("%s", d.PlanWide)
	p("%s", d.Caveat)
	p("")
	p("Current totals: %s", archeraTotalsLine(d.Current))
	for i := range d.Hypotheticals {
		h := &d.Hypotheticals[i]
		p("")
		p("Hypothetical %d: term %s, payment %s", i+1, archeraStr(h.ContractTerm), h.PaymentOption)
		p("  totals: %s", archeraTotalsLine(h.Totals))
		p("  delta vs current: monthly net savings %s, monthly commitment cost %s, upfront %s",
			archeraStr(h.DeltaMonthlyNetSavings), archeraStr(h.DeltaMonthlyCommitmentCost), archeraStr(h.DeltaUpfrontCost))
		for _, li := range h.LineItems {
			p("  line item %s: term %s, payment %s, type %s, reason %s", li.LineItemID, archeraStr(li.ActualTerm),
				archeraStr(li.ActualPaymentOption), archeraStr(li.ActualCommitmentType), li.Reason)
		}
	}
	for i := range d.Rows {
		r := &d.Rows[i]
		p("")
		p("Line item %s", r.LineItemID)
		writeArcheraOffer(p, "current", r.Current)
		for j := range r.Candidates {
			writeArcheraOffer(p, "candidate", r.Candidates[j])
		}
	}
	p("")
	p("%s", d.NonGatingDisclosure)
	p("%s", d.SponsorshipDisclosure)
	_, err := io.WriteString(w, b.String())
	return err
}

func writeArcheraOffer(p func(string, ...any), kind string, o archeraOfferDTO) {
	lease := ""
	if o.LeaseAttached {
		lease = ", lease attached"
	}
	p("  %s offer %s: %s %s, region %s, term %s, payment %s%s", kind, o.OfferID, o.Provider, o.CommitmentType,
		archeraStr(o.Region), archeraStr(o.ContractTerm), archeraStr(o.PaymentOption), lease)
	if o.ArcheraOfferName != nil {
		p("    Archera offer name: %s", *o.ArcheraOfferName)
	}
	p("    discount rate %s (0-1 basis), breakeven days %s", archeraStr(o.DiscountRate), archeraStr(o.BreakevenDays))
	p("    %s", archeraTotalsLine(o.Totals))
	p("    delta vs current: monthly net savings %s, upfront %s, discount rate %s, breakeven days %s",
		archeraStr(o.DeltaVsCurrent.MonthlyNetSavings), archeraStr(o.DeltaVsCurrent.UpfrontCost),
		archeraStr(o.DeltaVsCurrent.DiscountRate), archeraStr(o.DeltaVsCurrent.BreakevenDays))
	if o.ArcheraProductSupport.Status == string(insurance.ProductSupportSupported) {
		p("    Archera product support: supported (%s; %s)", o.ArcheraProductSupport.Source, o.ArcheraProductSupport.Evidence)
	} else {
		p("    Archera product support: unknown")
	}
}
