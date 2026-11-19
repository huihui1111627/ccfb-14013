(* 泵站控制程序 v2：双泵轮换
   在 v1 基础上增加主备泵轮换，高液位时两台并泵；
   变量 PUMP_SEL 由 v1 的 INT 改为 LREAL，专门验证在线换程序的类型转换迁移。 *)

VAR_GLOBAL
  LEVEL_M  : LREAL;
  PUMP1    : BOOL;
  PUMP2    : BOOL;
  VIN_CMD  : LREAL;
  VOUT_CMD : LREAL;

  BUF      : ARRAY[0..7] OF LREAL;
  IDX      : INT;
  FILT     : LREAL;
  SUM      : LREAL;
  K        : INT;
  HI_TIM   : TON;
  LO_TIM   : TON;
  RUN_TIM  : TON;
  STOP_TIM : TON;
  REQ      : BOOL;
  HI_Q     : BOOL;
  LO_Q     : BOOL;
  RUN_Q    : BOOL;
  STOP_Q   : BOOL;
  CYCLES   : DINT;
  VERSION  : INT := 2;
  (* v2 新增/改型变量 *)
  PUMP_SEL : LREAL := 1.0;   (* v1 中为 INT，v2 改 LREAL：验证类型迁移 *)
  HI2_TIM  : TON;            (* 并泵确认定时器 *)
  HI2_Q    : BOOL;
  SWAP_CT  : DINT;           (* 轮换计数 *)
END_VAR

PROGRAM CTRL
VAR
  HI_LIM  : LREAL := 6.0;
  HI2_LIM : LREAL := 7.2;
  LO_LIM  : LREAL := 4.0;
  I       : INT;
  S       : LREAL;
  LEAD    : INT;
END_VAR
BEGIN
  CYCLES := CYCLES + 1;

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

  HI_TIM(IN := FILT > HI_LIM, PT := T#3s, Q => HI_Q);
  LO_TIM(IN := FILT < LO_LIM, PT := T#3s, Q => LO_Q);
  HI2_TIM(IN := FILT > HI2_LIM, PT := T#4s, Q => HI2_Q);

  IF HI_Q THEN
    REQ := TRUE;
  ELSIF LO_Q THEN
    REQ := FALSE;
  END_IF;

  RUN_TIM(IN := PUMP1 OR PUMP2, PT := T#10s, Q => RUN_Q);
  STOP_TIM(IN := (NOT PUMP1) AND (NOT PUMP2), PT := T#10s, Q => STOP_Q);

  (* 主泵选择：PUMP_SEL 在 1/2 间轮换，每次由请求下降沿触发 *)
  IF PUMP_SEL >= 1.5 THEN
    LEAD := 2;
  ELSE
    LEAD := 1;
  END_IF;

  IF REQ AND STOP_Q THEN
    IF LEAD = 1 THEN
      PUMP1 := TRUE;
    ELSE
      PUMP2 := TRUE;
    END_IF;
    SWAP_CT := SWAP_CT + 1;
  ELSIF (NOT REQ) AND RUN_Q THEN
    PUMP1 := FALSE;
    PUMP2 := FALSE;
  END_IF;

  (* 超高液位第二台并泵 *)
  IF HI2_Q THEN
    PUMP1 := TRUE;
    PUMP2 := TRUE;
  END_IF;

  IF PUMP1 OR PUMP2 THEN
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
