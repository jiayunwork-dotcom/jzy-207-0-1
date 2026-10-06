# yardstress — 堆场地基附加应力准入服务

内河集装箱码头后方堆场的作业准入服务。每一次落箱、提箱、翻箱在执行前先过
"地基"这道关：按弹性半空间 Boussinesq 集中力解叠加计算黏土顶面检查点网格上
的竖向附加应力，执行后任一点将超过分区允许值则整笔拒绝；通过才生效并记为一条
事件。事件可重放、可按编号查询历史网格、支持称重更正及影响分析。

- 语言 Go 1.22，Web 框架 Gin，事件与快照持久化在 PostgreSQL 16
- 对外只有 HTTP 接口；`docker compose up` 一键起服务（app + postgres:16-alpine）

## 物理模型

地基按弹性半空间看待。地表集中力 P（kN）在深度 z（m）、水平距离 r（m）处产生
的竖向附加应力（kPa = kN/m²）采用经典 Boussinesq 解：

```
σ_z = 3P / (2π z²) · (1 + (r/z)²)^(−5/2)
```

多荷载按叠加原理求和（弹性解的线性性）。两条必须成立的关系由测试保证
（`internal/stress/stress_test.go`）：

1. **平面积分守恒**：任一深度水平面上 σ_z 对全平面积分等于地表荷载总和。
   测试对 Boussinesq 核做中点法数值积分（积分域 ±60z，截断尾部 ~5×10⁻⁶），
   在 z = 2 / 5 / 12 m 三个深度上相对误差 < 10⁻³。
2. **叠加性**：多荷载共同作用 = 各自单独作用之和。核层面与引擎层面（两台独立
   引擎分别施加再求和，与合并施加逐点比较）均有测试，结果逐点相等。

核对值（顾问给定，测试锁定）：

| 荷载 | 深度 | 位置 | σ_z 期望 | 本服务计算 |
|---|---|---|---|---|
| 100 kN 集中力 | 2 m | 正下方 | ≈ 11.94 kPa | 11.9366 kPa |
| 100 kN 集中力 | 2 m | 水平偏 1.5 m | ≈ 3.91 kPa | 3.9114 kPa |

## 荷载离散

**方案**：堆位荷载按均布压力 q = W/A 摊在整个堆位底面（堆载经路面结构层扩散，
这是土力学对面荷载的标准处理；不模拟四个角件的点集中——角件集中会被路面调平，
且在 12 m 深处其影响已不可分辨）。底面再细分为 nₓ×n_y 个矩形小块（边长不超过
`discretization.max_cell`），每小块用形心处一个集中力代替（对 Boussinesq 核做中
点积分）。细分加密时结果收敛到**均布矩形荷载精确解**（`stress.RectangleUniform`，
Boussinesq 核在矩形域上的解析积分，代码中作为收敛基准）。

**误差量级**：中点法则为二阶精度，单点相对误差 ~ O((h/z)²)，h 为小块边长、z 为
检查点深度。实测（12.5 m × 3 m 堆位，W = 1500 kN，z = 12 m，
`internal/discretize/discretize_test.go` 输出）：

| 细分 | 正下方中心点绝对误差 | 外边点 (16, 6) 绝对误差 |
|---|---|---|
| 1×1 | 9.25×10⁻¹ kPa（23%） | 1.92×10⁻¹ kPa |
| 2×2 | 1.34×10⁻¹ kPa | 3.45×10⁻² kPa |
| 4×4 | 3.32×10⁻² kPa | 7.59×10⁻³ kPa |
| 8×8 | 8.24×10⁻³ kPa | 1.84×10⁻³ kPa |
| 16×16 | 2.06×10⁻³ kPa | 4.57×10⁻⁴ kPa |
| 32×32 | 5.14×10⁻⁴ kPa（0.013%） | 1.14×10⁻⁴ kPa |

每加密一倍误差约 ÷4（二阶收敛），各测点均收敛到同一精确解。默认
`max_cell = 3 m`（≈ z/4，12.5×3 m 堆位分成 5×1 = 5 块）时实测误差：堆位中心
正下方 +1.69%、边中点正下方 +1.23%、外部点 < 0.1%，对几十 kPa 量级的允许值
可忽略；需要更高精度调小 `max_cell` 即可（如 max_cell = z/8 时误差再降约 4 倍）。

**影响半径截断**：单个子荷载对检查点的贡献低于 `discretization.cutoff`
（默认 10⁻⁶ kPa）时忽略，对应影响半径 r = z·√((3P/(2πεz²))^{2/5} − 1)。
截断是确定性的（重放走同一代码路径），不影响"网格 = 重放"的一致性；它对真值
的偏差上界为每笔作业每点 < ε，十万笔后 < 10⁻⁴ kPa，远小于允许值量级。

## 增量维护 vs 全量重算 —— 选择与实测

**选择：增量维护 + 定期快照。** 每笔生效作业只把本笔贡献加到网格上
（O(影响点数)），快照仅用于历史查询与故障恢复，不参与当前状态计算。

理由：

1. **线性性保证增量与全量等价**。应力场是荷载的线性泛函，网格当前值就是全部
   生效事件贡献之和。浮点求和顺序若不同会有舍入漂移，但本服务在瓦片锁临界区内
   分配事件序号，保证任一检查点上增量累加顺序与事件序号顺序一致——增量网格与
   按序重放在浮点层面**逐位相同**，不是"近似相同"。
2. **全量重算不可行**。每笔作业后从空场重算是 O(事件数 × 检查点数)，十万笔后
   单笔延迟无法接受。
3. 快照本身也由"前一快照 + 区间事件重放"生成（而非拷贝并发中的活网格），因此
   快照永远等于某个序号上的顺序重放结果，与并发作业无关。

**实测**（`TestIncrementalVsReplay100k`，10 万笔随机落/提混合作业）：

| 对比 | 最大偏差 |
|---|---|
| 增量网格 vs 从空场按序重放 | **0 kPa**（逐位一致，满足 ≪ 10⁻⁶ kPa 的指标） |
| 乱序重放（求和顺序敏感性上界） | 3.07×10⁻¹¹ kPa |

即使求和顺序完全打乱，float64 累加漂移也只有 10⁻¹¹ kPa 量级，相对 10⁻⁶ kPa
的验收线有 5 个数量级余量。

## 并发控制

多台场桥同时提交作业。核心矛盾：荷载影响范围越过箱区边界，相邻箱区各自合法的
作业合起来可能超限。

- **检查点网格分瓦片加锁**（默认 8×8 点/瓦片）。每笔作业按其荷载影响半径确定
  触及的瓦片集合，**只锁这些瓦片**，按瓦片编号升序加锁。
- **堆位状态（层数/重量）用每堆位锁**，按堆位 id 升序、先于瓦片锁获取。全局
  固定加锁顺序（堆位 → 瓦片，各自升序）从结构上排除死锁。
- **准入判定在瓦片锁内重新读网格当前值**：相邻箱区两笔作业在共享瓦片上串行，
  后到者看到的是已含先到者贡献的状态——单独看都合法、合起来超限的两笔并发作业
  **只有一笔生效**（`TestConcurrentAdjacentBlocksSingleEffect`，20 轮并发均恰
  好 1 笔生效）。
- **影响范围不相交的作业不共享任何瓦片，互不等待**
  （`TestConcurrentNonAdjacentNoBlock`：一笔作业被钩子卡在临界区内，远处另一笔
  照常完成）。
- 事件序号在瓦片临界区内经提交互斥锁分配并落库，保证"每点累加顺序 = 事件顺序"
  （见上节）；高并发混合作业后活网格与重放逐位一致
  （`TestConcurrentReplayConsistency`，`-race` 通过）。

## 事件溯源、快照与历史查询

- 每笔生效作业（含更正）追加为一条事件，按生效顺序编号（seq 从 1 递增），
  写入 PostgreSQL `events` 表（JSONB，只插不改）。
- 每 `engine.snapshot_interval` 笔事件生成一份快照（网格值 + 堆位状态），
  写入 `snapshots` 表；快照由日志重放生成，与并发无关。
- `GET /api/v1/grid/at/{seq}`：取 ≤ seq 的最近快照 + 重放其后事件，重建任一
  历史时刻的网格。
- 服务重启时从最新快照 + 尾部事件重放恢复（`TestRecoveryFromStore`）。
- 验收指标"当前网格 = 空场起按序重放，差 ≤ 10⁻⁶ kPa"由
  `TestIncrementalVsReplay100k` 锁定（实测 0）。

## 称重更正

称重系统报错时，对已生效作业更正箱重：

- 更正**不改写历史**：作为一条新的 `CORRECTION` 事件追加（携带目标事件号、新
  箱重、按堆位展开的荷载差量），当前网格按差量增量调整。更正本身不做准入判定
  ——它描述的是物理事实（箱子就是那个重量），但响应里会给出更正后当前网格的
  超限点供参考。
- **影响清单**：把日志中所有更正折叠进目标事件（多次更正后者为准），从空场按
  序重放"更正后的数据"，对目标之后的每一笔已生效作业重新做准入判定，列出会被
  拒绝的作业及其超限点。口径说明：重放时后续作业**无论是否会被拒绝都照常施加**
  （因为它们物理上确实发生了，地基确实承受了），回答的问题是"这笔作业在它提交
  的时刻能否通过地基这道关"。
- `GET /api/v1/grid/at/{seq}` 对更正前的历史保持不变（更正事件在更晚的序号上）。

## 校验规则（拒收并指明字段）

作业请求（HTTP 422，`errors[].field` 指明字段）：

| 规则 | 字段 |
|---|---|
| 箱重不为正 | `weight` |
| 层数变化 < 1 | `tiers` |
| 落箱后层数超过堆位允许层数 | `tiers` |
| 提箱/翻箱时堆位上没有那么多层 | `tiers` |
| 堆位不存在 | `stack_id` / `from_id` / `to_id` |
| 作业类型未知 | `type` |
| 更正的新箱重不为正 | `new_weight` |
| 更正目标不是已生效作业事件 | `event_id` |

配置（启动时拒绝并列出全部字段错误）：

| 规则 | 字段 |
|---|---|
| 检查点深度（黏土顶面深度）不为正 | `ground.clay_top_depth` |
| 允许值（默认或分区）不为正 | `allowable.default` / `allowable.zones[i].value` |
| 分区块不合法 | `allowable.zones[i].rect` |
| 网格间距/点数不为正 | `grid.spacing[i]` / `grid.count[i]` |
| 堆位尺寸（贝宽/排宽）不为正 | `blocks[i].bay_width` / `blocks[i].row_width` |
| 贝/排数、允许层数不为正 | `blocks[i].bays` / `.rows` / `.max_tiers` |
| 堆位互相重叠（含跨箱区） | `stacks[<id>,<id>]` |
| 箱区 id 重复 | `blocks[i].id` |

## HTTP API

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/v1/jobs` | 提交作业。201 `{"seq": n}`；409 超限拒绝（含最严重的前 top_k 个超限点）；422 字段错误 |
| POST | `/api/v1/corrections` | 称重更正。200 `{"seq", "impacted", "current_exceedances"}`；422 字段错误 |
| GET | `/api/v1/grid/current` | 当前网格（含 seq） |
| GET | `/api/v1/grid/at/{seq}` | 指定事件编号时的网格；越界 404 |
| GET | `/api/v1/events?from=&to=` | 事件列表 |
| GET | `/api/v1/stacks` | 全部堆位当前状态 |
| GET | `/healthz` | 健康检查 |

作业请求体：

```json
{"type": "PLACE",  "stack_id": "A-B01-R01", "weight": 285.0, "tiers": 1}
{"type": "PICK",   "stack_id": "A-B01-R01", "weight": 285.0, "tiers": 1}
{"type": "RESTOW", "from_id": "A-B01-R01", "to_id": "B-B02-R01", "weight": 285.0, "tiers": 1}
```

409 响应体：

```json
{"reason": "allowable_exceeded", "count": 118,
 "exceedances": [{"x": 122, "y": 14, "stress": 271.95, "allowable": 60, "excess": 211.95}]}
```

更正请求体 / 响应：

```json
{"event_id": 12, "new_weight": 310.5}
{"seq": 57, "impacted": [{"seq": 20, "count": 3, "exceedances": [...]}],
 "current_exceedances": [...]}
```

## 运行

```bash
docker compose up --build        # app(:8080) + postgres:16-alpine
```

本地开发（不需要数据库时用内存存储，重启即丢）：

```bash
go test ./...                    # 全部测试
go test -race ./internal/admit/  # 并发测试（竞态检测）
CONFIG_PATH=config.yaml go run ./cmd/server
```

环境变量：`CONFIG_PATH`（默认 `config.yaml`）、`DATABASE_URL`（覆盖
`database.dsn`；未设置时用内存存储并打印警告）。

## 配置（config.yaml）

```yaml
ground:    { clay_top_depth: 12.0 }          # 黏土顶面深度 = 检查点深度 z
grid:      { origin: [0,0], spacing: [2,2], count: [111,41] }   # 检查点网格，可跨箱区
allowable: { default: 60, zones: [{rect: [60,10,100,50], value: 25}] }  # 分区允许值，先匹配先赢
discretization: { max_cell: 3.0, cutoff: 1.0e-6 }  # 细分边长上限(m)、贡献截断(kPa)
engine:    { tile_size: 8, snapshot_interval: 1000, top_k: 5 }
blocks:    [ { id: A, origin_x: 10, origin_y: 10, bays: 6, rows: 4,
               bay_width: 12.5, row_width: 3.0, max_tiers: 5 }, ... ]
```

堆位 id 规则：`<箱区>-B<贝两位>-R<排两位>`（如 `A-B01-R01`），平面位置与尺寸由
箱区原点、贝宽、排宽推出。

## 包结构

```
cmd/server            入口：装配配置、存储、引擎、HTTP
internal/stress       应力计算：Boussinesq 核、均布矩形精确解
internal/discretize   荷载离散：面荷载→集中力、影响半径
internal/grid         检查点网格：几何、允许值分区、瓦片锁
internal/yard         堆场静态模型：箱区/贝/排/堆位与校验
internal/events       事件类型、荷载差量、更正折叠、重放纯函数
internal/store        事件/快照存储接口、内存实现、PostgreSQL 实现
internal/admit        作业准入与并发控制、网格增量维护、快照、更正与影响分析
internal/httpapi      Gin HTTP 层（薄翻译层）
internal/config       配置加载与字段级校验
internal/ferr, geom   字段错误、矩形几何（共享小件）
```

## 测试清单（与需求逐条对应）

| 需求 | 测试 |
|---|---|
| 两个核对值 | `stress.TestPointLoadCheckValues` |
| 平面积分 = 总荷载 | `stress.TestPlaneIntegralEqualsLoad` |
| 叠加性 | `stress.TestSuperposition`、`admit.TestEngineSuperposition` |
| 离散细分收敛 | `discretize.TestConvergenceToExactRectangle`（对精确解二阶收敛） |
| 超限拒绝后网格不变 | `admit.TestRejectLeavesGridUnchanged` |
| 增量与重放一致（10 万笔） | `admit.TestIncrementalVsReplay100k`（实测 0 kPa） |
| 跨箱区并发只生效一笔 | `admit.TestConcurrentAdjacentBlocksSingleEffect` |
| 不相邻箱区并发不阻塞 | `admit.TestConcurrentNonAdjacentNoBlock` |
| 并发下重放一致性 | `admit.TestConcurrentReplayConsistency`（-race） |
| 历史事件编号查询 | `admit.TestGridAtEventSeq`、`TestRecoveryFromStore` |
| 称重更正影响清单 | `admit.TestCorrectionImpactList`、`TestCorrectionOnRestow` |
| 字段级拒收 | `admit.TestValidationErrors`、`config.TestValidateFieldErrors`、`yard.TestNewRejects*` |
| HTTP 端到端 | `httpapi.TestJobLifecycleOverHTTP`、`TestCorrectionOverHTTP` |
| PostgreSQL 存储 | `store.TestPostgresStore`（设 `DATABASE_URL` 后启用） |
