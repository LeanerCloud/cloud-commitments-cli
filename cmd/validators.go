package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/spf13/cobra"
)

// validateFlags performs validation on command line flags before execution
func validateFlags(cmd *cobra.Command, args []string) error {
	if err := validateNumericRanges(); err != nil {
		return err
	}

	if err := validatePaymentAndTerm(); err != nil {
		return err
	}

	if err := validateFilePaths(); err != nil {
		return err
	}

	if err := validateFilterFlags(); err != nil {
		return err
	}

	return nil
}

// validateNumericRanges validates all numeric configuration values
func validateNumericRanges() error {
	// Validate coverage percentage
	if toolCfg.Coverage < 0 || toolCfg.Coverage > 100 {
		return fmt.Errorf("coverage percentage must be between 0 and 100, got: %.2f", toolCfg.Coverage)
	}

	// Validate max instances
	if toolCfg.MaxInstances < 0 {
		return fmt.Errorf("max-instances must be 0 (no limit) or a positive number, got: %d", toolCfg.MaxInstances)
	}

	if toolCfg.MaxInstances > MaxReasonableInstances {
		return fmt.Errorf("max-instances (%d) exceeds reasonable limit of %d", toolCfg.MaxInstances, MaxReasonableInstances)
	}

	// Validate override count
	if toolCfg.OverrideCount < 0 {
		return fmt.Errorf("override-count must be 0 (disabled) or a positive number, got: %d", toolCfg.OverrideCount)
	}

	if toolCfg.OverrideCount > MaxReasonableInstances {
		return fmt.Errorf("override-count (%d) exceeds reasonable limit of %d", toolCfg.OverrideCount, MaxReasonableInstances)
	}

	return nil
}

// validatePaymentAndTerm validates payment options and term configuration
func validatePaymentAndTerm() error {
	// Validate payment option
	validPaymentOptions := map[string]bool{
		"all-upfront":     true,
		"partial-upfront": true,
		"no-upfront":      true,
	}
	if !validPaymentOptions[toolCfg.PaymentOption] {
		return fmt.Errorf("invalid payment option: %s. Must be one of: all-upfront, partial-upfront, no-upfront", toolCfg.PaymentOption)
	}

	// Validate term years
	if toolCfg.TermYears != 1 && toolCfg.TermYears != 3 {
		return fmt.Errorf("invalid term: %d years. Must be 1 or 3", toolCfg.TermYears)
	}

	// Warn about RDS 3-year no-upfront limitation
	return warnRDS3YearNoUpfront()
}

// warnRDS3YearNoUpfront warns if RDS service is selected with 3-year no-upfront
func warnRDS3YearNoUpfront() error {
	if toolCfg.PaymentOption != "no-upfront" || toolCfg.TermYears != 3 {
		return nil
	}

	services := determineServicesToProcess(toolCfg)
	hasRDS := toolCfg.AllServices || containsService(services, common.ServiceRDS)

	if hasRDS {
		log.Println("⚠️  WARNING: AWS does not offer 3-year no-upfront Reserved Instances for RDS.")
		log.Println("    RDS 3-year RIs only support: all-upfront, partial-upfront")
		log.Println("    No RDS recommendations will be found with this combination.")
	}

	return nil
}

// containsService checks if a service exists in the slice
func containsService(services []common.ServiceType, service common.ServiceType) bool {
	for _, svc := range services {
		if svc == service {
			return true
		}
	}
	return false
}

// validateFilePaths validates CSV input/output paths
func validateFilePaths() error {
	// Validate CSV output path if provided
	if toolCfg.CSVOutput != "" {
		dir := filepath.Dir(toolCfg.CSVOutput)
		if dir != "." && dir != "" {
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				return fmt.Errorf("output directory does not exist: %s", dir)
			}
		}
	}

	// Validate CSV input path if provided
	if toolCfg.CSVInput != "" {
		if _, err := os.Stat(toolCfg.CSVInput); os.IsNotExist(err) {
			return fmt.Errorf("input CSV file does not exist: %s", toolCfg.CSVInput)
		}
		if !strings.HasSuffix(strings.ToLower(toolCfg.CSVInput), ".csv") {
			return fmt.Errorf("input file must have .csv extension: %s", toolCfg.CSVInput)
		}
	}

	return nil
}

// validateFilterFlags validates filter configuration flags
func validateFilterFlags() error {
	// Check for region conflicts
	if err := validateNoConflicts(toolCfg.IncludeRegions, toolCfg.ExcludeRegions, "region"); err != nil {
		return err
	}

	// Check for instance type conflicts
	if err := validateNoConflicts(toolCfg.IncludeInstanceTypes, toolCfg.ExcludeInstanceTypes, "instance type"); err != nil {
		return err
	}

	// Check for engine conflicts
	if err := validateNoConflicts(toolCfg.IncludeEngines, toolCfg.ExcludeEngines, "engine"); err != nil {
		return err
	}

	// Validate instance types format
	if err := validateInstanceTypes(toolCfg.IncludeInstanceTypes); err != nil {
		return fmt.Errorf("invalid include-instance-types: %w", err)
	}
	if err := validateInstanceTypes(toolCfg.ExcludeInstanceTypes); err != nil {
		return fmt.Errorf("invalid exclude-instance-types: %w", err)
	}

	return nil
}

// validateNoConflicts checks that include and exclude lists don't overlap
func validateNoConflicts(include, exclude []string, itemType string) error {
	if len(include) == 0 || len(exclude) == 0 {
		return nil
	}

	for _, inc := range include {
		for _, exc := range exclude {
			if inc == exc {
				return fmt.Errorf("%s '%s' cannot be both included and excluded", itemType, inc)
			}
		}
	}

	return nil
}

// validateInstanceTypes performs basic validation on instance type names
func validateInstanceTypes(instanceTypes []string) error {
	if len(instanceTypes) == 0 {
		return nil
	}

	for _, t := range instanceTypes {
		if t == "" {
			return fmt.Errorf("empty instance type")
		}
		if !strings.Contains(t, ".") {
			return fmt.Errorf("invalid instance type format '%s': expected format like 'db.t3.micro'", t)
		}
	}

	return nil
}
