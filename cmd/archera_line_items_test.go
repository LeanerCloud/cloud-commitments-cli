package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArcheraLineItemsRegistration(t *testing.T) {
	rec := setupArchera(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected request") })
	cmd, _, err := rootCmd.Find([]string{"archera-line-items"})
	require.NoError(t, err)
	require.Equal(t, "archera-line-items", cmd.Name())
	require.Empty(t, rec.requests)
}

func listingWire(t *testing.T, number int) string {
	t.Helper()
	body, err := os.ReadFile("testdata/archera_line_item.json")
	require.NoError(t, err)
	id := fmt.Sprintf("%08x-aaaa-4aaa-8aaa-aaaaaaaaaaaa", number)
	if number == 2 {
		id = strings.ToUpper(id)
	}
	return strings.ReplaceAll(string(body), "LINE_ID", id)
}

func listingHandler(t *testing.T, total int, late http.HandlerFunc) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/comparison") {
			okHandler(w, r)
			return
		}
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1/org/"+archeraTestOrg+"/commitment-plans/"+archeraTestPlan+"/line-items", r.URL.Path)
		assert.Equal(t, archeraTestKey, r.Header.Get("x-api-key"))
		page, err := strconv.Atoi(r.URL.Query().Get("page"))
		require.NoError(t, err)
		assert.Equal(t, "100", r.URL.Query().Get("page_size"))
		assert.Equal(t, "id", r.URL.Query().Get("order_by"))
		assert.Len(t, r.URL.Query(), 3)
		pages := (total + 99) / 100
		w.Header().Set("X-Pagination", fmt.Sprintf(`{"total":%d,"total_pages":%d,"page":%d,"first_page":1,"last_page":%d}`, total, pages, page, pages))
		if page == 2 && late != nil {
			late(w, r)
			return
		}
		rows := []string{}
		for i := (page - 1) * 100; i < min(page*100, total); i++ {
			rows = append(rows, listingWire(t, total-i))
		}
		_, _ = w.Write([]byte("[" + strings.Join(rows, ",") + "]"))
	}
}

func runListing(t *testing.T, env map[string]string, args ...string) (string, string, error) {
	t.Helper()
	return runArcheraCommand(t, "archera-line-items", env, args...)
}

func TestArcheraLineItemsWindowsAndComparisonReuse(t *testing.T) {
	for _, tc := range []struct{ offset, limit, total, want, next int }{
		{0, 100, 101, 100, 100}, {1, 1, 101, 1, 2}, {0, 200, 101, 101, -1}, {101, 100, 101, 0, -1}, {102, 100, 101, -1, -1}, {0, 100, 0, 0, -1},
	} {
		t.Run(fmt.Sprint(tc), func(t *testing.T) {
			rec := setupArchera(t, listingHandler(t, tc.total, nil))
			out, stderr, err := runListing(t, fullEnv(), "--format=json", "--offset", strconv.Itoa(tc.offset), "--limit", strconv.Itoa(tc.limit))
			if tc.want < 0 {
				require.ErrorContains(t, err, "--offset")
				assert.Empty(t, out)
				return
			}
			require.NoError(t, err)
			var dto archeraListingPage
			require.NoError(t, json.Unmarshal([]byte(out), &dto))
			assert.EqualValues(t, tc.total, dto.Total)
			assert.EqualValues(t, tc.offset, dto.Offset)
			assert.EqualValues(t, tc.limit, dto.Limit)
			require.Len(t, dto.LineItems, tc.want)
			assert.NotNil(t, dto.LineItems)
			if tc.next < 0 {
				assert.Nil(t, dto.NextOffset)
			} else {
				require.NotNil(t, dto.NextOffset)
				assert.EqualValues(t, tc.next, *dto.NextOffset)
			}
			assert.Contains(t, dto.Note, "no atomic snapshot")
			assert.NotEmpty(t, dto.NonGatingDisclosure)
			assert.NotEmpty(t, dto.SponsorshipDisclosure)
			assert.NotContains(t, out+stderr, archeraTestKey)
			assert.NotContains(t, out, archeraTestPlan)
			assert.NotContains(t, out, "private-account")
			assert.NotContains(t, out, "private-catalog")
			require.Len(t, rec.requests, max(1, (tc.total+99)/100))
			if tc.offset == 1 {
				row := dto.LineItems[0]
				assert.Equal(t, "00000002-AAAA-4AAA-8AAA-AAAAAAAAAAAA", row.LineItemID)
				assert.Equal(t, "9007199254740993", row.SelectedQuantity)
				assert.Equal(t, "0."+strings.Repeat("0", 100)+"1", row.SelectedCommitment)
				assert.Equal(t, "contract-type", row.CommitmentType)
				assert.Equal(t, "no_upfront", row.PaymentOption)
				assert.Nil(t, row.Region)
				assert.Nil(t, row.InstanceFamily)
				assert.Nil(t, row.NativePlanType)
				require.NotNil(t, row.NativeDurationSeconds)
				assert.Equal(t, "31536000.25", *row.NativeDurationSeconds)
				var raw map[string]any
				require.NoError(t, json.Unmarshal([]byte(out), &raw))
				assert.Len(t, raw["line_items"].([]any)[0].(map[string]any), 13)
				_, _, err = runArchera(t, fullEnv(), "--line-item-ids", row.LineItemID)
				require.NoError(t, err)
				require.Len(t, rec.requests, 3)
				assert.Equal(t, []string{row.LineItemID}, rec.requests[2].URL.Query()["line_item_ids"])
			}
		})
	}
}

func TestArcheraLineItemsRejectsBeforeRequestAndDefaultOff(t *testing.T) {
	rec := setupArchera(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected request") })
	for _, args := range [][]string{
		{"--limit=0"}, {"--limit=201"}, {"--limit=-1"}, {"--limit=1.5"}, {"--limit=9223372036854775808"},
		{"--offset=-1"}, {"--offset=1.5"}, {"--offset=9223372036854775808"}, {"--format=xml"},
		{"--org-id=bad"}, {"--plan-id=bad"}, {"--limit=" + archeraTestKey}, {archeraTestKey},
	} {
		out, stderr, err := runListing(t, fullEnv(), args...)
		require.Error(t, err)
		assert.Empty(t, out)
		assert.NotContains(t, stderr+err.Error(), archeraTestKey)
	}
	_, _, err := runListing(t, nil)
	require.ErrorContains(t, err, "ARCHERA_API_KEY")
	for _, setting := range []string{archeraEnvOrgID, archeraEnvPlanID} {
		env := fullEnv()
		delete(env, setting)
		_, _, err = runListing(t, env)
		require.ErrorContains(t, err, setting)
	}
	out, _, err := runListing(t, nil, "--help")
	require.NoError(t, err)
	for _, flag := range []string{"--org-id", "--plan-id", "--format", "--offset", "--limit"} {
		assert.Contains(t, out, flag)
	}
	rootCmd.SetArgs([]string{"configure-gcp", "--help"})
	require.NoError(t, rootCmd.Execute())
	resetHelpFlags()
	assert.Empty(t, rec.requests)
}

func TestArcheraLineItemsLateFailuresNeverPrintPartialRows(t *testing.T) {
	for _, failure := range []string{"http", "decode", "metadata", "duplicate", "size", "cancel", "429", "redirect"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			archeraLineItemsCmd.SetContext(ctx)
			t.Cleanup(func() { archeraLineItemsCmd.SetContext(context.Background()) })
			rec := setupArchera(t, listingHandler(t, 101, func(w http.ResponseWriter, _ *http.Request) {
				switch failure {
				case "http":
					http.Error(w, archeraTestKey, http.StatusInternalServerError)
				case "decode":
					_, _ = w.Write([]byte("[{}]"))
				case "metadata":
					w.Header().Set("X-Pagination", "{}")
					_, _ = w.Write([]byte("[]"))
				case "duplicate":
					_, _ = w.Write([]byte("[" + listingWire(t, 101) + "]"))
				case "size":
					_, _ = w.Write([]byte("[" + listingWire(t, 1) + "]" + strings.Repeat(" ", 8<<20)))
				case "cancel":
					cancel()
				case "429":
					w.Header().Set("Retry-After", "2")
					w.WriteHeader(http.StatusTooManyRequests)
				case "redirect":
					w.Header().Set("Location", "https://api.archera.ai/redirect")
					w.WriteHeader(http.StatusFound)
				}
			}))
			out, stderr, err := runListing(t, fullEnv(), "--format=json", "--limit=1")
			require.Error(t, err)
			assert.Empty(t, out)
			assert.NotContains(t, stderr+err.Error(), archeraTestKey)
			assert.Len(t, rec.requests, 2)
			if failure == "429" {
				assert.Contains(t, err.Error(), "retry after 2s")
			}
			if failure == "redirect" {
				assert.Contains(t, err.Error(), "302")
			}
		})
	}
}

type listingTrip func(*http.Request) (*http.Response, error)

func (f listingTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestArcheraLineItemsAggregateDeadline(t *testing.T) {
	setupArchera(t, listingHandler(t, 101, nil))
	base := archeraHTTPClient.Transport
	var first time.Time
	archeraHTTPClient.Transport = listingTrip(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		require.True(t, ok)
		assert.LessOrEqual(t, time.Until(deadline), 30*time.Second)
		if first.IsZero() {
			first = deadline
		} else {
			assert.Equal(t, first, deadline)
		}
		return base.RoundTrip(r)
	})
	_, _, err := runListing(t, fullEnv(), "--format=json", "--limit=1")
	require.NoError(t, err)
}

func TestArcheraLineItemsPrivacyAndTable(t *testing.T) {
	var logs bytes.Buffer
	oldLog, oldSlog := log.Writer(), slog.Default()
	log.SetOutput(&logs)
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { log.SetOutput(oldLog); slog.SetDefault(oldSlog) })
	for _, format := range []string{"json", "table"} {
		rec := setupArchera(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Pagination", `{"total":1,"total_pages":1,"page":1,"first_page":1,"last_page":1}`)
			wire := strings.Replace(listingWire(t, 1), `"contract-type"`, mustJSON(t, archeraTestKey+"\x1b[31m"+strings.Repeat("x", 500)), 1)
			wire = strings.Replace(wire, "9007199254740993", "0", 1)
			wire = strings.Replace(wire, "0.1e-100", "0", 1)
			_, _ = w.Write([]byte("[" + wire + "]"))
		})
		out, stderr, err := runListing(t, fullEnv(), "--format", format)
		require.NoError(t, err)
		assert.NotContains(t, out+stderr+logs.String(), archeraTestKey)
		assert.NotContains(t, out, "\x1b")
		assert.Contains(t, out, "[redacted]")
		assert.NotContains(t, out, strings.Repeat("x", 257))
		require.Len(t, rec.requests, 1)
		if format == "json" {
			var dto archeraListingPage
			require.NoError(t, json.Unmarshal([]byte(out), &dto))
			assert.Equal(t, "0", dto.LineItems[0].SelectedQuantity)
			assert.Equal(t, "0", dto.LineItems[0].SelectedCommitment)
		} else {
			assert.Contains(t, out, "Total 1; offset 0; limit 100")
			assert.Contains(t, out, "QUANTITY")
		}
	}
	env := fullEnv()
	env[archeraEnvAPIKey] = "00000001-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	setupArchera(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Pagination", `{"total":1,"total_pages":1,"page":1,"first_page":1,"last_page":1}`)
		_, _ = w.Write([]byte("[" + listingWire(t, 1) + "]"))
	})
	out, stderr, err := runListing(t, env, "--format=json")
	require.Error(t, err)
	assert.Empty(t, out)
	assert.NotContains(t, stderr+logs.String()+err.Error(), env[archeraEnvAPIKey])
}
