#!/bin/bash
# Setup git-secrets to prevent committing sensitive information

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo "========================================"
echo "Git Secrets Setup"
echo "========================================"
echo ""

# Check if git-secrets is installed
if ! command -v git-secrets &> /dev/null; then
    echo -e "${RED}✗ git-secrets not installed${NC}"
    echo ""
    echo "Installation instructions:"
    echo "  macOS: brew install git-secrets"
    echo "  Linux: git clone https://github.com/awslabs/git-secrets.git && cd git-secrets && make install"
    echo ""
    exit 1
fi

echo -e "${GREEN}✓ git-secrets is installed${NC}"
echo ""

# Install git hooks
echo "Installing git-secrets hooks..."
if git secrets --install -f; then
    echo -e "${GREEN}✓ Git hooks installed${NC}"
else
    echo -e "${RED}✗ Failed to install git hooks${NC}"
    exit 1
fi

# Register AWS secret patterns
echo ""
echo "Registering AWS secret patterns..."
git secrets --register-aws

# Add custom patterns
echo ""
echo "Adding custom secret patterns..."

# AWS patterns
git secrets --add 'AKIA[0-9A-Z]{16}'                                    # AWS Access Key ID
git secrets --add '[^A-Za-z0-9/+=]{40}[^A-Za-z0-9/+=]'                 # AWS Secret Access Key
git secrets --add 'aws(.{0,20})?['\''"][0-9a-zA-Z/+]{40}['\''"]'       # AWS Credentials

# GCP patterns
git secrets --add 'type.*service_account'                               # GCP Service Account JSON
git secrets --add 'AIza[0-9A-Za-z-_]{35}'                              # GCP API Key

# Azure patterns
git secrets --add '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}' # Azure GUID
git secrets --add 'DefaultEndpointsProtocol=https'                      # Azure Connection String

# Generic secrets
git secrets --add 'password.*[=:]\s*[^\s]+'                            # Password assignments
git secrets --add 'api[_-]?key.*[=:]\s*[^\s]+'                        # API keys
git secrets --add 'secret.*[=:]\s*[^\s]+'                             # Secrets
git secrets --add 'token.*[=:]\s*[^\s]+'                              # Tokens
git secrets --add 'private[_-]?key'                                   # Private keys
git secrets --add '-----BEGIN (RSA|DSA|EC|OPENSSH) PRIVATE KEY-----'  # PEM private keys

# Database connection strings
git secrets --add 'postgres://[^:]+:[^@]+@'                           # PostgreSQL
git secrets --add 'mysql://[^:]+:[^@]+@'                              # MySQL
git secrets --add 'mongodb(\+srv)?://[^:]+:[^@]+@'                    # MongoDB

# Add allowed patterns (things that look like secrets but aren't)
echo ""
echo "Adding allowed patterns (false positives)..."

# Terraform variables and outputs
git secrets --add --allowed 'var\.'
git secrets --add --allowed 'local\.'
git secrets --add --allowed 'output\.'
git secrets --add --allowed 'data\.'

# Test files
git secrets --add --allowed '_test\.go'
git secrets --add --allowed 'testdata/'
git secrets --add --allowed 'test_password'
git secrets --add --allowed 'test_secret'

# Documentation and examples
git secrets --add --allowed 'example\.com'
git secrets --add --allowed 'YOUR_'
git secrets --add --allowed '<your-'
git secrets --add --allowed 'placeholder'

echo -e "${GREEN}✓ Secret patterns registered${NC}"

# Scan existing repository
echo ""
echo "========================================"
echo "Scanning Existing Repository"
echo "========================================"
echo ""

if git secrets --scan -r; then
    echo ""
    echo -e "${GREEN}✓ No secrets found in repository${NC}"
else
    echo ""
    echo -e "${RED}✗ Secrets detected in repository!${NC}"
    echo "Please remove them before committing."
    exit 1
fi

echo ""
echo "========================================"
echo "Setup Complete"
echo "========================================"
echo ""
echo "Git-secrets is now active and will prevent commits containing secrets."
echo ""
echo "Useful commands:"
echo "  git secrets --scan        - Scan staged files"
echo "  git secrets --scan-history - Scan entire history"
echo "  git secrets --list        - List registered patterns"
echo ""
