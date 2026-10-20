package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
)

// runGCPSetupCommands guides the operator through GCP setup.
//
// Step 1 (gcloud auth login) and Step 1b (gcloud auth application-default
// login): performed via the GCP CLI. Both are interactive browser-based OAuth
// flows that cannot be replicated through SDK calls on behalf of an operator
// who does not yet have a credential. Step 1 establishes the gcloud user
// session; Step 1b populates the Application Default Credentials (ADC) cache
// that every SDK-based step below authenticates through. Both are run in one
// pass so a fresh operator completes setup without re-running the wizard.
//
// Step 2 (list projects): performed via the Cloud Resource Manager SDK v1,
// using Application Default Credentials (ADC). Fails loud if ADC is not
// available (no CLI fallback).
//
// Step 3 (gcloud config set project): sets local gcloud config state. There is
// no cloud-API equivalent for writing to the local gcloud configuration file,
// so this step remains a CLI call.
//
// Steps 4-6 (create SA, grant role, create key): performed via GCP IAM and
// Cloud Resource Manager SDK v1 APIs using ADC. Fail loud on any SDK error
// (no CLI fallback).
func runGCPSetupCommands(ctx context.Context, reader *bufio.Reader, beginUpload func() context.Context) (keyFile, keyName string, err error) {
	err = gcpStepLogin(reader)
	if err != nil {
		return "", "", err
	}

	projectID, err := gcpStepSelectProject(ctx, reader)
	if err != nil {
		return "", "", err
	}

	saEmail, err := gcpStepCreateServiceAccount(ctx, reader, projectID)
	if err != nil {
		return "", "", err
	}

	err = gcpStepGrantRole(ctx, reader, projectID, saEmail)
	if err != nil {
		return "", "", err
	}

	return gcpStepCreateKey(reader, saEmail, beginUpload)
}

// gcpStepLogin runs the two interactive gcloud logins the wizard needs:
// "gcloud auth login" (user session) and "gcloud auth application-default
// login" (ADC cache). Both are browser-based OAuth flows with no SDK
// equivalent. The ADC login is required because the SDK-based steps below
// (list projects, create service account, grant role, create key) authenticate
// through Application Default Credentials, which "gcloud auth login" alone does
// NOT populate -- so without it a fresh operator would hard-abort at Step 2.
func gcpStepLogin(reader *bufio.Reader) error {
	fmt.Println("Step 1: GCP Login")
	fmt.Println("-----------------")
	fmt.Println("This opens a browser window for GCP authentication (user session).")
	fmt.Println()
	if err := promptAndRunGCPCommand(reader, "GCP Login", "gcloud auth login", "gcloud", "auth", "login"); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("Step 1b: GCP Application Default Credentials Login")
	fmt.Println("-------------------------------------------------")
	fmt.Println("This opens a browser window to populate the Application Default")
	fmt.Println("Credentials (ADC) cache used by the SDK-based steps below")
	fmt.Println("(list projects, create service account, grant role, create key).")
	fmt.Println()
	return promptAndRunGCPCommand(reader, "GCP ADC Login",
		"gcloud auth application-default login",
		"gcloud", "auth", "application-default", "login")
}

// gcpStepSelectProject optionally lists projects and prompts for a project ID.
// The listing is behind a [R]un/[S]kip prompt so an operator who already knows
// their project ID can proceed even if the SDK listing would fail; when RUN it
// fails loud (no CLI fallback), instructing the operator to run
// "gcloud auth application-default login" first.
func gcpStepSelectProject(ctx context.Context, reader *bufio.Reader) (string, error) {
	fmt.Println()
	fmt.Println("Step 2: Select Project")
	fmt.Println("----------------------")

	run, err := promptRunOrSkipListing(reader, "the GCP project listing (via SDK)")
	if err != nil {
		return "", err
	}
	if run {
		fmt.Println("Listing your GCP projects via SDK (Application Default Credentials)...")
		fmt.Println()
		if err = listGCPProjects(ctx); err != nil {
			return "", fmt.Errorf("failed to list GCP projects via SDK: %w\n"+
				"Ensure Application Default Credentials are set: run 'gcloud auth application-default login' first", err)
		}
		fmt.Println()
	}

	projectID, err := readRequiredInputLine(reader, "Enter your Project ID: ", "project ID")
	if err != nil {
		return "", err
	}
	if err := validateGCPProjectID(projectID); err != nil {
		return "", err
	}

	// Set the project in the local gcloud config. This is a local operation
	// (writes to ~/.config/gcloud/properties) with no cloud-API equivalent.
	fmt.Println()
	fmt.Println("Setting gcloud project context (local config)...")
	// #nosec G204 G702 -- local gcloud config write: fixed argv ("gcloud config set project"), projectID pre-validated by validateGCPProjectID (strict regex) just above, passed as a discrete argv element with no shell, so it cannot inject
	cmd := exec.Command("gcloud", "config", "set", "project", projectID)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to set project: %w", err)
	}
	return projectID, nil
}
