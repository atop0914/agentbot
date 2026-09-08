<div align="center">

# 🤖 AgentBot

**Enterprise AI Agent Platform**

[![Go Version](https://img.shields.io/badge/Go-1.25-00ADD8?style=flat-square&logo=go&logoColor=white)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-blue?style=flat-square)](LICENSE)
[![Build Status](https://img.shields.io/badge/Build-Passing-brightgreen?style=flat-square)](#)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?style=flat-square&logo=docker&logoColor=white)](#)

**Build autonomous AI agents that work for you 24/7**

[Features](#features) • [Architecture](#architecture) • [Quick Start](#quick-start) • [Documentation](#documentation) • [API](#api)

---

## 🎯 What is AgentBot?

AgentBot is an **enterprise-grade AI agent platform** inspired by [xAI's Grok Bot](https://x.ai/news/grok-bot-for-enterprise). It enables you to deploy autonomous AI agents that:

- 🖥️ **Run in isolated cloud environments** — Each agent has its own container with full system access
- 🔄 **Execute tasks autonomously** — From simple commands to complex multi-step workflows
- 🌐 **Interact with any application** — Browser automation, terminal, file system, APIs
- 🤝 **Collaborate in teams** — Multiple agents can communicate and coordinate
- 📊 **Provide full observability** — Real-time monitoring, audit logs, and performance metrics

### Why AgentBot?

<table>
<tr>
<td width="50%">

**Without AgentBot**
- ❌ Manual, repetitive tasks
- ❌ Context switching overhead
- ❌ Limited working hours
- ❌ Human error prone
- ❌ Difficult to scale

</td>
<td width="50%">

**With AgentBot**
- ✅ Automated workflows
- ✅ 24/7 autonomous operation
- ✅ Consistent execution
- ✅ Full audit trail
- ✅ Scale to hundreds of agents

</td>
</tr>
</table>

---

## ✨ Features

### 🖥️ Cloud Environment
- Isolated Docker containers per agent
- Resource limits (CPU, Memory, Disk)
- Persistent storage across sessions
- Network isolation and security

### 🌐 Browser Automation
- Playwright-based browser control
- Chrome profile import/export
- Screenshot and DOM extraction
- Cookie and session management

### 💻 Terminal Access
- Execute commands in sandbox
- File upload/download
- Process management
- SSH tunneling

### 📋 Task Management
- Goal decomposition (AI-powered)
- Task dependency graph
- Parallel execution
- Progress tracking

### 🤖 Multi-Agent Collaboration
- Agent-to-agent messaging
- Group chat and channels
- Task delegation
- Status reporting

### 📝 Workflow Templates
- Record and replay workflows
- Template marketplace
- Parameterized execution
- Version control

### 🔐 Enterprise Security
- JWT authentication
- OAuth2 (GitHub, Google)
- Role-based access control (RBAC)
- Audit logging

### 📊 Monitoring & Observability
- Real-time dashboard
- Resource metrics
- Error tracking
- Alert system

---

## 🏗️ Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                        AgentBot Platform                         │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐          │
│  │   Web UI     │  │   REST API   │  │  WebSocket   │          │
│  │  (React)     │  │   (Gin)      │  │   Server     │          │
│  └──────────────┘  └──────────────┘  └──────────────┘          │
│                           │                                      │
│                           ▼                                      │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │                    Service Layer                         │   │
│  ├─────────────┬─────────────┬─────────────┬─────────────┤   │
│  │    Agent    │    Task     │   Browser   │  Template   │   │
│  │   Service   │   Service   │   Service   │   Service   │   │
│  └─────────────┴─────────────┴─────────────┴─────────────┘   │
│                           │                                      │
│                           ▼                                      │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │                  Infrastructure Layer                    │   │
│  ├─────────────┬─────────────┬─────────────┬─────────────┤   │
│  │  PostgreSQL │    Redis    │   Docker    │   NATS      │   │
│  │  (Storage)  │   (Cache)   │ (Isolation) │  (Messaging)│   │
│  └─────────────┴─────────────┴─────────────┴─────────────┘   │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### Component Overview

| Component | Technology | Purpose |
|-----------|------------|---------|
| **API Server** | Go + Gin | REST API and WebSocket |
| **Agent Runtime** | Docker | Isolated execution environment |
| **Task Engine** | Go | Task decomposition and scheduling |
| **Browser** | Playwright | Web automation |
| **Database** | PostgreSQL | Persistent storage |
| **Cache** | Redis | Session and real-time data |
| **Message Queue** | NATS | Agent communication |
| **Monitoring** | Prometheus + Grafana | Metrics and alerting |

---

## 🚀 Quick Start

### Prerequisites

- Go 1.25+
- Docker 24+
- PostgreSQL 15+
- Redis 7+

### Installation

```bash
# Clone repository
git clone git@github.com:atop0914/agentbot.git
cd agentbot

# Install dependencies
go mod download

# Copy config template
cp config.example.json config.json

# Edit configuration
vim config.json

# Run database migrations
go run cmd/migrate/main.go

# Start server
go run cmd/agentbot/main.go
```

### Docker Compose

```bash
# Start all services
docker-compose up -d

# Check status
docker-compose ps

# View logs
docker-compose logs -f agentbot
```

### Configuration

```json
{
  "server": {
    "host": "0.0.0.0",
    "port": 8080
  },
  "database": {
    "host": "localhost",
    "port": 5432,
    "user": "agentbot",
    "password": "your_password",
    "db_name": "agentbot"
  },
  "redis": {
    "host": "localhost",
    "port": 6379
  },
  "agent": {
    "default_model": "gpt-4",
    "max_concurrent": 10,
    "task_timeout": "30m"
  }
}
```

---

## 📚 Documentation

### Core Concepts

#### Agent
An autonomous AI worker that runs in an isolated cloud environment. Each agent has:
- Unique ID and configuration
- Own container with resource limits
- Memory (short-term and long-term)
- Tools and capabilities

#### Task
A goal to be accomplished. Tasks are:
- Decomposed into subtasks by AI
- Executed with dependency tracking
- Monitored for progress
- Retried on failure

#### Template
A reusable workflow that can be:
- Recorded from live execution
- Shared across teams
- Parameterized for flexibility
- Version controlled

### API Reference

#### Authentication

```bash
# Register
curl -X POST http://localhost:8080/api/v1/auth/register \
  -H "Content-Type: application/json" \
  -d '{"email":"user@example.com","username":"user","password":"pass"}'

# Login
curl -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email":"user@example.com","password":"pass"}'
```

#### Agents

```bash
# List agents
curl http://localhost:8080/api/v1/agents \
  -H "Authorization: Bearer YOUR_TOKEN"

# Create agent
curl -X POST http://localhost:8080/api/v1/agents \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"my-agent","config":{"model":"gpt-4"}}'

# Get agent status
curl http://localhost:8080/api/v1/agents/{id} \
  -H "Authorization: Bearer YOUR_TOKEN"

# Send message to agent
curl -X POST http://localhost:8080/api/v1/agents/{id}/messages \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"content":"Search for recent AI papers"}'
```

#### Tasks

```bash
# Create task
curl -X POST http://localhost:8080/api/v1/tasks \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent_id":"agent-123","goal":"Research competitor products"}'

# Get task progress
curl http://localhost:8080/api/v1/tasks/{id} \
  -H "Authorization: Bearer YOUR_TOKEN"

# Cancel task
curl -X DELETE http://localhost:8080/api/v1/tasks/{id} \
  -H "Authorization: Bearer YOUR_TOKEN"
```

---

## 🔧 Development

### Project Structure

```
agentbot/
├── cmd/
│   └── agentbot/
│       └── main.go          # Application entry point
├── internal/
│   ├── agent/               # Agent core logic
│   ├── task/                # Task management
│   ├── browser/             # Browser automation
│   ├── cloud/               # Cloud environment
│   ├── communication/       # Multi-agent messaging
│   ├── template/            # Workflow templates
│   ├── monitor/             # Monitoring and metrics
│   └── audit/               # Audit logging
├── pkg/
│   ├── config/              # Configuration
│   ├── logger/              # Logging
│   ├── errors/              # Error types
│   └── middleware/           # HTTP middleware
├── api/
│   ├── http/                # REST handlers
│   ├── websocket/           # WebSocket handlers
│   └── grpc/                # gRPC handlers (future)
├── web/                     # Frontend (React)
├── deployments/             # Docker and K8s configs
├── docs/                    # Documentation
├── scripts/                 # Utility scripts
└── tests/                   # Integration tests
```

### Running Tests

```bash
# Unit tests
go test ./...

# Integration tests
go test -tags=integration ./...

# With coverage
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

### Code Quality

```bash
# Linting
golangci-lint run

# Formatting
gofmt -w .

# Vet
go vet ./...
```

---

## 📈 Roadmap

### Phase 1: Foundation (Week 1) ✅
- [x] Project structure
- [ ] Authentication (JWT + OAuth)
- [ ] Agent core model
- [ ] Docker environment manager

### Phase 2: Task System (Week 2)
- [ ] Task decomposition engine
- [ ] Browser automation
- [ ] Terminal execution
- [ ] File operations

### Phase 3: Collaboration (Week 3)
- [ ] Multi-agent messaging
- [ ] Workflow recording
- [ ] Template marketplace
- [ ] Health monitoring

### Phase 4: Enterprise (Week 4)
- [ ] Admin dashboard
- [ ] RBAC permissions
- [ ] Audit logging
- [ ] Network routing

### Future
- [ ] Kubernetes support
- [ ] gRPC API
- [ ] Plugin system
- [ ] Mobile app

---

## 🤝 Contributing

We welcome contributions! Please see our [Contributing Guide](CONTRIBUTING.md) for details.

### Development Setup

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'feat: add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

### Commit Convention

We follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add new feature
fix: fix bug
docs: update documentation
style: formatting
refactor: code refactoring
test: add tests
chore: maintenance
```

---

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

---

## 🙏 Acknowledgments

- Inspired by [xAI Grok Bot](https://x.ai/news/grok-bot-for-enterprise)
- Built with [Go](https://go.dev/), [Gin](https://github.com/gin-gonic/gin), and [Playwright](https://playwright.dev/)
- Thanks to all [contributors](../../graphs/contributors)

---

## 📞 Contact

- **GitHub**: [@atop0914](https://github.com/atop0914)
- **Telegram**: [@atop0914](https://t.me/atop0914)
- **Email**: 1390885469@qq.com

---

<div align="center">

**⭐ Star this repo if you find it useful!**

</div>
