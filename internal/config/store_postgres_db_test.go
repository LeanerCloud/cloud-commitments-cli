//go:build integration
// +build integration

package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/LeanerCloud/CUDly/internal/database"
	"github.com/LeanerCloud/CUDly/internal/database/postgres/migrations"
	"github.com/LeanerCloud/CUDly/internal/database/postgres/testhelpers"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// getTestMigrationsPath returns the absolute path to migrations directory.
func getTestMigrationsPath() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "database", "postgres", "migrations")
}

// setupTestContainerDB starts a PostgreSQL container via testcontainers,
// runs migrations, and adds a UNIQUE constraint on execution_id for ON CONFLICT support.
// Returns the database.Connection or skips the test.
func setupTestContainerDB(t *testing.T) *database.Connection {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	container := testhelpers.RequirePostgresContainer(ctx, t)
	t.Cleanup(func() {
		container.Cleanup(context.Background())
	})

	// Migrating is the thing under test here, not a precondition of it: a
	// migration that will not apply is a broken schema, and skipping on it would
	// report the whole suite green on exactly the defect it exists to catch
	// (issue #1597).
	require.NoError(t,
		migrations.RunMigrations(ctx, container.DB.Pool(), getTestMigrationsPath(), "", ""),
		"migrations failed to apply to a fresh database")

	// The store code uses ON CONFLICT (execution_id) but the migration schema
	// does not create a UNIQUE constraint on execution_id. Add it for tests.
	_, err := container.DB.Exec(ctx,
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_purchase_executions_execution_id_unique ON purchase_executions(execution_id)")
	require.NoError(t, err,
		"could not add the unique index on execution_id the ON CONFLICT paths need; "+
			"a migrated schema this DDL cannot be applied to is a failure, not a reason to skip")

	return container.DB
}

// cleanupTestData deletes all data from test tables.
func cleanupTestData(t *testing.T, conn *database.Connection) {
	t.Helper()
	ctx := context.Background()

	tables := []string{
		"purchase_executions",
		"purchase_history",
		"purchase_plans",
		"service_configs",
	}
	for _, table := range tables {
		_, _ = conn.Exec(ctx, fmt.Sprintf("DELETE FROM %s", table))
	}
	// Reset global_config to defaults
	_, _ = conn.Exec(ctx, "DELETE FROM global_config")
	_, _ = conn.Exec(ctx, "INSERT INTO global_config (id) VALUES (1) ON CONFLICT (id) DO NOTHING")
}

// ==========================================
// REAL DATABASE TESTS
// ==========================================

func TestPostgresStoreDB_GlobalConfig(t *testing.T) {
	conn := setupTestContainerDB(t)
	if conn == nil {
		return
	}

	store := NewPostgresStore(conn)
	ctx := context.Background()

	t.Run("GetGlobalConfig returns defaults when row exists", func(t *testing.T) {
		cfg, err := store.GetGlobalConfig(ctx)
		require.NoError(t, err)
		assert.NotNil(t, cfg)
		assert.True(t, cfg.ApprovalRequired)
		assert.Equal(t, 3, cfg.DefaultTerm)
		assert.Equal(t, "all-upfront", cfg.DefaultPayment)
		assert.Equal(t, 80.0, cfg.DefaultCoverage)
		assert.Equal(t, "immediate", cfg.DefaultRampSchedule)
	})

	t.Run("SaveGlobalConfig and GetGlobalConfig round trip", func(t *testing.T) {
		email := "test@example.com"
		cfg := &GlobalConfig{
			EnabledProviders:    []string{"aws", "gcp"},
			NotificationEmail:   &email,
			ApprovalRequired:    false,
			DefaultTerm:         3,
			DefaultPayment:      "no-upfront",
			DefaultCoverage:     90.0,
			DefaultRampSchedule: "weekly-25pct",
		}

		err := store.SaveGlobalConfig(ctx, cfg)
		require.NoError(t, err)

		retrieved, err := store.GetGlobalConfig(ctx)
		require.NoError(t, err)
		assert.Equal(t, []string{"aws", "gcp"}, retrieved.EnabledProviders)
		assert.Equal(t, &email, retrieved.NotificationEmail)
		assert.False(t, retrieved.ApprovalRequired)
		assert.Equal(t, 3, retrieved.DefaultTerm)
		assert.Equal(t, "no-upfront", retrieved.DefaultPayment)
		assert.Equal(t, 90.0, retrieved.DefaultCoverage)
		assert.Equal(t, "weekly-25pct", retrieved.DefaultRampSchedule)
	})

	t.Run("SaveGlobalConfig upserts existing config", func(t *testing.T) {
		cfg := &GlobalConfig{
			EnabledProviders:    []string{"azure"},
			ApprovalRequired:    true,
			DefaultTerm:         1,
			DefaultPayment:      "all-upfront",
			DefaultCoverage:     50.0,
			DefaultRampSchedule: "immediate",
		}

		err := store.SaveGlobalConfig(ctx, cfg)
		require.NoError(t, err)

		retrieved, err := store.GetGlobalConfig(ctx)
		require.NoError(t, err)
		assert.Equal(t, []string{"azure"}, retrieved.EnabledProviders)
		assert.True(t, retrieved.ApprovalRequired)
		assert.Equal(t, 1, retrieved.DefaultTerm)
	})

	t.Run("SaveGlobalConfig converts nil providers to empty slice", func(t *testing.T) {
		cfg := &GlobalConfig{
			EnabledProviders:    nil,
			DefaultTerm:         3,
			DefaultPayment:      "all-upfront",
			DefaultCoverage:     80.0,
			DefaultRampSchedule: "immediate",
		}

		err := store.SaveGlobalConfig(ctx, cfg)
		require.NoError(t, err)
		assert.NotNil(t, cfg.EnabledProviders)
	})
}

func TestPostgresStoreDB_ServiceConfig(t *testing.T) {
	conn := setupTestContainerDB(t)
	if conn == nil {
		return
	}

	cleanupTestData(t, conn)

	store := NewPostgresStore(conn)
	ctx := context.Background()

	t.Run("GetServiceConfig not found", func(t *testing.T) {
		_, err := store.GetServiceConfig(ctx, "aws", "nonexistent")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("SaveServiceConfig and GetServiceConfig round trip", func(t *testing.T) {
		cfg := &ServiceConfig{
			Provider:       "aws",
			Service:        "rds",
			Enabled:        true,
			Term:           3,
			Payment:        "all-upfront",
			Coverage:       80.0,
			RampSchedule:   "immediate",
			IncludeEngines: []string{"postgres", "mysql"},
			ExcludeEngines: []string{},
			IncludeRegions: []string{"us-east-1"},
			ExcludeRegions: []string{},
			IncludeTypes:   []string{"db.r5.large"},
			ExcludeTypes:   []string{},
		}

		err := store.SaveServiceConfig(ctx, cfg)
		require.NoError(t, err)

		retrieved, err := store.GetServiceConfig(ctx, "aws", "rds")
		require.NoError(t, err)
		assert.Equal(t, "aws", retrieved.Provider)
		assert.Equal(t, "rds", retrieved.Service)
		assert.True(t, retrieved.Enabled)
		assert.Equal(t, 3, retrieved.Term)
		assert.Equal(t, "all-upfront", retrieved.Payment)
		assert.Equal(t, 80.0, retrieved.Coverage)
		assert.Equal(t, []string{"postgres", "mysql"}, retrieved.IncludeEngines)
		assert.Equal(t, []string{"us-east-1"}, retrieved.IncludeRegions)
		assert.Equal(t, []string{"db.r5.large"}, retrieved.IncludeTypes)
	})

	t.Run("ListServiceConfigs", func(t *testing.T) {
		// Add another config
		cfg2 := &ServiceConfig{
			Provider: "aws",
			Service:  "elasticache",
			Enabled:  true,
			Term:     1,
			Payment:  "no-upfront",
			Coverage: 70.0,
		}
		err := store.SaveServiceConfig(ctx, cfg2)
		require.NoError(t, err)

		configs, err := store.ListServiceConfigs(ctx)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(configs), 2)

		// Verify ordering by provider, service
		for i := 1; i < len(configs); i++ {
			prev := configs[i-1].Provider + configs[i-1].Service
			curr := configs[i].Provider + configs[i].Service
			assert.True(t, prev <= curr, "configs should be ordered by provider, service")
		}
	})

	t.Run("SaveServiceConfig upsert", func(t *testing.T) {
		cfg := &ServiceConfig{
			Provider: "aws",
			Service:  "rds",
			Enabled:  false,
			Term:     1,
			Payment:  "no-upfront",
			Coverage: 60.0,
		}

		err := store.SaveServiceConfig(ctx, cfg)
		require.NoError(t, err)

		retrieved, err := store.GetServiceConfig(ctx, "aws", "rds")
		require.NoError(t, err)
		assert.False(t, retrieved.Enabled)
		assert.Equal(t, 1, retrieved.Term)
		assert.Equal(t, 60.0, retrieved.Coverage)
	})
}

func TestPostgresStoreDB_PurchasePlans(t *testing.T) {
	conn := setupTestContainerDB(t)
	if conn == nil {
		return
	}

	cleanupTestData(t, conn)

	store := NewPostgresStore(conn)
	ctx := context.Background()

	t.Run("CreatePurchasePlan generates UUID", func(t *testing.T) {
		plan := &PurchasePlan{
			Name:                   "Test Plan",
			Enabled:                true,
			AutoPurchase:           false,
			NotificationDaysBefore: 7,
			Services: map[string]ServiceConfig{
				"aws:rds": {Provider: "aws", Service: "rds", Enabled: true, Term: 3, Coverage: 80},
			},
			RampSchedule: RampSchedule{Type: "immediate", PercentPerStep: 100, TotalSteps: 1},
		}

		err := store.CreatePurchasePlan(ctx, plan)
		require.NoError(t, err)
		assert.NotEmpty(t, plan.ID)
		assert.False(t, plan.CreatedAt.IsZero())
		assert.False(t, plan.UpdatedAt.IsZero())
	})

	t.Run("CreatePurchasePlan preserves existing UUID", func(t *testing.T) {
		existingID := uuid.New().String()
		plan := &PurchasePlan{
			ID:                     existingID,
			Name:                   "Custom ID Plan",
			Enabled:                true,
			NotificationDaysBefore: 3,
			Services:               map[string]ServiceConfig{},
			RampSchedule:           RampSchedule{Type: "weekly", PercentPerStep: 25, StepIntervalDays: 7, TotalSteps: 4},
		}

		err := store.CreatePurchasePlan(ctx, plan)
		require.NoError(t, err)
		assert.Equal(t, existingID, plan.ID)
	})

	t.Run("GetPurchasePlan retrieves plan with services and ramp", func(t *testing.T) {
		plan := &PurchasePlan{
			Name:                   "Retrieve Test Plan",
			Enabled:                true,
			AutoPurchase:           true,
			NotificationDaysBefore: 5,
			Services: map[string]ServiceConfig{
				"aws:ec2":         {Provider: "aws", Service: "ec2", Enabled: true, Term: 1, Coverage: 70},
				"aws:elasticache": {Provider: "aws", Service: "elasticache", Enabled: true, Term: 3, Coverage: 90},
			},
			RampSchedule: RampSchedule{
				Type:             "monthly",
				PercentPerStep:   10,
				StepIntervalDays: 30,
				TotalSteps:       10,
				CurrentStep:      2,
			},
		}

		err := store.CreatePurchasePlan(ctx, plan)
		require.NoError(t, err)

		retrieved, err := store.GetPurchasePlan(ctx, plan.ID)
		require.NoError(t, err)
		assert.Equal(t, plan.Name, retrieved.Name)
		assert.True(t, retrieved.Enabled)
		assert.True(t, retrieved.AutoPurchase)
		assert.Equal(t, 5, retrieved.NotificationDaysBefore)
		assert.Len(t, retrieved.Services, 2)
		assert.Equal(t, "monthly", retrieved.RampSchedule.Type)
		assert.Equal(t, 10.0, retrieved.RampSchedule.PercentPerStep)
		assert.Equal(t, 30, retrieved.RampSchedule.StepIntervalDays)
		assert.Equal(t, 10, retrieved.RampSchedule.TotalSteps)
	})

	t.Run("GetPurchasePlan not found", func(t *testing.T) {
		nonexistentID := uuid.New().String()
		_, err := store.GetPurchasePlan(ctx, nonexistentID)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("GetPurchasePlan with nullable timestamps", func(t *testing.T) {
		nextExec := time.Now().Add(24 * time.Hour).Truncate(time.Microsecond)
		lastExec := time.Now().Add(-24 * time.Hour).Truncate(time.Microsecond)
		lastNotif := time.Now().Add(-12 * time.Hour).Truncate(time.Microsecond)

		plan := &PurchasePlan{
			Name:                   "Timestamps Plan",
			Enabled:                true,
			NotificationDaysBefore: 3,
			Services:               map[string]ServiceConfig{},
			RampSchedule:           RampSchedule{Type: "immediate", PercentPerStep: 100, TotalSteps: 1},
			NextExecutionDate:      &nextExec,
			LastExecutionDate:      &lastExec,
			LastNotificationSent:   &lastNotif,
		}

		err := store.CreatePurchasePlan(ctx, plan)
		require.NoError(t, err)

		retrieved, err := store.GetPurchasePlan(ctx, plan.ID)
		require.NoError(t, err)
		assert.NotNil(t, retrieved.NextExecutionDate)
		assert.NotNil(t, retrieved.LastExecutionDate)
		assert.NotNil(t, retrieved.LastNotificationSent)
	})

	t.Run("UpdatePurchasePlan", func(t *testing.T) {
		plan := &PurchasePlan{
			Name:                   "Update Me",
			Enabled:                true,
			NotificationDaysBefore: 7,
			Services:               map[string]ServiceConfig{},
			RampSchedule:           PresetRampSchedules["immediate"],
		}
		err := store.CreatePurchasePlan(ctx, plan)
		require.NoError(t, err)

		plan.Name = "Updated Name"
		plan.Enabled = false
		nextExec := time.Now().Add(48 * time.Hour)
		plan.NextExecutionDate = &nextExec

		err = store.UpdatePurchasePlan(ctx, plan)
		require.NoError(t, err)

		retrieved, err := store.GetPurchasePlan(ctx, plan.ID)
		require.NoError(t, err)
		assert.Equal(t, "Updated Name", retrieved.Name)
		assert.False(t, retrieved.Enabled)
		assert.NotNil(t, retrieved.NextExecutionDate)
	})

	t.Run("UpdatePurchasePlan not found", func(t *testing.T) {
		nonexistentID := uuid.New().String()
		plan := &PurchasePlan{
			ID:           nonexistentID,
			Name:         "Ghost",
			Services:     map[string]ServiceConfig{},
			RampSchedule: RampSchedule{},
		}

		err := store.UpdatePurchasePlan(ctx, plan)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("DeletePurchasePlan", func(t *testing.T) {
		plan := &PurchasePlan{
			Name:                   "Delete Me",
			Enabled:                true,
			NotificationDaysBefore: 1,
			Services:               map[string]ServiceConfig{},
			RampSchedule:           PresetRampSchedules["immediate"],
		}
		err := store.CreatePurchasePlan(ctx, plan)
		require.NoError(t, err)

		err = store.DeletePurchasePlan(ctx, plan.ID)
		require.NoError(t, err)

		_, err = store.GetPurchasePlan(ctx, plan.ID)
		assert.Error(t, err)
	})

	t.Run("DeletePurchasePlan not found", func(t *testing.T) {
		nonexistentID := uuid.New().String()
		err := store.DeletePurchasePlan(ctx, nonexistentID)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("ListPurchasePlans", func(t *testing.T) {
		cleanupTestData(t, conn)

		plans := []*PurchasePlan{
			{
				Name:                   "Plan A",
				Enabled:                true,
				NotificationDaysBefore: 7,
				Services:               map[string]ServiceConfig{},
				RampSchedule:           PresetRampSchedules["immediate"],
			},
			{
				Name:                   "Plan B",
				Enabled:                false,
				AutoPurchase:           true,
				NotificationDaysBefore: 3,
				Services:               map[string]ServiceConfig{},
				RampSchedule:           RampSchedule{Type: "weekly", PercentPerStep: 25, StepIntervalDays: 7, TotalSteps: 4},
			},
		}

		for _, plan := range plans {
			err := store.CreatePurchasePlan(ctx, plan)
			require.NoError(t, err)
		}

		retrieved, err := store.ListPurchasePlans(ctx, PurchasePlanFilter{})
		require.NoError(t, err)
		assert.Len(t, retrieved, 2)
	})

	t.Run("ListPurchasePlans with nullable timestamps", func(t *testing.T) {
		cleanupTestData(t, conn)

		nextExec := time.Now().Add(24 * time.Hour)
		lastExec := time.Now().Add(-24 * time.Hour)
		lastNotif := time.Now().Add(-12 * time.Hour)

		plan := &PurchasePlan{
			Name:                   "Timestamps List Plan",
			Enabled:                true,
			NotificationDaysBefore: 3,
			Services:               map[string]ServiceConfig{},
			RampSchedule:           PresetRampSchedules["immediate"],
			NextExecutionDate:      &nextExec,
			LastExecutionDate:      &lastExec,
			LastNotificationSent:   &lastNotif,
		}

		err := store.CreatePurchasePlan(ctx, plan)
		require.NoError(t, err)

		plans, err := store.ListPurchasePlans(ctx, PurchasePlanFilter{})
		require.NoError(t, err)
		assert.Len(t, plans, 1)
		assert.NotNil(t, plans[0].NextExecutionDate)
		assert.NotNil(t, plans[0].LastExecutionDate)
		assert.NotNil(t, plans[0].LastNotificationSent)
	})
}

func TestPostgresStoreDB_PurchaseExecutions(t *testing.T) {
	conn := setupTestContainerDB(t)
	if conn == nil {
		return
	}

	cleanupTestData(t, conn)

	store := NewPostgresStore(conn)
	ctx := context.Background()

	// Create a plan first for FK reference
	plan := &PurchasePlan{
		Name:                   "Execution Test Plan",
		Enabled:                true,
		NotificationDaysBefore: 7,
		Services:               map[string]ServiceConfig{},
		RampSchedule:           PresetRampSchedules["immediate"],
	}
	err := store.CreatePurchasePlan(ctx, plan)
	require.NoError(t, err)

	t.Run("SavePurchaseExecution generates ID", func(t *testing.T) {
		exec := &PurchaseExecution{
			PlanID:           plan.ID,
			Status:           "pending",
			StepNumber:       1,
			ScheduledDate:    time.Now().Add(24 * time.Hour),
			TotalUpfrontCost: 1500.00,
			EstimatedSavings: 300.00,
			Recommendations: []RecommendationRecord{
				{ID: "rec-1", Provider: "aws", Service: "rds", Savings: 100.0},
			},
		}

		err := store.SavePurchaseExecution(ctx, exec)
		require.NoError(t, err)
		assert.NotEmpty(t, exec.ExecutionID)
	})

	t.Run("SavePurchaseExecution preserves existing UUID", func(t *testing.T) {
		existingExecID := uuid.New().String()
		exec := &PurchaseExecution{
			PlanID:           plan.ID,
			ExecutionID:      existingExecID,
			Status:           "notified",
			StepNumber:       2,
			ScheduledDate:    time.Now().Add(48 * time.Hour),
			TotalUpfrontCost: 2000.00,
			EstimatedSavings: 400.00,
			Recommendations:  []RecommendationRecord{},
			TTL:              time.Now().Add(30 * 24 * time.Hour).Unix(),
		}

		err := store.SavePurchaseExecution(ctx, exec)
		require.NoError(t, err)
		assert.Equal(t, existingExecID, exec.ExecutionID)
	})

	// Store a generated exec ID for use in later subtests
	var generatedExecID string
	t.Run("GetPendingExecutions", func(t *testing.T) {
		pending, err := store.GetPendingExecutions(ctx)
		require.NoError(t, err)
		assert.NotNil(t, pending)
		assert.GreaterOrEqual(t, len(pending), 1)
		// Store the first pending execution ID for later use
		if len(pending) > 0 {
			generatedExecID = pending[0].ExecutionID
		}
	})

	t.Run("GetExecutionByID success", func(t *testing.T) {
		if generatedExecID == "" {
			t.Skip("no execution ID available from prior test")
		}
		exec, err := store.GetExecutionByID(ctx, generatedExecID)
		require.NoError(t, err)
		assert.Equal(t, generatedExecID, exec.ExecutionID)
		assert.Equal(t, plan.ID, exec.PlanID)
	})

	t.Run("GetExecutionByID not found", func(t *testing.T) {
		nonexistentID := uuid.New().String()
		exec, err := store.GetExecutionByID(ctx, nonexistentID)
		// Contract: not-found lookups fail loud with an error wrapping
		// ErrNotFound; callers use errors.Is to map it to their own 404.
		require.Error(t, err)
		require.ErrorIs(t, err, ErrNotFound)
		assert.Nil(t, exec)
	})

	t.Run("GetExecutionByPlanAndDate success", func(t *testing.T) {
		scheduledDate := time.Now().Add(72 * time.Hour).Truncate(time.Microsecond)
		exec := &PurchaseExecution{
			PlanID:          plan.ID,
			Status:          "pending",
			StepNumber:      3,
			ScheduledDate:   scheduledDate,
			Recommendations: []RecommendationRecord{},
		}

		err := store.SavePurchaseExecution(ctx, exec)
		require.NoError(t, err)

		retrieved, err := store.GetExecutionByPlanAndDate(ctx, plan.ID, scheduledDate)
		require.NoError(t, err)
		assert.Equal(t, plan.ID, retrieved.PlanID)
	})

	t.Run("GetExecutionByPlanAndDate not found", func(t *testing.T) {
		farFuture := time.Date(2099, 12, 31, 0, 0, 0, 0, time.UTC)
		_, err := store.GetExecutionByPlanAndDate(ctx, plan.ID, farFuture)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("SavePurchaseExecution with all timestamps", func(t *testing.T) {
		notifSent := time.Now().Add(-1 * time.Hour)
		completedAt := time.Now()

		exec := &PurchaseExecution{
			PlanID:           plan.ID,
			Status:           "completed",
			StepNumber:       1,
			ScheduledDate:    time.Now().Add(-2 * time.Hour),
			NotificationSent: &notifSent,
			ApprovalToken:    "approval-xyz",
			TotalUpfrontCost: 5000.00,
			EstimatedSavings: 1000.00,
			CompletedAt:      &completedAt,
			Recommendations: []RecommendationRecord{
				{
					ID: "rec-complete", Provider: "aws", Service: "rds",
					Selected: true, Purchased: true, PurchaseID: "aws-ri-123",
				},
			},
			TTL: time.Now().Add(30 * 24 * time.Hour).Unix(),
		}

		err := store.SavePurchaseExecution(ctx, exec)
		require.NoError(t, err)

		retrieved, err := store.GetExecutionByID(ctx, exec.ExecutionID)
		require.NoError(t, err)
		assert.Equal(t, "completed", retrieved.Status)
		assert.NotNil(t, retrieved.CompletedAt)
		assert.NotNil(t, retrieved.NotificationSent)
		assert.Len(t, retrieved.Recommendations, 1)
		assert.True(t, retrieved.Recommendations[0].Purchased)
	})

	t.Run("SavePurchaseExecution upsert updates existing", func(t *testing.T) {
		upsertExecID := uuid.New().String()
		exec := &PurchaseExecution{
			PlanID:          plan.ID,
			ExecutionID:     upsertExecID,
			Status:          "pending",
			StepNumber:      1,
			ScheduledDate:   time.Now().Add(96 * time.Hour),
			Recommendations: []RecommendationRecord{},
		}

		err := store.SavePurchaseExecution(ctx, exec)
		require.NoError(t, err)

		// Update the same execution
		exec.Status = "approved"
		exec.ApprovalToken = "new-token"
		err = store.SavePurchaseExecution(ctx, exec)
		require.NoError(t, err)

		retrieved, err := store.GetExecutionByID(ctx, upsertExecID)
		require.NoError(t, err)
		assert.Equal(t, "approved", retrieved.Status)
		assert.Equal(t, "new-token", retrieved.ApprovalToken)
	})
}

func TestPostgresStoreDB_PurchaseHistory(t *testing.T) {
	conn := setupTestContainerDB(t)
	if conn == nil {
		return
	}

	cleanupTestData(t, conn)

	store := NewPostgresStore(conn)
	ctx := context.Background()

	t.Run("SavePurchaseHistory and GetPurchaseHistory", func(t *testing.T) {
		record := &PurchaseHistoryRecord{
			AccountID:        "123456789012",
			PurchaseID:       "purchase-001",
			Timestamp:        time.Now().Truncate(time.Microsecond),
			Provider:         "aws",
			Service:          "rds",
			Region:           "us-east-1",
			ResourceType:     "db.r5.large",
			Count:            3,
			Term:             3,
			Payment:          "all-upfront",
			UpfrontCost:      2250.00,
			MonthlyCost:      pf(0),
			EstimatedSavings: 450.00,
			PlanID:           "",
			PlanName:         "Test Plan",
			RampStep:         1,
		}

		err := store.SavePurchaseHistory(ctx, record)
		require.NoError(t, err)

		history, err := store.GetPurchaseHistory(ctx, "123456789012", 10)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(history), 1)

		found := false
		for _, h := range history {
			if h.PurchaseID == "purchase-001" {
				found = true
				assert.Equal(t, "aws", h.Provider)
				assert.Equal(t, "rds", h.Service)
				assert.Equal(t, 3, h.Count)
				assert.Equal(t, "Test Plan", h.PlanName)
				require.NotNil(t, h.MonthlyCost, "expected non-nil MonthlyCost for explicit 0.0 row")
				assert.Equal(t, 0.0, *h.MonthlyCost)
			}
		}
		assert.True(t, found, "Expected purchase record not found")
	})

	t.Run("MonthlyCost nil round-trip", func(t *testing.T) {
		// Regression guard for migration 000063: a row inserted with
		// MonthlyCost: nil must round-trip back as nil (NULL -> nil), not 0.0.
		// This is the contract the slice's main change established, and the
		// frontend depends on it to render a dash instead of "$0.00" for Azure
		// all-upfront commitments where no recurring charge exists.
		record := &PurchaseHistoryRecord{
			AccountID:        "123456789012",
			PurchaseID:       "purchase-nil-cost",
			Timestamp:        time.Now().Truncate(time.Microsecond),
			Provider:         "azure",
			Service:          "vm",
			Region:           "eastus",
			ResourceType:     "Standard_D4s_v3",
			Count:            1,
			Term:             1,
			Payment:          "all-upfront",
			UpfrontCost:      1200.00,
			MonthlyCost:      nil,
			EstimatedSavings: 240.00,
		}

		err := store.SavePurchaseHistory(ctx, record)
		require.NoError(t, err)

		history, err := store.GetPurchaseHistory(ctx, "123456789012", 10)
		require.NoError(t, err)

		found := false
		for _, h := range history {
			if h.PurchaseID == "purchase-nil-cost" {
				found = true
				assert.Nil(t, h.MonthlyCost, "expected nil MonthlyCost to round-trip as nil (NULL -> nil)")
			}
		}
		assert.True(t, found, "Expected nil-MonthlyCost record not found")
	})

	t.Run("SavePurchaseHistory with empty optional fields", func(t *testing.T) {
		record := &PurchaseHistoryRecord{
			AccountID:    "empty-account",
			PurchaseID:   "purchase-empty",
			Timestamp:    time.Now(),
			Provider:     "gcp",
			Service:      "compute",
			Region:       "us-central1",
			ResourceType: "n1-standard-4",
			Count:        1,
			Term:         1,
			Payment:      "no-upfront",
			// PlanID and PlanName intentionally empty
		}

		err := store.SavePurchaseHistory(ctx, record)
		require.NoError(t, err)

		history, err := store.GetPurchaseHistory(ctx, "empty-account", 10)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(history), 1)

		for _, h := range history {
			if h.PurchaseID == "purchase-empty" {
				assert.Empty(t, h.PlanID)
				assert.Empty(t, h.PlanName)
			}
		}
	})

	t.Run("GetAllPurchaseHistory", func(t *testing.T) {
		// Add records for multiple accounts
		records := []*PurchaseHistoryRecord{
			{
				AccountID:    "account-a",
				PurchaseID:   "purchase-a1",
				Timestamp:    time.Now(),
				Provider:     "aws",
				Service:      "ec2",
				Region:       "us-west-2",
				ResourceType: "m5.xlarge",
				Count:        5,
				Term:         1,
				Payment:      "no-upfront",
			},
			{
				AccountID:    "account-b",
				PurchaseID:   "purchase-b1",
				Timestamp:    time.Now(),
				Provider:     "azure",
				Service:      "vm",
				Region:       "westus",
				ResourceType: "Standard_D2s_v3",
				Count:        2,
				Term:         3,
				Payment:      "partial-upfront",
			},
		}

		for _, record := range records {
			err := store.SavePurchaseHistory(ctx, record)
			require.NoError(t, err)
		}

		allHistory, err := store.GetAllPurchaseHistory(ctx, 100)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(allHistory), 2)
	})

	t.Run("GetPurchaseHistory empty result", func(t *testing.T) {
		history, err := store.GetPurchaseHistory(ctx, "nonexistent-account", 10)
		require.NoError(t, err)
		assert.NotNil(t, history)
		assert.Empty(t, history)
	})
}

// TestPostgresStoreDB_QueryExecutions_NullableTimestamps tests the queryExecutions
// helper's handling of all nullable timestamp fields.
func TestPostgresStoreDB_QueryExecutions_NullableTimestamps(t *testing.T) {
	conn := setupTestContainerDB(t)
	if conn == nil {
		return
	}

	cleanupTestData(t, conn)

	store := NewPostgresStore(conn)
	ctx := context.Background()

	// Create a plan for FK reference
	plan := &PurchasePlan{
		Name:                   "Nullable Test Plan",
		Enabled:                true,
		NotificationDaysBefore: 7,
		Services:               map[string]ServiceConfig{},
		RampSchedule:           PresetRampSchedules["immediate"],
	}
	err := store.CreatePurchasePlan(ctx, plan)
	require.NoError(t, err)

	t.Run("execution with no nullable timestamps", func(t *testing.T) {
		exec := &PurchaseExecution{
			PlanID:          plan.ID,
			Status:          "pending",
			StepNumber:      1,
			ScheduledDate:   time.Now().Add(24 * time.Hour),
			Recommendations: []RecommendationRecord{},
		}
		err := store.SavePurchaseExecution(ctx, exec)
		require.NoError(t, err)

		retrieved, err := store.GetExecutionByID(ctx, exec.ExecutionID)
		require.NoError(t, err)
		assert.Nil(t, retrieved.NotificationSent)
		assert.Nil(t, retrieved.CompletedAt)
		assert.Zero(t, retrieved.TTL)
	})

	t.Run("execution with all nullable timestamps set", func(t *testing.T) {
		notifSent := time.Now().Add(-2 * time.Hour)
		completedAt := time.Now().Add(-1 * time.Hour)

		exec := &PurchaseExecution{
			PlanID:           plan.ID,
			Status:           "completed",
			StepNumber:       3,
			ScheduledDate:    time.Now().Add(-3 * time.Hour),
			NotificationSent: &notifSent,
			CompletedAt:      &completedAt,
			Recommendations:  []RecommendationRecord{},
			TTL:              time.Now().Add(30 * 24 * time.Hour).Unix(),
		}
		err := store.SavePurchaseExecution(ctx, exec)
		require.NoError(t, err)

		retrieved, err := store.GetExecutionByID(ctx, exec.ExecutionID)
		require.NoError(t, err)
		assert.NotNil(t, retrieved.NotificationSent)
		assert.NotNil(t, retrieved.CompletedAt)
		assert.NotZero(t, retrieved.TTL)
	})
}

// TestPostgresStoreDB_PurchaseHistory_NullStrings tests the queryPurchaseHistory
// helper's handling of nullable string fields (plan_id, plan_name).
func TestPostgresStoreDB_PurchaseHistory_NullStrings(t *testing.T) {
	conn := setupTestContainerDB(t)
	if conn == nil {
		return
	}

	cleanupTestData(t, conn)

	store := NewPostgresStore(conn)
	ctx := context.Background()

	t.Run("history with plan_name set but no plan_id FK", func(t *testing.T) {
		// plan_id is a UUID FK to purchase_plans, so we leave it empty (NULL)
		// but plan_name can be any string
		record := &PurchaseHistoryRecord{
			AccountID:    "null-test-account",
			PurchaseID:   "null-test-purchase-1",
			Timestamp:    time.Now(),
			Provider:     "aws",
			Service:      "rds",
			Region:       "us-east-1",
			ResourceType: "db.r5.large",
			Count:        1,
			Term:         3,
			Payment:      "all-upfront",
			PlanID:       "", // NULL in DB (empty maps to sql.NullString{Valid: false})
			PlanName:     "Some Plan",
		}
		err := store.SavePurchaseHistory(ctx, record)
		require.NoError(t, err)

		history, err := store.GetPurchaseHistory(ctx, "null-test-account", 10)
		require.NoError(t, err)

		for _, h := range history {
			if h.PurchaseID == "null-test-purchase-1" {
				assert.Empty(t, h.PlanID)
				assert.Equal(t, "Some Plan", h.PlanName)
			}
		}
	})

	t.Run("history with valid plan_id UUID", func(t *testing.T) {
		// Create a plan to reference
		plan := &PurchasePlan{
			Name:                   "History Ref Plan",
			Enabled:                true,
			NotificationDaysBefore: 7,
			Services:               map[string]ServiceConfig{},
			RampSchedule:           PresetRampSchedules["immediate"],
		}
		err := store.CreatePurchasePlan(ctx, plan)
		require.NoError(t, err)

		record := &PurchaseHistoryRecord{
			AccountID:    "null-test-account",
			PurchaseID:   "null-test-purchase-with-plan",
			Timestamp:    time.Now(),
			Provider:     "aws",
			Service:      "rds",
			Region:       "us-east-1",
			ResourceType: "db.r5.large",
			Count:        1,
			Term:         3,
			Payment:      "all-upfront",
			PlanID:       plan.ID,
			PlanName:     "History Ref Plan",
		}
		err = store.SavePurchaseHistory(ctx, record)
		require.NoError(t, err)

		history, err := store.GetPurchaseHistory(ctx, "null-test-account", 10)
		require.NoError(t, err)

		for _, h := range history {
			if h.PurchaseID == "null-test-purchase-with-plan" {
				assert.Equal(t, plan.ID, h.PlanID)
				assert.Equal(t, "History Ref Plan", h.PlanName)
			}
		}
	})

	t.Run("history with empty plan_id and plan_name", func(t *testing.T) {
		record := &PurchaseHistoryRecord{
			AccountID:    "null-test-account",
			PurchaseID:   "null-test-purchase-2",
			Timestamp:    time.Now(),
			Provider:     "aws",
			Service:      "ec2",
			Region:       "us-west-2",
			ResourceType: "m5.large",
			Count:        1,
			Term:         1,
			Payment:      "no-upfront",
			// PlanID and PlanName empty
		}
		err := store.SavePurchaseHistory(ctx, record)
		require.NoError(t, err)

		history, err := store.GetPurchaseHistory(ctx, "null-test-account", 10)
		require.NoError(t, err)

		for _, h := range history {
			if h.PurchaseID == "null-test-purchase-2" {
				assert.Empty(t, h.PlanID)
				assert.Empty(t, h.PlanName)
			}
		}
	})
}

// ==========================================
// JSON MARSHALING EDGE CASES
// ==========================================

func TestPostgresStoreDB_JSONMarshalingEdgeCases(t *testing.T) {
	// Test that JSON marshaling works for various data structures
	// These tests validate the json.Marshal calls in the store methods

	t.Run("marshal empty services", func(t *testing.T) {
		services := map[string]ServiceConfig{}
		data, err := json.Marshal(services)
		require.NoError(t, err)
		assert.Equal(t, "{}", string(data))
	})

	t.Run("marshal nil services", func(t *testing.T) {
		var services map[string]ServiceConfig
		data, err := json.Marshal(services)
		require.NoError(t, err)
		assert.Equal(t, "null", string(data))
	})

	t.Run("marshal complex services", func(t *testing.T) {
		services := map[string]ServiceConfig{
			"aws:rds": {
				Provider:       "aws",
				Service:        "rds",
				Enabled:        true,
				Term:           3,
				Payment:        "all-upfront",
				Coverage:       80.0,
				IncludeEngines: []string{"postgres", "mysql"},
				ExcludeRegions: []string{"us-west-1"},
			},
		}
		data, err := json.Marshal(services)
		require.NoError(t, err)
		assert.Contains(t, string(data), "postgres")
	})

	t.Run("marshal ramp schedule", func(t *testing.T) {
		ramp := RampSchedule{
			Type:             "weekly",
			PercentPerStep:   25,
			StepIntervalDays: 7,
			CurrentStep:      2,
			TotalSteps:       4,
			StartDate:        time.Now(),
		}
		data, err := json.Marshal(ramp)
		require.NoError(t, err)
		assert.Contains(t, string(data), "weekly")
	})

	t.Run("marshal recommendations", func(t *testing.T) {
		recs := []RecommendationRecord{
			{
				ID: "rec-1", Provider: "aws", Service: "rds",
				Region: "us-east-1", ResourceType: "db.r5.large",
				Engine: "postgres", Count: 2, Term: 3,
				Payment: "all-upfront", UpfrontCost: 1500,
				Savings: 300, Selected: true, Purchased: false,
			},
		}
		data, err := json.Marshal(recs)
		require.NoError(t, err)

		var unmarshaled []RecommendationRecord
		err = json.Unmarshal(data, &unmarshaled)
		require.NoError(t, err)
		assert.Len(t, unmarshaled, 1)
		assert.Equal(t, "rec-1", unmarshaled[0].ID)
	})

	t.Run("unmarshal services from JSON", func(t *testing.T) {
		jsonData := `{"aws:rds":{"provider":"aws","service":"rds","enabled":true}}`
		var services map[string]ServiceConfig
		err := json.Unmarshal([]byte(jsonData), &services)
		require.NoError(t, err)
		assert.Len(t, services, 1)
		assert.Equal(t, "aws", services["aws:rds"].Provider)
	})

	t.Run("unmarshal ramp schedule from JSON", func(t *testing.T) {
		jsonData := `{"type":"monthly","percent_per_step":10,"step_interval_days":30,"total_steps":10}`
		var ramp RampSchedule
		err := json.Unmarshal([]byte(jsonData), &ramp)
		require.NoError(t, err)
		assert.Equal(t, "monthly", ramp.Type)
		assert.Equal(t, 10.0, ramp.PercentPerStep)
	})
}

// ==========================================
// HELPER FUNCTION EDGE CASES
// ==========================================

func TestTimeFromTTL_ZeroReturnsNil(t *testing.T) {
	result := timeFromTTL(0)
	assert.Nil(t, result)
}

func TestTimeFromTTL_FutureDate(t *testing.T) {
	futureUnix := time.Now().Add(30 * 24 * time.Hour).Unix()
	result := timeFromTTL(futureUnix)
	assert.NotNil(t, result)
	timePtr, ok := result.(*time.Time)
	assert.True(t, ok)
	assert.Equal(t, futureUnix, timePtr.Unix())
}

func TestNullStringFromString_WithPlanID(t *testing.T) {
	result := nullStringFromString("plan-123")
	assert.True(t, result.Valid)
	assert.Equal(t, "plan-123", result.String)
}

func TestNullStringFromString_EmptyPlanID(t *testing.T) {
	result := nullStringFromString("")
	assert.False(t, result.Valid)
	assert.Equal(t, "", result.String)
}

// ==========================================
// SQL NULL TIME HANDLING
// ==========================================

func TestSQLNullTimeHandling(t *testing.T) {
	t.Run("valid NullTime", func(t *testing.T) {
		nt := sql.NullTime{Time: time.Now(), Valid: true}
		assert.True(t, nt.Valid)
		assert.False(t, nt.Time.IsZero())
	})

	t.Run("invalid NullTime", func(t *testing.T) {
		nt := sql.NullTime{}
		assert.False(t, nt.Valid)
	})

	t.Run("NullString from string round trip", func(t *testing.T) {
		ns := nullStringFromString("test-value")
		assert.True(t, ns.Valid)
		assert.Equal(t, "test-value", ns.String)

		// And back
		var result string
		if ns.Valid {
			result = ns.String
		}
		assert.Equal(t, "test-value", result)
	})

	t.Run("NullString from empty string", func(t *testing.T) {
		ns := nullStringFromString("")
		assert.False(t, ns.Valid)

		var result string
		if ns.Valid {
			result = ns.String
		}
		assert.Empty(t, result)
	})
}

// TestPostgresStoreDB_SaveRIExchangeRecord_DefaultsEmptyPaymentDue pins the
// boundary fix in SaveRIExchangeRecord: an empty PaymentDue string is mapped
// to "0" before being passed to pgx, because the DECIMAL(20,6) column can't
// cast "" but CAN cast "0". The round-trip assertion uses strconv.ParseFloat
// rather than a byte-exact string compare because the DB may return "0" or
// "0.000000" depending on driver formatting.
func TestPostgresStoreDB_SaveRIExchangeRecord_DefaultsEmptyPaymentDue(t *testing.T) {
	conn := setupTestContainerDB(t)
	if conn == nil {
		return
	}

	cleanupTestData(t, conn)

	store := NewPostgresStore(conn)
	ctx := context.Background()

	record := &RIExchangeRecord{
		// AccountID must satisfy the ri_exchange_history schema
		// invariants: VARCHAR(20) + CHECK (account_id ~ '^\d{12}$').
		// The earlier test value "payment-due-test-account" tripped both
		// the length cap and the regex check — replaced with a
		// 12-digit AWS-style account ID.
		AccountID:          "123456789012",
		ExchangeID:         "payment-due-test-exchange",
		Region:             "us-east-1",
		SourceRIIDs:        []string{"ri-1"},
		SourceInstanceType: "r5.large",
		SourceCount:        1,
		TargetOfferingID:   "off-1",
		TargetInstanceType: "r5.xlarge",
		TargetCount:        1,
		PaymentDue:         "", // the case under test
		Status:             "pending",
		Mode:               "manual",
	}

	require.NoError(t, store.SaveRIExchangeRecord(ctx, record))
	require.NotEmpty(t, record.ID, "SaveRIExchangeRecord should populate ID")

	retrieved, err := store.GetRIExchangeRecord(ctx, record.ID)
	require.NoError(t, err)
	require.NotNil(t, retrieved)

	parsed, err := strconv.ParseFloat(retrieved.PaymentDue, 64)
	require.NoError(t, err, "PaymentDue must parse as a float; got %q", retrieved.PaymentDue)
	assert.Equal(t, 0.0, parsed, "empty PaymentDue must round-trip as numeric zero")
}

// TestPostgresStoreDB_CleanupOldExecutions_RetainsCanceledInsideHealthWindow
// is the regression guard for the retention/health-window mismatch: the
// cleanup sweep used to retain canceled rows on `scheduled_date` while
// CountExecutionsByPlanAndStatus counts them on `updated_at`.
//
// CancelExecutionAtomic stamps `updated_at` and leaves `scheduled_date`
// alone, and a plan's executions are created up front (with
// parseCreatePurchasesRequest accepting a past start date), so a purchase
// canceled TODAY can carry a `scheduled_date` from 40 days ago. Under the
// old predicate the very next sweep deleted it, even though the plan health
// score was still counting it, and the plan's badge silently gained up to
// 20 points overnight with no operator action.
//
// Runs against a real PostgreSQL rather than pgxmock because the defect is
// in what the predicate MEANS, not in how it is spelled: only the database
// can answer which rows actually survive.
func TestPostgresStoreDB_CleanupOldExecutions_RetainsCanceledInsideHealthWindow(t *testing.T) {
	conn := setupTestContainerDB(t)
	if conn == nil {
		return
	}

	cleanupTestData(t, conn)

	store := NewPostgresStore(conn)
	ctx := context.Background()

	plan := &PurchasePlan{
		Name:                   "Retention Window Plan",
		Enabled:                true,
		NotificationDaysBefore: 7,
		Services:               map[string]ServiceConfig{},
		RampSchedule:           PresetRampSchedules["immediate"],
	}
	require.NoError(t, store.CreatePurchasePlan(ctx, plan))

	// Rows are inserted with raw SQL so scheduled_date / updated_at /
	// expires_at can be set independently. The updated_at trigger is BEFORE
	// UPDATE only, so an explicit INSERT value is preserved.
	const retentionDays = 30
	cases := []struct {
		name          string
		status        string
		scheduledDays int  // offset in days from now (negative = past)
		updatedDays   int  // offset in days from now
		expiresDays   *int // nil => NULL expires_at
		shouldSurvive bool
		why           string
	}{
		{
			name:          "canceled-today-old-scheduled-date",
			status:        StatusCanceled,
			scheduledDays: -40,
			updatedDays:   0,
			shouldSurvive: true,
			why:           "canceled inside the health window; the score still counts it",
		},
		{
			name:          "canceled-today-old-scheduled-date-and-expired-token",
			status:        StatusCanceled,
			scheduledDays: -40,
			updatedDays:   0,
			expiresDays:   intPtr(-40),
			shouldSurvive: true,
			why:           "expires_at branch must not purge what the canceled branch retains",
		},
		{
			// Deliberately the LegacyStatusCanceled constant, i.e. the
			// pre-migration-000089 double-l DB value. The label avoids
			// spelling it out only to keep the misspell linter quiet.
			name:          "legacy-spelling-canceled-today-old-scheduled-date",
			status:        LegacyStatusCanceled,
			scheduledDays: -40,
			updatedDays:   0,
			shouldSurvive: true,
			why:           "the legacy spelling is retained on updated_at too",
		},
		{
			// The `failed` counterpart of the canceled case above, and the
			// reason branch 3's exclusion is sourced from
			// HealthScoredExecutionStatuses rather than a canceled-only
			// literal. The score counts failed at -10 each up to -40,
			// twice canceled's worst case, so purging one inside the
			// window is the larger of the two overnight jumps.
			name:          "failed-today-with-lapsed-expires-at",
			status:        StatusFailed,
			scheduledDays: -40,
			updatedDays:   0,
			expiresDays:   intPtr(-40),
			shouldSurvive: true,
			why:           "failed inside the health window; the score still counts it at up to -40",
		},
		{
			name:          "failed-long-ago-no-expiry",
			status:        StatusFailed,
			scheduledDays: -40,
			updatedDays:   -40,
			shouldSurvive: true,
			why:           "the sweep never deletes failed rows; plan_health.go's lookback depends on it",
		},
		{
			name:          "canceled-long-ago",
			status:        StatusCanceled,
			scheduledDays: -40,
			updatedDays:   -40,
			shouldSurvive: false,
			why:           "genuinely past retention; the fix must not disable cleanup",
		},
		{
			name:          "completed-old-scheduled-date",
			status:        "completed",
			scheduledDays: -40,
			updatedDays:   0,
			shouldSurvive: false,
			why:           "completed rows still retain on scheduled_date",
		},
		{
			name:          "notified-with-long-expired-token",
			status:        "notified",
			scheduledDays: 400,
			updatedDays:   0,
			expiresDays:   intPtr(-40),
			shouldSurvive: false,
			why:           "dead approval token; the expires_at branch still reaps non-canceled rows",
		},
		{
			name:          "pending-old-scheduled-date-no-expiry",
			status:        "pending",
			scheduledDays: -40,
			updatedDays:   0,
			shouldSurvive: true,
			why:           "non-terminal with no expiry deadline is never swept",
		},
	}

	byExecutionID := make(map[string]string, len(cases))
	for _, tc := range cases {
		execID := uuid.New().String()
		byExecutionID[execID] = tc.name

		var expiresAt any
		if tc.expiresDays != nil {
			expiresAt = time.Now().AddDate(0, 0, *tc.expiresDays)
		}

		_, err := conn.Exec(ctx, `
			INSERT INTO purchase_executions
			    (plan_id, execution_id, status, step_number,
			     scheduled_date, expires_at, created_at, updated_at)
			VALUES ($1, $2, $3, 1, $4, $5, NOW(), $6)
		`,
			plan.ID,
			execID,
			tc.status,
			time.Now().AddDate(0, 0, tc.scheduledDays),
			expiresAt,
			time.Now().AddDate(0, 0, tc.updatedDays),
		)
		require.NoErrorf(t, err, "seeding %s", tc.name)
	}

	deleted, err := store.CleanupOldExecutions(ctx, retentionDays)
	require.NoError(t, err)

	rows, err := conn.Query(ctx,
		`SELECT execution_id FROM purchase_executions WHERE plan_id = $1`, plan.ID)
	require.NoError(t, err)
	defer rows.Close()

	survived := make(map[string]bool)
	for rows.Next() {
		var execID string
		require.NoError(t, rows.Scan(&execID))
		survived[byExecutionID[execID]] = true
	}
	require.NoError(t, rows.Err())

	wantDeleted := int64(0)
	for _, tc := range cases {
		if tc.shouldSurvive {
			assert.Truef(t, survived[tc.name], "%s must SURVIVE the sweep: %s", tc.name, tc.why)
		} else {
			assert.Falsef(t, survived[tc.name], "%s must be DELETED: %s", tc.name, tc.why)
			wantDeleted++
		}
	}
	assert.Equal(t, wantDeleted, deleted, "RowsAffected must match the rows actually purged")
}
