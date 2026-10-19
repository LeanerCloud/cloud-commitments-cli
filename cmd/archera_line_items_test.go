package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

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
