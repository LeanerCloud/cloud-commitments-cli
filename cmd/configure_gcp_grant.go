package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"
)

func gcpStepGrantRole(ctx context.Context, reader *bufio.Reader, projectID, saEmail string) error {
	member := fmt.Sprintf("serviceAccount:%s", saEmail)

	fmt.Println()
	fmt.Println("Step 4: Grant IAM Roles")
	fmt.Println("-----------------------")
	fmt.Println("Grant the required roles to the service account.")
	fmt.Println()
	fmt.Println("Prerequisite: enable recommender.googleapis.com in the service account's project (this step does not enable it).")
	fmt.Printf("[r]un, [S]kip (default)? (creates or validates %s with %s and %s with %s, then grants both and roles/compute.viewer to %s on project %s via SDK) ",
		gcpPurchaserRole.id, gcpPurchaserRole.permission, gcpRecommendationReaderRole.id, gcpRecommendationReaderRole.permission, saEmail, projectID)

	choice, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read grant-role choice: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(choice)) {
	case "r", "run":
		grants := []string{"roles/compute.viewer"}
		for _, spec := range []gcpCustomRole{gcpPurchaserRole, gcpRecommendationReaderRole} {
			role, roleErr := ensureGCPCustomRole(ctx, projectID, spec)
			if roleErr != nil {
				return roleErr
			}
			grants = append(grants, role)
		}
		for _, grant := range grants {
			if grantErr := grantGCPIAMRole(ctx, projectID, member, grant); grantErr != nil {
				return grantErr
			}
			fmt.Printf("Role %s granted to %s on project %s.\n", grant, saEmail, projectID)
		}
	case "s", "skip", "":
		fmt.Println("Skipping Grant IAM Roles")
	default:
		fmt.Printf("Unknown option, skipping\n")
	}
	return nil
}

// gcpStepCreateKey creates a JSON key file for the service account. It returns
// the written key-file path and the key resource name only when a key was
// actually created; on skip or an unknown choice it returns empty strings so
// the caller knows to prompt for an existing credentials file instead of
// assuming one was written.
func gcpStepCreateKey(reader *bufio.Reader, saEmail string, beginUpload func() context.Context) (keyFile, keyName string, err error) {
	keyFile, err = newMintedGCPKeyPath()
	if err != nil {
		return "", "", err
	}

	fmt.Println()
	fmt.Println("Step 5: Create and Download Key")
	fmt.Println("-------------------------------")
	fmt.Println("Create a JSON key file for the service account.")
	fmt.Println()
	fmt.Printf("[r]un, [S]kip (default)? (creates key for %s via SDK; the local copy is removed after upload) ", saEmail)

	choice, err := reader.ReadString('\n')
	if err != nil {
		return "", "", errors.Join(fmt.Errorf("failed to read create-key choice: %w", err), removeMintedGCPKey(keyFile))
	}
	switch strings.ToLower(strings.TrimSpace(choice)) {
	case "r", "run":
		keyName, err = createGCPServiceAccountKey(beginUpload(), saEmail, keyFile)
		if err != nil {
			return "", "", errors.Join(err, removeMintedGCPKey(keyFile))
		}
		return keyFile, keyName, nil
	case "s", "skip", "":
		fmt.Println("Skipping Create Key")
	default:
		fmt.Printf("Unknown option, skipping\n")
	}

	// No key file was written; the caller will prompt for an existing one.
	fmt.Println()
	return "", "", removeMintedGCPKey(keyFile)
}
