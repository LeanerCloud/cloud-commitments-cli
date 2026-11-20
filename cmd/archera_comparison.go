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
// Look-alike letters from other scripts (Cyrillic Т, dotless ı) are a known
// limit, pinned by TestArcheraConfusablesAreKnownLimit.
func archeraKeyReadable(msg, key string) bool {
	budget := archeraSearchBudget
	return archeraKeyReadableWithin(msg, key, &budget)
}

// archeraKeyReadableWithin is archeraKeyReadable spending from a caller budget.
func archeraKeyReadableWithin(msg, key string, budget *int) bool {
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
		if archeraRunReadable(ms, run, budget) {
			return true
		}
	}
	return false
}

// archeraRunReadable reports whether ms shows run under any of the three rules.
// budget is the shared separator-search step allowance (archeraSearchBudget).
func archeraRunReadable(ms, run []rune, budget *int) bool {
	cms, crun := archeraCollapseRepeats(ms), archeraCollapseRepeats(run)
	return archeraHasRepeatedSeparatorRun(ms, run) || archeraHasGappedRun(ms, run) ||
		archeraHasMixedSeparatorRun(cms, crun) || archeraHasSharedSetRun(cms, crun, budget)
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

// archeraCollapseRepeats drops a rune equal to its predecessor, so a separator
// of any length made of one repeated rune counts as one.
func archeraCollapseRepeats(rs []rune) []rune {
	out := make([]rune, 0, len(rs))
	for i, r := range rs {
		if i == 0 || r != rs[i-1] {
			out = append(out, r)
		}
	}
	return out
}

// archeraSearchBudget caps the total sweep steps (one step is one rune of ms
// visited) the shared-set search may spend across all windows of one message.
// A step costs about 3 to 7 ns (slice lookups only), so 2e8 is roughly 0.6 s on
// the error path. A realistic ~1 KB message built from a 41-rune key needs about
// 8e7 steps over its 34 windows (TestArcheraRealisticMessageStaysWithinBudget),
// so ordinary errors stay shown; a stuttered key keeps many sets alive and would
// reach 4e8 steps or more, and an exhausted budget withholds (fail-closed).
const archeraSearchBudget = 200_000_000

// archeraMaxSepRunes is how many distinct runes the gaps of a shared-set
// spelling may draw from.
const archeraMaxSepRunes = 3

// archeraHasMixedSeparatorRun reports whether ms spells run with gaps of up to
// archeraMaxSep runes that are all drawn from the at most two distinct runes of
// the first gap, so separators that alternate in length or shape ("-" then
// "---", "0x0" then "x0x0") still read as the key.
func archeraHasMixedSeparatorRun(ms, run []rune) bool {
	if len(run) < 2 {
		return false
	}
	for i, r := range ms {
		if r != run[0] {
			continue
		}
		sep := map[rune]bool{}
		for j := i + 1; j < len(ms) && j-i-1 <= archeraMaxSep; j++ {
			if ms[j] == run[1] && spellsWithinSeparatorSet(ms, j, run[2:], sep) {
				return true
			}
			sep[ms[j]] = true
			if len(sep) > 2 {
				break
			}
		}
	}
	return false
}

// spellsWithinSeparatorSet reports whether rest follows ms[at], each rune after
// a gap of at most archeraMaxSep runes all in sep.
func spellsWithinSeparatorSet(ms []rune, at int, rest []rune, sep map[rune]bool) bool {
	reach := map[int]bool{at: true}
	for _, want := range rest {
		next := map[int]bool{}
		for p := range reach {
			for q := p + 1; q < len(ms) && q-p-1 <= archeraMaxSep; q++ {
				if ms[q] == want {
					next[q] = true
				}
				if !sep[ms[q]] {
					break
				}
			}
		}
		if len(next) == 0 {
			return false
		}
		reach = next
	}
	return true
}

// archeraHasSharedSetRun reports whether ms spells run with gaps of any length
// all drawn from one shared set of at most archeraMaxSepRunes runes, at least
// two of them non-empty, so separators that alternate in shape ("hgh" then
// "fhf") are read as the key. It only adds to archeraHasMixedSeparatorRun.
//
// Only full-size sets (a smaller one is covered by a superset) holding a rune
// that directly follows a run rune are tried, each once. With d distinct runes
// in ms that is at most C(d, 3) sweeps per window, each O(len(ms)*len(run)) and
// allocation-free, charged against budget; a spent budget reports true.
func archeraHasSharedSetRun(ms, run []rune, budget *int) bool {
	if len(run) < 3 {
		return false
	}
	d, msIDs, runIDs, ok := archeraRuneIDs(ms, run)
	if !ok {
		return false
	}
	starts, seeds := archeraSharedSetStartsAndSeeds(msIDs, runIDs, d)
	if len(starts) == 0 {
		return false
	}
	pool := make([]int, d)
	for i := range pool {
		pool[i] = i
	}
	sep := make([]bool, d)
	bufs := [2][]int8{make([]int8, len(ms)), make([]int8, len(ms))}
	try := func() bool {
		return archeraSharedSetSweep(msIDs, starts, runIDs[1:], sep, bufs, budget) || *budget < 0
	}
	size := min(archeraMaxSepRunes, d)
	for _, g := range seeds {
		pool = slices.DeleteFunc(slices.Clone(pool), func(v int) bool { return v == g })
		sep[g] = true
		ok := archeraAnySeparatorSet(pool, size-1, sep, try)
		sep[g] = false
		if ok {
			return true
		}
	}
	return false
}

// archeraRuneIDs numbers the distinct runes of ms from 0 and returns that
// count with ms and run as numbers; ok is false when a run rune is not in ms.
func archeraRuneIDs(ms, run []rune) (d int, msIDs, runIDs []int, ok bool) {
	id := map[rune]int{}
	msIDs = make([]int, len(ms))
	for i, r := range ms {
		v, seen := id[r]
		if !seen {
			v = len(id)
			id[r] = v
		}
		msIDs[i] = v
	}
	runIDs = make([]int, len(run))
	for i, r := range run {
		v, found := id[r]
		if !found {
			return 0, nil, nil, false
		}
		runIDs[i] = v
	}
	return len(id), msIDs, runIDs, true
}

// archeraSharedSetStartsAndSeeds returns the positions of run[0] and the
// distinct runes that directly follow a run rune, in first-seen order.
func archeraSharedSetStartsAndSeeds(ms, run []int, d int) (starts, seeds []int) {
	inRun := make([]bool, d)
	for _, v := range run {
		inRun[v] = true
	}
	seen := make([]bool, d)
	for i, v := range ms {
		if v == run[0] {
			starts = append(starts, i)
		}
		if i > 0 && inRun[ms[i-1]] && !seen[v] {
			seen[v] = true
			seeds = append(seeds, v)
		}
	}
	return starts, seeds
}

// archeraAnySeparatorSet calls try with sep extended by every n-choice from
// pool and reports whether any call returned true. A pool smaller than n yields none.
func archeraAnySeparatorSet(pool []int, n int, sep []bool, try func() bool) bool {
	if n == 0 {
		return try()
	}
	for i := 0; i+n <= len(pool); i++ {
		sep[pool[i]] = true
		ok := archeraAnySeparatorSet(pool[i+1:], n-1, sep, try)
		sep[pool[i]] = false
		if ok {
			return true
		}
	}
	return false
}

// archeraSharedSetSweep reports whether rest follows any of starts, each rune
// after a gap of any length made only of runes in sep, with at least two gaps
// non-empty. bufs are scratch of len(ms).
func archeraSharedSetSweep(ms, starts, rest []int, sep []bool, bufs [2][]int8, budget *int) bool {
	reach, next := bufs[0], bufs[1]
	for i := range reach {
		reach[i] = -1
	}
	for _, j := range starts {
		reach[j] = 0
	}
	for _, want := range rest {
		if *budget -= len(ms); *budget < 0 || !archeraSharedSetStep(ms, reach, next, want, sep) {
			return false
		}
		reach, next = next, reach
	}
	return slices.Max(reach) >= 2
}

// archeraSharedSetStep fills next with, for each position of want, the most
// non-empty gaps (capped at 2) on a path from reach through sep gaps, or -1,
// and reports whether any position is reachable.
func archeraSharedSetStep(ms []int, reach, next []int8, want int, sep []bool) bool {
	// m: best reach[p] over p < q with ms[p+1:q] all in sep; prevM is m one step ago.
	var m, prevM int8 = -1, -1
	any := false
	for q, r := range ms {
		val := int8(-1)
		if q > 0 {
			val = reach[q-1]
			if prevM >= 0 && sep[ms[q-1]] {
				val = max(val, min(prevM+1, 2))
			}
		}
		if r != want {
			val = -1
		}
		next[q] = val
		any = any || val >= 0
		prevM = m
		m = reach[q]
		if sep[r] {
			m = max(m, prevM)
		}
	}
	return any
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
