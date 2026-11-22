# CFData-Next

在 [CFData-WEB](https://github.com/PoemMisty/CFData-WEB) 基础上做了三件事的 Docker 化版本：

1. **多个 IP 来源列表**：一次配置多个 IP 列表 / 订阅地址，逐个跑完再合并去重
2. **容器内定时测速**：不依赖 systemd，进程内调度，支持固定间隔或每天定时
3. **结果推送到 CFNext 面板**：测速完成后自动把优选 IP 写进面板的 `preferredIPs`

UI 完全沿用原版（同一套 HTML / CSS / 交互），只在右上角「设置」菜单里加了三块内容，没有改动原有布局。

## 与原版的关系

- 扫描、测速、导出、CSV 格式、GitHub 上传等逻辑**原样保留**，未做修改
- 原版的 systemd 定时任务（`schedule.go`）保留不动，容器里改用新加的进程内调度
- 新增配置独立存放在 `panel-config.json`，与上游 `cfdata-config.json` 互不干扰，便于跟随上游升级

## 目录结构

```
panelconfig.go    多来源 / 推送 / 定时 / 跑批参数的读写（panel-config.json）
cfnext.go         CFNext 面板客户端（登录、APIPATH 提取、读写配置）
multisource.go    多来源批量执行、合并去重、导出
sched.go          容器内定时调度
panel_api.go      新增的 WebSocket 接口
panel_runtime.go  启动时加载配置、起调度器
Dockerfile        多阶段构建
docker-compose.yml / docker-compose.host.yml
```

## 部署

在 NAS 上（源码目录）：

```bash
cd /vol1/1000/docker/cfdata-next
docker compose up -d --build
```

访问 `http://NAS_IP:13335`。

需要扫描 / 测速 **IPv6** 地址时用 host 网络版本：

```bash
docker compose -f docker-compose.host.yml up -d --build
```

数据都在 `./data`：

| 文件 | 说明 |
|---|---|
| `panel-config.json` | 来源、推送、定时、跑批参数 |
| `cfdata-config.json` | 原版配置文件 |
| `cfdata-merged.csv` | 多来源合并结果 |
| `multi-<来源名>.csv` | 单个来源的结果 |

## 设置说明（右上角 ⚙️）

### 📚 多来源 IP

一行一个，格式 `名称|地址|端口|TLS`，后两项可省略：

```
主力|https://example.com/ip.txt|443|1
备用|/data/backup.txt
```

- 地址支持 `http(s)://` 订阅或 IP 列表，也支持容器内绝对路径（如 `/data/xxx.txt`）
- 端口省略默认 443；TLS 填 `1`/`0`，省略默认开启
- 点「保存来源」后用「▶ 跑全部来源」执行：逐个来源扫描 → 测速 → 合并去重（按 `IP:端口` 去重，保留速度更高的一条）→ 导出 `cfdata-merged.csv`

跑批参数沿用原版非标优选的语义（线程数、延迟阈值、结果上限、测速线程、测速阈值等），当前取 `panel-config.json` 里 `runner` 段的值。

### 🌐 非标优选页面的网络 URL（支持多个）

「非标优选」面板里的「🌐 网络 URL」现在可以一次填多个地址，一行一个：

```
https://example.com/ip-list.txt
https://example.com/backup-list.txt
```

- 服务端按行拆分后逐个拉取，内容拼接后统一解析，重复的 `IP:端口` 自动去重
- 某个地址失败只跳过并记一条 ⚠️ 警告日志，其余地址照常继续；全部失败才终止任务
- 仍与「📄 IP 文件」二选一，两者同时填会提示先清掉一个

### ☁️ CFNext 推送

填面板入口地址（例如 `https://xxx.pages.dev/abcd`）和面板密码即可。

推送流程与 `cf-optimizer` 的 `push_panel.sh` 一致：

1. `POST` 面板入口（表单 `password` + `next`）登录，拿 `luma_auth` cookie
2. `GET` 面板页，从 HTML 里提取 `var APIPATH = "..."`（每次部署都不同，必须动态取）
3. `GET {origin}{APIPATH}/api/config` 读当前配置
4. `POST` 同一地址**增量合并**，只带 `preferredIPs` 和 `optimizer` 的 `subMode` / `subIncludeDefault`
5. 回读校验条数是否一致

选项：

- **最多推送条数**：按下载速度降序取前 N 条
- **只推送达到测速阈值的**：跳过未达标 / 未测速的
- **测速完成后自动推送**：定时任务和手动「跑全部来源」都会在结束后自动推送
- **同时把订阅模式设为 custom**：让面板订阅只用这批优选 IP

「测试连接」只登录并回读，不写入，可用来验证地址和密码。

### ⏱ 定时测速

- **固定间隔**：每 N 分钟跑一次（最小 5 分钟）
- **每天定时**：每天 `HH:MM` 跑一次

调度器每 20 秒检查一次；有任务在跑时自动跳过本次，不会并发。上次执行时间和结果会显示在下面。

## 常见问题

**容器内解析不了面板域名 / DNS 超时**
启动参数已带 `-dns 223.5.5.5,8.8.8.8` 强制使用公共 DNS，且推送客户端复用了程序的解析链（系统 DNS 失败会回退）。如果仍然失败，把 compose 里的 `-dns` 换成你本地可用的 DNS。

**IPv6 地址全都测不通**
默认 bridge 网络没有 IPv6 出口，改用 `docker-compose.host.yml`。

**局域网打不开 13335**
这台 NAS（fnOS）的 `security_service` 会用 XDP 做端口白名单，nft/iptables 里看不到。需要在 NAS 管理界面把 `13335` 加入放行端口，或者用 Lucky 反向代理转发过去。

**测速结果普遍偏低**
测速地址默认 `auto`（会挑可用的内置源）。可在 `panel-config.json` 的 `runner.speedUrl` 里换成你自己的测速地址，例如 `speed.cloudflare.com/__down?bytes=100000000`。

**推送报「未能从面板页提取 APIPATH」**
说明登录后的页面不是控制台页。检查地址是不是面板入口（`https://域名/自定义路径`），而不是 `/sub` 或后端 Worker 地址。

## 许可

上游 CFData-WEB 的许可见 [LICENSE](./LICENSE)。本仓库的新增部分同样遵循该许可。
