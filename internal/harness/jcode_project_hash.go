package harness

import (
	"encoding/binary"
	"fmt"
	"math/bits"
)

// JcodeProjectHash is the name jcode gives a per-project memory store
// (memory/projects/<hash>.json): Rust's DefaultHasher (SipHash-1-3, zero keys)
// over a Path, which hashes the path's bytes without separators, then the
// rotated count of those bytes (std's Path::hash on Unix). Verified against
// jcode's own output for six live stores and eight edge-case paths.
func JcodeProjectHash(path string) string {
	b := []byte(path)
	var buf []byte
	var n uint64
	start := 0
	for i := 0; i < len(b); i++ {
		if b[i] != '/' {
			continue
		}
		if i > start {
			buf = append(buf, b[start:i]...)
			n = bits.RotateLeft64(n+uint64(i-start), -2)
		}
		start = i + 1
		// A "." component is skipped by Path's components, so its byte is not hashed.
		if tail := b[start:]; len(tail) == 1 && tail[0] == '.' || len(tail) >= 2 && tail[0] == '.' && tail[1] == '/' {
			start++
		}
	}
	if start < len(b) {
		buf = append(buf, b[start:]...)
		n = bits.RotateLeft64(n+uint64(len(b)-start), -2)
	}
	var l [8]byte
	binary.LittleEndian.PutUint64(l[:], n)
	return fmt.Sprintf("%016x", sipHash13(append(buf, l[:]...)))
}

func sipHash13(msg []byte) uint64 {
	v0, v1, v2, v3 := uint64(0x736f6d6570736575), uint64(0x646f72616e646f6d), uint64(0x6c7967656e657261), uint64(0x7465646279746573)
	round := func() {
		v0 += v1
		v1 = bits.RotateLeft64(v1, 13) ^ v0
		v0 = bits.RotateLeft64(v0, 32)
		v2 += v3
		v3 = bits.RotateLeft64(v3, 16) ^ v2
		v0 += v3
		v3 = bits.RotateLeft64(v3, 21) ^ v0
		v2 += v1
		v1 = bits.RotateLeft64(v1, 17) ^ v2
		v2 = bits.RotateLeft64(v2, 32)
	}
	length := len(msg)
	for len(msg) >= 8 {
		m := binary.LittleEndian.Uint64(msg)
		v3 ^= m
		round()
		v0 ^= m
		msg = msg[8:]
	}
	var last [8]byte
	copy(last[:], msg)
	m := binary.LittleEndian.Uint64(last[:]) | uint64(length)<<56
	v3 ^= m
	round()
	v0 ^= m
	v2 ^= 0xff
	round()
	round()
	round()
	return v0 ^ v1 ^ v2 ^ v3
}
