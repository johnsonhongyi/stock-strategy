# easy-stock 部署验证记录（2026-09-27，Ubuntu 24.04）

## 验证结论：后端可在 Linux 无头部署，前端/Electron 不需要

- Go 1.26 编译一次成功（纯 Go，无 CGO，modernc.org/sqlite）
- 后端二进制：`/tmp/easy-stock-backend`（测试用）；源码在 `~/workspace/easy-stock-analysis/backend`
- 启动：`A_STOCK_ADDR=127.0.0.1:20081 A_STOCK_TOKEN=<token> A_STOCK_LOG_DIR=<dir> ./easy-stock-backend`
- 数据源直连公网免费接口，无需 key：新浪 / 东方财富 / 财联社 / 开盘啦(短线侠) / 腾讯，全部实测 OK

## 关键接口（token 鉴权：`?token=` 或 `X-A-Stock-Token` 头）

- GET /api/health（公开）
- GET /api/v1/sources — 数据源状态
- GET /api/v1/quotes/realtime?symbols=sh600000,sz000001
- GET /api/v1/quotes/kline?symbol=sh600000&period=day&limit=120
- GET /api/v1/quotes/kline/batch?symbols=...&period=day&limit=40（最多30只）
- GET /api/v1/market/margin-balance?limit=120 — 两融余额
- GET /api/v1/market/news?source=cls&limit=20 — 财联社电文
- GET /api/v1/stocks/directory — A股代码名称本地模糊搜索
- GET /api/v1/stocks/hot-ranks — 同花顺+东财热股Top100去重
- GET /api/v1/themes/overview — 趋势题材雷达
- GET /api/v1/sector-map?theme=semiconductor_materials — 产业链地图
- GET /api/v1/short-term/limit-up-ladder — 涨停梯队/连板（上游是开盘啦，5分钟刷新节流）
- POST /api/v1/strategy/inflections/evaluate — 确定性拐点评估引擎（无需LLM）
- GET /api/v1/ws/stream?symbols=...&interval_ms=3000 — WebSocket 行情推送

## 实测数据样本（2026-09-27 周日休市，返回上个交易日 09-24）

- 涨停 52 家，连板 13 家，最高 5 板：新华文轩 601811.SH
- K线：浦发银行 600000.SH 近5日 daily（东财源）
- 资讯：财联社实时电文正常

## 注意事项

1. 官方 release 只有 macOS/Windows 安装包，无 Linux 版；Linux 走源码 `go build` + Web 模式
2. 需要 Hermes/LLM 的能力（个股AI分析、复盘提炼、侧边栏对话）依赖 Python Hermes Runtime + 自备模型 key；无头部署可绕过，由 Muse 直接做推理
3. 大V复盘的雪球/淘股吧抓取依赖 Electron 内置浏览器登录态，无头环境受限；微信公众号需 integrations/wechat-download-api 的 docker 服务
4. 许可：PolyForm Noncommercial 1.0.0 — 个人非商业使用 OK，商业用途需作者书面授权
5. Go 工具链：~/workspace/tools/go/bin/go（1.26.0）
