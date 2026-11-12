package main

import (
	"encoding/json"
	"errors"
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
	archeraMaxErrorSize = 512
	archeraMaxMoneyPrec = 164
)

// archeraSanitize strips control characters (ESC, C1 and the rest of
// unicode.IsControl), format characters (bidi overrides, zero-width) and line
// or paragraph separators from a vendor string and caps it at
// archeraMaxFieldSize bytes on a rune boundary, so it is safe to print to a
// terminal.
func archeraSanitize(s string) string { return archeraSanitizeN(s, archeraMaxFieldSize) }

func archeraSanitizeN(s string, maxBytes int) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > maxBytes {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

type archeraMapper struct {
	err error
	key insurance.Secret
}

var errArcheraCredential = errors.New("archera comparison withheld: credential material in vendor data")

// Mirrors cloud-commitments-mcp tools/archera_comparison.go's whole-value policy.
func archeraSkeleton(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case utf8.RuneError, 0x115F, 0x1160, 0x3164, 0xFFA0:
			continue
		}
		if !unicode.In(r, unicode.Cc, unicode.Cf, unicode.Mn, unicode.Me, unicode.Zl, unicode.Zp, unicode.Zs) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (m *archeraMapper) check(s string) {
	key := strings.TrimSpace(m.key.Reveal())
	if m.err == nil && key != "" && strings.Contains(archeraSkeleton(s), archeraSkeleton(key)) {
		m.err = errArcheraCredential
	}
}

func (m *archeraMapper) clean(s string) string {
	m.check(s)
	return archeraSanitize(s)
}

func (m *archeraMapper) money(r *big.Rat) *string {
	s := archeraMoney(r)
	if s != nil {
		m.check(*s)
	}
	return s
}

func (m *archeraMapper) ptr(s *string) *string {
	if s == nil {
		return nil
	}
	v := m.clean(*s)
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

type archeraOfferDTO struct { //nolint:govet // fieldalignment: field order is the JSON output contract (testdata/archera_json.golden)
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
type archeraComparisonDTO struct { //nolint:govet // fieldalignment: field order is the JSON output contract (testdata/archera_json.golden)
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

func (m *archeraMapper) financials(f insurance.Financials) archeraFinancialsDTO {
	return archeraFinancialsDTO{
		CommitmentCostTotal: m.money(f.CommitmentCostTotal),
		CloudProviderCost:   m.money(f.CloudProviderCost),
		Premium:             m.money(f.Premium),
		GrossSavings:        m.money(f.GrossSavings),
		NetSavings:          m.money(f.NetSavings),
		CoveredOnDemandCost: m.money(f.CoveredOnDemandCost),
	}
}

func (m *archeraMapper) totals(t insurance.Totals) archeraTotalsDTO {
	return archeraTotalsDTO{Monthly: m.financials(t.Monthly), UpfrontCost: m.money(t.UpfrontCost)}
}

func (m *archeraMapper) payment(p *insurance.PaymentOption) *string {
	if p == nil {
		return nil
	}
	s := m.clean(string(*p))
	return &s
}

func (m *archeraMapper) offer(e insurance.OfferEntry) archeraOfferDTO {
	support := insurance.AssessProductSupport(e.Provider, e.CommitmentType)
	ps := archeraProductSupportDTO{Status: string(support.Status)}
	if support.Status == insurance.ProductSupportSupported {
		ps.Source, ps.Evidence = support.Source, support.Evidence
	}
	return archeraOfferDTO{
		IsCurrent:        e.IsCurrent,
		OfferID:          m.clean(e.OfferID),
		CommitmentType:   m.clean(e.CommitmentType),
		Provider:         m.clean(string(e.Provider)),
		Region:           m.ptr(e.Region),
		ContractTerm:     m.ptr(e.ContractTerm),
		PaymentOption:    m.payment(e.PaymentOption),
		LeaseAttached:    e.LeaseBacked(),
		ArcheraOfferName: m.ptr(e.GuaranteedDisplayName),
		DiscountRate:     m.money(e.DiscountRate),
		BreakevenDays:    m.money(e.BreakevenDays),
		Totals:           archeraTotalsDTO{Monthly: m.financials(e.Monthly), UpfrontCost: m.money(e.UpfrontCost)},
		DeltaVsCurrent: archeraDeltaDTO{
			MonthlyNetSavings: m.money(e.Delta.MonthlyNetSavings),
			UpfrontCost:       m.money(e.Delta.UpfrontCost),
			DiscountRate:      m.money(e.Delta.DiscountRate),
			BreakevenDays:     m.money(e.Delta.BreakevenDays),
		},
		ArcheraProductSupport: ps,
	}
}

func buildArcheraDTO(c *insurance.Comparison, key insurance.Secret) (archeraComparisonDTO, error) {
	m := &archeraMapper{key: key}
	dto := archeraComparisonDTO{
		Title:                 archeraTitle,
		PlanID:                m.clean(c.PlanID),
		FetchedAt:             c.FetchedAt.UTC().Format("2006-01-02T15:04:05Z"),
		Currency:              m.ptr(c.Currency),
		PremiumIncluded:       true,
		MonthlyBasis:          archeraMonthlyBasis,
		UpfrontBasis:          archeraUpfrontBasis,
		PlanWide:              archeraPlanWideNote,
		Caveat:                archeraCaveat,
		Current:               m.totals(c.Current),
		Hypotheticals:         []archeraHypotheticalDTO{},
		Rows:                  []archeraRowDTO{},
		NonGatingDisclosure:   common.ArcheraNonGatingDisclosure,
		SponsorshipDisclosure: common.ArcheraSponsorshipDisclosure,
	}
	for i := range c.Hypotheticals {
		h := &c.Hypotheticals[i]
		hd := archeraHypotheticalDTO{
			ContractTerm:               m.ptr(h.ContractTerm),
			PaymentOption:              m.clean(string(h.PaymentOption)),
			Totals:                     m.totals(h.Totals),
			DeltaMonthlyNetSavings:     m.money(h.DeltaMonthlyNetSavings),
			DeltaMonthlyCommitmentCost: m.money(h.DeltaMonthlyCommitmentCost),
			DeltaUpfrontCost:           m.money(h.DeltaUpfrontCost),
			LineItems:                  []archeraLineItemDTO{},
		}
		for _, li := range h.LineItems {
			hd.LineItems = append(hd.LineItems, archeraLineItemDTO{
				LineItemID:           m.clean(li.LineItemID),
				ActualTerm:           m.ptr(li.ActualTerm),
				ActualPaymentOption:  m.payment(li.ActualPaymentOption),
				ActualCommitmentType: m.ptr(li.ActualCommitmentType),
				Reason:               m.clean(string(li.Reason)),
			})
		}
		dto.Hypotheticals = append(dto.Hypotheticals, hd)
	}
	for i := range c.Rows {
		r := &c.Rows[i]
		rd := archeraRowDTO{LineItemID: m.clean(r.LineItemID), Current: m.offer(r.Current), Candidates: []archeraOfferDTO{}}
		for j := range r.Candidates {
			rd.Candidates = append(rd.Candidates, m.offer(r.Candidates[j]))
		}
		dto.Rows = append(dto.Rows, rd)
	}
	if m.err != nil {
		return archeraComparisonDTO{}, m.err
	}
	return dto, nil
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
