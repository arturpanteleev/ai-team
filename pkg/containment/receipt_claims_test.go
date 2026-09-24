package containment

import "testing"

// receipt_claims_test.go — QS-24: receipt не должен утверждать наблюдений,
// которых не было, и должен детерминированно выводиться из профиля (это и
// делает его проверяемым в `ai-team verify`).

func TestTrustedLocalReceiptClaimsNoUnobservedCleanup(t *testing.T) {
	receipt := DefaultTrustedLocalReceipt()
	flags := receipt.Details[AxisProc]
	if _, present := flags["cleanup_verified"]; present {
		t.Fatal("cleanup_verified писался литералом true: process.TrackAndCleanup " +
			"в продакшен-пути не вызывается, поэтому флаг удалён — вернуть его можно " +
			"только вместе с фактической проверкой")
	}
	if !flags["process_group_kill"] {
		t.Fatal("process_group_kill — свойство кода (process.Run), оно остаётся")
	}
}

func TestCanonicalReceiptMatchesController(t *testing.T) {
	if !CanonicalReceipt("trusted-local").Equal(DefaultTrustedLocalReceipt()) {
		t.Fatal("канонический receipt для trusted-local обязан совпадать с тем, что пишет контроллер")
	}
	strict := CanonicalReceipt("strict")
	if strict.Profile != "strict" || !strict.HasUnavailable() {
		t.Fatalf("для профиля без backend ожидался UNAVAILABLE receipt: %+v", strict)
	}
}

func TestReceiptEqualDetectsFlippedAxisAndFlag(t *testing.T) {
	base := DefaultTrustedLocalReceipt()

	flippedAxis := DefaultTrustedLocalReceipt()
	flippedAxis.Axes = map[Axis]Level{
		AxisFS: LevelENFORCED, AxisNet: LevelPARTIAL, AxisProc: LevelPARTIAL, AxisEnv: LevelPARTIAL,
	}
	if base.Equal(flippedAxis) {
		t.Fatal("переворот уровня оси обязан ломать сравнение")
	}

	flippedFlag := DefaultTrustedLocalReceipt()
	flippedFlag.Details = map[Axis]map[string]bool{
		AxisFS:   {"symlink_reject": false, "worktree_isolation": true, "credential_deny": true},
		AxisNet:  {"tool_deny": true, "env_isolation": true},
		AxisProc: {"process_group_kill": true},
		AxisEnv:  {"allow_list": true, "config_dir_isolation": true, "credential_deny": true},
	}
	if base.Equal(flippedFlag) {
		t.Fatal("переворот флага обязан ломать сравнение")
	}

	extraFlag := DefaultTrustedLocalReceipt()
	extraFlag.Details[AxisProc] = map[string]bool{"process_group_kill": true, "cleanup_verified": true}
	if base.Equal(extraFlag) {
		t.Fatal("дописанный флаг обязан ломать сравнение")
	}
}
