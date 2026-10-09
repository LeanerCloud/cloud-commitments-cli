package main

import (
	"strconv"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/services/savingsplans"
)

// csvDetailColumns are the report columns that carry the fields of a
// recommendation's polymorphic Details that Engine/Deployment cannot: Savings
// Plan identity and EC2 tenancy and scope. They are appended after the
// original columns so existing readers, which look columns up by name, keep
// working. The writer and csvDetails below must stay in step.
var csvDetailColumns = []string{"PlanType", "HourlyCommitment", "InstanceFamily", "OfferingID", "DetailsRegion", "Tenancy", "Scope"}

// detailCells renders rec.Details for csvDetailColumns, in that order.
func detailCells(rec common.Recommendation) []string {
	cells := make([]string, len(csvDetailColumns))
	switch d := rec.Details.(type) {
	case *common.SavingsPlanDetails:
		if d != nil {
			cells[0] = d.PlanType
			cells[1] = strconv.FormatFloat(d.HourlyCommitment, 'f', -1, 64)
			cells[2] = d.InstanceFamily
			cells[3] = d.OfferingID
			cells[4] = d.Region
		}
	case *common.ComputeDetails:
		if d != nil {
			cells[5], cells[6] = d.Tenancy, d.Scope
		}
	case common.ComputeDetails:
		cells[5], cells[6] = d.Tenancy, d.Scope
	}
	return cells
}

// csvDetails reconstructs the service Details of a CSV row. The purchase path
// needs them: RDS findOfferingID rejects nil Details, Savings Plan purchases
// need the plan type and hourly commitment, and EC2 offerings are keyed by
// platform, tenancy and scope. This mirrors the writer (extractEngine,
// extractDeployment, detailCells), so a CSV the tool wrote round-trips.
//
// A row without the identifying columns (a minimal or older CSV) keeps nil
// Details; validatePurchasePreconditions then reports it rather than the
// loader inventing values.
func csvDetails(rec common.Recommendation, record []string, colIdx map[string]int) (common.ServiceDetails, error) {
	if _, isSP := savingsplans.PlanTypeForServiceType(rec.Service); isSP {
		return csvSavingsPlanDetails(record, colIdx)
	}
	engine := getCSVField(record, colIdx, "Engine")
	if engine == "" {
		return nil, nil
	}
	switch rec.Service {
	case common.ServiceRDS, common.ServiceRelationalDB:
		return &common.DatabaseDetails{
			Engine:        engine,
			AZConfig:      getCSVField(record, colIdx, "Deployment"),
			InstanceClass: rec.ResourceType,
		}, nil
	case common.ServiceElastiCache, common.ServiceCache:
		return &common.CacheDetails{Engine: engine, NodeType: rec.ResourceType}, nil
	case common.ServiceEC2, common.ServiceCompute:
		return &common.ComputeDetails{
			InstanceType: rec.ResourceType,
			Platform:     engine,
			Tenancy:      getCSVField(record, colIdx, "Tenancy"),
			Scope:        getCSVField(record, colIdx, "Scope"),
		}, nil
	}
	return nil, nil
}

func csvSavingsPlanDetails(record []string, colIdx map[string]int) (common.ServiceDetails, error) {
	planType := getCSVField(record, colIdx, "PlanType")
	if planType == "" && getCSVField(record, colIdx, "HourlyCommitment") == "" {
		return nil, nil
	}
	d := &common.SavingsPlanDetails{
		PlanType:       planType,
		InstanceFamily: getCSVField(record, colIdx, "InstanceFamily"),
		OfferingID:     getCSVField(record, colIdx, "OfferingID"),
		Region:         getCSVField(record, colIdx, "DetailsRegion"),
	}
	if err := parseCSVFloat(record, colIdx, "HourlyCommitment", &d.HourlyCommitment); err != nil {
		return nil, err
	}
	return d, nil
}
