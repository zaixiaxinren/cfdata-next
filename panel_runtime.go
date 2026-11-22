package main

// 面板扩展运行时初始化：确保数据目录存在、加载配置、启动内置调度器。

import (
	"fmt"
	"os"
)

func initPanelRuntime() {
	dir := panelDataDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Printf("[panel] 数据目录不可用: %v\n", err)
	} else {
		fmt.Printf("[panel] 数据目录: %s\n", dir)
	}
	cfg := loadPanelConfig()
	fmt.Printf("[panel] 已加载 %d 个 IP 来源，定时任务: %v\n", len(cfg.Sources), cfg.Schedule.Enabled)
	if cfg.CFNext.PanelURL != "" {
		fmt.Printf("[panel] CFNext 面板: %s（自动推送: %v）\n", cfg.CFNext.PanelURL, cfg.CFNext.AutoPush)
	}
	startPanelScheduler()
}
