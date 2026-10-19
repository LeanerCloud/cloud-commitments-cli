package main

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/insurance"
	"github.com/spf13/cobra"
	"golang.org/x/text/unicode/norm"
)

// Archera settings come from the environment, read only inside RunE. The API
// key has no flag by design.
const (
	archeraEnvAPIKey = "ARCHERA_API_KEY" // #nosec G101 -- environment variable name, not a credential
	archeraEnvOrgID  = "ARCHERA_ORG_ID"
	archeraEnvPlanID = "ARCHERA_PLAN_ID"
)

// archeraHTTPClient is nil in production, which selects the hardened default
// client inside pkg/insurance. Tests replace it with a fixed-origin transport.
// It never holds the key; the key lives only in RunE locals.
var archeraHTTPClient *http.Client

var archeraOpts struct {
	OrgID          string
	PlanID         string
	Format         string
	LineItemIDs    []string
	ContractTerms  []string
	PaymentOptions []string
}

var archeraComparisonCmd = &cobra.Command{
	Use:   "archera-comparison",
	Short: "Show a read-only Archera commitment plan comparison",
	Long: `Fetch and print the comparison for one Archera commitment plan.

This command is read-only: it never purchases, binds or submits anything, and
it is independent of --purchase. It makes no Archera request unless it is run.

Configuration (default off; there is no flag for the API key):
  ARCHERA_API_KEY   Archera API key (environment only)
  ARCHERA_ORG_ID    Archera organization UUID (or --org-id)
  ARCHERA_PLAN_ID   Archera commitment plan UUID (or --plan-id)`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runArcheraComparison,
}

func init() {
	rootCmd.AddCommand(archeraComparisonCmd)
	archeraComparisonCmd.Flags().StringVar(&archeraOpts.OrgID, "org-id", "", "Archera organization UUID (default: $"+archeraEnvOrgID+")")
	archeraComparisonCmd.Flags().StringVar(&archeraOpts.PlanID, "plan-id", "", "Archera commitment plan UUID (default: $"+archeraEnvPlanID+")")
	archeraComparisonCmd.Flags().StringVar(&archeraOpts.Format, "format", "table", "Output format: table or json")
	archeraComparisonCmd.Flags().StringArrayVar(&archeraOpts.LineItemIDs, "line-item-ids", nil, "Archera line-item UUIDs, comma-separated or repeated (max 200)")
	archeraComparisonCmd.Flags().StringArrayVar(&archeraOpts.ContractTerms, "contract-terms", nil, fmt.Sprintf("Exact Archera contract terms, comma-separated or repeated (max %d)", len(insurance.ContractTerms())))
	archeraComparisonCmd.Flags().StringArrayVar(&archeraOpts.PaymentOptions, "payment-options", nil, "Payment options: no_upfront, partial_upfront, all_upfront (comma-separated or repeated; max 3)")
}

// archeraSetting returns the flag value, else the environment value, trimmed.
func archeraSetting(flagValue, envName string) string {
	if v := strings.TrimSpace(flagValue); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv(envName))
}

func runArcheraComparison(cmd *cobra.Command, _ []string) error {
	format := archeraOpts.Format
	if format != "table" && format != "json" {
		return errors.New("--format must be table or json")
	}
	key, orgID, planID, err := archeraSettings(archeraOpts.OrgID, archeraOpts.PlanID)
	if err != nil {
		return err
	}

	req, err := archeraComparisonRequest(planID)
	if err != nil {
		return err
	}

	// Config and client stay local to this call; never store them in a
	// package variable or struct.
	client, err := insurance.NewClient(insurance.Config{APIKey: insurance.NewSecret(key), OrgID: orgID}, archeraHTTPClient)
	if err != nil {
		return err
	}
	comparison, err := client.Comparison(cmd.Context(), req)
	if err != nil {
		return archeraSafeError(err, key)
	}
	dto := buildArcheraDTO(comparison)
	if format == "json" {
		return renderArcheraJSON(cmd.OutOrStdout(), dto)
	}
	return renderArcheraTable(cmd.OutOrStdout(), dto)
}

func archeraComparisonRequest(planID string) (insurance.ComparisonRequest, error) {
	req := insurance.ComparisonRequest{PlanID: planID}
	filters := []struct {
		values *[]string
		flag   string
		raw    []string
		limit  int
	}{
		{&req.LineItemIDs, "line-item-ids", archeraOpts.LineItemIDs, 200},
		{&req.ContractTerms, "contract-terms", archeraOpts.ContractTerms, len(insurance.ContractTerms())},
	}
	for _, filter := range filters {
		values, err := archeraFilterValues(filter.flag, filter.raw, filter.limit)
		if err != nil {
			return req, err
		}
		*filter.values = values
	}
	payments, err := archeraFilterValues("payment-options", archeraOpts.PaymentOptions, 3)
	if err != nil {
		return req, err
	}
	for _, payment := range payments {
		req.PaymentOptions = append(req.PaymentOptions, insurance.PaymentOption(payment))
	}
	return req, nil
}

func archeraFilterValues(flag string, raw []string, limit int) ([]string, error) {
	var values []string
	for occurrence, entry := range raw {
		for segment, value := range strings.Split(entry, ",") {
			if strings.TrimSpace(value) == "" {
				return nil, fmt.Errorf("--%s occurrence %d entry %d must not be empty", flag, occurrence+1, segment+1)
			}
			if len(values) >= limit {
				return nil, fmt.Errorf("--%s accepts at most %d entries", flag, limit)
			}
			values = append(values, value)
		}
	}
	return values, nil
}

// archeraSettings reads the key, org ID and plan ID, naming (never echoing)
// the first missing setting. The key is returned to RunE only.
func archeraSettings(orgFlag, planFlag string) (key, orgID, planID string, err error) {
	key = strings.TrimSpace(os.Getenv(archeraEnvAPIKey))
	if key == "" {
		return "", "", "", fmt.Errorf("%s is not set (read from the environment only)", archeraEnvAPIKey)
	}
	orgID = archeraSetting(orgFlag, archeraEnvOrgID)
	if orgID == "" {
		return "", "", "", fmt.Errorf("archera org ID is not set (--org-id or %s)", archeraEnvOrgID)
	}
	planID = archeraSetting(planFlag, archeraEnvPlanID)
	if planID == "" {
		return "", "", "", fmt.Errorf("archera plan ID is not set (--plan-id or %s)", archeraEnvPlanID)
	}
	return key, orgID, planID, nil
}

var archeraQuotedEscapeRE = regexp.MustCompile(`\\(?:u[0-9a-fA-F]{4}|U[0-9a-fA-F]{8}|x[0-9a-fA-F]{2})`)

// archeraWithheld replaces vendor text in which the key is still readable
// after masking.
const archeraWithheld = "[message withheld: contained the API key]"

// archeraSafeError renders a client error as bounded, terminal-safe text.
// HTTP errors keep their status and Retry-After. Decode errors echo the
// rejected vendor value. Both are vendor-controlled, so the order is: clean
// the text (controls, format and separator characters removed, which would
// otherwise let a key split by U+200B slip past an exact-match mask), mask the
// trimmed key, check the full text for a key hidden behind characters the
// clean leaves in place (combining marks, variation selectors, fillers,
// invalid bytes) and withhold it if one is found, then cap.
func archeraSafeError(err error, key string) error {
	key = strings.TrimSpace(key)
	msg, suffix := err.Error(), ""
	// The shared validator echoes rejected input; keep its validation but omit values.
	switch {
	case strings.HasPrefix(msg, "archera line item ID "):
		msg = "--line-item-ids entries must be canonical UUIDs"
	case strings.HasPrefix(msg, "unsupported contract term "):
		msg = "--contract-terms entries must be supported Archera contract terms (exact case)"
	case strings.HasPrefix(msg, "payment option: "):
		msg = "--payment-options entries must be no_upfront, partial_upfront or all_upfront (exact case)"
	}
	var httpErr *insurance.HTTPError
	isHTTP := errors.As(err, &httpErr)
	if isHTTP && httpErr.RetryAfter > 0 {
		suffix = fmt.Sprintf(" (retry after %s; not retried automatically)", httpErr.RetryAfter)
	}
	// pkg decode errors quote the rejected value with %q, which spells control
	// and format characters as literal \uXXXX text; drop those escapes too so
	// they cannot split the key.
	msg = archeraQuotedEscapeRE.ReplaceAllString(archeraSanitizeN(msg+suffix, math.MaxInt), "")
	if key != "" {
		msg = strings.ReplaceAll(msg, key, "[redacted]")
		if archeraKeyReadable(msg, key) {
			msg = archeraWithheld
			if isHTTP {
				msg = fmt.Sprintf("archera API returned HTTP %d: %s%s", httpErr.StatusCode, archeraWithheld, suffix)
			}
		}
	}
	return errors.New(archeraSanitizeN(msg, archeraMaxErrorSize))
}

// archeraFragmentLen is the longest key fragment that may remain readable in
// vendor text. The pkg cuts HTTP messages at 200 bytes before the CLI sees
// them, so a split key can arrive as a prefix rather than whole.
const archeraFragmentLen = 8

// archeraMaxGap is how many other runes may sit at one irregular spot between
// two consecutive key characters before the text stops reading as the key.
const archeraMaxGap = 2

// archeraMaxSep is the longest separator, in key-alphabet runes, that may be
// repeated between every pair of key characters (such as "-- -" or "0x0").
const archeraMaxSep = 16

// archeraCheckSlack is how far past the displayed cap the readability check
// looks, so a key cut by the cap is still seen whole. It also bounds the work:
// the check never reads more than archeraMaxErrorSize + archeraCheckSlack bytes.
const archeraCheckSlack = archeraFragmentLen * (archeraMaxSep + 1) * utf8.UTFMax

// archeraFold maps text to the form a reader would compare: NFKD (fullwidth
// to ASCII, accents split off as droppable marks) then lower case.
func archeraFold(s string) string { return strings.ToLower(norm.NFKD.String(s)) }

// archeraKeyReadable reports whether the displayable part of msg shows
// min(len(key), archeraFragmentLen) consecutive key characters, either
// separated by one repeated separator or in order with at most archeraMaxGap
// other characters at each spot. Both sides are folded, and msg is reduced to
// the runes that occur in the key, so any inserted character class drops out.
// The key's own non-graphic and space runes are ignored, as the reader does.
// Look-alike letters from other scripts are not handled.
func archeraKeyReadable(msg, key string) bool {
	var ks []rune
	alphabet := map[rune]bool{}
	for _, r := range archeraFold(key) {
		if unicode.IsGraphic(r) && !unicode.IsSpace(r) {
			ks = append(ks, r)
			alphabet[r] = true
		}
	}
	if len(ks) == 0 {
		return true
	}
	var ms []rune
	for _, r := range archeraFold(archeraSanitizeN(msg, archeraMaxErrorSize+archeraCheckSlack)) {
		if alphabet[r] {
			ms = append(ms, r)
		}
	}
	n := min(len(ks), archeraFragmentLen)
	for i := 0; i+n <= len(ks); i++ {
		run := ks[i : i+n]
		if archeraHasRepeatedSeparatorRun(ms, run) || archeraHasGappedRun(ms, run) {
			return true
		}
	}
	return false
}

// archeraHasRepeatedSeparatorRun reports whether ms spells run with the same
// separator (possibly empty, up to archeraMaxSep runes) between every pair of
// neighbors. The separator is read off the first pair.
func archeraHasRepeatedSeparatorRun(ms, run []rune) bool {
	if len(run) < 2 {
		return false
	}
	for i, r := range ms {
		if r != run[0] {
			continue
		}
		for j := i + 1; j < len(ms) && j-i-1 <= archeraMaxSep; j++ {
			if ms[j] == run[1] && spellsWithSeparator(ms[i:], run, ms[i+1:j]) {
				return true
			}
		}
	}
	return false
}

func spellsWithSeparator(ms, run, sep []rune) bool {
	pos := 0
	for k, r := range run {
		if k > 0 {
			if pos+len(sep) > len(ms) || !slices.Equal(ms[pos:pos+len(sep)], sep) {
				return false
			}
			pos += len(sep)
		}
		if pos >= len(ms) || ms[pos] != r {
			return false
		}
		pos++
	}
	return true
}

// archeraHasGappedRun reports whether run occurs in ms as a subsequence with
// at most archeraMaxGap skipped runes between neighbors. Gap 0 is a plain
// substring match. It is O(len(ms)*len(run)).
func archeraHasGappedRun(ms, run []rune) bool {
	reach := make([]bool, len(ms))
	for i, r := range ms {
		reach[i] = r == run[0]
	}
	for _, want := range run[1:] {
		next := make([]bool, len(ms))
		for j, r := range ms {
			if r != want {
				continue
			}
			for g := 0; g <= archeraMaxGap && j-1-g >= 0; g++ {
				if reach[j-1-g] {
					next[j] = true
					break
				}
			}
		}
		reach = next
	}
	return slices.Contains(reach, true)
}
