module github.com/jeon-jihyeon/meetproxy

go 1.26.10

// Legacy API superseded by v0.2.0: migrate to the generic control and event API
retract [v0.0.0-0, v0.2.0-0]

require (
	github.com/stretchr/testify v1.12.1
	go.uber.org/mock v0.6.0
)

require go.yaml.in/yaml/v3 v3.0.5 // indirect
