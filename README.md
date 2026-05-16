# 个股智能分析系统 — AI Agent + RAG 工程实践

![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat&logo=go)
![Eino](https://img.shields.io/badge/Eino-0.7.x-3C3C3C?style=flat)
![License](https://img.shields.io/badge/License-MIT-green.svg)

基于大模型与混合检索技术的个股智能分析平台，实现自然语言 → 结构化意图 → 多源数据（Python 分析服务）→ AI 报告的端到端链路。

## ✨ 核心功能

### 自然语言意图理解

支持「茅台近三年营收」「对比德方纳米与信维通信业绩」等查询，经 **知识库倒排槽位**（`data/knowledge.json`）与可选 **Function Calling** 解析为 `task_kind`、`symbols`、`skill_hints` 等结构化槽位；高置信时可 **跳过 FC**（`combo` + `ShouldSkipFC`）。

### 多轮对话与上下文指代

- 用户：「茅台怎么样？」→ 解析标的 `600519` 并分析  
- 用户：「它的基本面呢？」→ 在无新股票名时沿用 **上一轮标的**

实现方式（无需前端传整段 Memory）：

- 请求体可选 **`symbol`**（六位代码）与 **`session_id`**（同一会话固定 ID）  
- 后端 **`tools` 包内会话缓存**（`session_id` → 最近标的，内存 TTL 24h）  
- 演示页 `static/index.html` 用 `sessionStorage` 记住 `session_id` 与 `last_symbol`，输入框为空时自动带上轮代码  

记忆正文仍由服务端按标的从 `memory/stock/<symbol>/` 读取，不必由前端上传。

### 智能新闻检索（RAG）

- 混合检索：向量召回 + BM25（Redis Stack `FT.SEARCH`）  
- RRF 融合 + 规则重排（标题、时效、来源等）  
- 定时任务可按 `cron_stocks` 订阅列表同步新闻入 Redis  

### AI 报告生成

- **Eino ADK** `ChatModelAgent` + 工具：`run_technical` / `run_fundamental` / `run_news` 等（经 `STOCK_PYTHON_URL` 调用 Python 服务）  
- 意图命中 `skill_hints` 时可 **预取** 部分维度数据注入上下文；基本面长报告避免与对话内重复展示（预取与用户消息拆分策略见 `tools/skill_hints_run.go`）  
- `/chat` 以 **SSE** 流式返回 Markdown 报告  

### 可观测性

- 链路分段耗时：`intent_slot` / `intent_fc` / `prefetch` / `context` / `generate` 等（`tools/pipeline.go`，日志前缀 `[chat]` / `[pipeline]`）  
- Token 用量：意图 FC 与主对话流式回复合并统计，可选 SSE `metrics` 事件  
- 离线评测：意图集、检索 Hit@K、Prompt 评测（见下文）

## 🏗️ 技术架构

```
用户输入
  → queryaug（词典槽位 + 规则 / 可选 easyrules）
  → NL 改写（combo.NLQueryRewrite）
  → 意图 FC（可选跳过）
  → skill_hints 预取 + Python 工具
  → Eino Agent 生成（SSE）
```

| 组件 | 技术选型 |
|------|----------|
| 后端 | Go + [CloudWeGo Eino](https://github.com/cloudwego/eino)（ADK Runner） |
| 大模型 | OpenAI 兼容 API（`config/stock.json` → `chatOpenAI`） |
| 意图 | 内存倒排 + `submit_parsed_intent` FC；可选 `config/intent_rules.json` |
| 检索 | Redis Stack + Embedding（`rag` 包） |
| 分析数据 | Python HTTP 服务（`stockPython.baseURL`） |
| 会话标的 | 内存 `session_id` 缓存 + 请求 `symbol` |

## 📦 快速开始

### 前置要求

- Go 1.25+  
- Redis Stack（启用 RAG / 新闻检索时；可用 `redis/redis-stack` 镜像）  
- 大模型 API Key（OpenAI 兼容：DeepSeek / 通义 / Volc 等）  
- 可选：Python 分析服务（行情、基本面、技术面等接口）

### 克隆与配置

```bash
git clone https://github.com/zoe0806/stock-see.git
cd stock-see
cp config/stock.example.json config/stock.json   # 若尚无 stock.json
# 编辑 config/stock.json：chatOpenAI、rag.redisAddr、stockPython.baseURL 等
# 也可通过环境变量 STOCK_CONFIG 指定配置文件路径
```

### 启动服务

```bash
go mod tidy
go run .
```

默认监听 **http://localhost:8080**。

| 路径 | 说明 |
|------|------|
| `/` | 对话演示页（`static/index.html`） |
| `POST /chat` | SSE 流式对话（`message`、`symbol`、`session_id` 等） |
| `/rag` | RAG 检索演示 |
| `/api/rag/search`、`/api/rag/sync` | 新闻检索与同步 |
| `/api/breakout_score` | 突破评分接口 |

### 交互示例（API）

```json
POST /chat
{
  "message": "它的基本面呢",
  "session_id": "550e8400-e29b-41d4-a716-446655440000",
  "symbol": "600519"
}
```

`symbol` 可省略：若带 `session_id` 且上一轮已解析出标的，后端会自动沿用。

## 🎯 使用示例

### 1. 单实体查询

输入：贵州茅台最新财报净利润  

解析结果示例（概念上）：`task_kind: fundamental`，`symbols: ["600519"]`，`skill_hints` 含 `fundamental`。

### 2. 多标的对比

输入：对比德方纳米与信维通信业绩  

`task_kind: compare`，`symbols` 为两只股票代码；预取可拉多标的维度数据。

### 3. 多轮指代

```
User: 宁德时代
Assistant: …（解析 300750，并写入 session 缓存）

User: 它的技术面怎么样？
Assistant: …（无新代码时沿用 300750 + NL 改写补全标的）
```

## 🧪 评测与优化

评测集默认路径可在 `config/stock.json` 的 `eval` 段配置。

```bash
# 意图评测（fc / combo / pipeline）
go run . -eval-intent -eval-intent-mode=pipeline -eval-intent-verbose
go run . -eval-intent -eval-intent-suite=data/eval/intent_suite.json -eval-intent-json=out/intent.json

# 检索评测（需 Redis + Embedding，无需对话模型）
go run . -eval-retrieval -eval-retrieval-verbose

# Prompt / 输出格式评测（需配置 chatOpenAI）
go run . -eval -eval-suite=data/eval/suite.json
```

## 📂 项目结构（概要）

```
.
├── main.go                 # HTTP 服务、/chat SSE、评测入口
├── intent/                 # FC 意图、校验、评测；combo 槽位/规则/NL 改写；queryaug
├── rag/                    # Redis 新闻 RAG、混合检索、检索评测
├── tools/                  # Python 客户端、分析工具、skill 预取、配置、会话标的缓存
├── prompt/                 # 系统提示与 {Context} 组装
├── eval/                   # Prompt 离线评测
├── evalintent/             # 意图评测预测器（fc / combo / pipeline）
├── memory/                 # 按标的落盘记忆（若已接入）
├── config/                 # stock.json、intent_rules.json、prompt 模板
├── data/                   # knowledge.json、intent_fewshot.json、eval 用例
├── static/                 # index.html、rag.html 等演示页
└── skills/                 # 可选 SKILL.md 技能文档
```

更细的 Agent 行为见仓库根目录 `AGENTS.md`。

## 📄 许可证

MIT
