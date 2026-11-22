package main

// 面板扩展配置：多 IP 来源、CFNext 推送、容器内定时任务。
// 单独存一个文件 panel-config.json，避免与上游 cfdata-config.json 的迁移逻辑冲突。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// panelDataDir 返回持久化数据目录：优先环境变量（Docker 挂载卷），否则二进制所在目录。
func panelDataDir() string {
	if v := strings.TrimSpace(os.Getenv("CFDATA_DATA_DIR")); v != "" {
		return v
	}
	exe, err := os.Executable()
	if err == nil && strings.TrimSpace(exe) != "" {
		return filepath.Dir(exe)
	}
	return "."
}

func panelConfigPath() string {
	return filepath.Join(panelDataDir(), "panel-config.json")
}

// ipSource 一个 IP 来源：可以是在线订阅/列表 URL，也可以是本地文件。
type ipSource struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Port    int    `json:"port"`    // 0 表示用全局备用端口
	TLS     bool   `json:"tls"`     // 该来源是否走 TLS 探测
	Enabled bool   `json:"enabled"`
}

// cfnextConfig CFNext 面板推送设置。
type cfnextConfig struct {
	AutoPush      bool   `json:"autoPush"`      // 每次测速完成后自动推送
	PanelURL      string `json:"panelUrl"`      // 面板入口地址，例如 https://xxx.workers.dev/abc
	Password      string `json:"password"`      // 面板登录密码
	MaxCount      int    `json:"maxCount"`      // 最多推送多少条
	OnlyQualified bool   `json:"onlyQualified"` // 只推送达到测速阈值的
	ForceCustom   bool   `json:"forceCustom"`   // 同时把订阅模式设为 custom
	Proxy         string `json:"proxy"`         // 单独代理；留空用管理面代理
	LastPushAt    string `json:"lastPushAt"`
	LastPushMsg   string `json:"lastPushMsg"`
}

// scheduleConfig 容器内定时任务（不依赖 systemd）。
type scheduleConfig struct {
	Enabled         bool   `json:"enabled"`
	Mode            string `json:"mode"` // interval | daily
	IntervalMinutes int    `json:"intervalMinutes"`
	DailyTime       string `json:"dailyTime"` // HH:MM
	LastRun         string `json:"lastRun"`
	LastResult      string `json:"lastResult"`
}

// exportConfig 结果操作的导出/后缀设置，随页面保存持久化，供定时任务与 CFNext 推送沿用。
type exportConfig struct {
	Format       string   `json:"format"`       // txt | csv
	Fields       []string `json:"fields"`       // 导出字段顺序（前端字段 key）
	Separator    string   `json:"separator"`    // TXT 字段分隔符
	IPv6Brackets bool    `json:"ipv6Brackets"` // IPv6 是否加方括号
	CustomFields string   `json:"customFields"` // 形如 备注:测试版
	SuffixMode   string   `json:"suffixMode"`   // dc（数据中心）| custom（固定文本）| none（不加）
	SuffixText   string   `json:"suffixText"`   // 固定文本，支持 {dc} 占位符
}

// runnerConfig 多来源批量执行的统一参数。
type runnerConfig struct {
	Threads      int     `json:"threads"`
	Delay        int     `json:"delay"`
	ResultLimit  int     `json:"resultLimit"`
	SpeedTest    int     `json:"speedTest"`
	SpeedMin     float64 `json:"speedMin"`
	SpeedLimit   int     `json:"speedLimit"`
	ScanMode     string  `json:"scanMode"`
	FallbackPort int     `json:"fallbackPort"`
	SpeedURL     string  `json:"speedUrl"`
	OutFile      string  `json:"outFile"`
}

type panelConfig struct {
	Sources   []ipSource     `json:"sources"`
	CFNext    cfnextConfig   `json:"cfnext"`
	Schedule  scheduleConfig `json:"schedule"`
	Runner    runnerConfig   `json:"runner"`
	Export    exportConfig   `json:"export"`
	UpdatedAt string         `json:"updatedAt"` // 最后一次页面保存时间（RFC3339），前端用来显示「已保存」
}

var panelConfigState = struct {
	sync.RWMutex
	cfg panelConfig
}{}

func defaultPanelConfig() panelConfig {
	return panelConfig{
		Sources: []ipSource{},
		CFNext: cfnextConfig{
			MaxCount:      30,
			OnlyQualified: true,
			ForceCustom:   true,
		},
		Schedule: scheduleConfig{
			Mode:            "interval",
			IntervalMinutes: 360,
			DailyTime:       "04:00",
		},
		Runner: runnerConfig{
			Threads:      20,
			Delay:        200,
			ResultLimit:  50,
			SpeedTest:    5,
			SpeedMin:     5,
			SpeedLimit:   10,
			ScanMode:     scanModeTCPing,
			FallbackPort: 443,
			SpeedURL:     autoSpeedURLValue,
			OutFile:      "cfdata-merged.csv",
		},
		Export: exportConfig{
			Format:       "txt",
			Fields:       []string{"ipport", "dc"},
			Separator:    "-",
			IPv6Brackets: true,
			SuffixMode:   "dc",
		},
	}
}

func normalizePanelConfig(cfg panelConfig) panelConfig {
	def := defaultPanelConfig()
	if cfg.Runner.Threads <= 0 {
		cfg.Runner.Threads = def.Runner.Threads
	}
	if cfg.Runner.ResultLimit <= 0 {
		cfg.Runner.ResultLimit = def.Runner.ResultLimit
	}
	if cfg.Runner.SpeedTest < 0 {
		cfg.Runner.SpeedTest = 0
	}
	if cfg.Runner.SpeedLimit <= 0 {
		cfg.Runner.SpeedLimit = def.Runner.SpeedLimit
	}
	if cfg.Runner.FallbackPort <= 0 {
		cfg.Runner.FallbackPort = def.Runner.FallbackPort
	}
	if cfg.Runner.SpeedURL == "" {
		cfg.Runner.SpeedURL = autoSpeedURLValue
	}
	if cfg.Runner.OutFile == "" {
		cfg.Runner.OutFile = def.Runner.OutFile
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Runner.ScanMode)) {
	case scanModeHTTPing:
		cfg.Runner.ScanMode = scanModeHTTPing
	case scanModeTCPing, "":
		cfg.Runner.ScanMode = scanModeTCPing
	default:
		cfg.Runner.ScanMode = scanModeTCPing
	}
	if cfg.CFNext.MaxCount <= 0 {
		cfg.CFNext.MaxCount = def.CFNext.MaxCount
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Export.Format)) {
	case "csv":
		cfg.Export.Format = "csv"
	default:
		cfg.Export.Format = "txt"
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Export.SuffixMode)) {
	case "custom":
		cfg.Export.SuffixMode = "custom"
	case "none":
		cfg.Export.SuffixMode = "none"
	default:
		cfg.Export.SuffixMode = "dc"
	}
	cfg.Export.SuffixText = strings.TrimSpace(cfg.Export.SuffixText)
	if len(cfg.Export.Fields) == 0 {
		cfg.Export.Fields = def.Export.Fields
	}
	if strings.TrimSpace(cfg.Export.Separator) == "" {
		cfg.Export.Separator = def.Export.Separator
	}
	if cfg.Schedule.Mode != "daily" {
		cfg.Schedule.Mode = "interval"
	}
	if cfg.Schedule.IntervalMinutes <= 0 {
		cfg.Schedule.IntervalMinutes = def.Schedule.IntervalMinutes
	}
	if _, _, err := parseDailyTime(cfg.Schedule.DailyTime); err != nil {
		cfg.Schedule.DailyTime = def.Schedule.DailyTime
	}
	for i := range cfg.Sources {
		cfg.Sources[i].Name = strings.TrimSpace(cfg.Sources[i].Name)
		cfg.Sources[i].URL = strings.TrimSpace(cfg.Sources[i].URL)
		if cfg.Sources[i].Name == "" {
			cfg.Sources[i].Name = sourceDefaultName(cfg.Sources[i].URL, i)
		}
	}
	if cfg.Sources == nil {
		cfg.Sources = []ipSource{}
	}
	return cfg
}

func sourceDefaultName(url string, idx int) string {
	trimmed := strings.TrimSpace(url)
	if trimmed != "" {
		if base := filepath.Base(trimmed); base != "" && base != "." && base != "/" {
			if len(base) > 32 {
				base = base[:32]
			}
			return base
		}
	}
	return "来源" + itoa(idx+1)
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	buf := make([]byte, 0, 8)
	for v > 0 {
		buf = append([]byte{byte('0' + v%10)}, buf...)
		v /= 10
	}
	if neg {
		return "-" + string(buf)
	}
	return string(buf)
}

func loadPanelConfig() panelConfig {
	panelConfigState.RLock()
	loaded := panelConfigState.cfg
	hasLoaded := len(loaded.Sources) > 0 || loaded.CFNext.PanelURL != "" || loaded.Schedule.LastRun != "" || loaded.UpdatedAt != ""
	panelConfigState.RUnlock()
	if hasLoaded {
		return loaded
	}

	cfg := defaultPanelConfig()
	data, err := os.ReadFile(panelConfigPath())
	if err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &cfg); err != nil {
			recordProgramDebugError("panel_config", "解析 panel-config.json 失败: "+err.Error())
			cfg = defaultPanelConfig()
		}
	}
	cfg = normalizePanelConfig(cfg)
	panelConfigState.Lock()
	panelConfigState.cfg = cfg
	panelConfigState.Unlock()
	return cfg
}

func savePanelConfig(cfg panelConfig) error {
	cfg = normalizePanelConfig(cfg)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(panelDataDir(), 0755); err != nil {
		return err
	}
	if err := atomicWriteFile(panelConfigPath(), data, 0600); err != nil {
		return err
	}
	panelConfigState.Lock()
	panelConfigState.cfg = cfg
	panelConfigState.Unlock()
	return nil
}

// updatePanelConfig 读-改-写，供各 WS 处理器复用。
func updatePanelConfig(mutate func(*panelConfig) error) (panelConfig, error) {
	cfg := loadPanelConfig()
	if err := mutate(&cfg); err != nil {
		return cfg, err
	}
	if err := savePanelConfig(cfg); err != nil {
		return cfg, err
	}
	return normalizePanelConfig(cfg), nil
}

// parseDailyTime 解析 HH:MM。
func parseDailyTime(value string) (hour, minute int, err error) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0, 0, errInvalidTime
	}
	hour, err = atoiStrict(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, errInvalidTime
	}
	minute, err = atoiStrict(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, 0, errInvalidTime
	}
	return hour, minute, nil
}

type panelConfigError string

func (e panelConfigError) Error() string { return string(e) }

const errInvalidTime = panelConfigError("时间格式应为 HH:MM")

func atoiStrict(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errInvalidTime
	}
	value := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errInvalidTime
		}
		value = value*10 + int(r-'0')
	}
	return value, nil
}

func nowRFC3339() string {
	return time.Now().Format(time.RFC3339)
}

func parseRFC3339Safe(value string) (time.Time, bool) {
	t, err := time.ParseInLocation(time.RFC3339, strings.TrimSpace(value), time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
