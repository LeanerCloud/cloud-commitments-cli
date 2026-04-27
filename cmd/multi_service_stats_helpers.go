package main

import (
	"github.com/LeanerCloud/CUDly/pkg/common"
)

// SPTypeBreakdown holds savings information broken down by Savings Plan type
type SPTypeBreakdown struct {
	ComputeSavings     float64
	EC2InstanceSavings float64
	SageMakerSavings   float64
	DatabaseSavings    float64
	ComputeCount       int
	EC2InstanceCount   int
	SageMakerCount     int
	DatabaseCount      int
}

// categorizeSPRecommendations categorizes Savings Plan recommendations by type
func categorizeSPRecommendations(recommendations []common.Recommendation) SPTypeBreakdown {
	breakdown := SPTypeBreakdown{}

	for _, rec := range recommendations {
		if common.IsSavingsPlan(rec.Service) {
			if details, ok := rec.Details.(*common.SavingsPlanDetails); ok {
				switch details.PlanType {
				case "Compute":
					breakdown.ComputeSavings += rec.EstimatedSavings
					breakdown.ComputeCount++
				case "EC2Instance":
					breakdown.EC2InstanceSavings += rec.EstimatedSavings
					breakdown.EC2InstanceCount++
				case "SageMaker":
					breakdown.SageMakerSavings += rec.EstimatedSavings
					breakdown.SageMakerCount++
				case "Database":
					breakdown.DatabaseSavings += rec.EstimatedSavings
					breakdown.DatabaseCount++
				}
			}
		}
	}

	return breakdown
}

// printSPTypeSummaries prints the summary for each Savings Plan type
func printSPTypeSummaries(breakdown SPTypeBreakdown) {
	if breakdown.ComputeCount > 0 {
		AppLogger.Printf("  Compute SP    | Recs: %3d | Covers: EC2, Fargate, Lambda | $%8.2f/mo\n",
			breakdown.ComputeCount, breakdown.ComputeSavings)
	}
	if breakdown.EC2InstanceCount > 0 {
		AppLogger.Printf("  EC2 Inst SP   | Recs: %3d | Covers: EC2 only (better rate) | $%8.2f/mo\n",
			breakdown.EC2InstanceCount, breakdown.EC2InstanceSavings)
	}
	if breakdown.SageMakerCount > 0 {
		AppLogger.Printf("  SageMaker SP  | Recs: %3d | Covers: SageMaker instances    | $%8.2f/mo\n",
			breakdown.SageMakerCount, breakdown.SageMakerSavings)
	}
	if breakdown.DatabaseCount > 0 {
		AppLogger.Printf("  Database SP   | Recs: %3d | Covers: RDS, Aurora, ElastiCache, etc. | $%8.2f/mo\n",
			breakdown.DatabaseCount, breakdown.DatabaseSavings)
	}
}

// printBestSPOptions prints the best Savings Plan options by category
func printBestSPOptions(breakdown SPTypeBreakdown) {
	AppLogger.Println()

	// Best for EC2/Compute
	if breakdown.EC2InstanceSavings > 0 || breakdown.ComputeSavings > 0 {
		if breakdown.EC2InstanceSavings > breakdown.ComputeSavings {
			AppLogger.Printf("  ⭐ Best for EC2: EC2 Instance SP ($%.2f/mo)\n", breakdown.EC2InstanceSavings)
		} else if breakdown.ComputeSavings > 0 {
			AppLogger.Printf("  ⭐ Best for Compute: Compute SP ($%.2f/mo) - more flexible\n", breakdown.ComputeSavings)
		}
	}

	// Best for Databases
	if breakdown.DatabaseSavings > 0 {
		AppLogger.Printf("  ⭐ Best for Databases: Database SP ($%.2f/mo)\n", breakdown.DatabaseSavings)
	}

	// Best for ML
	if breakdown.SageMakerSavings > 0 {
		AppLogger.Printf("  ⭐ Best for ML: SageMaker SP ($%.2f/mo)\n", breakdown.SageMakerSavings)
	}
}

// SPSavingsByType holds Savings Plan savings categorized by plan type
type SPSavingsByType struct {
	EC2SPSavings      float64
	ComputeSPSavings  float64
	DatabaseSPSavings float64
}

// collectSPSavings collects Savings Plan savings by type
func collectSPSavings(recommendations []common.Recommendation) SPSavingsByType {
	savings := SPSavingsByType{}

	for _, rec := range recommendations {
		if common.IsSavingsPlan(rec.Service) {
			if details, ok := rec.Details.(*common.SavingsPlanDetails); ok {
				switch details.PlanType {
				case "EC2Instance":
					savings.EC2SPSavings += rec.EstimatedSavings
				case "Compute":
					savings.ComputeSPSavings += rec.EstimatedSavings
				case "Database":
					savings.DatabaseSPSavings += rec.EstimatedSavings
				}
			}
		}
	}

	return savings
}

// RISavingsByService holds Reserved Instance savings categorized by service
type RISavingsByService struct {
	EC2RISavings float64
	DBRISavings  float64
}

// collectRISavings collects Reserved Instance savings by service
func collectRISavings(riStats map[common.ServiceType]ServiceProcessingStats) RISavingsByService {
	savings := RISavingsByService{}

	// EC2 RIs
	if stats, ok := riStats[common.ServiceEC2]; ok {
		savings.EC2RISavings = stats.TotalEstimatedSavings
	}

	// Database RIs (RDS, ElastiCache, MemoryDB, Redshift)
	for service, stats := range riStats {
		if service == common.ServiceRDS || service == common.ServiceElastiCache ||
			service == common.ServiceMemoryDB || service == common.ServiceRedshift {
			savings.DBRISavings += stats.TotalEstimatedSavings
		}
	}

	return savings
}

// ComparisonOptions holds the calculated savings for different purchasing options
type ComparisonOptions struct {
	Option1Savings    float64
	Option2Savings    float64
	Option3Savings    float64
	BestComputeSP     float64
	BestComputeSPName string
	HasDatabaseSP     bool
}

// calculateComparisonOptions calculates savings for all comparison options
func calculateComparisonOptions(riSavings float64, spSavings SPSavingsByType, risByService RISavingsByService) ComparisonOptions {
	opts := ComparisonOptions{
		Option1Savings: riSavings,
		HasDatabaseSP:  spSavings.DatabaseSPSavings > 0,
	}

	// Determine best compute SP
	opts.BestComputeSP = spSavings.EC2SPSavings
	opts.BestComputeSPName = "EC2 Instance SP"
	if spSavings.ComputeSPSavings > spSavings.EC2SPSavings {
		opts.BestComputeSP = spSavings.ComputeSPSavings
		opts.BestComputeSPName = "Compute SP"
	}

	// Option 2: Best compute SP + non-EC2 RIs
	opts.Option2Savings = riSavings - risByService.EC2RISavings + opts.BestComputeSP

	// Option 3: Compute SP + Database SP (if available)
	if opts.HasDatabaseSP {
		opts.Option3Savings = riSavings - risByService.EC2RISavings - risByService.DBRISavings +
			opts.BestComputeSP + spSavings.DatabaseSPSavings
	}

	return opts
}

// printComparisonOptions prints all comparison options
func printComparisonOptions(opts ComparisonOptions) {
	// Option 1: All RIs
	AppLogger.Printf("Option 1 (All RIs):\n")
	AppLogger.Printf("  Total monthly savings: $%.2f\n", opts.Option1Savings)
	AppLogger.Printf("  Pros: Highest discount for specific instance types\n")
	AppLogger.Printf("  Cons: Less flexible, locked to instance family/engine\n")

	// Option 2: Best compute SP + non-EC2 RIs
	AppLogger.Printf("\nOption 2 (%s for compute + RIs for databases):\n", opts.BestComputeSPName)
	AppLogger.Printf("  Total monthly savings: $%.2f\n", opts.Option2Savings)
	AppLogger.Printf("  Pros: Flexible compute (can change EC2 families)\n")
	AppLogger.Printf("  Cons: DB RIs still locked to engine/instance type\n")

	// Option 3: If we have Database SP recommendations
	if opts.HasDatabaseSP {
		AppLogger.Printf("\nOption 3 (%s + Database SP):\n", opts.BestComputeSPName)
		AppLogger.Printf("  Total monthly savings: $%.2f\n", opts.Option3Savings)
		AppLogger.Printf("  Pros: Maximum flexibility for both compute and databases\n")
		AppLogger.Printf("  Cons: May have slightly lower discount than targeted RIs\n")
	}
}

// determineBestOption determines and prints the best purchasing option
func determineBestOption(opts ComparisonOptions) {
	if !opts.HasDatabaseSP {
		// Only 2 options available
		if opts.Option2Savings > opts.Option1Savings {
			AppLogger.Printf("\n  ⭐ RECOMMENDATION: Use Option 2 (saves $%.2f/mo more)\n",
				opts.Option2Savings-opts.Option1Savings)
		} else {
			AppLogger.Printf("\n  ⭐ RECOMMENDATION: Use Option 1 (saves $%.2f/mo more)\n",
				opts.Option1Savings-opts.Option2Savings)
		}
		return
	}

	// All 3 options available - find the best
	best := "Option 1 (All RIs)"
	bestSavings := opts.Option1Savings

	if opts.Option2Savings > bestSavings {
		best = "Option 2 (Compute SP + DB RIs)"
		bestSavings = opts.Option2Savings
	}

	if opts.Option3Savings > bestSavings {
		best = "Option 3 (Compute SP + Database SP)"
		bestSavings = opts.Option3Savings
	}

	AppLogger.Printf("\n  ⭐ RECOMMENDATION: %s ($%.2f/mo)\n", best, bestSavings)
}
