# GitHub 加速网关 (go-proxy-github)

一个部署在局域网内的 GitHub 加速网关,用 Go 开发(gin + zap + SQLite)。

```
浏览器 / git ──HTTP──> 网关 ──带用户名密码的 HTTP Proxy──> GitHub
```

## 特性

- **三种接入方式,按需选用**:
  1. **HTTP 代理模式**:客户端把网关配成系统/浏览器/git 代理(`CONNECT` 隧道 + 绝对 URI),https 流量端到端加密,支持任意 GitHub 域名;
  2. **路径模式**(ghproxy 风格):`http://网关:38018/https://github.com/xxx`,配合 git 的 `url.*.insteadOf` 使用;
  3. **域名直连模式**:通过 DNS/hosts 把 GitHub 域名指向网关,网关按 `Host` 头转发。
- **上游代理认证**:网关访问 GitHub 时走带用户名密码的 HTTP Proxy(自动生成 `Proxy-Authorization: Basic`),也支持 `https://` 协议的上游代理。
- **域名白名单**:默认只放行 GitHub 相关域名(后缀匹配),`CONNECT` 隧道默认同样受限,防止被当作开放代理滥用。
- **管理面板**(嵌入式单页,无外部资源):
  - 项目检查:输入 GitHub 项目地址,校验仓库是否存在(API 受限时自动降级网页检查),一键生成可用的 clone 命令;
  - 访问日志:仅展示 Git 项目检查记录(IP / 项目地址 / 结果),**SQLite 持久化 + 分页**,重启不丢;
  - 系统信息:运行状态、局域网 IP、上游代理可用性(实时检测 + 手动"再次检测")、流量统计。
- **gin + zap**:gin 提供管理面与路由分发,zap 输出 JSON 结构化访问日志。
- **项目级零污染**:构建产物/日志/数据库全部落在项目目录内,不安装到系统、不修改全局配置;支持自包含打包部署。

## 快速开始

### 1. 准备配置(样例模板)

```bash
cp config.example.yaml config.yaml
```

编辑 `config.yaml`,把上游代理换成你的(必须带用户名密码):

```yaml
proxy:
  upstream_proxy: "http://USERNAME:PASSWORD@your-proxy.example.com:8080"
```

> `config.yaml` 已被 `.gitignore` 忽略(含敏感信息),请勿提交;仓库内只保留 `config.example.yaml` 模板。

### 2. 构建并运行

**构建当前平台二进制**(产物输出到项目 `bin/`, 不安装到系统):

```bash
./scripts/build.sh          # Linux/macOS, 输出 bin/github-gateway-<os>-<arch>
scripts\build.bat           # Windows, 输出 bin\github-gateway-<os>-<arch>.exe
```

**运行**:

```bash
./bin/github-gateway-linux-amd64 -config config.yaml   # 按平台选择对应二进制
```

Windows 本地调试: `scripts\build.bat` 后直接运行 `bin\github-gateway-windows-amd64.exe -config config.yaml`(前台)。

**部署到其他服务器(项目级打包, 服务器无需 Go)**:

```bash
PLATFORMS="linux/amd64 linux/arm64" ./scripts/build.sh   # 多平台构建并打包到 dist/
# 拷到服务器任意目录:
tar xzf dist/github-gateway-<version>.tar.gz
cp config.example.yaml config.yaml   # 填写上游代理账号密码
./github-gateway-linux-amd64 -config config.yaml   # 按服务器架构选择对应二进制
```

需要开机自启时, 使用项目内自带的 systemd 示例(按需拷贝, 不主动安装):

```bash
sudo cp deploy/github-gateway.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now github-gateway
```

> 提示: 也可用环境变量 `GATEWAY_UPSTREAM_PROXY` 覆盖上游代理, 避免明文落盘(容器/生产推荐)。

### 3. 验证

浏览器访问 `http://网关IP:38018/` 打开管理面板;`http://网关IP:38018/healthz` 返回 `ok`。

## 管理面板

- **项目检查**:输入 GitHub 项目地址(支持 `https://` / `git@` / `ssh://` 等格式)→ 校验仓库是否存在并展示星级等详情 → 生成两条可复制的 clone 命令(HTTP 代理模式 + 路径模式,网关局域网 IP 自动探测)。
- **访问日志**:仅展示 Git 项目检查记录(时间 / IP / 项目地址 / 耗时 / 结果),SQLite 持久化,分页浏览,首页自动刷新。
- **系统信息**:服务运行状态、监听地址、局域网 IP、上游代理(密码脱敏)、**代理可用性**(后台每 60s 自动检测,支持手动"再次检测")、流量统计。

## 客户端配置(任选一种)

**方式一:git 走 HTTP 代理(最通用,支持所有 GitHub 域名)**

以下写法均为**项目级/一次性**,不写全局配置、不污染环境:

```bash
# 克隆时一次性使用(不写任何配置文件):
git -c http.https://github.com.proxy=http://192.168.1.10:38018 clone https://github.com/xxx/yyy.git

# 已克隆的仓库内设置(只写该仓库的 .git/config, 不影响其他项目):
cd yyy
git config http.https://github.com.proxy http://192.168.1.10:38018
git config https.https://github.com.proxy http://192.168.1.10:38018

# 需要全局默认时再考虑(会修改 ~/.gitconfig):
# git config --global http.proxy  http://192.168.1.10:38018
# git config --global https.proxy http://192.168.1.10:38018
```

> 注意: 若机器上已有 URL 级代理配置(如 `http.https://github.com.proxy=...`,常见于 v2ray/clash),
> 其优先级高于 `http.proxy`, 上面的 URL 级写法恰好能覆盖它。

**方式二:git `insteadOf` 路径模式(项目级, 不设代理, 只重写 URL)**

在**仓库内**设置(只写该仓库的 `.git/config`):

```bash
cd yyy
git config url."http://192.168.1.10:38018/https://github.com/".insteadOf "https://github.com/"
git config url."http://192.168.1.10:38018/https://raw.githubusercontent.com/".insteadOf "https://raw.githubusercontent.com/"
```

**方式三:域名直连(DNS/hosts 指向网关)**

```bash
192.168.1.10 github.com api.github.com raw.githubusercontent.com codeload.github.com
```

然后 `git clone http://github.com/xxx`(明文 http 到网关,网关再以 https 回源)。

## 配置说明

| 配置项 | 说明 | 默认 |
|---|---|---|
| `server.listen` | 网关监听地址 | `0.0.0.0:38018` |
| `server.db_path` | Git 项目检查记录存储路径(SQLite), 留空则仅存内存 | `data/checks.db` |
| `proxy.upstream_proxy` | 上游 HTTP 代理(带用户名密码),支持 `http/https` | 必填 |
| `proxy.allowed_domains` | GitHub 域名白名单,后缀匹配(`github.com` 会命中 `api.github.com` 等) | GitHub 常用域名 |
| `proxy.path_proxy` | 是否启用路径模式 `/https://github.com/xxx` | `true` |
| `proxy.connect_allow_any` | 是否允许 CONNECT 任意域名(开放代理,慎用) | `false` |
| `proxy.insecure_skip_verify` | 跳过上游 TLS 证书校验(仅调试) | `false` |
| `proxy.dial_timeout` / `tls_timeout` / `response_timeout` / `idle_conn_timeout` | 连接/握手/响应/空闲连接超时 | `10s/30s/60s/90s` |
| `log.level` / `log.output` | 日志级别 / stdout 或文件 | `info` / `stdout` |

环境变量覆盖(便于容器部署):`GATEWAY_LISTEN`、`GATEWAY_UPSTREAM_PROXY`、`GATEWAY_LOG_LEVEL`。

## 项目结构

```
├── main.go                     # 入口: CONNECT 与 gin 路由分发、优雅退出
├── config.example.yaml         # 配置样例模板(真实 config.yaml 被 .gitignore 忽略)
├── scripts/
│   ├── build.sh               # 构建脚本: 单平台模式(到 bin/) / 打包模式(PLATFORMS= 多平台 tar.gz)
├── deploy/
│   └── github-gateway.service  # systemd 服务示例(按需拷贝)
└── internal/
    ├── config/config.go        # 配置加载/校验(支持时长与环境变量)
    ├── logger/logger.go        # zap JSON 日志
    └── gateway/
        ├── gateway.go          # Gateway 结构、gin 引擎、管理面板接口
        ├── transport.go        # 经上游代理(带认证)的 http.Transport
        ├── reverse.go          # HTTP 反向代理(绝对URI / 路径 / Host 三种模式)
        ├── connect.go          # CONNECT 隧道(经上游代理,双向透传)
        ├── repo.go             # 项目检查(GitHub API + 网页降级 + clone 命令生成)
        ├── store.go            # SQLite 检查记录存储(纯 Go, 无 CGO)
        ├── proxycheck.go       # 上游代理可用性检测(定时 + 手动)
        ├── logs.go             # 内存日志缓冲(proxy/manage)
        ├── stats.go            # 运行统计
        └── web/index.html      # 管理面板(嵌入式单页, Semi Design 亮色主题)
```

## 测试

```bash
go test ./... -v
```

测试覆盖:路径模式反向代理 + 上游认证、CONNECT 隧道 + 上游认证、Host 域名直连、白名单拦截(路径与 CONNECT)、项目地址解析、SQLite 持久化与分页、日志分页接口、代理检测接口等。

## 安全提示

- 网关默认只代理 GitHub 白名单域名;`connect_allow_any` 请勿在生产开启。
- `config.yaml` 含上游代理账号密码,已被 `.gitignore` 忽略,请勿提交;建议用 `config.example.yaml` 模板 + 环境变量。
- 网关本身没有客户端鉴权,请部署在可信局域网内。

## 许可

MIT License
