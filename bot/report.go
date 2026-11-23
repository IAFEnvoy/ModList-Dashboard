package bot

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

const reportSummaryLimit = 200
const reportSummaryFallback = "预处理报告已生成，暂时无法生成摘要，请在网站查看完整报告。"

// ReportSummarizer is supplied by the application so the bot module remains
// independent of AI providers. It runs once before queued report delivery.
type ReportSummarizer func(context.Context, string) (string, error)
type ReportShareCreator func(sessionID, report string) (string, error)
type Option func(*Service)

func WithReportSummarizer(summarize ReportSummarizer) Option {
	return func(s *Service) { s.summarizeReport = summarize }
}

func WithReportShareCreator(create ReportShareCreator) Option {
	return func(s *Service) { s.createReportShare = create }
}

var (
	summaryThinking = regexp.MustCompile(`(?is)<think(?:ing)?>.*?</think(?:ing)?>`)
	summaryCode     = regexp.MustCompile("(?s)```.*?```|~~~.*?~~~")
	summaryLink     = regexp.MustCompile(`!?\[([^\]\n]*)\]\([^\)\n]*\)`)
	summaryPrefix   = regexp.MustCompile(`(?m)^\s{0,3}(?:#{1,6}\s*|>\s*|[-*+]\s+|[0-9]+[.)、]\s*)`)
	summaryURL      = regexp.MustCompile(`https?://[^\s<>]+`)
	summaryPath     = regexp.MustCompile(`(?:[A-Za-z]:[\\/]|(?:[A-Za-z0-9_.-]+[\\/])+)[A-Za-z0-9_./\\:#-]+|[A-Za-z0-9_.-]+\.(?:java|kt|go|js|ts|tsx|jsx|py|cpp|hpp|cs|rs|json|yml|yaml|md)(?::[0-9]+)*`)
	summaryHTML     = regexp.MustCompile(`<[^>]+>`)
	legacyPart      = regexp.MustCompile(`^\[[0-9]+/[0-9]+\] `)
)

func compactReportText(text string, limit int) string {
	text = summaryThinking.ReplaceAllString(text, "")
	text = summaryCode.ReplaceAllString(text, "")
	text = summaryLink.ReplaceAllString(text, "$1")
	text = summaryURL.ReplaceAllString(text, "")
	text = summaryPath.ReplaceAllString(text, "")
	text = summaryHTML.ReplaceAllString(text, "")
	text = summaryPrefix.ReplaceAllString(text, "")
	text = strings.NewReplacer("`", "", "*", "", "_", "", "~", "", "#", "", "|", " ", "---", " ").Replace(text)
	return limitReportText(strings.Join(strings.Fields(text), " "), limit)
}

func limitReportText(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return string(runes)
	}
	if limit <= 1 {
		return "…"
	}
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}

// Only the summary body has a 200-character limit. Keep the header and link
// separate; this compact report is queued as one message, without splitting.
func (s *Service) reportMessage(report Report, summary, shareID string) string {
	project := compactReportText(report.Project, 28)
	if project == "" {
		project = limitReportText(report.Repo, 28)
	}
	header := "Issue 预处理摘要"
	if project != "" {
		header += " · " + project
	}
	if report.Number > 0 {
		header += fmt.Sprintf(" #%d", report.Number)
	}
	suffix := "\n完整报告请在网站查看。"
	if s.cfg.DashboardURL != "" && shareID != "" {
		suffix = "\n完整报告：" + strings.TrimRight(s.cfg.DashboardURL, "/") + "/share/" + shareID
	}
	body := compactReportText(summary, reportSummaryLimit)
	if body == "" {
		body = reportSummaryFallback
	}
	return header + "\n" + body + suffix
}

// Old queues contained full reports split into many parts. Reconstruct one
// draft on startup and summarize it before sending any remaining parts.
func legacyReport(item delivery) *Report {
	var content strings.Builder
	for _, part := range item.Parts {
		content.WriteString(legacyPart.ReplaceAllString(part, ""))
	}
	text := content.String()
	report := &Report{SessionID: strings.TrimPrefix(item.ID, "report:"), Content: text}
	if strings.HasPrefix(text, "Issue 预处理报告 · ") {
		header, body, ok := strings.Cut(text, "\n\n")
		lines := strings.Split(header, "\n")
		if ok && len(lines) >= 2 {
			report.Project = strings.TrimPrefix(lines[0], "Issue 预处理报告 · ")
			fmt.Sscanf(lines[1], "%s #%d", &report.Repo, &report.Number)
			report.Content, _, _ = strings.Cut(body, "\n\n报告会话历史（需登录）:")
		}
	}
	return report
}
