package adapter

import (
	"context"
	"fmt"
	"time"
)

// CalendarAdapter implements the Adapter interface for calendar services (Google Calendar, CalDAV).
type CalendarAdapter struct {
	status   AdapterStatus
	config   *Config
	settings map[string]string
}

// NewCalendarAdapter creates a new calendar adapter instance.
func NewCalendarAdapter() Adapter {
	return &CalendarAdapter{
		status: StatusDisconnected,
	}
}

func (c *CalendarAdapter) Name() string { return "Calendar Adapter" }

func (c *CalendarAdapter) Type() AdapterType { return AdapterTypeCalendar }

func (c *CalendarAdapter) Connect(ctx context.Context, cfg *Config) error {
	c.config = cfg
	c.settings = cfg.Settings

	// Validate required settings
	if c.settings["provider"] == "" {
		return fmt.Errorf("calendar provider is required (google, caldav)")
	}

	// In production, this would establish OAuth2 or CalDAV connection
	c.status = StatusConnected
	return nil
}

func (c *CalendarAdapter) Disconnect(ctx context.Context) error {
	c.status = StatusDisconnected
	c.config = nil
	c.settings = nil
	return nil
}

func (c *CalendarAdapter) Status() AdapterStatus { return c.status }

func (c *CalendarAdapter) ListActions() []Action {
	return []Action{
		{
			Name:        "list_events",
			Description: "List upcoming calendar events",
			Parameters: []ActionParam{
				{Name: "calendar_id", Type: "string", Required: false, Default: "primary"},
				{Name: "time_min", Type: "string", Required: false, Description: "RFC3339 start time"},
				{Name: "time_max", Type: "string", Required: false, Description: "RFC3339 end time"},
				{Name: "limit", Type: "number", Required: false, Default: "20"},
			},
		},
		{
			Name:        "create_event",
			Description: "Create a new calendar event",
			Parameters: []ActionParam{
				{Name: "summary", Type: "string", Required: true, Description: "Event title"},
				{Name: "start", Type: "string", Required: true, Description: "RFC3339 start time"},
				{Name: "end", Type: "string", Required: true, Description: "RFC3339 end time"},
				{Name: "description", Type: "string", Required: false},
				{Name: "location", Type: "string", Required: false},
				{Name: "attendees", Type: "string", Required: false, Description: "Comma-separated emails"},
			},
		},
		{
			Name:        "update_event",
			Description: "Update an existing calendar event",
			Parameters: []ActionParam{
				{Name: "event_id", Type: "string", Required: true},
				{Name: "summary", Type: "string", Required: false},
				{Name: "start", Type: "string", Required: false},
				{Name: "end", Type: "string", Required: false},
			},
		},
		{
			Name:        "delete_event",
			Description: "Delete a calendar event",
			Parameters: []ActionParam{
				{Name: "event_id", Type: "string", Required: true},
			},
		},
		{
			Name:        "get_freebusy",
			Description: "Check free/busy status for a time range",
			Parameters: []ActionParam{
				{Name: "time_min", Type: "string", Required: true},
				{Name: "time_max", Type: "string", Required: true},
			},
		},
	}
}

func (c *CalendarAdapter) Execute(ctx context.Context, req *ActionRequest) (*ActionResult, error) {
	start := time.Now()

	if c.status != StatusConnected {
		return &ActionResult{
			Success:  false,
			Error:    "adapter not connected",
			Duration: time.Since(start),
		}, nil
	}

	switch req.Action {
	case "list_events":
		return c.listEvents(req.Parameters, start)
	case "create_event":
		return c.createEvent(req.Parameters, start)
	case "update_event":
		return c.updateEvent(req.Parameters, start)
	case "delete_event":
		return c.deleteEvent(req.Parameters, start)
	case "get_freebusy":
		return c.getFreeBusy(req.Parameters, start)
	default:
		return &ActionResult{
			Success:  false,
			Error:    fmt.Sprintf("unknown action: %s", req.Action),
			Duration: time.Since(start),
		}, nil
	}
}

func (c *CalendarAdapter) listEvents(params map[string]string, start time.Time) (*ActionResult, error) {
	// Mock implementation
	return &ActionResult{
		Success: true,
		Data: map[string]string{
			"count":   "0",
			"message": "calendar event listing not yet implemented",
		},
		Duration: time.Since(start),
	}, nil
}

func (c *CalendarAdapter) createEvent(params map[string]string, start time.Time) (*ActionResult, error) {
	summary := params["summary"]
	if summary == "" {
		return &ActionResult{
			Success:  false,
			Error:    "summary is required",
			Duration: time.Since(start),
		}, nil
	}

	return &ActionResult{
		Success: true,
		Data: map[string]string{
			"event_id": fmt.Sprintf("evt-%d", time.Now().UnixNano()),
			"summary":  summary,
			"status":   "created",
		},
		Duration: time.Since(start),
	}, nil
}

func (c *CalendarAdapter) updateEvent(params map[string]string, start time.Time) (*ActionResult, error) {
	eventID := params["event_id"]
	if eventID == "" {
		return &ActionResult{
			Success:  false,
			Error:    "event_id is required",
			Duration: time.Since(start),
		}, nil
	}

	return &ActionResult{
		Success: true,
		Data: map[string]string{
			"event_id": eventID,
			"status":   "updated",
		},
		Duration: time.Since(start),
	}, nil
}

func (c *CalendarAdapter) deleteEvent(params map[string]string, start time.Time) (*ActionResult, error) {
	eventID := params["event_id"]
	if eventID == "" {
		return &ActionResult{
			Success:  false,
			Error:    "event_id is required",
			Duration: time.Since(start),
		}, nil
	}

	return &ActionResult{
		Success: true,
		Data: map[string]string{
			"event_id": eventID,
			"status":   "deleted",
		},
		Duration: time.Since(start),
	}, nil
}

func (c *CalendarAdapter) getFreeBusy(params map[string]string, start time.Time) (*ActionResult, error) {
	if params["time_min"] == "" || params["time_max"] == "" {
		return &ActionResult{
			Success:  false,
			Error:    "time_min and time_max are required",
			Duration: time.Since(start),
		}, nil
	}

	return &ActionResult{
		Success: true,
		Data: map[string]string{
			"busy_count": "0",
			"message":    "freebusy query not yet implemented",
		},
		Duration: time.Since(start),
	}, nil
}
