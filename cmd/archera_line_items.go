package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/insurance"
	"github.com/spf13/cobra"
)

const archeraListingNote = "Local pages repeat a complete vendor read; no atomic snapshot is guaranteed. Vendor UUIDs do not establish recommendation or cross-account association. Selection is not a quote or purchase; no currency is supplied."

var archeraListingOpts struct {
	OrgID, PlanID, Format string
	Offset, Limit         int64
}

var archeraLineItemsCmd = &cobra.Command{
	Use: "archera-line-items", Short: "List read-only Archera plan line-item identities",
	Long: "Fetch all vendor pages, then print a local window of line items. Configuration: ARCHERA_API_KEY (environment only), ARCHERA_ORG_ID/--org-id, ARCHERA_PLAN_ID/--plan-id. No request occurs unless explicitly invoked.",
	Args: func(_ *cobra.Command, args []string) error {
		if len(args) > 0 {
			return errors.New("archera-line-items accepts no positional arguments")
		}
		return nil
	}, SilenceUsage: true, SilenceErrors: true, RunE: runArcheraLineItems,
}

func init() {
	rootCmd.AddCommand(archeraLineItemsCmd)
	archeraLineItemsCmd.SetFlagErrorFunc(func(_ *cobra.Command, _ error) error {
		return errors.New("invalid archera-line-items flag; use --help for integer bounds and options")
	})
	f := archeraLineItemsCmd.Flags()
	f.StringVar(&archeraListingOpts.OrgID, "org-id", "", "Archera organization UUID (default: $ARCHERA_ORG_ID)")
	f.StringVar(&archeraListingOpts.PlanID, "plan-id", "", "Archera plan UUID (default: $ARCHERA_PLAN_ID)")
	f.StringVar(&archeraListingOpts.Format, "format", "table", "Output format: table or json")
	f.Int64Var(&archeraListingOpts.Offset, "offset", 0, "Local output offset, nonnegative; all vendor pages are fetched")
	f.Int64Var(&archeraListingOpts.Limit, "limit", 100, "Local output limit, 1..200; all vendor pages are fetched")
}

type archeraListingRow struct {
	Region                *string `json:"region"`
	InstanceFamily        *string `json:"instance_family"`
	NativePlanType        *string `json:"native_plan_type"`
	NativeDurationSeconds *string `json:"native_duration_seconds"`
	LineItemID            string  `json:"line_item_id"`
	OfferID               string  `json:"offer_id"`
	SelectedQuantity      string  `json:"selected_quantity"`
	SelectedCommitment    string  `json:"selected_commitment"`
	ContractTerm          string  `json:"contract_term"`
	PaymentOption         string  `json:"payment_option"`
	Provider              string  `json:"provider"`
	CommitmentType        string  `json:"commitment_type"`
	IsSelected            bool    `json:"is_selected"`
}

type archeraListingPage struct {
	NextOffset            *int64              `json:"next_offset"`
	NonGatingDisclosure   string              `json:"non_gating_disclosure"`
	SponsorshipDisclosure string              `json:"sponsorship_disclosure"`
	Note                  string              `json:"note"`
	LineItems             []archeraListingRow `json:"line_items"`
	Total                 int64               `json:"total"`
	Offset                int64               `json:"offset"`
	Limit                 int64               `json:"limit"`
}

func archeraListingText(s, key string) string {
	s = strings.ReplaceAll(archeraSanitizeN(s, math.MaxInt), key, "[redacted]")
	if archeraKeyReadable(s, key) {
		return archeraWithheld
	}
	return archeraSanitize(s)
}

func archeraListingRowDTO(row *insurance.LineItem, key string) (archeraListingRow, error) {
	if archeraListingText(row.ID, key) != row.ID || archeraListingText(row.OfferID, key) != row.OfferID {
		return archeraListingRow{}, errors.New("line-item identities cannot be safely displayed")
	}
	text := func(s string) string { return archeraListingText(s, key) }
	nullable := func(s *string) *string {
		if s == nil {
			return nil
		}
		value := text(*s)
		return &value
	}
	return archeraListingRow{
		LineItemID: row.ID, OfferID: row.OfferID, IsSelected: row.IsSelected,
		SelectedQuantity:   strconv.FormatInt(row.SelectedQuantity, 10),
		SelectedCommitment: *archeraMoney(row.SelectedCommitment),
		ContractTerm:       text(row.ContractTerm), PaymentOption: text(string(row.PaymentOption)),
		Provider: text(string(row.Offer.Provider)), CommitmentType: text(row.ContractSpec.CommitmentType),
		Region: nullable(row.Offer.Region), InstanceFamily: nullable(row.Offer.InstanceFamily),
		NativePlanType: nullable(row.Offer.PlanType), NativeDurationSeconds: archeraMoney(row.Offer.DurationSeconds),
	}, nil
}

func archeraFetchLineItems(cmd *cobra.Command, key, org, plan string) ([]insurance.LineItem, error) {
	client, err := insurance.NewClient(insurance.Config{APIKey: insurance.NewSecret(key), OrgID: org}, archeraHTTPClient)
	if err != nil {
		return nil, archeraSafeError(err, key)
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	rows, err := client.LineItems(ctx, plan)
	if err != nil {
		return nil, archeraSafeError(err, key)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	return rows, nil
}

func archeraListingWindow(rows []insurance.LineItem, offset, limit int64, key string) (archeraListingPage, error) {
	total := int64(len(rows))
	if offset > total {
		return archeraListingPage{}, errors.New("--offset exceeds the total line-item count")
	}
	slices.SortFunc(rows, func(a, b insurance.LineItem) int {
		return strings.Compare(strings.ToLower(a.ID), strings.ToLower(b.ID))
	})
	end := offset + min(limit, total-offset)
	page := archeraListingPage{
		Total: total, Offset: offset, Limit: limit, LineItems: []archeraListingRow{},
		NonGatingDisclosure:   common.ArcheraNonGatingDisclosure,
		SponsorshipDisclosure: common.ArcheraSponsorshipDisclosure, Note: archeraListingNote,
	}
	if end < total {
		page.NextOffset = &end
	}
	for i := offset; i < end; i++ {
		dto, err := archeraListingRowDTO(&rows[i], key)
		if err != nil {
			return archeraListingPage{}, err
		}
		page.LineItems = append(page.LineItems, dto)
	}
	return page, nil
}

func archeraListingRender(page *archeraListingPage, format, key string) ([]byte, error) {
	output, err := json.Marshal(page)
	if err != nil {
		return nil, err
	}
	if archeraKeyReadable(string(output), key) {
		return nil, errors.New("line-item output cannot be safely displayed")
	}
	if format == "table" {
		output = archeraListingTable(page)
	}
	if len(output) > 512*1024 {
		return nil, errors.New("line-item output exceeds 512 KiB; choose a smaller --limit")
	}
	return output, nil
}

func runArcheraLineItems(cmd *cobra.Command, _ []string) error {
	o := archeraListingOpts
	if o.Format != "table" && o.Format != "json" {
		return errors.New("--format must be table or json")
	}
	if o.Offset < 0 || o.Limit < 1 || o.Limit > 200 {
		return errors.New("--offset must be nonnegative and --limit must be 1..200")
	}
	key, org, plan, err := archeraSettings(o.OrgID, o.PlanID)
	if err != nil {
		return err
	}
	rows, err := archeraFetchLineItems(cmd, key, org, plan)
	if err != nil {
		return err
	}
	page, err := archeraListingWindow(rows, o.Offset, o.Limit, key)
	if err != nil {
		return err
	}
	output, err := archeraListingRender(&page, o.Format, key)
	if err != nil {
		return err
	}
	_, err = cmd.OutOrStdout().Write(append(output, '\n'))
	return err
}

func archeraListingTable(page *archeraListingPage) []byte {
	var out bytes.Buffer
	_, _ = fmt.Fprintf(&out, "Total %d; offset %d; limit %d; next %s\n%s\n%s\n%s\n", page.Total, page.Offset, page.Limit, archeraListingNext(page.NextOffset), page.Note, page.NonGatingDisclosure, page.SponsorshipDisclosure)
	_, _ = fmt.Fprintln(&out, "LINE ITEM\tOFFER\tSELECTED\tQUANTITY\tCOMMITMENT\tTERM\tPAYMENT\tPROVIDER\tTYPE\tREGION\tFAMILY\tNATIVE PLAN\tNATIVE SECONDS")
	for i := range page.LineItems {
		row := &page.LineItems[i]
		_, _ = fmt.Fprintf(&out, "%s\t%s\t%t\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", row.LineItemID, row.OfferID, row.IsSelected, row.SelectedQuantity, row.SelectedCommitment, row.ContractTerm, row.PaymentOption, row.Provider, row.CommitmentType, archeraStr(row.Region), archeraStr(row.InstanceFamily), archeraStr(row.NativePlanType), archeraStr(row.NativeDurationSeconds))
	}
	return out.Bytes()
}

func archeraListingNext(next *int64) string {
	if next == nil {
		return "none"
	}
	return strconv.FormatInt(*next, 10)
}
