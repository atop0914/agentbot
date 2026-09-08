package cloud

import (
	"context"
	"time"
)

// Environment represents a cloud environment for an agent
type Environment struct {
	ID          string          `json:"id"`
	AgentID     string          `json:"agent_id"`
	Type        EnvironmentType `json:"type"`
	State       EnvironmentState `json:"state"`
	ContainerID string          `json:"container_id,omitempty"`
	Resources   Resources       `json:"resources"`
	Network     NetworkConfig   `json:"network"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	ExpiresAt   time.Time       `json:"expires_at,omitempty"`
}

// EnvironmentType represents the type of environment
type EnvironmentType string

const (
	EnvTypeContainer EnvironmentType = "container"
	EnvTypeVM        EnvironmentType = "vm"
	EnvTypeSandbox   EnvironmentType = "sandbox"
)

// EnvironmentState represents the state of environment
type EnvironmentState string

const (
	EnvStateCreating  EnvironmentState = "creating"
	EnvStateRunning   EnvironmentState = "running"
	EnvStatePaused    EnvironmentState = "paused"
	EnvStateStopping  EnvironmentState = "stopping"
	EnvStateStopped   EnvironmentState = "stopped"
	EnvStateError     EnvironmentState = "error"
)

// Resources defines resource limits
type Resources struct {
	CPU      string `json:"cpu"`      // e.g., "1" or "500m"
	Memory   string `json:"memory"`   // e.g., "512Mi"
	Disk     string `json:"disk"`     // e.g., "1Gi"
	GPU      string `json:"gpu,omitempty"`
}

// NetworkConfig defines network configuration
type NetworkConfig struct {
	Ports      []PortMapping `json:"ports,omitempty"`
	Proxy      string        `json:"proxy,omitempty"`
	DNS        []string      `json:"dns,omitempty"`
	Outbound   bool          `json:"outbound"`
	Inbound    bool          `json:"inbound"`
}

// PortMapping defines a port mapping
type PortMapping struct {
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
	Protocol      string `json:"protocol"` // tcp, udp
}

// Manager manages cloud environments
type Manager interface {
	// Create creates a new environment
	Create(ctx context.Context, agentID string, config EnvironmentConfig) (*Environment, error)
	// Destroy destroys an environment
	Destroy(ctx context.Context, envID string) error
	// Get returns environment info
	Get(ctx context.Context, envID string) (*Environment, error)
	// List lists environments for an agent
	List(ctx context.Context, agentID string) ([]*Environment, error)
	
	// Start starts a stopped environment
	Start(ctx context.Context, envID string) error
	// Stop stops a running environment
	Stop(ctx context.Context, envID string) error
	// Pause pauses a running environment
	Pause(ctx context.Context, envID string) error
	// Resume resumes a paused environment
	Resume(ctx context.Context, envID string) error
	
	// ExecuteCommand executes a command in the environment
	ExecuteCommand(ctx context.Context, envID string, command string) (*ExecResult, error)
	// UploadFile uploads a file to the environment
	UploadFile(ctx context.Context, envID string, path string, content []byte) error
	// DownloadFile downloads a file from the environment
	DownloadFile(ctx context.Context, envID string, path string) ([]byte, error)
	// ListFiles lists files in a directory
	ListFiles(ctx context.Context, envID string, path string) ([]FileInfo, error)
	
	// GetMetrics returns resource usage metrics
	GetMetrics(ctx context.Context, envID string) (*Metrics, error)
}

// EnvironmentConfig defines environment configuration
type EnvironmentConfig struct {
	Type      EnvironmentType `json:"type"`
	Image     string          `json:"image"`
	Resources Resources       `json:"resources"`
	Network   NetworkConfig   `json:"network"`
	Env       map[string]string `json:"env,omitempty"`
	Volumes   []Volume        `json:"volumes,omitempty"`
}

// Volume defines a volume mount
type Volume struct {
	HostPath      string `json:"host_path"`
	ContainerPath string `json:"container_path"`
	ReadOnly      bool   `json:"read_only"`
}

// ExecResult represents command execution result
type ExecResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Duration time.Duration `json:"duration"`
}

// FileInfo represents file information
type FileInfo struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	IsDir   bool      `json:"is_dir"`
	ModTime time.Time `json:"mod_time"`
	Mode    string    `json:"mode"`
}

// Metrics represents resource usage metrics
type Metrics struct {
	CPUUsage    float64 `json:"cpu_usage"`    // percentage
	MemoryUsage float64 `json:"memory_usage"` // percentage
	DiskUsage   float64 `json:"disk_usage"`   // percentage
	NetworkIn   int64   `json:"network_in"`   // bytes
	NetworkOut  int64   `json:"network_out"`  // bytes
	Timestamp   time.Time `json:"timestamp"`
}

// Router handles network routing for environments
type Router interface {
	// CreateTunnel creates a tunnel from environment to local
	CreateTunnel(ctx context.Context, envID string, localPort int) (remotePort int, err error)
	// DestroyTunnel destroys a tunnel
	DestroyTunnel(ctx context.Context, tunnelID string) error
	// GetTunnelStatus returns tunnel status
	GetTunnelStatus(ctx context.Context, tunnelID string) (string, error)
}
