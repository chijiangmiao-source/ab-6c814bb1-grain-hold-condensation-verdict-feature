# 散粮船舱口通风露点判定（Vue 3 + Go Gin）

散粮船抵港前遇到昼夜温差，大副需要决定是否开启舱口通风。**只看相对湿度会误判**：
相对湿度只表示空气接近饱和的程度，是否会在冷粮表面结露，取决于粮温与舱内空气
**露点温度**之差。本应用按 Magnus 公式计算露点，并以未舍入的温差给出通风结论。

前端（Vue 3 + Vite）只负责录入与展示，**不在浏览器里复算**；所有 γ、Td、Δ 与结论
均由 Go（Gin）API 计算并返回，SQLite 同时保存输入、未舍入中间量和结论，因此
“页面一套、API 一套”不可能发生，合法提交刷新后结论仍然一致。

同一航次同一舱位**连续测量**时，成功的提交会在**同一数据库事务**内锁定该舱按创建
顺序的最近一条有效记录作为前序（首测无前序）；详情接口在此之上返回两者的未舍入
变化量（粮温、气温、湿度、露点、温差），页面用首次测量提示或对照卡展示，浏览器
同样不参与减法复算。靠港前也可以在录入页切换到**批量模式**，把最多二十行测量
一次提交：整批在同一事务内按测量先后落库，批内同航次同舱的后续行关联本批较早
记录，不同舱位各自承接数据库中的最近前序，任一行非法则整批回滚。

---

## 一、复算口径（务必以此为准）

### 1. 输入与量纲

| 字段 | 含义 | 单位 | 合法范围 |
| --- | --- | --- | --- |
| `voyage` | 航次代号 | 文本 | 非空（自动去除首尾空白） |
| `hatch` | 舱号 | 文本 | 非空 |
| `Tg` | 粮温 | ℃ | −20.0 ≤ Tg ≤ 60.0 |
| `Ta` | 舱内气温 | ℃ | −20.0 ≤ Ta ≤ 60.0 |
| `RH` | 相对湿度 | % | 1.0 ≤ RH ≤ 100.0 |

任一数值**非有限**（`NaN`、`±Infinity`、`1e999` 这类溢出字面量）或**越界**，
整次请求返回 **HTTP 422**，逐字段给出错误，**不生成任何记录**。

### 2. 公式（顺序固定，不跳步舍入）

```
γ  = ln(RH/100) + 17.62 × Ta / (243.12 + Ta)
Td = 243.12 × γ / (17.62 − γ)
Δ  = Tg − Td
```

- γ、Td、Δ 全程使用 **float64 未舍入值**参与下一步运算和最终判定；
- 仅在“展示”时按**四舍五入（round half away from zero）保留两位小数**，
  即 Go 的 `math.Round(v*100)/100`；
- SQLite 中保存的是未舍入的 `gamma / td / delta`（REAL），展示值在读取时再派生。

### 3. 判定区间（闭区间，含两个端点）

以**未舍入 Δ** 为准：

| 条件 | 结论 | 含义 |
| --- | --- | --- |
| Δ > 2.00 | `allowed` 允许通风 | 粮温显著高于露点 |
| Δ < −2.00 | `denied` 禁止通风 | 湿空气会在冷粮表面结露 |
| **−2.00 ≤ Δ ≤ 2.00（含 −2.00 与 2.00）** | `retest` 暂停并复测 | 临界区间 |

关键：**结论绝不能用页面上的两位小数反推**。例如未舍入 Δ = 2.004 时展示为
`2.00`，但仍判“允许”；未舍入 Δ = 1.998 时展示也是 `2.00`，却判“复测”。
所以前端只渲染 API 返回的 `verdict`。两个数学端点 Δ = ±2.00 都只能落入
“复测”，这一点在 Go 单元测试、HTTP 集成测试、Playwright E2E 和验收探针中
均有专门用例（含 ULP 级边界与二分定位）。

### 4. 校验示例

| 输入（Ta=20, RH=70，Td≈14.3592） | 未舍入 Δ | 展示 Δ | 结论 |
| --- | --- | --- | --- |
| Tg = 16.36 | 2.0008 | 2.00 | allowed |
| Tg = 16.357 | 1.9978 | 2.00 | **retest** |
| Tg = 12.36 | −1.9992 | −2.00 | **retest** |
| Tg = 12.357 | −2.0022 | −2.00 | denied |
| Tg = 25 | 10.6408 | 10.64 | allowed |
| Tg = 5（Ta=28, RH=95） | — | — | denied |

---

## 二、架构

```
浏览器 (Vue 3 SPA)
   │  同源 /api/*
   ▼
nginx (web 容器，托管 dist 并反代 /api)
   │
   ▼
Go Gin (api 容器)  ── decision.Evaluate()  ← 唯一的公式实现
   │
   ▼
SQLite 文件 (modernc.org/sqlite，纯 Go，静态编译；命名卷 api-data)
```

- 后端：Go 1.23、Gin、modernc.org/sqlite（无 CGO）、testify；
- 前端：Vue 3 + Vue Router + Vite，Vitest + @vue/test-utils，Playwright；
- 计算实现在 `api/internal/decision/decision.go`，HTTP 层与测试都调用它。

## 三、目录结构

```
api/
  cmd/server/            程序入口（API_PORT / DB_PATH 可配）
  internal/decision/     Magnus 公式、范围校验、区间判定（唯一计算口径）
  internal/store/        SQLite 建表/增量迁移、增查（未舍入中间量 + 同舱前序 prev_id）
  internal/httpapi/      Gin 路由、422 字段错误、公式代入明细
web/
  src/lib/api.js         仅做 fetch，不含任何公式
  src/pages/             录入页 HomePage、详情页 DetailPage、舱位概览页 OverviewPage
  src/components/        VerdictBadge 等展示组件
  test/unit/             Vitest 单元/组件测试
  test/e2e/              Playwright 端到端（真实 Gin + SQLite）
verify/
  Dockerfile             一次性验收镜像（Playwright 镜像 + Go 工具链）
  entrypoint.sh          go test → 独立复算探针 → Playwright
  acceptance.mjs         用独立复写的 Magnus 公式核对真实服务，假接口过不了
scripts/e2e-serve.sh     本地一键起真实栈供 Playwright 使用
docker-compose.yml       api / web / verify 三个服务
```

## 四、用 Docker Compose 运行

```bash
docker compose up --build
# 浏览器打开 http://localhost:8081 （WEB_PORT 默认 8081，API 宿主端口默认 8080）
```

覆盖宿主端口（容器内端口不变）：

```bash
WEB_PORT=9000 API_PORT=9001 docker compose up --build
# web -> http://localhost:9000   api -> http://localhost:9001
```

也可复制 `.env.example` 为 `.env` 后直接 `docker compose up`。

### 一次性验收服务 `verify`

`verify` 是一次性服务（`restart: "no"`，带 compose profile），它会等待
api/web 健康后依次执行：

1. `go test ./...`（testify 单元 + HTTP/SQLite 集成测试）；
2. `verify/acceptance.mjs`：**独立复写** Magnus 公式核对真实 API 的数值与结论，
   校验刷新一致性、两个临界端点、422 不落库、舱位概览按 `MAX(id)` 取每舱最新项，
   以及批量抄录的批内前序关联、交错/重复舱位概览与中间行越界整批回滚；
3. Playwright/Chromium 经 nginx → Gin → SQLite 跑浏览器端到端。

```bash
docker compose build
docker compose run --rm verify
```

全部通过会打印 `VERIFY OK ...`，容器随即退出，不影响常驻的 api/web 服务。

## 五、本地开发（不用 Docker）

前置：Go 1.23+、Node 20+。

```bash
# 终端 1：API（:8080，SQLite 文件可指定）
cd api
DB_PATH=./ventilation.db API_PORT=8080 go run ./cmd/server

# 终端 2：前端（:5173，/api 自动代理到 8080）
cd web
npm install
npm run dev
```

### 运行测试

```bash
# Go：testify
(cd api && go test ./... -count=1)

# 前端：Vitest 单元/组件测试
(cd web && npm test)

# 端到端：脚本自动编译并启动真实 Gin（临时 SQLite）+ vite preview，
# 再由 Playwright 驱动 Chromium
(cd web && npm run test:e2e)
```

## 六、HTTP API

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/healthz` | 健康检查 |
| POST | `/api/assessments` | 提交一次评估；成功 201，请求体格式错误 400，字段非法 422 |
| POST | `/api/assessments/batch` | 靠港前批量抄录：有序测量数组（1–20 行），整批校验、单事务落库；任一行非法返回带行号的 422 且整批不落库 |
| GET | `/api/assessments` | 列表（最新在前） |
| GET | `/api/assessments/:id` | 详情，含公式逐行代入字符串；复测记录另含可选前序对照 `comparison` |
| GET | `/api/voyages/:voyage/hatches/latest` | 舱位概览（只读）：该航次每个舱号各一条**最新**快照，按舱号升序 |

成功响应（节选）：

```json
{
  "id": 1, "voyage": "V-2026-09", "hatch": "3H",
  "tg": 25, "ta": 20, "rh": 70,
  "gamma": 0.9826379171132591, "gamma_display": 0.98,
  "td": 14.359183217771522,    "td_display": 14.36,
  "delta": 10.640816782228478, "delta_display": 10.64,
  "verdict": "allowed",
  "formula": {
    "gamma_line": "γ = ln(RH/100) + 17.62 × Ta / (243.12 + Ta) = ln(70/100) + 17.62 × 20 / (243.12 + 20) = 0.982638（展示值 0.98）",
    "td_line": "Td = 243.12 × γ / (17.62 − γ) = … = 14.359183 ℃（展示值 14.36 ℃）",
    "delta_line": "Δ = Tg − Td = 25 − 14.359183 = 10.640817 ℃（展示值 10.64 ℃）",
    "rule_line": "判定以未舍入 Δ 为准：Δ > 2.00 允许通风；Δ < −2.00 禁止通风；−2.00 ≤ Δ ≤ 2.00（含两端点）暂停并复测。"
  }
}
```

422 响应（字段级错误，整次拒绝、不落库）：

```json
{
  "error": "输入校验失败，未生成任何记录",
  "fields": [
    { "field": "tg", "code": "out_of_range", "message": "粮温 Tg必须在 -20.0 至 60.0 之间" },
    { "field": "rh", "code": "not_finite", "message": "相对湿度 RH必须为有限数值" }
  ]
}
```

400 响应（请求体结构/格式错误，不逐字段报错、不落库）。以下请求与“缺字段”无关，
不会被误报成五个字段全部缺失：

- 顶层不是 JSON 对象：`null`、`[]`、标量；
- 对象闭合后附带任何内容（`{"…":…}]`、`{"…":…} GARBAGE`、第二个 JSON 值等）；
- 同一字段在对象中重复声明（即使两处值相同），避免“同字段两个值”的歧义；
- 空请求体或非法 JSON 文本。

```json
{ "error": "请求体格式错误：字段 \"rh\" 重复声明，请求含义不唯一" }
```

注意 `1e999` 这类**值**的非有限问题走 422（`code: "not_finite"`），不属于
结构/格式错误；结构校验只判定文档形态，不影响逐字段报错能力。

### 批量抄录（靠港前集中录入）

抵港前大副连续抄录多个舱位时，录入页可切换到**批量模式**，按测量先后一次
填写最多二十行（航次、舱号、粮温、气温、湿度）：

```
POST /api/assessments/batch
{ "measurements": [ { …五个字段… }, { …五个字段… } ] }
```

- `measurements` 必须是 **1–20 个 JSON 对象**组成的数组；空数组、非数组、
  缺少该键或超过 20 行都是 **400** 请求体格式错误；包装层只允许
  `measurements` 一个键。每行内部的结构规则与单条请求完全一致（重复键、
  尾随内容、非对象、未知字段均为 400，并在错误信息中标注**行号**）。
- 每行复用与 `POST /api/assessments` **完全相同**的解析、范围校验与露点
  判定（`parseAssessmentRow` + `decision.Evaluate`），字段 `code`/`message`
  一致。**任一行非法整批不落库**，422 的每个错误都带 1 基行号：

```json
{
  "error": "批量输入校验失败，未生成任何记录",
  "fields": [
    { "row": 2, "field": "tg", "code": "out_of_range",
      "message": "粮温 Tg必须在 -20.0 至 60.0 之间" }
  ]
}
```

- 合法批次在**单个数据库事务**内按数组顺序逐行保存，因此各记录编号严格
  遵循测量先后；保存过程中任一行失败也整体回滚，不留部分记录或断裂关联。
- **批内前序规则**：某航次某舱的后续行关联本批中该航次该舱的较早记录；
  该舱在批内首次出现时，仍承接数据库中已提交的最近有效前序；不同航次/舱
  位互不串链。被整批回滚的行不会成为任何记录的前序。
- 成功响应 201：`{"items":[ …, … ]}`，每个元素与单条 POST 的成功体**逐字段
  一致**（含 `formula`，不含 `comparison`），顺序与提交行一致；详情接口
  照旧在需要时附加 `comparison`，历史列表与舱位概览按真实创建顺序
  （`MAX(id)`）立即反映本批记录。

页面在批量模式下逐行显示错误并定位到第一个问题行，保留全部已填内容；成功
后逐行展示评估编号、温差与结论，编号链接到既有详情页。

详情页直接展示 `formula.*_line` 的代入文本与持久化的未舍入值，使审计口径一目了然。

### 前序对照（仅详情接口）

成功提交时，服务端在**同一事务**内按 `voyage + hatch` 找到按创建顺序（id）最近的
前一条**有效**记录，把其编号存入新行的 `prev_id`：

- 该航次该舱位的**首测**没有前序：POST、列表、详情的字段均与旧版一致（不含 `comparison`）；
- 复测记录的**详情响应**在原字段上额外增加可选 `comparison`；POST 响应与列表项永不含该块；
- 前序的选取只看同航次同舱，交错舱位提交不会串舱；422 不落库，也不会成为任何记录的前序。

有可用前序时：

```json
"comparison": {
  "available": true,
  "previous": {
    "id": 7, "voyage": "V-2026-09", "hatch": "3H",
    "tg": 25.345, "ta": 20.123, "rh": 71.5,
    "gamma": 1.051, "td": 15.359183217771522, "delta": 9.985816782228477,
    "gamma_display": 1.05, "td_display": 15.36, "delta_display": 9.99,
    "verdict": "allowed",
    "created_at": "2026-09-12T00:00:00Z"
  },
  "changes": {
    "tg": -1.227999999999998, "ta": 1.8639999999999972, "rh": -3.25,
    "td": 1.058764057682854, "delta": -2.286764057682852
  }
}
```

`changes.*` 全部是 **Go 用未舍入 float64 计算的（本次 − 前序）**：粮温、气温、
湿度、露点、温差五项，浏览器只渲染，不做减法。

若保存的前序记录事后已不存在，或已不属于同一航次同一舱，当前评估照常返回，对照
标记为不可用，**不会临时改绑**到其他记录：

```json
"comparison": { "available": false, "prev_id": 99, "reason": "保存的前序记录已不存在，无法形成对照" }
```

页面据此显示“首次测量”提示、对照卡（含前序摘要链接与五项未舍入变化量）或
“前序对照不可用”的明确告警。

### 舱位概览（按航次，只读）

抵港交接时，大副要在同一航次的多舱记录里快速确认**各舱当前风险**，不必逐条
打开历史详情，也不能把较早的测量误当成当前依据。历史区为每个出现过的航次
提供一个“舱位概览”入口：

```
GET /api/voyages/:voyage/hatches/latest
```

- 服务端按 `voyage` 过滤、按 `hatch` 分组，以**最大记录编号 `MAX(id)`** 确定
  每个舱号唯一的最新项——不看 `created_at` 文本，因此交错提交、甚至同一
  纳秒时间戳（或时钟回拨的旧时间戳）都不会选错记录；判定口径与“最新=创建
  顺序最后一条”一致。
- 结果按**舱号升序**稳定排序（同舱号再以 id 兜底），多次读取顺序一致。
- 仅返回该航次本舱记录；同舱号但属于其他航次的行绝不串入。
- **航次不存在**时是正常的 `200` + 空集合：`{"voyage":…,"items":[]}`，不是错误。
- 航次代号含 `/` 时用百分号编码访问（`V/A` → `/api/voyages/V%2FA/...`），
  路由按原始路径匹配并反转义；**非法路径编码**（如 `%zz`、残缺的 `%`）由
  net/http 在进入处理前直接以 **400** 拒绝，空航次段（`/voyages//...`）返回
  明确的 400 请求错误；错误页保留“返回历史区”入口。

响应是比详情更精简的只读快照（**不含** `formula` 与 `comparison`）：

```json
{
  "voyage": "V-2026-09",
  "items": [
    {
      "id": 12, "voyage": "V-2026-09", "hatch": "2P",
      "tg": 26, "ta": 20, "rh": 70,
      "gamma": 0.9826379171132591, "td": 14.359183217771522,
      "delta": 11.640816782228478,
      "gamma_display": 0.98, "td_display": 14.36, "delta_display": 11.64,
      "verdict": "allowed",
      "created_at": "2026-09-13T08:00:00Z"
    }
  ]
}
```

页面逐行展示舱号、最新评估编号、测量时间、**未舍入 Δ** 与展示 Δ、结论，
每一行可直接跳到该记录的原详情；页面完全渲染接口返回值，不在浏览器里挑选
或重算“最新项”。该接口为只读，POST/列表/详情/前序关联及旧响应结构均不变。

### SQLite 迁移

启动时自动建表并做**增量、无损**迁移：旧版数据库（无 `prev_id` 列）启动后通过
`ALTER TABLE assessments ADD COLUMN prev_id INTEGER` 增加关联字段并补建
`(voyage, hatch, id DESC)` 索引；历史行 `prev_id` 为 NULL（视作各舱首测），
历史详情、列表顺序（id 倒序）与新记录创建均保持可用。
