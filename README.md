# SoftPLC — Go 软控制器运行时

把停产老控制器上的 IEC 61131-3 结构化文本（ST）程序，直接搬到普通工控机上运行的软控制器。
ST 源码**直接加载解释执行，不重写**；支持在线换程序、多任务优先级抢占、看门狗安全值、
I/O 强制与过程仿真闭环。

## 目录结构

```
internal/
  st/        ST 语言层：词法 → 语法(AST) → 类型系统 → 加载时检查 → 树遍历解释器
  fb/        原生功能块：TON/TOF/TP/R_TRIG/F_TRIG（扫描拍精确时序）
  runtime/   运行时：任务调度/抢占/看门狗/过程映像/强制/在线换程序/抖动统计
  plant/     泵站过程仿真：液位积分、泵启停特性、阀门行程、传感器毛刺/卡死
  webui/     Web 监控台（实时变量、强制点、任务耗时与抖动）
cmd/
  softplc/   主服务（扫描循环 + Web）
  simtest/   验收驱动（一个月虚拟时间、三次在线换程序）
programs/    三版 ST 控制程序（在线换程序验收用）
```

## ST 语言支持

### 数据类型
- 基本类型：BOOL / SINT INT DINT LINT / USINT UINT UDINT ULINT /
  BYTE WORD DWORD LWORD / REAL LREAL / STRING / TIME(LTIME)
- 结构体 `TYPE ... STRUCT ... END_STRUCT END_TYPE`
- 数组 `ARRAY[lo..hi, ...] OF T`（多维、行主序、运行期越界检查）
- 枚举 `TYPE Color : (RED,GREEN,BLUE); END_TYPE`，支持裸值 `RED` 与限定 `Color#RED`
- 功能块实例类型

### 字面量
- 时间：`T#500ms`、`TIME#1d2h3m4s5ms`、`LT#...`（纳秒精度，支持负时间）
- 位串：`2#1010_1100`、`8#777`、`16#FF`（按位宽自动选 BYTE/WORD/DWORD/LWORD）
- 整数、实数（含指数）、布尔、字符串（`$N $R $T $$ $'` 转义）

### 程序组织单元
- `PROGRAM` / `FUNCTION_BLOCK` / `FUNCTION`
- 变量区：`VAR` / `VAR_INPUT` / `VAR_OUTPUT` / `VAR_IN_OUT` / `VAR_GLOBAL` / `VAR_TEMP`，
  支持 `CONSTANT`、`RETAIN`、声明初值
- 调用：位置参数与命名参数 `name := v`，输出参数 `name => lvalue`

### 语句与运算
- 赋值、过程调用
- `IF/ELSIF/ELSE`、`CASE`（单值与 `lo..hi` 区间标签、ELSE）
- `FOR/BY`、`WHILE/DO`、`REPEAT/UNTIL`、`EXIT`、`RETURN`
- 运算：`AND OR XOR NOT &`、`= <> < > <= >=`、`+ - * / MOD **`，按 IEC 优先级
- 内置函数：ABS/SQRT/LN/LOG/EXP/SIN…/MIN/MAX/LIMIT/SEL/MUX/TRUNC/转换函数/LEN/CONCAT

### 加载时类型检查
全部检查在加载阶段完成，不通过则拒绝加载（在线换程序时保留旧程序继续运行）：
未声明标识符、类型不兼容赋值、形参不匹配/重复、数组维数不一致、下标非整数、
条件非 BOOL、CASE 选择类型、循环计数器类型等。

## 运行时语义

### 扫描映像
每拍：拍首把全部物理输入（含强制覆盖）**锁存**成输入映像写入全局帧，
程序一拍内读到的输入恒定；拍末把输出映像**一次性刷新**到物理输出。

### 多任务与抢占
每个任务有周期与优先级。到点任务按优先级运行；高优先级任务在低优先级任务的
**语句边界检查点**到点时抢占，被抢占任务从断点继续（扫描程序幂等，可见效果等价于
精确续跑）。抢占、看门狗、换程序挂起都经检查点以 panic 展开 + 恢复实现，
并有本拍已完成集合与调用栈防止递归重入。

### 看门狗
双口径：墙钟预算（抓真实死循环）+ 单拍语句数上限（虚拟时钟下确定性可测）。
动作后所有输出点退回**各自配置的安全值**，强制点仍保持强制。

### 在线换程序
在**两拍之间**原子完成：加载新程序（全量检查）→ 实例化新帧 → 迁移变量 → 替换。
迁移规则：
- 同名同类型：值整体带过去
- 同名类型变化：按转换规则逐叶子转换（如 INT→LREAL）
- 新增变量：取声明初值；移除变量：丢弃并报告
- 功能块：原生 FB（TON 等）快照/恢复内部状态，用户 FB 按成员名迁移
- 加载失败：保留旧程序继续运行，返回错误

### I/O 强制
任意输入/输出点可强制为指定 ST 字面量。输入强制只覆盖过程映像、不污染物理输入，
解除后下一拍自然恢复物理值；输出强制覆盖程序输出。Web 页面强制点红色闪烁标记。

## 运行

```bash
# 仿真闭环 + Web 监控台（虚拟时钟，确定性）
go run ./cmd/softplc -http :8080 -prog programs/pump_v1.st
# 打开 http://localhost:8080

# 一个月虚拟时间验收（中途 3 次在线换程序）
go run ./cmd/simtest -dur 720h -swap1 240h -swap2 432h -swap3 600h -report report.txt
```

## 测试

```bash
go test ./...          # 语言层、功能块时序、运行时、仿真全部用例
go test -race ./...    # 含竞态检测
```

- `internal/fb/timing_test.go`：按 IEC 标准逐拍核对 TON/TOF/TP/R_TRIG/F_TRIG 时序
- `internal/runtime/`：抢占、看门狗安全值、输入锁存、强制、在线换程序迁移
- `internal/st/`：类型/结构体/数组/枚举/字面量/控制流/用户 FB 与 FUNCTION
