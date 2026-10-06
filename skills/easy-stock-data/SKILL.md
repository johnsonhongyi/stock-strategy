# easy-stock A股数据底座

本地常驻的 A 股行情数据服务（easy-stock 项目的 Go 后端，纯数据层，不依赖 Electron/Hermes）。
当用户问起 A 股行情、个股、题材、涨停、复盘相关问题时，优先用它拉实时/历史数据再做分析，而不是凭记忆回答。

## 连接方式

- 基地址：`http://127.0.0.1:20081`（仅 loopback）
- 鉴权：每次调用时从 `~/workspace/easy-stock-service/.env` 读取 `A_STOCK_TOKEN`，
  以 `?token=` 查询参数或 `X-A-Stock-Token` 请求头携带。**不要把 token 写进记忆、文件或回复里。**
- 健康检查：`GET /api/health`（无需鉴权）
- 服务目录：`~/workspace/easy-stock-service/`（`bin/` 二进制，`data/` SQLite 库，`logs/` 日志，`start.sh` 幂等启动脚本）

## 常用接口

| 用途 | 请求 |
| --- | --- |
| 数据源状态 | `GET /api/v1/sources` |
| 实时行情 | `GET /api/v1/quotes/realtime?symbols=sh600000,sz000001`（代码格式 `sh600000`/`sz000001`） |
| K 线 | `GET /api/v1/quotes/kline?symbol=sh600000&period=day&limit=120`（period: day/week/month/min30 等） |
| 批量 K 线 | `GET /api/v1/quotes/kline/batch?symbols=sh600000,sz000001&period=day&limit=40`（最多 30 只） |
| 财联社电文 | `GET /api/v1/market/news?source=cls&limit=20` |
| 两融余额 | `GET /api/v1/market/margin-balance?limit=120` |
| 热股榜 | `GET /api/v1/stocks/hot-ranks`（返回 `{"data":{"stocks":[...]}}`） |
| 代码/名称搜索 | `GET /api/v1/stocks/directory`（本地缓存全 A 股代码表） |
| 趋势题材雷达 | `GET /api/v1/themes/overview` |
| 产业链地图 | `GET /api/v1/sector-map?theme=semiconductor_materials` |
| 涨停梯队/连板 | `GET /api/v1/short-term/limit-up-ladder`（含 `current.trade_date/limit_up_count/board_count/max_streak/levels`） |
| 拐点评估（确定性引擎，无需 LLM） | `POST /api/v1/strategy/inflections/evaluate` |
| 行情推送 | `GET /api/v1/ws/stream?symbols=sh600000&interval_ms=3000`（WebSocket） |

## 注意事项

- 非交易日（周末/节假日）返回的是**上个交易日**的快照，注意看 `meta.fetched_at` 和 `trade_date`，不要当成当日实时数据。
- 涨停梯队上游是开盘啦，有 5 分钟刷新节流，频繁请求会被限。
- `tushare` 数据源需要 token，未配置时 `/api/v1/sources` 显示 FAIL，属正常。
- 需要 LLM 的能力（个股 AI 深度分析、大 V 复盘提炼）不走这个服务，由我直接推理；服务只负责取数。
- 研究方法论参考：`~/workspace/easy-stock-analysis/.agents/skills/`（个股/总览/复盘三套框架）、`backend/docs/`、游资心法 42 篇在 `backend/internal/methodology/builtin/documents/`。
- 运维笔记：`~/workspace/easy-stock-analysis/DEPLOY_NOTES.md`
- 许可：PolyForm Noncommercial 1.0.0，个人非商业使用。
