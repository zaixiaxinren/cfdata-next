package main

// 面板扩展的 WebSocket 处理器：多来源、CFNext 推送、定时任务。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

func panelWSHandlers(session *appSession) map[string]func(json.RawMessage) {
	return map[string]func(json.RawMessage){
		"panel_get": func(data json.RawMessage) {
			session.sendWSMessage("panel_data", panelStatePayload())
		},
		"panel_save": func(data json.RawMessage) {
			var params struct {
				CFNext   *cfnextConfig   `json:"cfnext"`
				Schedule *scheduleInput  `json:"schedule"`
				Runner   *runnerConfig   `json:"runner"`
			}
			if err := json.Unmarshal(data, &params); err != nil {
				session.sendWSMessage("error", "panel_save 参数解析失败: "+err.Error())
				return
			}
			cfg, err := updatePanelConfig(func(c *panelConfig) error {
				if params.CFNext != nil {
					next := *params.CFNext
					// 密码留空表示「不修改」，避免前端回显密码。
					if strings.TrimSpace(next.Password) == "" {
						next.Password = c.CFNext.Password
					}
					next.LastPushAt = c.CFNext.LastPushAt
					next.LastPushMsg = c.CFNext.LastPushMsg
					c.CFNext = next
				}
				if params.Runner != nil {
					c.Runner = *params.Runner
				}
				if params.Schedule != nil {
					in := *params.Schedule
					c.Schedule.Enabled = in.Enabled
					c.Schedule.Mode = in.Mode
					c.Schedule.IntervalMinutes = in.IntervalMinutes
					c.Schedule.DailyTime = in.DailyTime
				}
				return nil
			})
			if err != nil {
				session.sendWSMessage("error", "保存设置失败: "+err.Error())
				return
			}
			session.sendWSMessage("panel_saved", map[string]interface{}{"message": "设置已保存"})
			session.sendWSMessage("panel_data", panelStatePayloadFrom(cfg))
		},
		"sources_save": func(data json.RawMessage) {
			var params struct {
				Sources []ipSource `json:"sources"`
			}
			if err := json.Unmarshal(data, &params); err != nil {
				session.sendWSMessage("error", "sources_save 参数解析失败: "+err.Error())
				return
			}
			cfg, err := updatePanelConfig(func(c *panelConfig) error {
				c.Sources = params.Sources
				return nil
			})
			if err != nil {
				session.sendWSMessage("error", "保存来源失败: "+err.Error())
				return
			}
			session.sendWSMessage("panel_saved", map[string]interface{}{"message": fmt.Sprintf("来源已保存，共 %d 个", len(cfg.Sources))})
			session.sendWSMessage("panel_data", panelStatePayloadFrom(cfg))
		},
		"cfnext_test": func(data json.RawMessage) {
			cfg := loadPanelConfig()
			mergeCFNextInput(&cfg, data)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				result := testCFNextConnection(ctx, cfg.CFNext)
				session.sendWSMessage("cfnext_test_result", result)
			}()
		},
		"cfnext_push_now": func(data json.RawMessage) {
			cfg := loadPanelConfig()
			mergeCFNextInput(&cfg, data)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()
				result := pushLastResults(ctx, cfg)
				session.sendWSMessage("cfnext_push_result", result)
				session.sendWSMessage("panel_data", panelStatePayload())
			}()
		},
		"run_multisource": func(data json.RawMessage) {
			cfg := loadPanelConfig()
			if countEnabledSources(cfg.Sources) == 0 {
				session.sendWSMessage("error", "没有启用的 IP 来源，请先在设置里添加")
				return
			}
			session.startTaskNamed("多来源测速", "nsb", map[string]interface{}{"sources": countEnabledSources(cfg.Sources)}, func(ctx context.Context, s *appSession) {
				s.sendWSMessage("log", fmt.Sprintf("开始多来源测速，共 %d 个来源", countEnabledSources(cfg.Sources)))
				_, summary := runMultiSourcePipeline(ctx, cfg, s)
				if len(summary.Failed) > 0 {
					s.sendWSMessage("log", "失败来源: "+strings.Join(summary.Failed, "、"))
				}
				if cfg.CFNext.AutoPush && summary.Merged > 0 {
					s.sendWSMessage("log", "自动推送结果到 CFNext 面板…")
					result := pushLastResults(ctx, loadPanelConfig())
					if result.OK {
						s.sendWSMessage("log", result.Message)
					} else {
						s.sendWSMessage("error", "推送失败: "+result.Message)
					}
					s.sendWSMessage("panel_data", panelStatePayload())
				}
			})
		},
	}
}

// mergeCFNextInput 允许前端把未保存的表单值一起带上来做即时测试/推送。
func mergeCFNextInput(cfg *panelConfig, data json.RawMessage) {
	if len(data) == 0 {
		return
	}
	var params struct {
		CFNext *cfnextConfig `json:"cfnext"`
	}
	if err := json.Unmarshal(data, &params); err != nil || params.CFNext == nil {
		return
	}
	next := *params.CFNext
	if strings.TrimSpace(next.Password) == "" {
		next.Password = cfg.CFNext.Password
	}
	if next.MaxCount <= 0 {
		next.MaxCount = cfg.CFNext.MaxCount
	}
	if strings.TrimSpace(next.PanelURL) == "" {
		next.PanelURL = cfg.CFNext.PanelURL
	}
	cfg.CFNext = next
}

type scheduleInput struct {
	Enabled         bool   `json:"enabled"`
	Mode            string `json:"mode"`
	IntervalMinutes int    `json:"intervalMinutes"`
	DailyTime       string `json:"dailyTime"`
	LastRun         string `json:"lastRun"`
}

func panelStatePayload() map[string]interface{} {
	return panelStatePayloadFrom(loadPanelConfig())
}

func panelStatePayloadFrom(cfg panelConfig) map[string]interface{} {
	next, hasNext := nextPanelScheduleTime(cfg.Schedule, time.Now())
	nextText := ""
	if hasNext {
		nextText = next.Format("2006-01-02 15:04")
	}

	cfnext := cfg.CFNext
	hasPassword := strings.TrimSpace(cfnext.Password) != ""
	cfnext.Password = ""

	at, results, entries, summary := lastMultiSourceSnapshot()
	lastRunText := ""
	if !at.IsZero() {
		lastRunText = at.Format("2006-01-02 15:04")
	}

	return map[string]interface{}{
		"sources":  cfg.Sources,
		"cfnext":   cfnext,
		"runner":   cfg.Runner,
		"schedule": cfg.Schedule,
		"state": map[string]interface{}{
			"hasPassword":      hasPassword,
			"nextRun":          nextText,
			"schedulerRunning": atomic.LoadInt32(&panelSchedulerRunning) != 0,
			"lastBatchAt":      lastRunText,
			"resultCount":      len(results),
			"pendingPush":      len(entries),
			"lastBatchSummary": summary,
			"dataDir":          panelDataDir(),
		},
	}
}
