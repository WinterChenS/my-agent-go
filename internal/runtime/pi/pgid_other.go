//go:build !windows && !linux && !darwin

package pi

// liveCountInGroup 在非目标矩阵的 Unix 上没有可靠的非僵尸计数手段。
//
// 返回 ok=false 表示"无法判定"：调用方必须把它当成"清理结果未知"，
// 而不是伪造一个 0 当作成功。V1 的目标矩阵只有 linux/darwin，
// 这里保守失败比猜一个数字更安全。
func liveCountInGroup(pgid int) (int, bool) {
	_ = pgid
	return 0, false
}
