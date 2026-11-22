package main

// 多 IP 来源批量执行：逐个来源跑一遍非标测速，合并去重后导出，并可推送到 CFNext。

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// runNSBTask 结束时通过该 hook 把结构化结果回传给调用方（CSV 只保留字符串，不适合再解析）。
var nsbRunResultSink = struct {
	sync.Mutex
	fn func(results []iptestResult, status, message string)
}{}

func setNSBRunResultSink(fn func(results []iptestResult, status, message string)) {
	nsbRunResultSink.Lock()
	nsbRunResultSink.fn = fn
	nsbRunResultSink.Unlock()
}

// emitNSBRunResults 把结果交给注册的 sinks；返回 true 表示已被批量流程接管。
func emitNSBRunResults(results []iptestResult, status, message string) bool {
	nsbRunResultSink.Lock()
	fn := nsbRunResultSink.fn
	nsbRunResultSink.Unlock()
	if fn != nil {
		fn(results, status, message)
		return true
	}
	return false
}

// 最近一次多来源执行的结果，供「立即推送」复用。
var lastMultiSource = struct {
	sync.Mutex
	at      time.Time
	results []iptestResult
	entries []cfnextPreferredIP
	summary string
}{}

func setLastMultiSource(results []iptestResult, entries []cfnextPreferredIP, summary string) {
	lastMultiSource.Lock()
	lastMultiSource.at = time.Now()
	lastMultiSource.results = append([]iptestResult(nil), results...)
	lastMultiSource.entries = append([]cfnextPreferredIP(nil), entries...)
	lastMultiSource.summary = summary
	lastMultiSource.Unlock()
}

func lastMultiSourceSnapshot() (time.Time, []iptestResult, []cfnextPreferredIP, string) {
	lastMultiSource.Lock()
	defer lastMultiSource.Unlock()
	return lastMultiSource.at, append([]iptestResult(nil), lastMultiSource.results...), append([]cfnextPreferredIP(nil), lastMultiSource.entries...), lastMultiSource.summary
}

// headlessSession 让 runNSBTask 可以在没有 WebSocket 客户端时运行，
// 只把日志与进度转发给真正的会话（如果存在）。
func headlessSession(forward *appSession) *appSession {
	return &appSession{emit: func(msgType string, data interface{}) {
		if forward == nil {
			return
		}
		switch msgType {
		case "log", "error", "nsb_progress", "nsb_scan_sorted", "nsb_csv_ready":
			forward.sendWSMessage(msgType, data)
		}
	}}
}

func multiSourceOutputPath(cfg panelConfig) string {
	out := strings.TrimSpace(cfg.Runner.OutFile)
	if out == "" {
		out = "cfdata-merged.csv"
	}
	out = safeFilename(out)
	if filepath.IsAbs(out) {
		return out
	}
	return filepath.Join(panelDataDir(), out)
}

type multiSourceSummary struct {
	At           string   `json:"at"`
	SourceCount  int      `json:"sourceCount"`
	Sources      []string `json:"sources"`
	TotalResults int      `json:"totalResults"`
	Merged       int      `json:"merged"`
	Qualified    int      `json:"qualified"`
	OutFile      string   `json:"outFile"`
	Push         string   `json:"push"`
	Failed       []string `json:"failed"`
}

// fetchSourceContent 拉取来源内容：http(s) 走管理面代理，其它按本地文件读取。
func fetchSourceContent(ctx context.Context, source ipSource) (string, error) {
	target := strings.TrimSpace(source.URL)
	if target == "" {
		return "", fmt.Errorf("来源未填写地址")
	}
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		return getURLContentViaProxy(ctx, target)
	}
	data, err := osReadFileString(expandHome(target))
	if err != nil {
		return "", err
	}
	return data, nil
}

func osReadFileString(path string) (string, error) {
	data, err := getFileContent(path)
	if err != nil {
		return "", err
	}
	return data, nil
}

// mergeResults 合并多个来源的结果：按 ip|port 去重，保留速度更高的那条。
func mergeResults(all []iptestResult) []iptestResult {
	seen := make(map[string]int, len(all))
	merged := make([]iptestResult, 0, len(all))
	for _, res := range all {
		key := res.ipAddr + "|" + itoa(res.port)
		if idx, ok := seen[key]; ok {
			if res.downloadSpeed > merged[idx].downloadSpeed {
				merged[idx] = res
			}
			continue
		}
		seen[key] = len(merged)
		merged = append(merged, res)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].downloadSpeed != merged[j].downloadSpeed {
			return merged[i].downloadSpeed > merged[j].downloadSpeed
		}
		return merged[i].lossRate < merged[j].lossRate
	})
	return merged
}

// runMultiSourcePipeline 依次执行所有启用的来源，返回合并结果。
func runMultiSourcePipeline(ctx context.Context, cfg panelConfig, forward *appSession) ([]iptestResult, multiSourceSummary) {
	summary := multiSourceSummary{At: time.Now().Format("2006-01-02 15:04:05"), Failed: []string{}}
	notify := func(kind, msg string) {
		if forward != nil {
			forward.sendWSMessage("log", msg)
			forward.sendWSMessage("panel_status", map[string]interface{}{"kind": kind, "message": msg})
		}
	}

	enabled := make([]ipSource, 0, len(cfg.Sources))
	for _, source := range cfg.Sources {
		if source.Enabled && strings.TrimSpace(source.URL) != "" {
			enabled = append(enabled, source)
		}
	}
	if len(enabled) == 0 {
		notify("error", "没有启用的 IP 来源，请先添加来源")
		summary.Failed = append(summary.Failed, "无启用来源")
		return nil, summary
	}
	summary.SourceCount = len(enabled)

	all := make([]iptestResult, 0, 256)
	for idx, source := range enabled {
		if ctx.Err() != nil {
			break
		}
		name := strings.TrimSpace(source.Name)
		if name == "" {
			name = sourceDefaultName(source.URL, idx)
		}
		summary.Sources = append(summary.Sources, name)
		notify("log", fmt.Sprintf("[%d/%d] 开始处理来源: %s", idx+1, len(enabled), name))

		content, err := fetchSourceContent(ctx, source)
		if err != nil {
			notify("error", fmt.Sprintf("来源 %s 拉取失败: %v", name, err))
			summary.Failed = append(summary.Failed, name+"（拉取失败）")
			continue
		}
		if strings.TrimSpace(content) == "" {
			notify("error", fmt.Sprintf("来源 %s 内容为空", name))
			summary.Failed = append(summary.Failed, name+"（内容为空）")
			continue
		}

		port := source.Port
		if port <= 0 {
			port = cfg.Runner.FallbackPort
		}
		perFile := filepath.Join(panelDataDir(), "multi-"+safeFilename(strings.ReplaceAll(name, " ", "_"))+".csv")

		var captured []iptestResult
		var capturedStatus, capturedMsg string
		setNSBRunResultSink(func(results []iptestResult, status, message string) {
			captured = results
			capturedStatus = status
			capturedMsg = message
		})

		runNSBTask(
			ctx,
			headlessSession(forward),
			name,
			content,
			perFile,
			cfg.Runner.Threads,
			port,
			cfg.Runner.SpeedTest,
			cfg.Runner.SpeedURL,
			source.TLS,
			cfg.Runner.Delay,
			cfg.Runner.ResultLimit,
			"",
			cfg.Runner.SpeedMin,
			cfg.Runner.SpeedLimit,
			true,
			cfg.Runner.ScanMode,
			"",
		)
		setNSBRunResultSink(nil)

		if len(captured) == 0 {
			notify("error", fmt.Sprintf("来源 %s 没有产出有效结果（%s）", name, firstNonEmptyStr(capturedMsg, capturedStatus, "无可用 IP")))
			summary.Failed = append(summary.Failed, name+"（无有效结果）")
			continue
		}
		all = append(all, captured...)
		notify("log", fmt.Sprintf("来源 %s 完成，产出 %d 条（%s）", name, len(captured), firstNonEmptyStr(capturedMsg, "ok")))
	}

	if len(all) == 0 {
		notify("error", "所有来源都没有产出有效结果")
		return nil, summary
	}

	merged := mergeResults(all)
	summary.TotalResults = len(all)
	summary.Merged = len(merged)
	for _, res := range merged {
		if res.speedQualified {
			summary.Qualified++
		}
	}

	outFile := multiSourceOutputPath(cfg)
	if err := writeNSBCSV(outFile, merged, cfg.Runner.SpeedTest, true, cfg.Runner.ScanMode); err != nil {
		notify("error", "导出合并结果失败: "+err.Error())
		summary.Failed = append(summary.Failed, "导出失败")
	} else {
		summary.OutFile = outFile
		notify("log", fmt.Sprintf("合并完成：%d 条原始 → %d 条去重，已导出 %s", len(all), len(merged), outFile))
	}

	entries, skipped := buildCFNextIPs(merged, cfg.CFNext, cfg.Export, firstNonEmptyStr(strings.Join(summary.Sources, "/"), "CF"))
	summary.Push = fmt.Sprintf("待推送 %d 条（跳过 %d 条）", len(entries), skipped)
	setLastMultiSource(merged, entries, fmt.Sprintf("%d 条来源合并，共 %d 条结果", len(enabled), len(merged)))

	return merged, summary
}

// pushLastResults 推送最近一次结果（供「立即推送」使用）。
func pushLastResults(ctx context.Context, cfg panelConfig) cfnextPushResult {
	at, results, cached, _ := lastMultiSourceSnapshot()
	if len(results) > 0 {
		// 每次都按当前设置重建条目：改完后缀/条数后，直接「立即推送」即可生效。
		cached, _ = buildCFNextIPs(results, cfg.CFNext, cfg.Export, "CF")
	}
	if len(cached) == 0 {
		return cfnextPushResult{OK: false, Message: "还没有可推送的结果，请先跑一次测速"}
	}
	result := pushCFNext(ctx, cfg.CFNext, cached)
	if result.OK {
		_, _ = updatePanelConfig(func(c *panelConfig) error {
			c.CFNext.LastPushAt = nowRFC3339()
			c.CFNext.LastPushMsg = result.Message + "（" + at.Format("01-02 15:04") + " 的数据）"
			return nil
		})
	}
	return result
}
