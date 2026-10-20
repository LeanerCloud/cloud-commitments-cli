package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// promptAndRunExplicitCommand shows a command and asks to run or skip.
// Takes explicit program and args to avoid command injection via string splitting.
func promptAndRunExplicitCommand(reader *bufio.Reader, name, displayCmd, program string, args ...string) error {
	fmt.Printf("Command: %s\n", displayCmd)
	fmt.Println()
	fmt.Printf("[R]un, [S]kip? ")

	choice, err := readTrimmedLine(reader)
	if err != nil {
		return fmt.Errorf("failed to read choice: %w", err)
	}
	choice = strings.ToLower(choice)

	switch choice {
	case "r", "run", "":
		return executeExplicitCommand(reader, displayCmd, program, args...)
	case "s", "skip":
		fmt.Printf("Skipping %s\n", name)
		return nil
	default:
		fmt.Printf("Unknown option '%s', skipping\n", choice)
		return nil
	}
}

// executeExplicitCommand runs a command with explicit program and arguments.
// It is used only for the interactive "az login" auth bootstrap (Step 1),
// which has no SDK equivalent that preserves the cached-credential UX.
// The caller's reader is threaded through to the retry prompt so all input
// is consumed from one consistent buffered stream (a fresh
// bufio.NewReader(os.Stdin) here would drop input already buffered by the
// caller's reader, breaking piped input after earlier prompts).
func executeExplicitCommand(reader *bufio.Reader, displayCmd, program string, args ...string) error {
	fmt.Println()
	fmt.Printf("Executing: %s\n", displayCmd)
	fmt.Println(strings.Repeat("-", 60))

	// #nosec G204 -- interactive operator auth (az login): program and args are hardcoded literals from the caller (runAzureSetupCommands passes "az","login"), no shell, not attacker-controlled
	cmd := exec.Command(program, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	err := cmd.Run()
	fmt.Println(strings.Repeat("-", 60))

	if err != nil {
		fmt.Printf("Command failed: %v\n", err)
		fmt.Print("Continue anyway? [y/N]: ")
		response, readErr := readTrimmedLine(reader)
		if readErr != nil {
			return fmt.Errorf("failed to read response: %w", readErr)
		}
		if !strings.EqualFold(response, "y") {
			return fmt.Errorf("command failed: %w", err)
		}
	}

	return nil
}
