package main

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"strings"

	"modlist-dashboard/agent"
	"modlist-dashboard/badge"
	"modlist-dashboard/bot"
	"modlist-dashboard/bot/qqbot"

	"gopkg.in/yaml.v3"
)

var botService *bot.Service
var qqBotReceiver *qqbot.Receiver

type botSettings struct {
	bot.Config `yaml:",inline"`
	Providers  struct {
		QQ qqbot.Config `yaml:"qq"`
	} `yaml:"providers"`
}

func loadBotSettings(path string) (botSettings, error) {
	settings := botSettings{Config: bot.Config{NotifyNewIssues: true, NotifyReports: true}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return settings, nil
	}
	if err != nil {
		return settings, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&settings); err != nil {
		return settings, fmt.Errorf("无法读取 Bot 配置: %w", err)
	}
	settings.Providers.QQ.AppID = strings.TrimSpace(os.ExpandEnv(settings.Providers.QQ.AppID))
	settings.Providers.QQ.AppSecret = strings.TrimSpace(os.ExpandEnv(settings.Providers.QQ.AppSecret))
	return settings, nil
}

// This is the composition point for chat platforms. Adding another adapter only
// requires registering its Sender here; issue/agent code remains platform free.
func newBotService(configPath, statePath string) (*bot.Service, error) {
	qqBotReceiver = nil
	settings, err := loadBotSettings(configPath)
	if err != nil || !settings.Enabled {
		return nil, err
	}
	senders := make(map[string]bot.Sender)
	var qqSender *qqbot.Sender
	if settings.Providers.QQ.Enabled {
		sender, err := qqbot.New(settings.Providers.QQ)
		if err != nil {
			return nil, err
		}
		senders["qq"] = sender
		qqSender = sender
	}
	service, err := bot.New(settings.Config, statePath, senders)
	if err != nil {
		return nil, err
	}
	if qqSender != nil {
		receiver, err := qqbot.NewReceiver(qqSender, service.Bind)
		if err != nil {
			return nil, err
		}
		qqBotReceiver = receiver
	}
	return service, nil
}

func botIssues(items []GitHubItem) []bot.Issue {
	issues := make([]bot.Issue, 0, len(items))
	for _, item := range items {
		issues = append(issues, bot.Issue{Number: item.Number, Title: item.Title, State: item.State, URL: item.URL})
	}
	return issues
}

func seedBotIssues() {
	if !botService.WantsIssues() {
		return
	}
	for _, meta := range sortedModMetas() {
		if meta.IDs.GH == "" || isStatusArchived(meta.Status) {
			continue
		}
		if cached, found, _ := readCacheStale(badge.CacheKeyPrefix + meta.IDs.GH); found {
			if info, ok := cached.(*badge.GitHubInfo); ok && info != nil {
				if err := botService.SeedIssues(meta.IDs.GH, botIssues(info.Issues)); err != nil {
					log.Printf("[Bot] 无法保存 Issue 基线 %s: %v", meta.IDs.GH, err)
				}
			}
		}
	}
}

func notifyBotIssues(meta ModMeta, items []GitHubItem) {
	if !botService.WantsIssues() || isStatusArchived(meta.Status) {
		return
	}
	if err := botService.ObserveIssues(meta.IDs.GH, meta.Name, botIssues(items)); err != nil {
		log.Printf("[Bot] 无法保存新增 Issue 通知 %s: %v", meta.IDs.GH, err)
	}
}

func notifyBotReport(meta ModMeta, number int, sessionID string) {
	if botService == nil {
		return
	}
	content := agent.IssuePreprocessReportText(sessionID)
	if err := botService.PublishReport(bot.Report{
		Repo: meta.IDs.GH, Project: meta.Name, Number: number,
		SessionID: sessionID, Content: content,
	}); err != nil {
		log.Printf("[Bot] 无法保存预处理报告通知 %s#%d: %v", meta.IDs.GH, number, err)
	}
}
