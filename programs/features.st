(* ST 语法特性示例：覆盖类型系统、字面量、功能块、结构体、数组、枚举、控制流。
   可被运行时加载（go run ./cmd/softplc -prog programs/features.st）验证语法与类型检查。 *)

TYPE
  (* 枚举：支持裸值 RED 与限定 Color#RED 两种写法 *)
  Color : (RED, GREEN, BLUE);

  (* 结构体：可嵌套、可含数组 *)
  ValveState : STRUCT
    open   : BOOL;
    pos    : LREAL;
    counts : ARRAY[0..3] OF DINT;
    color  : Color;
  END_STRUCT;
END_TYPE

VAR_GLOBAL
  (* ---- 各种字面量 ---- *)
  gBool   : BOOL := TRUE;
  gInt    : DINT := -12345;
  gHex    : DWORD := 16#DEAD_BEEF;       (* 位串字面量 *)
  gBin    : WORD := 2#1010_1100;
  gOct    : BYTE := 8#377;
  gReal   : LREAL := 3.14159e2;
  gTime   : TIME := T#1d2h3m4s5ms;       (* 时间字面量，纳秒精度 *)
  gNegT   : TIME := T#-500ms;
  gText   : STRING := 'hello$Nworld';    (* 带转义的字符串 *)
  gEnum   : Color := GREEN;

  (* ---- 数组与结构体 ---- *)
  gMatrix : ARRAY[1..3, 0..2] OF INT;
  gValve  : ValveState;

  (* ---- 定时/边沿功能块实例 ---- *)
  ton1    : TON;
  tof1    : TOF;
  tp1     : TP;
  rise    : R_TRIG;
  fall    : F_TRIG;

  (* ---- I/O 风格标量 ---- *)
  LEVEL_M  : LREAL;
  PUMP_OUT : BOOL;
  tQ       : BOOL;
  tET      : TIME;
  edgeQ    : BOOL;
  ctrOut   : DINT;
END_VAR

(* 用户 FUNCTION：带输入参数与返回值 *)
FUNCTION Clamp : LREAL
VAR_INPUT
  x, lo, hi : LREAL;
END_VAR
BEGIN
  Clamp := LIMIT(lo, x, hi);
END_FUNCTION

(* 用户 FUNCTION_BLOCK：状态保留，含输入输出参数 *)
FUNCTION_BLOCK Counter
VAR_INPUT
  enable : BOOL;
  step   : DINT;
END_VAR
VAR_OUTPUT
  total : DINT;
END_VAR
BEGIN
  IF enable THEN
    total := total + step;
  END_IF;
END_FUNCTION_BLOCK

PROGRAM CTRL
VAR
  i, j   : INT;
  sel    : INT;
  ctr    : Counter;
  sum    : LREAL;
END_VAR
BEGIN
  (* 数组多维下标与循环 *)
  FOR i := 1 TO 3 DO
    FOR j := 0 TO 2 DO
      gMatrix[i, j] := i * 10 + j;
    END_FOR;
  END_FOR;

  (* 结构体字段与嵌套数组 *)
  gValve.open := TRUE;
  gValve.pos := Clamp(gReal * 0.01, 0.0, 1.0);
  gValve.counts[2] := gMatrix[2, 1];
  gValve.color := Color#BLUE;

  (* 原生定时功能块：命名输入参数 + 输出参数回写 *)
  ton1(IN := gBool, PT := T#5s, Q => tQ, ET => tET);
  rise(CLK := LEVEL_M > 5.0, Q => edgeQ);

  (* 用户功能块调用（状态跨拍保留），输出参数接标量全局变量 *)
  ctr(enable := gBool, step := 2, total => ctrOut);

  (* CASE：单值标签与区间标签 *)
  sel := gInt MOD 3;
  CASE sel OF
    0:       sum := 0.0;
    1, 2:    sum := gReal;
    -1..10:  sum := ABS(gReal);
  ELSE
    sum := SQRT(gReal);
  END_CASE;

  (* WHILE / REPEAT / 位运算 / 数学函数 *)
  j := 0;
  WHILE j < 3 DO
    j := j + 1;
  END_WHILE;

  REPEAT
    sel := sel - 1;
  UNTIL sel <= 0 END_REPEAT;

  PUMP_OUT := (gHex AND 16#0000_0001) <> 0;
END_PROGRAM

PROGRAM HOUSE
VAR
  n : DINT;
END_VAR
BEGIN
  n := n + 1;
END_PROGRAM
