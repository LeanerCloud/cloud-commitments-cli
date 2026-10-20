package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/cloudresourcemanager/v1"
	iamv1 "google.golang.org/api/iam/v1"
	"google.golang.org/api/option"
)

// gcpSDKCallTimeout bounds each GCP SDK helper so an ADC lookup or API call
// cannot hang indefinitely (the calls inherit context.Background()).
const gcpSDKCallTimeout = 60 * time.Second

// newGCPAPIOption returns an oauth2 token source option for the google.golang.org
// API client, using Application Default Credentials. ADC resolves credentials in
// priority order: GOOGLE_APPLICATION_CREDENTIALS env var, gcloud ADC cache
// (populated by "gcloud auth application-default login"), Workload Identity,
// Metadata Server.
//
// NOTE: "gcloud auth login" (wizard Step 1) updates the gcloud user session but
// does NOT populate the ADC cache; the wizard's Step 1b
// ("gcloud auth application-default login") does that. If ADC is still not
// available (e.g. the operator skipped Step 1b) these calls fail loud with a
// hint to run "gcloud auth application-default login".
func newGCPAPIOption(ctx context.Context) (option.ClientOption, error) {
	ts, err := google.DefaultTokenSource(ctx,
		"https://www.googleapis.com/auth/cloud-platform",
		"https://www.googleapis.com/auth/iam",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to obtain GCP Application Default Credentials: %w\n"+
			"Hint: run 'gcloud auth application-default login' first", err)
	}
	return option.WithTokenSource(ts), nil
}

// listGCPProjects lists GCP projects accessible to the operator via the Cloud
// Resource Manager API v1 and prints them in a table. This replaces the
// "gcloud projects list" CLI call.
func listGCPProjects(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, gcpSDKCallTimeout)
	defer cancel()

	opt, err := newGCPAPIOption(ctx)
	if err != nil {
		return err
	}

	svc, err := cloudresourcemanager.NewService(ctx, opt)
	if err != nil {
		return fmt.Errorf("failed to create resource manager client: %w", err)
	}

	fmt.Printf("%-30s  %-25s  %s\n", "NAME", "PROJECT_ID", "PROJECT_NUMBER")
	fmt.Println(strings.Repeat("-", 80))

	req := svc.Projects.List()
	if err := req.Pages(ctx, func(page *cloudresourcemanager.ListProjectsResponse) error {
		for _, p := range page.Projects {
			fmt.Printf("%-30s  %-25s  %d\n", p.Name, p.ProjectId, p.ProjectNumber)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to list GCP projects: %w", err)
	}
	return nil
}

// createGCPServiceAccount creates a GCP IAM service account via the IAM API v1.
// This replaces "gcloud iam service-accounts create".
func createGCPServiceAccount(ctx context.Context, projectID, saName string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gcpSDKCallTimeout)
	defer cancel()

	opt, err := newGCPAPIOption(ctx)
	if err != nil {
		return "", err
	}

	svc, err := iamv1.NewService(ctx, opt)
	if err != nil {
		return "", fmt.Errorf("failed to create IAM client: %w", err)
	}

	req := &iamv1.CreateServiceAccountRequest{
		AccountId: saName,
		ServiceAccount: &iamv1.ServiceAccount{
			DisplayName: "CUDly Service Account",
			Description: "Service account for CUDly commitment management",
		},
	}

	sa, err := svc.Projects.ServiceAccounts.Create("projects/"+projectID, req).Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("failed to create service account: %w", err)
	}

	return sa.Email, nil
}
