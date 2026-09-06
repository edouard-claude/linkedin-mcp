// Package clock provides the wall-clock implementation of domain.Clock.
package clock

import (
	"time"

	"github.com/edouard-claude/linkedin-mcp/internal/domain"
)

// System reads the machine clock. Tests substitute their own domain.Clock.
type System struct{}

var _ domain.Clock = System{}

// Now returns the current UTC time.
func (System) Now() time.Time { return time.Now().UTC() }
