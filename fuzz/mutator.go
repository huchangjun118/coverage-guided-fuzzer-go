package fuzz

import "math/rand"

// 变异策略编号，顺序固定以保证确定性。
const (
	mutBitFlip     = iota // 翻转单个比特
	mutByteFlip           // 替换单个字节
	mutInsert             // 插入随机字节
	mutDelete             // 删除一段
	mutReplace            // 替换一段
	mutSplice             // 与另一条语料拼接
	mutInteresting        // 写入边界特征值
	mutArithmetic         // 对字节做加减
	numMutStrategies
)

// interesting 边界特征值，覆盖常见整数边界。
var interesting = []byte{0, 1, 0x7f, 0x80, 0xff, 0xfe, 0x10, 0x20, 0x40}

// Mutator 基于固定种子的随机源对输入进行确定性变异。
// 同一个 Mutator 实例的调用序列完全由种子决定，与并发调度无关。
type Mutator struct {
	rng    *rand.Rand
	maxLen int
}

func newMutator(seed int64, maxLen int) *Mutator {
	return &Mutator{rng: rand.New(rand.NewSource(seed)), maxLen: maxLen}
}

// Mutate 对 base 施加一次随机变异，other 为可拼接的另一条语料（可为 nil）。
// 返回新分配的切片，不修改入参。
func (m *Mutator) Mutate(base, other []byte) []byte {
	if len(base) == 0 {
		base = []byte{0}
	}
	strategy := m.rng.Intn(numMutStrategies)
	out := make([]byte, len(base))
	copy(out, base)
	switch strategy {
	case mutBitFlip:
		i := m.rng.Intn(len(out))
		out[i] ^= 1 << uint(m.rng.Intn(8))
	case mutByteFlip:
		i := m.rng.Intn(len(out))
		out[i] = byte(m.rng.Intn(256))
	case mutInsert:
		n := 1 + m.rng.Intn(8)
		pos := m.rng.Intn(len(out) + 1)
		ins := make([]byte, n)
		for i := range ins {
			ins[i] = byte(m.rng.Intn(256))
		}
		out = append(out[:pos:pos], append(ins, out[pos:]...)...)
	case mutDelete:
		if len(out) > 1 {
			i := m.rng.Intn(len(out))
			j := i + 1 + m.rng.Intn(min(16, len(out)-i))
			out = append(out[:i:i], out[j:]...)
		}
	case mutReplace:
		n := 1 + m.rng.Intn(min(16, len(out)))
		i := m.rng.Intn(len(out) - n + 1)
		for k := 0; k < n; k++ {
			out[i+k] = byte(m.rng.Intn(256))
		}
	case mutSplice:
		if len(other) > 0 {
			i := m.rng.Intn(len(out))
			j := m.rng.Intn(len(other))
			merged := make([]byte, 0, i+len(other)-j)
			merged = append(merged, out[:i]...)
			merged = append(merged, other[j:]...)
			out = merged
		} else {
			i := m.rng.Intn(len(out))
			out[i] ^= 0xff
		}
	case mutInteresting:
		i := m.rng.Intn(len(out))
		out[i] = interesting[m.rng.Intn(len(interesting))]
	case mutArithmetic:
		i := m.rng.Intn(len(out))
		delta := int8(m.rng.Intn(35) - 17)
		if delta == 0 {
			delta = 1
		}
		out[i] += byte(delta)
	}
	if len(out) > m.maxLen {
		out = out[:m.maxLen]
	}
	if len(out) == 0 {
		out = []byte{0}
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
