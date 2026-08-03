# XR-Trading 工程指引（CLAUDE.md）

> 本文件是仓库的**自动加载上下文入口**。任何 Agent 或贡献者开始工作前必读。
> 详细规范位于 `.claude/standards/`，通过下方 `@import` 引入。
> 最近更新：2026-08-04

## 1. 这是什么项目

XR-Trading 是**面向个人的量化投资研究平台**，覆盖美股/ETF 与加密现货，行情来源为长桥（Longport）与 Bybit。
目标是打通「数据采集 → 分析 → 策略 → 组合构建 → 风控 → 执行 → 估值 → 复盘」的完整闭环。

产品对外统一表述为「XR-Trading 量化投资研究平台」，不使用基金/代客理财等经营性表述。
需求真相来源：`doc/ai_quant_trading_system_requirements.md`。

## 2. 仓库结构速览

| 路径 | 内容 | 技术栈 | 成熟度 |
| --- | --- | --- | --- |
| `backend/` | 核心业务后端（用户、组合、成员、持仓） | Python 标准库 `http.server` + SQLite | 原型，待架构升级 |
| `frontend/` | 管理与查询前端 | 原生 HTML/CSS/JS | 基础可用 |
| `market-info-service/` | 行情采集/存储/查询服务 | Go + PostgreSQL | 接近发布候选（M4→M5） |
| `doc/` | 需求与技术设计（真相来源） | Markdown | 完善 |
| `.claude/` | 工程规范与工作流（本规范集） | Markdown | 本次建立 |

## 3. 必须保持的项目优点（写规范的初衷）

这些是项目现有的核心优势，任何改动都不得削弱：

1. **文档驱动（spec-first）**：先在 `doc/` 写清设计，再写代码。设计文档与实现同步演进，二者不一致视为缺陷。
2. **领域边界清晰**：入口/基础设施依赖应用与领域接口，领域层不依赖 HTTP、DB 驱动或 Provider SDK。
3. **测试与可观测性优先**：Go 服务测试代码量 > 业务代码量，statement coverage ≥ 80%，race 检测、隔离集成测试、结构化日志与指标齐备。
4. **风险与合规内建**：`research/backtest/paper/live` 四环境严格隔离，`live` 必须显式双重确认并使用独立凭据；密钥不入库；fail-closed。
5. **统一领域建模**：内部 `asset_id`/UUIDv7 作为身份，可读 `code` 仅作展示；多来源行情不互相覆盖；金额用定点十进制。
6. **纪律化协作**：文档驱动 + 多 Agent 分波协作，接口/迁移/装配由整合负责人统一冻结。

## 4. 黄金规则（任何 Agent 都必须遵守）

- **先读设计，再动代码**：改任何模块前先读 `doc/` 对应文档；设计缺失就先补设计再实现。
- **绝不执行真实交易或资金操作**：Agent 只做研究、数据、回测、模拟；`live` 下单、转账、撤资一律交由用户本人。
- **绝不把密钥/真实账户数据写入仓库**：包括代码、文档、测试固定数据、日志、截图。参见安全规范。
- **改动必须带测试**：新增/修改的非平凡函数需覆盖正常、边界与主要失败路径；修缺陷先写复现回归测试。
- **提交前过质量门禁**：见各语言规范的「提交前门禁」章节 + `make security-check`。
- **保持核心不弱于外围**：不要过度打磨已完成的外围（如行情服务），优先补齐决定价值的核心链路（策略/风控/回测/执行）。

## 5. 规范索引

@.claude/standards/project-conventions.md
@.claude/standards/development-workflow.md
@.claude/standards/python-backend-standards.md
@.claude/standards/security-standards.md

Go 服务开发规范以 `doc/technical/market_information_service/09_go_development_standards.md` 为准（不在此重复）。
仓库级安全开发规范以 `doc/technical/12_security_development_standards.md` 为准，`.claude/standards/security-standards.md` 是其操作性补充与索引。

## 6. 开发建议与任务链

按优先级排序的下一步开发任务链见：`doc/technical/roadmap/README.md`。
先打通最小端到端纵切，再深化单模块——不要继续加深已「够用」的行情服务。
