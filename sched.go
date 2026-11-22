package main

// 容器内定时调度：不依赖 systemd，进程内 ticker 判断是否到期，到点跑多来源流程并可自动推送。

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

var panelSchedulerRunning int32

// nextPanelScheduleTime 计算下次执行时间，供界面展示。
func nextPanelScheduleTime(cfg scheduleConfig, now time.Time) (time.Time, bool) {
	if !cfg.Enabled {
		return time.Time{}, false
	}
	last, lastOK := parseRFC3339Safe(cfg.LastRun)
	switch cfg.Mode {
	case "daily":
		hour, minute, err := parseDailyTime(cfg.DailyTime)
		if err != nil {
			return time.Time{}, false
		}
		candidate := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, time.Local)
		if !candidate.After(now) {
			candidate = candidate.Add(24 * time.Hour)
		}
		// 如果今天的目标时间已过但还没跑过，下次就是「现在」。
		todayTarget := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, time.Local)
		if !now.Before(todayTarget) && (!lastOK || last.Before(todayTarget)) {
			return now, true
		}
		return candidate, true
	default:
		interval := time.Duration(cfg.IntervalMinutes) * time.Minute
		if interval <= 0 {
			return time.Time{}, false
		}
		if !lastOK {
			return now, true
		}
		return last.Add(interval), true
	}
}

func panelScheduleDue(cfg scheduleConfig, now time.Time) bool {
	next, ok := nextPanelScheduleTime(cfg, now)
	if !ok {
		return false
	}
	return !now.Before(next)
}

func startPanelScheduler() {
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			cfg := loadPanelConfig()
			if !cfg.Schedule.Enabled {
				continue
			}
			if anyTaskRunning() || atomic.LoadInt32(&panelSchedulerRunning) != 0 {
				continue
			}
			if !panelScheduleDue(cfg.Schedule, time.Now()) {
				continue
			}
			runScheduledPanelTask(cfg)
		}
	}()
}

func runScheduledPanelTask(cfg panelConfig) {
	if !atomic.CompareAndSwapInt32(&panelSchedulerRunning, 0, 1) {
		return
	}
	atomic.AddInt32(&globalRunningTasks, 1)
	defer func() {
		atomic.AddInt32(&globalRunningTasks, -1)
		atomic.StoreInt32(&panelSchedulerRunning, 0)
	}()

	fmt.Printf("[定时任务] 开始执行多来源测速，共 %d 个启用的来源\n", countEnabledSources(cfg.Sources))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
	defer cancel()

	logSession := &appSession{emit: func(msgType string, data interface{}) {
		if msgType == "log" || msgType == "error" {
			fmt.Printf("[定时任务] %v\n", data)
		}
	}}

	started := time.Now()
	_, summary := runMultiSourcePipeline(ctx, cfg, logSession)

	statusText := fmt.Sprintf("%s 完成：%d 个来源，合并 %d 条", started.Format("01-02 15:04"), summary.SourceCount, summary.Merged)
	if len(summary.Failed) > 0 {
		statusText += fmt.Sprintf("，%d 个来源失败", len(summary.Failed))
	}

	if cfg.CFNext.AutoPush {
		freshCfg := loadPanelConfig()
		result := pushLastResults(ctx, freshCfg)
		if result.OK {
			statusText += "，推送成功"
			fmt.Printf("[定时任务] %s\n", result.Message)
		} else {
			statusText += "，推送失败：" + result.Message
			fmt.Printf("[定时任务] 推送失败: %s\n", result.Message)
		}
	}

	_, _ = updatePanelConfig(func(c *panelConfig) error {
		c.Schedule.LastRun = started.Format(time.RFC3339)
		c.Schedule.LastResult = statusText
		return nil
	})
	fmt.Printf("[定时任务] %s\n", statusText)
}

func countEnabledSources(sources []ipSource) int {
	n := 0
	for _, s := range sources {
		if s.Enabled && s.URL != "" {
			n++
		}
	}
	return n
}
