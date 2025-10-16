# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Zadig is an open-source, distributed, cloud-native Continuous Delivery (CD) platform designed for developers. It provides CI/CD capabilities with Kubernetes integration, supporting high-concurrency workflows, service-oriented environments, and non-intrusive testing automation.

**Language:** Go 1.19
**Main Branch:** main

## Architecture

### Microservices Structure

Zadig follows a microservice architecture. All microservices are located in `pkg/microservice/`:

- **aslan**: Main business logic service (projects, environments, services, workflows, builds, system management). This is the core service.
- **cron**: Cronjob runner for scheduled tasks
- **warpdrive**: Workflow engine that manages reaper and predator
- **reaper**: Workflow runner for building and testing tasks
- **predator**: Workflow runner for distribution tasks
- **jenkins-plugin**: Connector to trigger Jenkins jobs and retrieve job information
- **packager**: Package management service
- **hubagent / hubserver**: Multi-cluster management components
- **policy**: Policy management service using OPA (Open Policy Agent)
- **user**: User management service
- **systemconfig**: System configuration service (codehosts, connectors, email, jira, etc.)
- **picket**: Evaluation and filtering service
- **podexec**: Pod execution service for remote terminal access

### Core Aslan Service Structure

The aslan service (`pkg/microservice/aslan/core/`) is organized by domain:

- **workflow**: Workflow orchestration, execution, and testing
- **environment**: Environment management for services
- **project**: Project configuration and management
- **service**: Service definitions and configurations
- **build**: Build configuration and execution
- **delivery**: Release and delivery management
- **common**: Shared utilities, registries, and repositories
- **system**: System-level configurations
- **multicluster**: Multi-cluster Kubernetes management
- **collaboration**: Team collaboration features
- **code**: Code host integrations (GitHub, GitLab, Gerrit, Gitee)
- **stat**: Statistics and analytics
- **log**: Log collection and management
- **label**: Labeling and categorization
- **templatestore**: Template management
- **cron**: Scheduled task definitions
- **policy**: Authorization policies

### Key Components

- **API Gateway**: Gloo Edge with OPA for authentication/authorization, Dex for identity management
- **Message Queue**: NSQ for inter-service communication
- **Databases**: MongoDB (business data), MySQL (user data and Dex configuration)
- **Workflow Runners**: Containerized job execution on Kubernetes
- **Frontend**: Vue.js application (separate repository: zadig-portal)

## Development Commands

### Building Microservices

Build Docker images using docker buildx for multi-architecture support:

```bash
# Build all microservices (without pushing)
make microservice

# Build and push all microservices
make microservice.push VERSION=<version>

# Build single microservice for development (amd64 only, pushes to registry)
make <service-name>.dev VERSION=<version>

# Available services:
# aslan, cron, hub-agent, hub-server, init, jenkins-plugin,
# packager-plugin, predator-plugin, resource-server, ua, warpdrive
```

### Testing

```bash
# Run unit tests for specific packages (see ut.file for test packages)
go test ./pkg/microservice/cron/core/service/client
go test ./pkg/microservice/reaper/core/service/...

# Run tests with coverage
go test -cover ./pkg/...

# Run tests for specific microservice
go test ./pkg/microservice/aslan/...
```

### Code Quality

Follow [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments) as the style guideline.

```bash
# Format code
go fmt ./...

# Run go vet
go vet ./...
```

### API Documentation

Zadig uses Swag for API documentation on the aslan service:

```bash
# Generate/update Swagger API docs after modifying aslan APIs
swag init -d ./pkg/microservice/aslan/ -g server/rest/router.go -o ./pkg/microservice/aslan/server/rest/doc

# Access API docs at: http://<your-instance>/api/aslan/apidocs/index.html
```

**Important**: When adding/modifying APIs in aslan, document them with [Swag declarative comments](https://github.com/swaggo/swag#declarative-comments-format) in your handler code.

### Running Locally

For local development and debugging, use the Zadig CLI (see [Zadig CLI documentation](https://docs.koderover.com/zadig/v1.7.1/cli/kodespace-usage-for-contributor/)).

Each microservice has an entry point in `cmd/<service-name>/main.go` that calls the corresponding server in `pkg/microservice/<service-name>/server/`.

## Important Patterns

### Router Injection Pattern

Services use a router injection pattern. See `pkg/microservice/aslan/server/rest/router.go` for the main example:

```go
type injector interface {
    Inject(router *gin.RouterGroup)
}
```

Handlers implement `Inject()` to register their routes. This pattern is used consistently across all API handlers.

### Microservice Communication

- Services communicate via NSQ message queue for async operations
- Direct HTTP calls for synchronous operations
- MongoDB for persistent data sharing

### Kubernetes Resource Management

The codebase extensively uses `k8s.io/client-go` and custom controllers:
- Environment operations create/update K8s resources dynamically
- Helm charts are the primary deployment method
- Support for raw YAML deployments as well
- Multi-cluster support via hub-agent/hub-server architecture

### Workflow Plugins

Custom workflow plugins can be added in `pkg/microservice/aslan/core/workflow/service/workflow/plugins/`. Each plugin is versioned (e.g., `v0.0.1/`) and includes YAML descriptors.

## Code Structure Conventions

- **Handler Layer** (`handler/`): HTTP request handling, validation, and response formatting
- **Service Layer** (`service/`): Business logic implementation
- **Repository Layer** (within `service/` or separate): MongoDB/database operations
- **Config** (`config/`): Service configuration and initialization

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for detailed contribution guidelines.

### Key Points

- Sign off all commits with DCO
- For non-trivial changes, create a design doc using the template in `community/rfc/`
- Follow atomic PR principles - break large changes into smaller PRs
- All PRs must pass existing tests
- Update API documentation if you modify aslan APIs
- Use "WIP:" prefix for draft PRs

### Testing Your Changes

1. **Cloud Testing**: Use the test environment at https://os.koderover.com, fork your environment, and run the zadig-workflow with your PR
2. **Local Testing**: Use Zadig CLI to test backend changes locally against your test environment

## Dependencies

Major dependencies:
- **Kubernetes**: k8s.io/client-go v0.25.0, controller-runtime
- **Helm**: helm.sh/helm/v3 v3.9.4, go-helm-client
- **Web Framework**: gin-gonic/gin v1.8.1
- **Database**: MongoDB driver v1.10.2, GORM v1.23.8 (MySQL)
- **Message Queue**: nsqio/go-nsq v1.1.0
- **Git Integrations**: go-github, go-gitlab, go-gerrit, go-gitee
- **Container Tools**: docker/docker, moby/buildkit
- **OPA**: dexidp/dex, open-policy-agent
