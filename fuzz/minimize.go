package fuzz

import "time"

// maxMinimizeExecs 限制最小化过程的执行次数，避免拖慢引擎。
const maxMinimizeExecs = 300

// minimize 在保持失败原因不变的前提下，确定性缩减输入。
// 算法：先按指数缩小的块做删除（类 ddmin），再逐字节清零尝试。
// 给定相同目标、输入与超时，产出唯一确定。
func minimize(t Target, input []byte, timeout time.Duration, reason FailReason) []byte {
	fails := func(candidate []byte) bool {
		if len(candidate) == 0 {
			return false
		}
		return execute(t, candidate, timeout).reason == reason
	}
	execs := 0
	cur := make([]byte, len(input))
	copy(cur, input)

	// 块删除：从大到小尝试删除连续块。
	for chunk := len(cur) / 2; chunk > 0 && execs < maxMinimizeExecs; chunk /= 2 {
		changed := false
		for off := 0; off+chunk <= len(cur) && execs < maxMinimizeExecs; {
			candidate := make([]byte, 0, len(cur)-chunk)
			candidate = append(candidate, cur[:off]...)
			candidate = append(candidate, cur[off+chunk:]...)
			execs++
			if fails(candidate) {
				cur = candidate
				changed = true
				// 不推进 off，继续在同一位置尝试。
			} else {
				off += chunk
			}
		}
		if !changed && chunk == 1 {
			break
		}
	}

	// 字节清零：逐字节尝试替换为 0。
	for i := 0; i < len(cur) && execs < maxMinimizeExecs; i++ {
		if cur[i] == 0 {
			continue
		}
		candidate := make([]byte, len(cur))
		copy(candidate, cur)
		candidate[i] = 0
		execs++
		if fails(candidate) {
			cur = candidate
		}
	}
	return cur
}
