# Getting Started

This guide will help you get started with LinguaFlow.

## Prerequisites

- Go 1.21 or higher
- Node.js 20 or higher
- pnpm package manager

## Installation

### Build from Source

```bash
# Clone the repository
git clone https://github.com/MeowSalty/LinguaFlow.git
cd LinguaFlow

# Install dependencies
task backend:install
task frontend:install

# Build
task backend:build
```

### Docker Deployment

```bash
docker pull ghcr.io/meowsalty/linguaflow:latest
docker run -p 8080:8080 ghcr.io/meowsalty/linguaflow:latest
```

## Configuration

LinguaFlow supports configuration through environment variables and configuration files. See the [Configuration Guide](/en/guide/configuration) for more details.

## Next Steps

- Read the [Configuration Guide](/en/guide/configuration) for detailed configuration options
- Check the [API Reference](/en/api/) for available API endpoints
