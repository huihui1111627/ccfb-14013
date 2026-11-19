(* 泵站控制程序 v3：连续调节 + 比例控制
   阀门改为按液位偏差的比例连续调节（不再分段），
   新增 LREAL 变量 KP、ERR、INT_ACC，验证新增变量按初值迁移。 *)

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
  VERSION  : INT := 3;
  PUMP_SEL : LREAL := 1.0;
  HI2_TIM  : TON;
  HI2_Q    : BOOL;
  SWAP_CT  : DINT;
  (* v3 新增 LREAL 变量（换程序时应进入 New 列表、取初值） *)
  KP       : LREAL := 0.25;
  ERR      : LREAL;
  INT_ACC  : LREAL;
  SETPT    : LREAL := 5.0;
END_VAR

PROGRAM CTRL
VAR
  HI_LIM  : LREAL := 6.0;
  HI2_LIM : LREAL := 7.2;
  LO_LIM  : LREAL := 4.0;
  I       : INT;
  S       : LREAL;
  LEAD    : INT;
  V       : LREAL;
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

  IF HI2_Q THEN
    PUMP1 := TRUE;
    PUMP2 := TRUE;
  END_IF;

  (* 出水阀随泵，叠加比例开度 *)
  IF PUMP1 OR PUMP2 THEN
    VOUT_CMD := 0.7 + 0.3 * LIMIT(0.0, FILT / 8.0, 1.0);
  ELSE
    VOUT_CMD := 0.0;
  END_IF;

  (* 进水阀：对设定点偏差的比例连续调节（带积分限幅） *)
  ERR := SETPT - FILT;
  INT_ACC := INT_ACC + ERR * 0.02;
  IF INT_ACC > 2.0 THEN
    INT_ACC := 2.0;
  ELSIF INT_ACC < -2.0 THEN
    INT_ACC := -2.0;
  END_IF;
  V := 0.5 + KP * ERR + 0.05 * INT_ACC;
  VIN_CMD := LIMIT(0.0, V, 1.0);
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
