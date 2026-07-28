
# 妙妙屋X - Xray 服务器管理与订阅拼车系统

<div align="center">
  <img height="200px" src="https://raw.githubusercontent.com/iluobei/miaomiaowuX/refs/heads/main/screenshots/mmwx_light.webp" />
</div>

妙妙屋X 是 [妙妙屋](https://github.com/iluobei/miaomiaowu) 的增强版本，在原有 Clash 订阅管理基础上，新增 Xray 多服务器管理、远程节点部署、流量监控、证书管理等功能。支持主控/子服务器架构，通过 [mmw-agent](https://github.com/iluobei/mmw-agent) 实现远程服务器的统一管理。

## 功能特性

### Xray 服务器管理（新增）
- 🖥️ 多服务器管理 - 主控统一管理多台远程 Xray 服务器
- 🔌 远程连接 - WebSocket / HTTP / Pull 三种连接模式，自动回退
- 📊 实时流量 - 各服务器流量统计与实时速度监控
- 🔧 远程配置 - 在线管理远程服务器的 Xray/Nginx 配置
- 📡 入站/出站管理 - 可视化管理 Xray 入站、出站、路由规则
- 🔐 证书管理 - ACME 自动申请/续期 SSL 证书，支持多种 DNS 提供商
- 🚀 一键部署 - 远程服务器一键安装 Xray + Nginx + Agent
- 📦 套餐管理 - 用户套餐与流量限额管理
- 🔄 节点同步 - 入站变更自动同步到订阅节点

### 订阅管理（继承自妙妙屋）
- 📊 流量监控 - 支持 Xray 流量采集与外部订阅流量聚合统计
- 📈 历史流量 - 30 天流量使用趋势图表
- 📦 节点管理 - 导入个人节点或机场节点，支持批量操作
- 👥 用户管理 - 管理员/普通用户角色区分，订阅权限管理
- 🌓 主题切换 - 支持亮色/暗色模式

### 支持的客户端格式
Clash(Meta) / Surge / Loon / Quantumult X / Shadowrocket / SingBox / Stash / Surfboard / V2Ray / Egern

## 安装部署

### 方式 1：一键安装（推荐）

```bash
curl -sL https://raw.githubusercontent.com/iluobei/miaomiaowuX/main/install.sh | sudo bash
```

自动检测架构、下载最新版本、创建 systemd 服务。安装完成后访问 `http://服务器IP:12889` 进入初始化向导。

更新：
```bash
curl -sL https://raw.githubusercontent.com/iluobei/miaomiaowuX/main/install.sh | sudo bash -s update
```

卸载：
```bash
curl -sL https://raw.githubusercontent.com/iluobei/miaomiaowuX/main/install.sh | sudo bash -s uninstall
```

### 方式 2：Docker 部署

> 默认使用 host 网络模式 — 便于 agent 反向连接、多端口场景,也避免后续新增端口又要改 compose。
>
> **Docker 下开启 HTTPS**:推荐在主控**宿主机**上装一个 agent,用它的 nginx 对外反代主控(容器内没有 systemd,容器内跑 nginx 维护性差)。主控容器只需提供 `12889` 的 http 面板。做法见下方「Docker 开启 HTTPS」。

```bash
docker run -d \
  --name miaomiaowux \
  --network host \
  --restart unless-stopped \
  -v $(pwd)/data:/app/data \
  -v $(pwd)/subscribes:/app/subscribes \
  -v $(pwd)/rule_templates:/app/rule_templates \
  ghcr.io/iluobei/miaomiaowux:latest
```

#### Docker Compose

```yaml
version: '3.8'

services:
  miaomiaowux:
    image: ghcr.io/iluobei/miaomiaowux:latest
    container_name: miaomiaowux
    restart: unless-stopped
    network_mode: host
    environment:
      - PORT=12889
      - LOG_LEVEL=info
    volumes:
      - ./data:/app/data
      - ./subscribes:/app/subscribes
      - ./rule_templates:/app/rule_templates
```

#### Docker 开启 HTTPS（宿主机 agent 反代）

Docker 主控自身开 HTTPS 推荐用宿主机 agent 反代（容器内无 systemd，内置 nginx 维护性差）：

1. 在主控宿主机上装一个 agent，`master_url` 配成 `http://127.0.0.1:12889`（本机直连）；
2. 在主控「服务管理」里给这台 agent 安装 nginx；
3. 点「反代主控」按钮：主控自动申请证书并下发，agent 的 nginx 对外 443 反代到 `127.0.0.1:12889`，主控域名即可走 HTTPS。

> 备选：容器内置 nginx 也能开 HTTPS（镜像已预装），但需 host 网络或额外映射 80/443，不推荐。

### 方式 3：二进制部署

从 [Releases](https://github.com/iluobei/miaomiaowuX/releases) 下载对应平台的二进制文件：

```bash
# Linux
chmod +x mmwx-linux-amd64
./mmwx-linux-amd64

# 或指定配置文件
./mmwx-linux-amd64 -c config.yaml
```

默认端口 `12889`，访问 `http://服务器IP:12889` 进入初始化向导。

### 远程服务器部署

在主控面板添加远程服务器后，会生成一键安装命令，在远程服务器上执行即可自动安装 [mmw-agent](https://github.com/iluobei/mmw-agent) 并连接到主控。

## 架构

```
┌─────────────────────────────────────────┐
│           妙妙屋X (主控)                 │
│                                         │
│  订阅管理 / Xray管理 / 证书管理 / 用户管理 │
│  流量统计 / 套餐管理 / 节点同步           │
└────────────────┬────────────────────────┘
                 │ WebSocket / HTTP / Pull
    ┌────────────┼────────────┐
    ▼            ▼            ▼
┌────────┐  ┌────────┐  ┌────────┐
│ Agent1 │  │ Agent2 │  │ Agent3 │
│ (Xray) │  │ (Xray) │  │ (Xray) │
└────────┘  └────────┘  └────────┘
```

## 配置文件

```yaml
mode: master              # master（默认）或 remote
port: "12889"             # 监听端口
# 以下为 remote 模式配置
master_server: ""         # 主控地址
remote_token: ""          # 服务器令牌
connection_mode: "auto"   # auto | websocket | http | pull
```

## 环境变量

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `PORT` | 服务端口 | `12889` |
| `LOG_LEVEL` | 日志级别 | `info` |
| `JWT_SECRET` | 会话令牌签名密钥，设置后 token 使用 HMAC 签名，更换密钥会使所有会话失效。未设置则使用纯随机 token | 未设置 |
| `ALLOWED_ORIGINS` | CORS 允许来源 | `*` |
| `MMWX_MODE` | 运行模式 | `master` |

## 技术栈

- 后端：Go 1.25 + net/http + SQLite (modernc.org/sqlite)
- 前端：React 19 + Vite 7 + TanStack Router + TailwindCSS 4 + shadcn/ui
- 单二进制部署，前端通过 Go embed 嵌入

## ⚠️ 免责声明

- 本程序仅供学习交流使用，请勿用于非法用途
- 使用本程序需遵守当地法律法规
- 作者不对使用者的任何行为承担责任

<details>
<summary>更新日志</summary>

### v0.3.8 (2026-07-28)
- 🌈 增加cdn更新配置
- 🌈优化mcp
- 🛠️ fix: mieru用户名错误，添加出站502
- 🛠️ fix: tgminiapp流量增加倍率
- 🛠️ fix: 偶发许可证失效无法自愈
- 🛠️ fix: 先屏蔽修改节点按钮
- 🛠️ fix: 分享服务器与证书加载bug
- 🛠️ fix: 小火箭订阅有效期兼容
- 🛠️ fix: 流量offset误报
- 🛠️ fix: 锁定节点IPv6不生效
- 🛠️ fix: 静默模式没有包含套餐短码
### v0.3.7 (2026-07-26)
- 🌈 ss默认开启tcpfastopen,hy2支持自签
- 🌈 增加cdn更新配置
- 🌈 支持探测已存在的tunnel
- 🌈 支持绑定tg用户
- 🌈 支持自定义部分网站信息
- 🌈 移植妙妙屋clash链式代理
- 🌈tg公告增加日志
- 🌈管理员模板支持分享用户
- 🛠️ fix: clash过滤snellv6的节点
- 🛠️ fix: http|socks5密码下发错误
- 🛠️ fix: ssrf
- 🛠️ fix: tg通知失败400
- 🛠️ fix: v6节点在没有v6的探测点误报被墙
- 🛠️ fix: 上报间隔修改导致sqlite busy
- 🛠️ fix: 中转过的节点用户凭据未生效
- 🛠️ fix: 修改节点的时候增加中转配置没有生效
- 🛠️ fix: 用户禁用后没有踢出登录状态
- 🛠️ fix: 纯 IPv6 服务器节点同步
- 🛠️ fix: 自装旧版Nginx时提示不兼容
- 🛠️ fix: 迁移过来的订阅无法删除
- 🛠️ fix: 近期改动导致分享服务器的功能缺失异常
### v0.3.6 (2026-07-23)
- 🌈 增加auto类型订阅客户端
- 🌈 编辑订阅支持调整可用节点比例
- 🌈 隧道和转发支持连通性探测
- 🛠️ fix: docker配置缺少订阅目录
- 🛠️ fix: 一堆bug
- 🛠️ fix: 已有倍率的节点流量回填错误
- 🛠️ fix: 支持服务器可用性探测
- 🛠️ fix: 服务器地址输入IP时没有同步节点配置
- 🛠️ fix: 真*wal*占用磁盘问题修复
- 🛠️ fix: 覆写功能被模板覆盖
- 🛠️ fix: 订阅文件重名覆盖
- 🛠️ fix: 许可证到期限速配置无法解除
### v0.3.5 (2026-07-21)
- Change license to Miaomiaowu X Source Available License
- 🌈 tg通知支持配置文本
- 🌈 增加共享 reality 域名池
- 🌈 增加日志管理
- 🌈 探针功能优化
- 🌈 探针支持系统指标
- 🌈 支持修改节点部分配置
- 🌈 支持用户设置默认模板
- 🌈 调整系统设置与增加公告
- 🌈 部分显示优化
- 🛠️ fix: agent安装接口命令注入#10
- 🛠️ fix: docker主控无法开启https
- 🛠️ fix: socks5节点无法复制uri
- 🛠️ fix: tg注册改为备注用户名与id
- 🛠️ fix: websocket并发写导致的panic
- 🛠️ fix: 增加ddns状态显示图标
- 🛠️ fix: 增加伪装探针时原登录页的开关
- 🛠️ fix: 增加用户流量限制覆写
- 🛠️ fix: 增加许可证失效容忍度，防止许可证服务不可用导致失效
- 🛠️ fix: 备份功能不可用
- 🛠️ fix: 套餐用户绑定同步
- 🛠️ fix: 接口ssrf漏洞
- 🛠️ fix: 移除兼容妙妙屋短链接
- 🛠️ fix: 管理员查看用户流量未计算倍率
- 🛠️ fix: 节点X无法确定所属服务器
- 🛠️ fix: 获取订阅订阅名称不正确
- 🛠️ fix: 订阅文件接口没有按用户鉴权
- 🛠️ fix: 路由出站匹配节点失败
- 🛠️ fix: 链式隧道优化
- 🛠️ fix: 首次安装时 Xray 启动报错 config.json not found #14
- 🛠️ fix: 默认开启udp=true参数
### v0.3.4 (2026-07-15)
- 🌈 增加二次元主题
- 🌈 增加伪装探针模式
- 🌈 增加幻想风格主题
- 🌈 增加默认主题设置
- 🌈 支持创建链式端口转发
- 🌈 用户管理增加续费视图
- 🌈 访问频繁的接口改为ws
- 🛠️ fix: snell节点路由出站与当作出站使用问题
- 🛠️ fix: 不带vision的reality路由出站配置错误
- 🛠️ fix: 流量统计带下划线用户不准确
### v0.3.3 (2026-07-10)
- 🌈 增加tgbot复制文案配置
- 🌈 客户端限流配置改为连接数限制
- 🌈 支持snell协议
- 🛠️ fix: anytls udp fullcone
- 🛠️ fix: mmwx-wal异常占用磁盘
- 🛠️ fix: tgapp上的流量无法重置
- 🛠️ fix: 流量信息流量显示错误
- 🛠️ fix: 流量通知没有计算offset
- 🛠️ fix: 添加节点证书首次下发失败
- 🛠️ fix: 管理员支持管理所有外部订阅
- 🛠️ fix: 节点名称重复的时候自动给新节点添加后缀
- 🛠️ fix:tg通知特殊字符导致通知失败
- 🛠️ fix:v4+v6双栈节点绑定套餐失败#6
- 🛠️ fix:ws和vless偷自己nginx配置冲突
- 🛠️ fix:xray配置错误导致的重启风暴
- 🛠️ fix:偶尔添加账户绑定套餐用户重复
- 🛠️ fix:分享的服务器节点丢失
- 🛠️ fix:分享的服务断联后无法恢复
- 🛠️ fix:外置xray添加anytls节点报错
- 🛠️ fix:用户管理支持复制用户的客户端订阅
### v0.3.2 (2026-07-03)
- 🌈 修改许可证服务器域名
- 🌈 增加ipv6开关
- 🌈 增加上下线通知容忍阈值
- 🌈 流量统计增加MAX(IN/OUT)模式
- 🌈 节点管理增加uri视图
- 🛠️ fix:Agent隐身模式更新报错504
- 🛠️ fix:clash配置导入节点失败
- 🛠️ fix:中转服务节点真实节点透出
- 🛠️ fix:从妙妙屋迁移的妙妙屋管理员无法删除
- 🛠️ fix:分享出去的服务器下发证书失败
- 🛠️ fix:删除用户网速相关后计算
- 🛠️ fix:套餐的用户ss节点配置下发错误
- 🛠️ fix:带中转配置的节点服务器匹配错误
- 🛠️ fix:开关外部订阅导致节点乱序
- 🛠️ fix:流量重置功能未生效
- 🛠️ fix:用户侧限速失效
- 🛠️ fix:用户外部订阅无法删除
- 🛠️ fix:节点倍率不再继承父节点
- 🛠️ fix:许可证触发限流时pro不可用
- 🛠️ fix:路由出站vless reality缺少配置
- 🛠️ fix删除节点耗时超长
### v0.3.1 (2026-06-29)
- 🛠️ fix:shadowrocket无法获取节点
- 🛠️ fix:上线通知发送失败
- 🛠️ fix:节点删除后流量信息一起删除
- 🛠️ fix:限速配置下发的子账户错误
### v0.3.0 (2026-06-27)
- ♻️ refactor: 节点解析/订阅转换迁移到 proxyparser 共享模块
- 🌈专家模式支持中转服务器端口配置
- 🌈创建节点支持选择多IP
- 🌈增加agent签名验证，防篡改
- 🌈增加备份加密
- 🛠️ fix:主动探测主控域名与浏览器域名不一致的情况
- 🛠️ fix:优化内存占用
- 🛠️ fix:删除节点级联解除出站
- 🛠️ fix:增加流量子账户快照
- 🛠️ fix:日志没有生成文件与自动清理
- 🛠️ fix:某些场景误封ip的问题
- 🛠️ fix:添加服务ddns开关样式错误
- 🛠️ fix:重复添加用户凭据
- 🛠️ fix:验证码配置误报
### v0.2.9 (2026-06-19)
- 🛠️ fix:hy2 parser 缺少 urldecode
- 🛠️ fix:migrate顺序不对导致全新启动失败
- 🛠️ fix:tg通知没有同步系统时区
### v0.2.8 (2026-06-18)
- Update README.md
- 🌈tunnel管理支持端口转发
- 🌈优化出站管理的显示
- 🌈服务器流量支持系统维度
- 🌈添加服务器支持通过dns添加域名与动态DNS
- 🛠️ fix:docker切换到nginx官方镜像
- 🛠️ fix:hy2协议订阅节点缺少sni参数
- 🛠️ fix:hy2等协议使用udping测试
- 🛠️ fix:ipv6机器更新agent失败
- 🛠️ fix:socks5节点入站绑定节点失败
- 🛠️ fix:telegram规则命名冲突
- 🛠️ fix:vless reality出站缺少sniffing.exclude配置
- 🛠️ fix:今日和本周改为0点开始统计
- 🛠️ fix:前端版本号不同步
- 🛠️ fix:多余文件删除
- 🛠️ fix:恢复新机器后旧机器重连报错
- 🛠️ fix:服务器上报IP优先级>服务器地址
- 🛠️ fix:用户网速计算周期不准确
- 🛠️ fix:移除配置模板中的冗余配置
- 🛠️ fix:系统的流量统计规则影响到节点流量显示
- 🛠️ fix:配置漂移时显示配置对比
### v0.2.7 (2026-06-14)
- 迁移前端代码到单独的仓库
- 🌈docker镜像内置nginx(beta)
- 🌈增加cloudflare人机验证测试
- 🌈增加家宽常用和测速分流路由规则
- 🛠️ fix:docker模式必须host网络启动
- 🛠️ fix:nginx没有添加开机启动
- 🛠️ fix:pull模式切换失败
- 🛠️ fix:pull模式添加心跳包
- 🛠️ fix:vless wss节点无法使用
- 🛠️ fix:前端版本号不同步
- 🛠️ fix:只有v6的机器没有显示IP
- 🛠️ fix:同步前端节点缓存
- 🛠️ fix:多管理员的情况下流量统计用户错误
- 🛠️ fix:模板管理与生成订阅失败
- 🛠️ fix:测速端镜像地址错误
- 🛠️ fix:用户网速显示错误
- 🛠️ fix:调整clash-to-loon remote-rule位置
- 🛠️ fix:迁移前端代码到单独的仓库
- 🛠️ fix:部分类型节点联动删除失败
- 🛠️ fix:部分通知发送失败没有重试
### v0.2.7 (2026-06-14)
- 迁移前端代码到单独的仓库
- 🌈docker镜像内置nginx(beta)
- 🌈增加cloudflare人机验证测试
- 🌈增加家宽常用和测速分流路由规则
- 🛠️ fix:docker模式必须host网络启动
- 🛠️ fix:nginx没有添加开机启动
- 🛠️ fix:pull模式切换失败
- 🛠️ fix:pull模式添加心跳包
- 🛠️ fix:vless wss节点无法使用
- 🛠️ fix:前端版本号不同步
- 🛠️ fix:只有v6的机器没有显示IP
- 🛠️ fix:同步前端节点缓存
- 🛠️ fix:多管理员的情况下流量统计用户错误
- 🛠️ fix:模板管理与生成订阅失败
- 🛠️ fix:测速端镜像地址错误
- 🛠️ fix:用户网速显示错误
- 🛠️ fix:调整clash-to-loon remote-rule位置
- 🛠️ fix:迁移前端代码到单独的仓库
- 🛠️ fix:部分类型节点联动删除失败
- 🛠️ fix:部分通知发送失败没有重试
### v0.2.7 (2026-06-14)
- 迁移前端代码到单独的仓库
- 🌈docker镜像内置nginx(beta)
- 🌈增加cloudflare人机验证测试
- 🌈增加家宽常用和测速分流路由规则
- 🛠️ fix:docker模式必须host网络启动
- 🛠️ fix:nginx没有添加开机启动
- 🛠️ fix:pull模式切换失败
- 🛠️ fix:pull模式添加心跳包
- 🛠️ fix:vless wss节点无法使用
- 🛠️ fix:前端版本号不同步
- 🛠️ fix:只有v6的机器没有显示IP
- 🛠️ fix:同步前端节点缓存
- 🛠️ fix:多管理员的情况下流量统计用户错误
- 🛠️ fix:模板管理与生成订阅失败
- 🛠️ fix:测速端镜像地址错误
- 🛠️ fix:用户网速显示错误
- 🛠️ fix:调整clash-to-loon remote-rule位置
- 🛠️ fix:迁移前端代码到单独的仓库
- 🛠️ fix:部分类型节点联动删除失败
- 🛠️ fix:部分通知发送失败没有重试
### v0.2.6 (2026-06-14)
- 🛠️ fix:迁移前端代码到单独的仓库
### v0.2.5 (2026-06-14)
- 迁移前端代码到单独的仓库
- 🌈docker镜像内置nginx(beta)
- 🌈增加cloudflare人机验证测试
- 🌈增加家宽常用和测速分流路由规则
- 🛠️ fix:docker模式必须host网络启动
- 🛠️ fix:nginx没有添加开机启动
- 🛠️ fix:pull模式切换失败
- 🛠️ fix:pull模式添加心跳包
- 🛠️ fix:vless wss节点无法使用
- 🛠️ fix:只有v6的机器没有显示IP
- 🛠️ fix:同步前端节点缓存
- 🛠️ fix:多管理员的情况下流量统计用户错误
- 🛠️ fix:模板管理与生成订阅失败
- 🛠️ fix:测速端镜像地址错误
- 🛠️ fix:用户网速显示错误
- 🛠️ fix:调整clash-to-loon remote-rule位置
- 🛠️ fix:迁移前端代码到单独的仓库
- 🛠️ fix:部分类型节点联动删除失败
- 🛠️ fix:部分通知发送失败没有重试
### v0.2.4 (2026-06-11)
- 🌈xray设置移动端页面适配
- 🌈测速端支持docker安装
- 🌈迁移订阅序列化格式切换设置
- 🌈迁移订阅覆写自选规则
- 🛠️ fix:docker无法自动部署证书问题
- 🛠️ fix:不是妙妙屋管理的路由出站改为可编辑
- 🛠️ fix:修复大量BUG
- 🛠️ fix:前后端交互偶发解密失败
- 🛠️ fix:套餐订阅漏发获取订阅通知
- 🛠️ fix:流量信息今日、本周、本月显示错误
- 🛠️ fix:节点与服务器识别错误
### v0.2.3 (2026-06-09)
- 🌈支持warp出站配置
- 🌈迁移妙妙屋节点多标签
### v0.2.2 (2026-06-08)
- 🌈前后端交互安全加固
- 🌈套餐管理显示优化
- 🌈手机端适配优化
- 🌈支持按入站+用户限速
- 🛠️ fix:下发TLS证书连接失败
- 🛠️ fix:找回密码脚本错误
- 🛠️ fix:暴力探测接口拉黑失败
- 🛠️ fix:简易模式证书自动配置失败
### v0.2.1 (2026-06-05)
- 🌈支持cloudflare turnstile 验证码
- 🛠️ fix: tgbot用户注册后无法访问节点
- 🛠️ fix: 检查版本号逻辑
### v0.2.0 (2026-06-04)
- 🌈MCP功能补全
- 🌈TG BOT开发测试
- 🌈TGBOT支持邀请码注册绑定套餐
- 🌈优化lxc容器的识别
- 🌈优化xray路由管理与出站管理
- 🌈支持TgBot和MiniApp
- 🌈支持节点单独设置倍率与倍率展示
- 🌈迁移妙妙屋代理组编辑的中转代理组功能
- 🛠️ fix: 修改插件默认组织
- 🛠️ fix: 功能性BUG修复
- 🛠️ fix: 每日流量折线图重启错误统计数据
- 🛠️ fix: 节点流量用户展示错误
- 🛠️ fix:出招allowinsecure改为pinnedPeerCertSha256
- 🛠️ fix:出站allowinsecure改为pinnedPeerCertSha256
- 🛠️ fix:已经是路由出站的节点不能再次添加路由出站
### v0.1.9 (2026-06-02)
- 🌈Xray管理路由规则管理优化
- 🌈支持AnyTLS
- 🌈证书上传功能优化
- 🛠️ fix:ss节点配置错误
- 🛠️ fix:前端UI功能优化
### v0.1.8 (2026-06-01)
- 🌈增加服务在线离线筛选
- 🌈增加自定义安全管控配置
- 🛠️ fix:历史版本使用user@email的流量未统计补丁
- 🛠️ fix:安全规则无法识别docker与反代环境的真实IP
- 🛠️ fix:离线通知IP错误
### v0.1.7 (2026-05-31)
- 🌈增加自定义安全管控配置
- 🛠️ fix:ss2022多用户密码拼接错误
- 🛠️ fix:添加服务器的安装命令丢失参数
- 🛠️ fix:用户流量统计聚合逻辑错误
### v0.1.6 (2026-05-30)
- 🌈优化主控使用CDN情况下的Agent互联
- 🛠️ fix:dialog溢出屏幕问题
- 🛠️ fix:docker开启https后无法访问
- 🛠️ fix:代理集合接口查询404
- 🛠️ fix:偶发Agent上报ipv6
- 🛠️ fix:恢复doh使用IP
- 🛠️ fix:模板dns配置不可用的覆盖补丁
- 🛠️ fix:点击模板管理报错缓存读取失败
- 🛠️ fix:订阅管理流量显示错误
- 🛠️ fix:许可证限制错误的应用在外部节点上
### v0.1.5 (2026-05-29)
- 🛠️ fix:dns配置错误
- 🛠️ fix:模板越权问题
- 🛠️ fix:调整普通用户的查看权限
- 🛠️ fix:路由出站功能BUG
- 🛠️ fix:迁移的用户订阅错误
### v0.1.4 (2026-05-28)
- 🌈代理集合前端代码优化
- 🌈优化xray管理代码结构
- 🌈优化编辑节点代码结构
- 🛠️ fix:Agent安装的主控地址错误
- 🛠️ fix:修复若干BUG
- 🛠️ fix:测速页面UIBUG
### v0.1.3 (2026-05-26)
- 🌈合并妙妙屋更新补丁
- 🌈增加妙妙屋迁移入口
- 🌈增加路由出站功能
- 🌈支持从妙妙屋迁移
- 🌈支持节点真延迟测试
- 🌈支持路由出站（入站复用）
- 🛠️ fix:debian13nginx安装失败
- 🛠️ fix:分享的服务器xray_mode没有解析
- 🛠️ fix:自定义Agent端口无效
- 🛠️ fix:自定义规则BUG修复
### v0.1.2 (2026-05-23)
- 🌈测速结果支持出口IP显示
- 🛠️ fix:订阅管理报错
### v0.1.1 (2026-05-23)
- 🌈增加MCP服务，可以接入openclaw或hermes
- 🛠️ fix:数据库增量更新顺序错误
### v0.1.0 (2026-05-22)
- 🛠️ fix:测速代码丢失
### v0.0.10 (2026-05-22)
- 🌈主控支持mihomo测速
- 🌈优化tunnel的配置流程与管理
- 🌈增加xray负载均衡出站配置
- 🌈增加节点测速
- 🌈支持HY2协议
- 🛠️ fix:Dokcer镜像打包失败
- 🛠️ fix:上报间隔配置不生效
- 🛠️ fix:服务器轮换密钥时短暂假离线
- 🛠️ fix:流量信息页面流量始终为0
- 🛠️ fix:系统配置异常丢失
### v0.0.9 (2026-05-21)
- 🌈 同步妙妙屋订阅管理
- 🌈 增加限速单位换算提示
- 🌈优化与licenseserver交互
- 🌈优化与许可证服务交互
- 🌈增加上报频率设置
- 🌈增加批量升级agent
- 🌈支持分享服务器给其他妙妙屋X
- 🌈支持普通用户访问部分妙妙屋功能
- 🌈支持用户配置自定义短码
- 🌈用户禁用时删除节点里的用户配置
- 🛠️ fix:Agent缺少某些错误提示
- 🛠️ fix:优化服务管理
- 🛠️ fix:流量统计错误
- 🛠️ fix:添加节点现在仅添加当前用户
- 🛠️ fix:缺少节点流量统计
- 🛠️ fix:迁移妙妙屋最新补丁
- 🛠️ fix:限速失败
- 🛠️ fix:首页用户网速显示错误
### v0.0.8 (2026-05-18)
- 🛠️ fix:交换密钥失败导致session断开
- 🛠️ fix:服务器卡片界面显示问题
- 🛠️ fix:用户管理绑定套餐看不见套餐
- 🛠️ fix:节点管理ip域名恢复错误
### v0.0.7 (2026-05-18)
- 🌈 增加与agent交互的错误提示
- 🌈 增加主控与agent交互协议展示
- 🛠️ fix:优化内嵌xray菜单展示
- 🛠️ fix:优化许可证展示
- 🛠️ fix:优化顶部菜单展示
- 🛠️ fix:添加服务器窗口异常撑大
### v0.0.6 (2026-05-18)
- 🛠️ fix:docker镜像打包系统版本不对
- 🛠️ fix:reality节点创建多了出站
### v0.0.5-beta (2026-05-18)
- 🛠️ fix:agent自动上报IPv4优先
### v0.0.5 (2026-05-18)
- 🛠️ fix:主控开启小黄云获取agent IP错误
### v0.0.4 (2026-05-17)
- 🌈 PRO功能展示优化
- 🌈 优化发布脚本
- 🌈 增加妙妙屋菜单
- 🌈 妙妙屋功能增加开关控制
- 🛠️ fix:同步妙妙屋修改
- 🛠️ fix:证书保存目录错误写死了/etc
### v0.0.4-beta (2026-05-17)
- 🛠️ fix:cloudflare证书不再本地验证dns
- 🛠️ fix:自动限速无法恢复
- 🌈 增加自动限速与解除限速
- 🛠️ fix:主控与偷自己逻辑优化
- 🌈 增加主控与agent交互加密
- 🌈 增加证书申请日志显示
- 🛠️ fix:修复大量bug
- 🌈 同步mmw功能
- 🌈 支持内联xray与外置xray切换
### v0.0.3-beta (2026-05-14)
- 🌈 支持套餐限速与用户限速
- 🌈 支持套餐限速与用户限速
- 🌈 同步mmw功能
- 🌈 同步mmw功能
### v0.0.2 (2026-05-13)
- 🛠️ fix:移植外部订阅功能
- 🛠️ fix:topbar 按钮阴影消失
- 🌈 支持i18n
- 🌈 支持扁平主题
- 🌈 优化发布流程
- 🌈 增加2fa
- 🌈 增加通知
- 🌈 允许用户自行添加出站
</details>

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=iluobei/miaomiaowuX&type=date&legend=top-left)](https://www.star-history.com/#iluobei/miaomiaowuX&type=date&legend=top-left)

## 许可证

MIT License

## 联系方式

- 问题反馈：[GitHub Issues](https://github.com/iluobei/miaomiaowuX/issues)
- 功能建议：[GitHub Discussions](https://github.com/iluobei/miaomiaowuX/discussions)
