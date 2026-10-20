package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"google.golang.org/api/googleapi"
	iamv1 "google.golang.org/api/iam/v1"
)

// gcpStepCreateServiceAccount creates the CUDly service account via the IAM
// SDK. It fails loud on any SDK error (no CLI fallback).
func gcpStepCreateServiceAccount(ctx context.Context, reader *bufio.Reader, projectID string) (string, error) {
	saName := "cudly-service-account"
	fmt.Println()
	fmt.Println("Step 3: Create Service Account")
	fmt.Println("------------------------------")
	fmt.Println("This creates a GCP Service Account for CUDly via the IAM API.")
	fmt.Println()
	fmt.Printf("[r]un, [S]kip (default)? (creates service account '%s' via SDK) ", saName)

	choice, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("failed to read service-account choice: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(choice)) {
	case "r", "run":
		email, createErr := createGCPServiceAccount(ctx, projectID, saName)
		if createErr != nil {
			return "", createErr
		}
		fmt.Printf("Service account created: %s\n", email)
		return email, nil
	case "s", "skip", "":
		fmt.Println("Skipping Create Service Account")
		return promptExistingGCPServiceAccount(reader)
	default:
		fmt.Printf("Unknown option, skipping\n")
		return promptExistingGCPServiceAccount(reader)
	}
}

// promptExistingGCPServiceAccount asks for the email of an existing service
// account when creation is skipped, so later steps never act on a principal
// that was neither created nor supplied by the operator.
func promptExistingGCPServiceAccount(reader *bufio.Reader) (string, error) {
	email, err := readRequiredInputLine(reader, "Skipped. Enter the existing service account email (name@project.iam.gserviceaccount.com): ", "service-account email")
	if err != nil {
		return "", err
	}
	email = strings.ToLower(email)
	local, domain, ok := strings.Cut(email, "@")
	label, hasSuffix := strings.CutSuffix(domain, ".gserviceaccount.com")
	if !ok || local == "" || !hasSuffix || label == "" || strings.Contains(domain, "@") ||
		strings.IndexFunc(email, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", fmt.Errorf("invalid service account email %q: expected a name@<project>.gserviceaccount.com address", email)
	}
	return email, nil
}

// gcpCustomRole is a single-permission custom role the wizard provisions.
type gcpCustomRole struct {
	id         string
	title      string
	permission string
}

var (
	gcpPurchaserRole = gcpCustomRole{id: "cudlyCommitmentPurchaser", title: "CUDly Commitment Purchaser", permission: "compute.commitments.create"}
	// gcpRecommendationReaderRole covers the project-scoped
	// google.compute.commitment.UsageCommitmentRecommender list call.
	gcpRecommendationReaderRole = gcpCustomRole{id: "cudlyRecommendationReader", title: "CUDly Recommendation Reader", permission: "recommender.usageCommitmentRecommendations.list"}
)

func ensureGCPCustomRole(ctx context.Context, projectID string, spec gcpCustomRole) (string, error) {
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
	parent := "projects/" + projectID
	name := parent + "/roles/" + spec.id
	want := []string{spec.permission}
	role, err := svc.Projects.Roles.Get(name).Context(ctx).Do()
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) && apiErr.Code == 404 {
		role, err = svc.Projects.Roles.Create(parent, &iamv1.CreateRoleRequest{
			RoleId: spec.id,
			Role:   &iamv1.Role{Title: spec.title, Stage: "GA", IncludedPermissions: want},
		}).Context(ctx).Do()
	}
	if err != nil {
		return "", fmt.Errorf("failed to provision custom role %s; review the role and setup permissions before retrying: %w", name, err)
	}
	if role.Name != name || role.Deleted || role.Stage == "DISABLED" || !slices.Equal(role.IncludedPermissions, want) {
		return "", fmt.Errorf("unsafe custom role %s: expected an enabled role containing only %s; review it before retrying", name, spec.permission)
	}
	return name, nil
}
