// Package bot delivers dashboard notifications independently of chat platforms.
// Platform adapters implement Sender; they do not own issue detection or retries.
package bot

import "context"

type Target struct {
	Provider string `yaml:"provider" json:"provider"`
	Type     string `yaml:"type" json:"type"`
	ID       string `yaml:"id" json:"id"`
}

type Sender interface {
	Validate(Target) error
	Send(context.Context, Target, string) error
}

type Config struct {
	Enabled         bool     `yaml:"enabled"`
	NotifyNewIssues bool     `yaml:"notify_new_issues"`
	NotifyReports   bool     `yaml:"notify_reports"`
	DashboardURL    string   `yaml:"dashboard_url"`
	Targets         []Target `yaml:"targets"`
}

type Issue struct {
	Number int
	Title  string
	State  string
	URL    string
}

type Report struct {
	Repo      string
	Project   string
	Number    int
	SessionID string
	Content   string
}
