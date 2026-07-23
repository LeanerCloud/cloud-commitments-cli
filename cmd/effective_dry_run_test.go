package main

import "testing"

// TestEffectiveDryRun documents the single-flag purchase contract: a run is a
// dry run unless the user opts into real purchases with --purchase. This guards
// the original regression (issue surfaced on #1364) where --purchase alone
// silently stayed in dry-run because a separate --dry-run flag defaulted to
// true. That flag has since been removed; --purchase is now the only control,
// so moving money is always an explicit opt-in and a bare run is always safe.
func TestEffectiveDryRun(t *testing.T) {
	tests := []struct {
		name           string
		actualPurchase bool // --purchase
		want           bool
	}{
		{name: "bare invocation is dry-run", actualPurchase: false, want: true},
		{name: "--purchase executes real purchases", actualPurchase: true, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := effectiveDryRun(Config{ActualPurchase: tt.actualPurchase})
			if got != tt.want {
				t.Errorf("effectiveDryRun(ActualPurchase=%v) = %v, want %v",
					tt.actualPurchase, got, tt.want)
			}
		})
	}
}

// TestDryRunFlagRemoved guards against reintroducing the --dry-run flag. It was
// removed because it was a footgun: as a default-true flag it silently
// suppressed real purchases even with --purchase (the #1364 regression), and
// once its default was flipped to false it became a redundant "force dry-run
// even with --purchase" override that only muddied the single-flag contract.
// --purchase is now the sole purchase control; a bare run is always a dry run.
func TestDryRunFlagRemoved(t *testing.T) {
	if f := rootCmd.Flags().Lookup("dry-run"); f != nil {
		t.Errorf("--dry-run flag is registered again (default %q); it was intentionally removed. "+
			"--purchase is the only purchase control; a bare run is always a dry run.", f.DefValue)
	}
}
