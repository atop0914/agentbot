package adapter

import (
	"context"
	"fmt"
	"time"
)

// EmailAdapter implements the Adapter interface for email services (SMTP/IMAP).
type EmailAdapter struct {
	status   AdapterStatus
	config   *Config
	settings map[string]string
}

// NewEmailAdapter creates a new email adapter instance.
func NewEmailAdapter() Adapter {
	return &EmailAdapter{
		status: StatusDisconnected,
	}
}

func (e *EmailAdapter) Name() string { return "Email Adapter" }

func (e *EmailAdapter) Type() AdapterType { return AdapterTypeEmail }

func (e *EmailAdapter) Connect(ctx context.Context, cfg *Config) error {
	e.config = cfg
	e.settings = cfg.Settings

	// Validate required settings
	if e.settings["smtp_host"] == "" {
		return fmt.Errorf("smtp_host is required")
	}

	// In production, this would establish an actual SMTP/IMAP connection
	e.status = StatusConnected
	return nil
}

func (e *EmailAdapter) Disconnect(ctx context.Context) error {
	e.status = StatusDisconnected
	e.config = nil
	e.settings = nil
	return nil
}

func (e *EmailAdapter) Status() AdapterStatus { return e.status }

func (e *EmailAdapter) ListActions() []Action {
	return []Action{
		{
			Name:        "send",
			Description: "Send an email message",
			Parameters: []ActionParam{
				{Name: "to", Type: "string", Required: true, Description: "Recipient email address"},
				{Name: "subject", Type: "string", Required: true, Description: "Email subject"},
				{Name: "body", Type: "string", Required: true, Description: "Email body (plain text or HTML)"},
				{Name: "cc", Type: "string", Required: false, Description: "CC recipients (comma-separated)"},
				{Name: "bcc", Type: "string", Required: false, Description: "BCC recipients (comma-separated)"},
			},
		},
		{
			Name:        "list",
			Description: "List recent emails from inbox",
			Parameters: []ActionParam{
				{Name: "folder", Type: "string", Required: false, Default: "INBOX", Description: "Mail folder"},
				{Name: "limit", Type: "number", Required: false, Default: "20", Description: "Max emails to return"},
			},
		},
		{
			Name:        "read",
			Description: "Read a specific email by ID",
			Parameters: []ActionParam{
				{Name: "id", Type: "string", Required: true, Description: "Email message ID"},
			},
		},
		{
			Name:        "search",
			Description: "Search emails by query",
			Parameters: []ActionParam{
				{Name: "query", Type: "string", Required: true, Description: "Search query"},
				{Name: "folder", Type: "string", Required: false, Default: "INBOX"},
			},
		},
	}
}

func (e *EmailAdapter) Execute(ctx context.Context, req *ActionRequest) (*ActionResult, error) {
	start := time.Now()

	if e.status != StatusConnected {
		return &ActionResult{
			Success:  false,
			Error:    "adapter not connected",
			Duration: time.Since(start),
		}, nil
	}

	switch req.Action {
	case "send":
		return e.sendEmail(req.Parameters, start)
	case "list":
		return e.listEmails(req.Parameters, start)
	case "read":
		return e.readEmail(req.Parameters, start)
	case "search":
		return e.searchEmails(req.Parameters, start)
	default:
		return &ActionResult{
			Success:  false,
			Error:    fmt.Sprintf("unknown action: %s", req.Action),
			Duration: time.Since(start),
		}, nil
	}
}

func (e *EmailAdapter) sendEmail(params map[string]string, start time.Time) (*ActionResult, error) {
	to := params["to"]
	subject := params["subject"]
	if to == "" || subject == "" {
		return &ActionResult{
			Success:  false,
			Error:    "to and subject are required",
			Duration: time.Since(start),
		}, nil
	}

	// In production, this would use net/smtp to send the email
	return &ActionResult{
		Success: true,
		Data: map[string]string{
			"message_id": fmt.Sprintf("msg-%d", time.Now().UnixNano()),
			"to":         to,
			"subject":    subject,
			"status":     "sent",
		},
		Duration: time.Since(start),
	}, nil
}

func (e *EmailAdapter) listEmails(params map[string]string, start time.Time) (*ActionResult, error) {
	// Mock implementation
	return &ActionResult{
		Success: true,
		Data: map[string]string{
			"count":   "0",
			"folder":  params["folder"],
			"message": "email listing not yet implemented",
		},
		Duration: time.Since(start),
	}, nil
}

func (e *EmailAdapter) readEmail(params map[string]string, start time.Time) (*ActionResult, error) {
	id := params["id"]
	if id == "" {
		return &ActionResult{
			Success:  false,
			Error:    "id is required",
			Duration: time.Since(start),
		}, nil
	}

	return &ActionResult{
		Success: true,
		Data: map[string]string{
			"id":      id,
			"message": "email reading not yet implemented",
		},
		Duration: time.Since(start),
	}, nil
}

func (e *EmailAdapter) searchEmails(params map[string]string, start time.Time) (*ActionResult, error) {
	query := params["query"]
	if query == "" {
		return &ActionResult{
			Success:  false,
			Error:    "query is required",
			Duration: time.Since(start),
		}, nil
	}

	return &ActionResult{
		Success: true,
		Data: map[string]string{
			"query":   query,
			"count":   "0",
			"message": "email search not yet implemented",
		},
		Duration: time.Since(start),
	}, nil
}
