module github.com/LeanerCloud/CUDly/providers/aws

go 1.26.6

require (
	github.com/LeanerCloud/CUDly/pkg v0.0.0
	github.com/aws/aws-sdk-go-v2 v1.41.5
	github.com/aws/aws-sdk-go-v2/config v1.29.12
	github.com/aws/aws-sdk-go-v2/service/costexplorer v1.61.0
	github.com/aws/aws-sdk-go-v2/service/ec2 v1.251.2
	github.com/aws/aws-sdk-go-v2/service/elasticache v1.50.3
	github.com/aws/aws-sdk-go-v2/service/memorydb v1.31.4
	github.com/aws/aws-sdk-go-v2/service/opensearch v1.52.3
	github.com/aws/aws-sdk-go-v2/service/organizations v1.45.3
	github.com/aws/aws-sdk-go-v2/service/rds v1.97.3
	github.com/aws/aws-sdk-go-v2/service/redshift v1.58.3
	github.com/aws/aws-sdk-go-v2/service/savingsplans v1.31.0
	github.com/aws/aws-sdk-go-v2/service/sts v1.33.17
	github.com/aws/smithy-go v1.24.2
	github.com/stretchr/testify v1.11.1
	golang.org/x/sync v0.21.0
)

require (
	github.com/aws/aws-sdk-go-v2/credentials v1.17.65 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.16.30 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.21 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.21 // indirect
	github.com/aws/aws-sdk-go-v2/internal/ini v1.8.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.21 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.25.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.30.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/stretchr/objx v0.5.3 // indirect
	golang.org/x/sys v0.47.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/LeanerCloud/CUDly/pkg => ../../pkg
