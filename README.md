# Stock Strategy：easy-stock 多市场策略研究扩展

本仓库以 [jundizhou/easy-stock](https://github.com/jundizhou/easy-stock) 为基础，保留其行情分析工作台，并补充 Python 策略模拟、多市场日线持久化、交易日判断和低资源 Docker 部署集成。它是个人部署与策略研究项目，不是上游 easy-stock 的官方发行版。

上游项目的完整产品介绍见[原项目 README](https://github.com/jundizhou/easy-stock/blob/main/README.md)。本 README 重点记录本仓库的功能边界和新增集成。

## 项目功能

- **行情与个股研究工作台**：沿用 easy-stock 的行情总览、趋势题材、短线连板、个股分析、持仓巡检、复盘记录和 AI 研究等页面能力。上游功能及界面说明以[原项目 README](https://github.com/jundizhou/easy-stock/blob/main/README.md)为准。
- **跨市场策略模拟**：策略层覆盖 A 股、美股和数字货币；各市场模拟账本隔离，参数和信号按 `strategy.yaml` 执行，不向券商或交易所提交真实订单。
- **策略研究与复盘**：策略版本、规则、阈值和变更记录集中在 [`easy-stock-service/strategy.yaml`](easy-stock-service/strategy.yaml)。当前配置为 v1.9，包含统一决策因子记录、影子评估、风险熔断、币圈动态标的池，以及只读的 P30 周期结构分析。
- **持久化行情管理**：页面支持按 A 股、美股、币圈筛选本地行情标的，加入自动更新名单、查看日线覆盖和最新日期，并直达个股分析或策略研判。跟踪名单与已缓存行情分开管理：移出名单保留历史，清理日线是独立操作。

## 相对上游的主要变动

### 1. Go 行情服务与 Python 策略共用本地行情底座

生产环境统一使用 `/mnt/4TB/dockerf/stockstrategy/data/easy-stock/stock-data.db`。Go 后端和 Python 行情脚本通过同一数据库读写 `daily_bars`；部署时由 `A_STOCK_DATA_DB` 和 `EASY_STOCK_DATA_DB` 指向该文件，避免策略层与页面各自维护一份日线库。

行情请求按本地优先处理：先读已持久化的日线；本地缺数据或落后于该市场最近一个已收盘交易日时，才尝试上游更新。自动刷新对同一标的和交易日设有重试间隔；上游失败且本地已有数据时返回带陈旧状态的缓存，避免网络故障直接抹掉可用历史。需要立即尝试刷新时，可在请求中设置 `X-Stock-Cache-Refresh: 1`。

周 K 和月 K 由本地日线聚合，不再依赖上游必须支持周/月周期接口。可重放的只读 HTTP 请求也可进入 SQLite 响应缓存；缓存有效时直接复用，过期后再请求上游，并在允许的回退期内提供旧响应。

相关实现：[`market_kline.go`](easy-stock-analysis/backend/internal/httpapi/market_kline.go)、[`marketbars`](easy-stock-analysis/backend/internal/marketbars)、[`marketcache`](easy-stock-analysis/backend/internal/marketcache)、[`data_store.py`](easy-stock-service/data_store.py)。

### 2. 交易日历与盘后日线回补

Go 后端与 Python 脚本使用同一份交易日历，部署文件位于 `/mnt/4TB/dockerf/stockstrategy/config/trading-calendar.json`。A 股休市日可本地维护并同步；美股常规整日休市按交易所规则计算，临时休市或特殊开市可通过日历覆盖。

宿主机定时任务在收盘后增量补齐日线：A 股工作日 15:40（`Asia/Shanghai`），美股 06:15（`Asia/Hong_Kong`）。任务调用 `bars.py --append` 或 `bars_us.py --append`，由交易日历判断目标交易日；失败会记入服务日志，不把未取得的数据误报为已补齐。周/月 K 后续从更新后的日线生成。

日历与更新入口：[`trading_calendar.py`](easy-stock-service/trading_calendar.py)、[`bars.py`](easy-stock-service/bars.py)、[`bars_us.py`](easy-stock-service/bars_us.py)、[`bars_crypto.py`](easy-stock-service/bars_crypto.py)。

### 3. 跨市场标的和本地数据管理

持久化行情页面将“自动更新名单”和“已保存行情”分开显示。名单可汇总原有自选、持仓、复盘候选，并支持手动添加；页面可按市场筛选、查看本地日线、跳转分析或策略研判。移出自动更新名单不会删除历史数据；清理后仍在名单中的标的会在后续盘后任务重新回补。

页面入口实现见 [`StockDataWorkspace.tsx`](easy-stock-web/src/components/StockDataWorkspace.tsx)，后端接口见 [`stock_directory.go`](easy-stock-analysis/backend/internal/httpapi/stock_directory.go) 和 [`market_data.go`](easy-stock-analysis/backend/internal/httpapi/market_data.go)。

### 4. 低资源 Docker 部署与外置持久化

当前部署将 Web 和 Go API 作为两个 Docker 服务运行，容器使用 `restart: always` 和健康检查；策略盘后回补由宿主机定时任务触发，不额外常驻一个预取容器。当前资源上限为 API 512 MiB / 1 CPU、Web 128 MiB / 0.5 CPU，容器日志采用轮转。

服务器项目目录为 `/root/johnson/stock-strategy`；持久化内容统一放在 `/mnt/4TB/dockerf/stockstrategy/`，主要包括：

| 路径 | 内容 |
| --- | --- |
| `data/easy-stock/stock-data.db` | Go 与 Python 共用的行情数据库 |
| `data/service/` | 策略辅助数据、复盘材料和任务锁 |
| `config/` | 交易日历与服务配置 |
| `logs/backend/`、`logs/web/`、`logs/service/` | 后端、前端日志，以及策略账本和任务日志 |
| `app/web-dist/` | 按发布版本保存的前端构建文件 |

本仓库聚焦应用与策略源码；生产部署使用的 Docker 安装和主机定时任务配置由部署环境维护。

## 代码导航

| 目录 | 说明 |
| --- | --- |
| [`easy-stock-analysis/backend`](easy-stock-analysis/backend) | Go API、行情服务、SQLite 持久化与上游 easy-stock 后端源码 |
| [`easy-stock-analysis/frontend`](easy-stock-analysis/frontend) | 上游 easy-stock 前端源码 |
| [`easy-stock-web`](easy-stock-web) | 当前集成部署使用的 React / TypeScript Web 工作台 |
| [`easy-stock-service`](easy-stock-service) | Python 行情回补、交易日历、策略模拟和复盘任务 |
| [`skills/easy-stock-data`](skills/easy-stock-data) | 行情数据接口与调用约定 |
| [`docs`](docs) | 系统、部署和恢复资料；其中版本说明是早期发布记录，当前策略参数以 `easy-stock-service/strategy.yaml` 为准 |

## 来源、许可与风险

上游 easy-stock 的版权声明和许可条款保留在 [`easy-stock-analysis/LICENSE`](easy-stock-analysis/LICENSE)；该许可证不授予 easy-stock 的商业使用权。本 README 不替代上游许可，也不改变第三方依赖、数据源和随包材料各自适用的条款。

本项目用于学习、研究和模拟验证，不构成投资建议或收益承诺。行情可能延迟或缺失，策略输出不保证盈利；请核对数据来源并独立承担投资决策。
