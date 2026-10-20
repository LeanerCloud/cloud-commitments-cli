package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArcheraErrorsMaskObfuscatedKey(t *testing.T) {
	k := archeraTestKey
	split := func(sep string) string { return k[:5] + sep + k[5:] }
	pad := strings.Repeat("p", archeraMaxErrorSize-len("archera API returned HTTP 403: ")-len(k)/2)
	cases := map[string]string{
		"zero-width split":  "bad key " + split("\u200b"),
		"control split":     "bad key " + split("\u0001"),
		"bidi split":        "bad key " + split("\u202e"),
		"echoed twice":      k + " and " + split("\u200b"),
		"straddles the cap": pad + k,
		"bidi isolate+RLO":  "x\u202e\u2066y\u200b" + k,
	}
	for name, msg := range cases {
		t.Run("http "+name, func(t *testing.T) {
			setupArchera(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "30")
				w.WriteHeader(403)
				body, _ := json.Marshal(map[string]string{"message": msg})
				_, _ = w.Write(body)
			})
			out, errOut, err := runArchera(t, fullEnv())
			require.Error(t, err)
			assert.NotContains(t, out+errOut+err.Error(), k)
			assertArcheraNoControlChars(t, err.Error())
			assert.LessOrEqual(t, len(err.Error()), archeraMaxErrorSize)
			assert.Contains(t, err.Error(), "retry after 30s")
			assert.NotContains(t, err.Error(), "\u202e")
		})
	}
	t.Run("key as enum value in a decode error", func(t *testing.T) {
		body := strings.Replace(archeraComparisonFixture(), `"payment_option":"no_upfront"`, `"payment_option":"`+split("\u200b")+`"`, 1)
		setupArchera(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
		out, errOut, err := runArchera(t, fullEnv())
		require.Error(t, err)
		assert.NotContains(t, out+errOut+err.Error(), k)
		// pkg/insurance no longer echoes the rejected value at all.
		assert.Contains(t, err.Error(), "not shown")
	})
	t.Run("plain key as enum value", func(t *testing.T) {
		body := strings.Replace(archeraComparisonFixture(), `"payment_option":"no_upfront"`, `"payment_option":"`+k+`"`, 1)
		setupArchera(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
		_, _, err := runArchera(t, fullEnv())
		require.Error(t, err)
		assert.NotContains(t, err.Error(), k)
	})
}

// archeraKeyHidingSeparators split the key with characters that survive the
// strip, so an exact-match mask misses a key the terminal still displays.
var archeraKeyHidingSeparators = map[string]string{
	"variation selector 16":   "\ufe0f",
	"variation selector 17":   "\U000e0100",
	"combining grapheme join": "\u034f",
	"combining acute":         "\u0301",
	"enclosing circle":        "\u20dd",
	"hangul choseong filler":  "\u115f",
	"hangul jungseong filler": "\u1160",
	"hangul filler":           "\u3164",
	"halfwidth hangul filler": "\uffa0",
	"space":                   " ",
	"invalid utf-8":           "\xff",
}

func archeraSplitEverywhere(k, sep string) string {
	return strings.Join(strings.Split(k, ""), sep)
}

type archeraFailingTransport struct{ err error }

func (f archeraFailingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

func TestArcheraErrorsWithholdKeyHiddenBehindSurvivingCharacters(t *testing.T) {
	k := archeraTestKey
	checkMsg := func(t *testing.T, got string) {
		t.Helper()
		assert.NotContains(t, got, k)
		assert.Contains(t, got, archeraWithheld)
		assertArcheraNoControlChars(t, got)
		assert.LessOrEqual(t, len(got), archeraMaxErrorSize)
	}
	for name, sep := range archeraKeyHidingSeparators {
		for shape, echo := range map[string]string{
			"split everywhere": archeraSplitEverywhere(k, sep),
			"split once":       k[:5] + sep + k[5:],
		} {
			msg := "bad key " + echo + " was rejected"
			t.Run("http "+name+" "+shape, func(t *testing.T) {
				setupArchera(t, func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Retry-After", "30")
					w.WriteHeader(403)
					body, _ := json.Marshal(map[string]string{"message": msg})
					_, _ = w.Write(body)
				})
				out, errOut, err := runArchera(t, fullEnv())
				require.Error(t, err)
				assert.NotContains(t, out+errOut, k)
				checkMsg(t, err.Error())
				assert.Contains(t, err.Error(), "HTTP 403")
				assert.Contains(t, err.Error(), "retry after 30s")
			})
			t.Run("decode "+name+" "+shape, func(t *testing.T) {
				body := strings.Replace(archeraComparisonFixture(), `"payment_option":"no_upfront"`, `"payment_option":`+mustJSON(t, echo), 1)
				setupArchera(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
				out, errOut, err := runArchera(t, fullEnv())
				require.Error(t, err)
				assert.NotContains(t, out+errOut, k)
				// pkg/insurance no longer echoes the rejected value, so there is
				// nothing to withhold; the key must still not appear.
				assert.NotContains(t, err.Error(), k)
				assertArcheraNoControlChars(t, err.Error())
				assert.LessOrEqual(t, len(err.Error()), archeraMaxErrorSize)
			})
			for kind, wrapped := range map[string]error{
				"transport": errors.New(msg),
				"deadline":  fmt.Errorf("%s: %w", msg, context.DeadlineExceeded),
				"canceled":  fmt.Errorf("%s: %w", msg, context.Canceled),
			} {
				t.Run(kind+" "+name+" "+shape, func(t *testing.T) {
					setupArchera(t, okHandler)
					archeraHTTPClient = &http.Client{Transport: archeraFailingTransport{err: wrapped}}
					out, errOut, err := runArchera(t, fullEnv())
					require.Error(t, err)
					assert.NotContains(t, out+errOut, k)
					checkMsg(t, err.Error())
				})
			}
		}
	}
}

func mustJSON(t *testing.T, v string) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// The mask must run before the cap: a key cut by the cap boundary would
// otherwise survive as a readable prefix. Sweep the boundary and assert no
// 4+ byte prefix of the key remains, for a plain and a strip-split key.
func TestArcheraErrorsLeaveNoKeyPrefixAtCap(t *testing.T) {
	t.Parallel()
	k := archeraTestKey
	for _, echo := range []string{k, k[:5] + "\u200b" + k[5:], archeraSplitEverywhere(k, "\u200b")} {
		for padLen := archeraMaxErrorSize - len(k) - 8; padLen <= archeraMaxErrorSize+2; padLen++ {
			err := archeraSafeError(errors.New(strings.Repeat("p", padLen)+echo), k)
			got := err.Error()
			for n := 4; n <= len(k); n++ {
				for start := 0; start+n <= len(k); start++ {
					require.NotContains(t, got, k[start:start+n], "pad %d, fragment %q", padLen, k[start:start+n])
				}
			}
		}
	}
}

// The withhold check must see the full text: a cut through a split key would
// hide it from a check run after the cap while leaving a readable prefix.
func TestArcheraErrorsWithholdSplitKeyAtAnyCapOffset(t *testing.T) {
	t.Parallel()
	k := archeraTestKey
	for name, sep := range map[string]string{"variation selector": "\ufe0f", "invalid utf-8": "\xff", "combining acute": "\u0301"} {
		for padLen := archeraMaxErrorSize - 3*len(k); padLen <= archeraMaxErrorSize+2; padLen++ {
			got := archeraSafeError(errors.New(strings.Repeat("p", padLen)+archeraSplitEverywhere(k, sep)), k).Error()
			require.Equal(t, archeraWithheld, got, "%s pad %d", name, padLen)
		}
	}
}

func TestArcheraErrorsTrimKeyBeforeMasking(t *testing.T) {
	t.Parallel()
	got := archeraSafeError(errors.New("bad key "+archeraTestKey[:5]+"\u200b"+archeraTestKey[5:]), " "+archeraTestKey+"\n").Error()
	assert.Equal(t, "bad key [redacted]", got)
}

// A log or slog write of the key would bypass os.Stderr captured per test, so
// route both default loggers into the capture for the whole run.
func TestArcheraRunDoesNotLogKey(t *testing.T) {
	var logs bytes.Buffer
	savedLog, savedFlags := log.Writer(), log.Flags()
	log.SetOutput(&logs)
	savedSlog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { log.SetOutput(savedLog); log.SetFlags(savedFlags); slog.SetDefault(savedSlog) })
	setupArchera(t, okHandler)
	_, _, err := runArchera(t, fullEnv())
	require.NoError(t, err)
	setupArchera(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(403) })
	_, _, _ = runArchera(t, fullEnv())
	assert.NotContains(t, logs.String(), archeraTestKey)
}
