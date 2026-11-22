# CFData-Next

在 [CFData-WEB](https://github.com/PoemMisty/CFData-WEB) 基础上做了三件事的 Docker 化版本：

1. **网络 URL 支持多个**：页面上一次填多个 IP 列表 / 订阅地址，合并去重后统一扫描测速
2. **容器内定时测速**：不依赖 systemd，进程内调度，支持固定间隔或每天定时
3. **结果推送到 CFNext 面板**：测速完成后自动把优选 IP 写进面板的 `preferredIPs`

UI 完全沿用原版（同一套 HTML / CSS / 交互），只在右上角「设置」菜单里加了推送和定时两块，没有改动原有布局。

## 与原版的关系

- 扫描、测速、导出、CSV 格式、GitHub 上传等逻辑**原样保留**，未做修改
- 原版的 systemd 定时任务（`schedule.go`）保留不动，容器里改用新加的进程内调度
- 新增配置独立存放在 `panel-config.json`，与上游 `cfdata-config.json` 互不干扰，便于跟随上游升级

## 目录结构

```
panelconfig.go    推送 / 定时 / 跑批参数的读写（panel-config.json）
cfnext.go         CFNext 面板客户端（登录、APIPATH 提取、读写配置）
multisource.go    批量执行、合并去重、导出（定时任务与页面保存共用）
sched.go          容器内定时调度
panel_api.go      新增的 WebSocket 接口
panel_runtime.go  启动时加载配置、起调度器
Dockerfile        多阶段构建
docker-compose.yml / docker-compose.host.yml
```

## 用现成镜像（推荐）

已发布到 Docker Hub，同一份 tag 同时支持 `linux/amd64` 与 `linux/arm64`（x86 NAS、群晖 ARM、树莓派、N1 都能用）：

```bash
docker pull zaixiaxinren/cfdata-next:latest
```

最小启动：

```bash
docker run -d --name cfdata-next \
  -p 13335:13335 \
  -v "$(pwd)/data:/data" \
  --restart unless-stopped \
  zaixiaxinren/cfdata-next:latest
```

需要扫描 / 测速 **IPv6** 时改用 host 网络（此时 `-p` 可以去掉，端口直接由宿主监听）：

```bash
docker run -d --name cfdata-next \
  --network host \
  -v "$(pwd)/data:/data" \
  --restart unless-stopped \
  zaixiaxinren/cfdata-next:latest
```

用 compose 拉镜像（把下面的内容存成 `docker-compose.yml`，不需要源码）：

```yaml
services:
  cfdata-next:
    image: zaixiaxinren/cfdata-next:latest
    container_name: cfdata-next
    restart: unless-stopped
    ports:
      - "13335:13335"
    volumes:
      - ./data:/data
    environment:
      - TZ=Asia/Shanghai
      - CFDATA_DATA_DIR=/data
```

tag 规则：

| tag | 含义 |
|---|---|
| `latest` | 跟随 `main` 分支最新一次构建 |
| `v1.0.0` | 推 `v*` 形式的 git tag 时自动生成，对应那个版本 |
| `sha-ade38f6` | 对应某次提交，需要精确回滚时用 |

镜像由 GitHub Actions 在 `main` 分支推送后自动构建；`Dockerfile` 用交叉编译，arm64 不依赖 QEMU 模拟。

## 从源码构建

在 NAS 上（源码目录）：

```bash
cd /opt/cfdata-next   # 换成你的实际目录
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
| `cfdata-merged.csv` | 批量/定时任务的合并结果 |
| `multi-<来源名>.csv` | 单个来源的结果 |

## 设置说明（右上角 ⚙️）

### 🌐 网络 URL（支持多个，同时是定时任务的来源）

「非标优选」面板里的「🌐 网络 URL」可以一次填多个地址，一行一个：

```
https://example.com/ip-list.txt
https://example.com/backup-list.txt
```

- 服务端按行拆分后逐个拉取，内容拼接后统一解析，重复的 `IP:端口` 自动去重
- 某个地址失败只跳过并记一条 ⚠️ 警告日志，其余地址照常继续；全部失败才终止任务
- 仍与「📄 IP 文件」二选一，两者同时填会提示先清掉一个

**定时任务复用这份配置。** 配置以服务端 `panel-config.json` 为唯一真源，不再依赖浏览器缓存：

- 页面顶部「💾 → 保存到服务器（含定时来源）」立即写入服务器；每个 URL 变成一个来源，端口取「备用端口」、TLS 取「TLS 开关」
- 当前非标参数（并发、延迟阈值、结果上限、测速次数/阈值、扫描方式、测速地址、输出文件名）写进 `runner` 段
- 改参数或「结果操作」里的后缀后约 1 秒自动保存到服务器，不需要手动点保存（此时不会覆盖已有来源）
- 刷新页面/换台设备打开，表单会按服务器保存的那份自动回填，日志区提示「已从服务器加载配置：N 个网络来源」
- 想手动对齐：点「💾 → 从服务器重新加载」
- 定时任务到点后直接用这份配置逐个来源运行 → 合并去重 → 导出 → 按需推送

注意：**定时任务必须用网络 URL**。用本地文件时文件只在浏览器里，服务端读不到，此时保存配置会提示「定时任务需要填写网络 URL」。

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
- **测速完成后自动推送**：定时任务结束后自动推送；手动单次任务跑完可点「立即推送上次结果」
- **同时把订阅模式设为 custom**：让面板订阅只用这批优选 IP

「测试连接」只登录并回读，不写入，可用来验证地址和密码。

### 🏷 IP 后缀与推送的节点名（结果操作）

「📥 结果操作」弹窗里，`IP:端口#` 后面那一段由**「IP 后缀」+ 勾选的字段**共同决定，导出 TXT 与面板节点名（`preferredIPs[].name`）完全一致：

| 后缀模式 | 勾选字段 | 导出的一行 | 面板 name |
| --- | --- | --- | --- |
| 数据中心（默认） | IP:PORT、数据中心 | `104.17.0.1:443#LAX` | `LAX` |
| 数据中心（默认） | IP:PORT、数据中心、IP类型 | `104.17.0.1:443#LAX-IPv4` | `LAX-IPv4` |
| 固定文本 `HK-{dc}` | IP:PORT、数据中心、IP类型 | `104.17.0.1:443#HK-LAX-IPv4` | `HK-LAX-IPv4` |
| 不加后缀 | IP:PORT、数据中心 | `104.17.0.1:443#LAX` | `LAX` |
| 不加后缀 | IP:PORT | `104.17.0.1:443` | `LAX`（面板条目必须有名称，回退数据中心） |

规则细节：

- 拼接用「分隔符」那一项（默认 `-`）；值为空的那一节自动跳过，不会出现 `LAX--`
- 后缀模式是「数据中心 / 固定文本」时，勾选列表里的「数据中心」不会重复拼一次；选「不加后缀」时它作为普通字段参与
- 支持延迟、下载速度、ASN、地区、城市、TLS 版本等全部字段，格式与导出的 CSV 一致
- 面板条目仍然是 `IP:端口#name` 三件套，字段都体现在 name 里，CFNext 没有单独的字段位

这份设置跟「💾 → 保存到服务器」一起写入 `panel-config.json` 的 `export` 段，勾选/排序字段后约 1 秒自动同步；刷新页面、容器重启、定时任务都沿用。改完点「立即推送上次结果」会按新规则重建条目，不需要重新跑测速。

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
