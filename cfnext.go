package main

// CFNext 面板推送客户端。
//
// 复刻 cf-optimizer 里 push_panel.sh 实测通过的流程：
//  1. POST 面板入口（表单 password + next）登录，拿 luma_auth cookie
//  2. GET 面板页，从 HTML 里提取 `var APIPATH = "..."`（真实 API 前缀每次部署都不同）
//  3. GET {origin}{APIPATH}/api/config → {"ok":true,"data":{...}}
//  4. POST 同一地址（增量合并），只带 preferredIPs 与 optimizer 的两个字段
//  5. 回读校验条数
//
// 注意：GET 请求必须带 Accept: application/json，否则会被当成页面请求返回 HTML 兜底页。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const cfnextUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

var cfnextAPIPathPattern = regexp.MustCompile(`var\s+APIPATH\s*=\s*"([^"]*)"`)

// cfnextPreferredIP 面板里「优选 IP」的一条记录。
type cfnextPreferredIP struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
	Name string `json:"name"`
}

type cfnextClient struct {
	base   string // 面板入口，去掉末尾斜杠
	origin string
	api    string // 形如 /xxxx-yyyy
	cookie *http.Client
	client *http.Client
}

type cfnextAPIResult struct {
	OK      bool            `json:"ok"`
	Data    json.RawMessage `json:"data"`
	Error   string          `json:"error"`
	Message string          `json:"message"`
}

func newCFNextClient(cfg cfnextConfig) (*cfnextClient, error) {
	panelURL := strings.TrimSpace(cfg.PanelURL)
	if panelURL == "" {
		return nil, fmt.Errorf("未配置面板地址")
	}
	if !strings.HasPrefix(panelURL, "http://") && !strings.HasPrefix(panelURL, "https://") {
		panelURL = "https://" + panelURL
	}
	panelURL = strings.TrimRight(panelURL, "/")
	parsed, err := url.Parse(panelURL)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("面板地址无法解析: %s", cfg.PanelURL)
	}
	origin := parsed.Scheme + "://" + parsed.Host

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// 复用程序自己的 DNS 解析链（系统 DNS 失败会回退到 -dns 指定的服务器），
	// 否则容器内的 127.0.0.11 解析 pages.dev / workers.dev 容易超时。
	transport.DialContext = dialContext
	if proxy := strings.TrimSpace(cfg.Proxy); proxy != "" {
		proxyURL, err := url.Parse(proxy)
		if err != nil {
			return nil, fmt.Errorf("推送代理地址无效: %v", err)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	} else if managementProxyConfigured() {
		if managementProxyURL, err := url.Parse(managementProxyRaw); err == nil && managementProxyRaw != "" {
			transport.Proxy = http.ProxyURL(managementProxyURL)
		}
	} else {
		transport.Proxy = http.ProxyFromEnvironment
	}
	client := &http.Client{
		Timeout:   45 * time.Second,
		Jar:       jar,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	return &cfnextClient{base: panelURL, origin: origin, client: client}, nil
}

func (c *cfnextClient) newRequest(ctx context.Context, method, target string, body io.Reader, contentType string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", cfnextUserAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req, nil
}

// Login 登录并提取 APIPATH。
func (c *cfnextClient) Login(ctx context.Context, password string) error {
	form := url.Values{}
	form.Set("password", password)
	form.Set("next", c.base)
	req, err := c.newRequest(ctx, http.MethodPost, c.base, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("登录请求失败: %v", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	resp.Body.Close()

	var result cfnextAPIResult
	if err := json.Unmarshal(body, &result); err != nil || !result.OK {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return fmt.Errorf("登录失败（密码是否正确？面板返回: %s）", snippet)
	}

	// 登录后访问面板页，从 HTML 中提取真实 API 前缀。
	pageReq, err := c.newRequest(ctx, http.MethodGet, c.base, nil, "")
	if err != nil {
		return err
	}
	pageReq.Header.Set("Accept", "text/html,application/xhtml+xml")
	pageResp, err := c.client.Do(pageReq)
	if err != nil {
		return fmt.Errorf("读取面板页失败: %v", err)
	}
	pageBody, _ := io.ReadAll(io.LimitReader(pageResp.Body, 4<<20))
	pageResp.Body.Close()

	match := cfnextAPIPathPattern.FindSubmatch(pageBody)
	if len(match) < 2 {
		return fmt.Errorf("未能从面板页提取 APIPATH（页面可能不是控制台，或入口地址不对）")
	}
	c.api = strings.TrimSpace(string(match[1]))
	if c.api == "" {
		return fmt.Errorf("面板返回的 APIPATH 为空")
	}
	if !strings.HasPrefix(c.api, "/") {
		c.api = "/" + c.api
	}
	return nil
}

func (c *cfnextClient) apiURL(suffix string) string {
	return c.origin + c.api + suffix
}

func (c *cfnextClient) getConfig(ctx context.Context) (cfnextAPIResult, json.RawMessage, error) {
	req, err := c.newRequest(ctx, http.MethodGet, c.apiURL("/api/config"), nil, "")
	if err != nil {
		return cfnextAPIResult{}, nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return cfnextAPIResult{}, nil, fmt.Errorf("读取配置失败: %v", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	resp.Body.Close()

	var result cfnextAPIResult
	if err := json.Unmarshal(body, &result); err != nil {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return result, nil, fmt.Errorf("配置返回不是 JSON（可能被反扫描页拦截）: %s", snippet)
	}
	if !result.OK {
		return result, nil, fmt.Errorf("读取配置失败: %s", firstNonEmptyStr(result.Error, result.Message, "面板返回 ok=false"))
	}
	return result, result.Data, nil
}

// PostConfig 增量写入配置。
func (c *cfnextClient) postConfig(ctx context.Context, payload map[string]interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodPost, c.apiURL("/api/config"), bytes.NewReader(data), "application/json")
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("写入配置失败: %v", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	resp.Body.Close()

	var result cfnextAPIResult
	if err := json.Unmarshal(body, &result); err != nil || !result.OK {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return fmt.Errorf("写入被拒绝: %s", snippet)
	}
	return nil
}

// readPreferredIPs 读取当前优选 IP 列表。
func (c *cfnextClient) readPreferredIPs(ctx context.Context) ([]cfnextPreferredIP, error) {
	_, raw, err := c.getConfig(ctx)
	if err != nil {
		return nil, err
	}
	var holder struct {
		PreferredIPs []cfnextPreferredIP `json:"preferredIPs"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &holder); err != nil {
			return nil, fmt.Errorf("解析配置失败: %v", err)
		}
	}
	return holder.PreferredIPs, nil
}

// readOptimizer 读取现有 optimizer 配置，用于增量合并（不能整体覆盖其他字段）。
func (c *cfnextClient) readOptimizer(ctx context.Context) map[string]interface{} {
	_, raw, err := c.getConfig(ctx)
	if err != nil || len(raw) == 0 {
		return map[string]interface{}{}
	}
	var holder struct {
		Optimizer map[string]interface{} `json:"optimizer"`
	}
	if err := json.Unmarshal(raw, &holder); err != nil || holder.Optimizer == nil {
		return map[string]interface{}{}
	}
	return holder.Optimizer
}

type cfnextTestResult struct {
	OK        bool                `json:"ok"`
	Message   string              `json:"message"`
	APIPath   string              `json:"apiPath"`
	Count     int                 `json:"count"`
	Sample    []cfnextPreferredIP `json:"sample"`
	SubMode   string              `json:"subMode"`
	PanelHost string              `json:"panelHost"`
}

// TestConnection 登录并回读配置，用于界面上「测试连接」。
func testCFNextConnection(ctx context.Context, cfg cfnextConfig) cfnextTestResult {
	client, err := newCFNextClient(cfg)
	if err != nil {
		return cfnextTestResult{OK: false, Message: err.Error()}
	}
	if err := client.Login(ctx, cfg.Password); err != nil {
		return cfnextTestResult{OK: false, Message: err.Error()}
	}
	_, raw, err := client.getConfig(ctx)
	if err != nil {
		return cfnextTestResult{OK: false, Message: err.Error()}
	}
	var holder struct {
		PreferredIPs []cfnextPreferredIP `json:"preferredIPs"`
		Optimizer    map[string]interface{} `json:"optimizer"`
	}
	_ = json.Unmarshal(raw, &holder)
	sample := holder.PreferredIPs
	if len(sample) > 5 {
		sample = sample[:5]
	}
	subMode := ""
	if holder.Optimizer != nil {
		if v, ok := holder.Optimizer["subMode"].(string); ok {
			subMode = v
		}
	}
	return cfnextTestResult{
		OK:        true,
		Message:   fmt.Sprintf("连接成功，面板当前有 %d 条优选 IP", len(holder.PreferredIPs)),
		APIPath:   client.api,
		Count:     len(holder.PreferredIPs),
		Sample:    sample,
		SubMode:   subMode,
		PanelHost: client.base,
	}
}

type cfnextPushResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	Count   int    `json:"count"`
	Skipped int    `json:"skipped"`
}

// buildCFNextIPs 把测速结果转成面板条目，去重后按速度优先排序。
func buildCFNextIPs(results []iptestResult, cfg cfnextConfig, fallbackName string) ([]cfnextPreferredIP, int) {
	type candidate struct {
		entry cfnextPreferredIP
		speed float64
	}
	seen := make(map[string]int)
	candidates := make([]candidate, 0, len(results))
	skipped := 0
	for _, res := range results {
		if cfg.OnlyQualified && !res.speedQualified {
			skipped++
			continue
		}
		ip := strings.TrimSpace(res.ipAddr)
		if ip == "" || res.port <= 0 {
			skipped++
			continue
		}
		name := strings.TrimSpace(res.dataCenter)
		if name == "" {
			name = strings.TrimSpace(fallbackName)
		}
		if name == "" {
			name = "CF"
		}
		key := ip + "|" + itoa(res.port)
		if idx, ok := seen[key]; ok {
			if res.downloadSpeed > candidates[idx].speed {
				candidates[idx].speed = res.downloadSpeed
				candidates[idx].entry.Name = name
			}
			continue
		}
		seen[key] = len(candidates)
		candidates = append(candidates, candidate{
			entry: cfnextPreferredIP{IP: ip, Port: res.port, Name: name},
			speed: res.downloadSpeed,
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].speed > candidates[j].speed
	})
	entries := make([]cfnextPreferredIP, 0, len(candidates))
	for _, item := range candidates {
		entries = append(entries, item.entry)
	}
	if cfg.MaxCount > 0 && len(entries) > cfg.MaxCount {
		entries = entries[:cfg.MaxCount]
	}
	return entries, skipped
}

// pushCFNext 完整推送流程：登录 → 读旧配置 → 写 preferredIPs → 回读校验。
func pushCFNext(ctx context.Context, cfg cfnextConfig, entries []cfnextPreferredIP) cfnextPushResult {
	if len(entries) == 0 {
		return cfnextPushResult{OK: false, Message: "没有可推送的 IP"}
	}
	client, err := newCFNextClient(cfg)
	if err != nil {
		return cfnextPushResult{OK: false, Message: err.Error()}
	}
	if err := client.Login(ctx, cfg.Password); err != nil {
		return cfnextPushResult{OK: false, Message: err.Error()}
	}

	existing := client.readOptimizer(ctx)
	payload := map[string]interface{}{"preferredIPs": entries}
	if cfg.ForceCustom {
		merged := map[string]interface{}{}
		for k, v := range existing {
			merged[k] = v
		}
		merged["subMode"] = "custom"
		merged["subIncludeDefault"] = false
		payload["optimizer"] = merged
	}
	if err := client.postConfig(ctx, payload); err != nil {
		return cfnextPushResult{OK: false, Message: err.Error()}
	}

	time.Sleep(2 * time.Second)
	now, err := client.readPreferredIPs(ctx)
	if err != nil {
		return cfnextPushResult{OK: false, Message: "写入成功但回读失败: " + err.Error(), Count: len(entries)}
	}
	if len(now) != len(entries) {
		return cfnextPushResult{
			OK:      false,
			Message: fmt.Sprintf("写入后回读条数不一致（提交 %d 条，面板 %d 条）", len(entries), len(now)),
			Count:   len(now),
		}
	}
	return cfnextPushResult{OK: true, Message: fmt.Sprintf("推送成功，面板现有 %d 条优选 IP", len(now)), Count: len(now)}
}

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
