(* 泵站控制程序 v1：基础带控
   - 8 点滑动平均滤波抑制传感器噪声/毛刺/卡死
   - 高低液位迟滞 + TON 多点确认，避免毛刺引起误动作
   - 泵最小运转/停止停留时间，防止频繁启停
   IO 点名与类型在三个版本中保持一致，便于在线换程序迁移。 *)

VAR_GLOBAL
  (* ---- I/O 点（与 runtime PointConfig 对应） ---- *)
  LEVEL_M  : LREAL;   (* 输入：液位传感器读数 m *)
  PUMP1    : BOOL;    (* 输出：1 号泵命令 *)
  PUMP2    : BOOL;    (* 输出：2 号泵命令 *)
  VIN_CMD  : LREAL;   (* 输出：进水阀目标开度 0..1 *)
  VOUT_CMD : LREAL;   (* 输出：出水阀目标开度 0..1 *)

  (* ---- 内部变量 ---- *)
  BUF      : ARRAY[0..7] OF LREAL;
  IDX      : INT;
  FILT     : LREAL;    (* 滤波后液位 *)
  SUM      : LREAL;
  K        : INT;
  HI_TIM   : TON;      (* 高液位确认 *)
  LO_TIM   : TON;      (* 低液位确认 *)
  RUN_TIM  : TON;      (* 泵最小运转计时 *)
  STOP_TIM : TON;      (* 泵最小停止计时 *)
  REQ      : BOOL;     (* 泵请求（迟滞结果） *)
  HI_Q     : BOOL;
  LO_Q     : BOOL;
  RUN_Q    : BOOL;
  STOP_Q   : BOOL;
  CYCLES   : DINT;     (* 慢任务统计：运行拍数 *)
  VERSION  : INT := 1; (* 版本标记，换程序后可观察 *)
END_VAR

(* 快速任务：1s 周期，液位闭环控制 *)
PROGRAM CTRL
VAR
  HI_LIM : LREAL := 6.0;
  LO_LIM : LREAL := 4.0;
  I      : INT;
  S      : LREAL;
END_VAR
BEGIN
  CYCLES := CYCLES + 1;

  (* 1. 环形缓冲更新与滑动平均 *)
  BUF[IDX] := LEVEL_M;
  IDX := IDX + 1;
  IF IDX > 7 THEN
    IDX := 0;
  END_IF;
  S := 0.0;
  FOR I := 0 TO 7 DO
    S := S + BUF[I];
  END_FOR;
  FILT := S / 8.0;

  (* 2. 高低液位多点确认（连续 3 拍），抑制毛刺/卡死 *)
  HI_TIM(IN := FILT > HI_LIM, PT := T#3s, Q => HI_Q);
  LO_TIM(IN := FILT < LO_LIM, PT := T#3s, Q => LO_Q);

  (* 3. 迟滞：高确认置请求，低确认清请求 *)
  IF HI_Q THEN
    REQ := TRUE;
  ELSIF LO_Q THEN
    REQ := FALSE;
  END_IF;

  (* 4. 最小运转 10s、最小停止 10s，防频繁启停 *)
  RUN_TIM(IN := PUMP1, PT := T#10s, Q => RUN_Q);
  STOP_TIM(IN := NOT PUMP1, PT := T#10s, Q => STOP_Q);

  IF REQ AND STOP_Q THEN
    PUMP1 := TRUE;
  ELSIF (NOT REQ) AND RUN_Q THEN
    PUMP1 := FALSE;
  END_IF;

  (* v1：只开一台泵，第二台常驻停止 *)
  PUMP2 := FALSE;

  (* 5. 阀门联动：出水阀随泵开；进水阀按液位反向调节，超高全关 *)
  IF PUMP1 THEN
    VOUT_CMD := 1.0;
  ELSE
    VOUT_CMD := 0.0;
  END_IF;
  IF FILT > 7.0 THEN
    VIN_CMD := 0.0;
  ELSIF FILT < 3.0 THEN
    VIN_CMD := 1.0;
  ELSE
    VIN_CMD := (7.0 - FILT) / 4.0;
  END_IF;
END_PROGRAM

(* 慢速任务：5s 周期，低优先级，做轻量统计 *)
PROGRAM HOUSE
VAR
  N : INT;
END_VAR
BEGIN
  N := N + 1;
  IF N > 1000 THEN
    N := 0;
  END_IF;
END_PROGRAM
