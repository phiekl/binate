// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package diff

import "bytes"

// maxCountWork bounds the steps Count takes to find the fewest lines
// changed, beyond which it counts those of the anchored diff of Lines
// instead, which may be more.
const maxCountWork = 50_000_000

// Count returns the lines added and deleted from a to b, as git's diff
// stat counts them: lines end after a newline, or at the end, a last line
// without one differing from the same line with one. The fewest lines that
// make a into b are counted, as of a longest common subsequence, which
// git's diff counts too, but for large changes, where it counts lines
// common on both sides, as blank ones, as changed if many others around
// them are, and so a few more.
func Count(a, b []byte) (added, deleted int) {
	x, y := splitLines(a), splitLines(b)
	// Lines the same at the start and end are kept.
	for len(x) > 0 && len(y) > 0 && x[0] == y[0] {
		x, y = x[1:], y[1:]
	}
	for len(x) > 0 && len(y) > 0 && x[len(x)-1] == y[len(y)-1] {
		x, y = x[:len(x)-1], y[:len(y)-1]
	}
	// A line on one side only can't be kept: it is counted, and left out
	// of the search.
	ids := map[string]int{}
	var inX, inY []bool
	id := func(line string, in *[]bool) int {
		n, ok := ids[line]
		if !ok {
			n = len(ids)
			ids[line] = n
			inX, inY = append(inX, false), append(inY, false)
		}
		(*in)[n] = true
		return n
	}
	xs, ys := make([]int, len(x)), make([]int, len(y))
	for i, l := range x {
		xs[i] = id(l, &inX)
	}
	for i, l := range y {
		ys[i] = id(l, &inY)
	}
	var xk, yk []int
	for _, n := range xs {
		if inY[n] {
			xk = append(xk, n)
		}
	}
	for _, n := range ys {
		if inX[n] {
			yk = append(yk, n)
		}
	}
	common, ok := lcsLen(xk, yk)
	if !ok {
		common = 0
		for _, op := range Lines(x, y) {
			if op.Kind == Equal {
				common += op.X1 - op.X0
			}
		}
	}
	return len(y) - common, len(x) - common
}

// splitLines splits data into lines, each with its newline.
func splitLines(data []byte) []string {
	var lines []string
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n') + 1
		if i == 0 {
			i = len(data)
		}
		lines = append(lines, string(data[:i]))
		data = data[i:]
	}
	return lines
}

// lcsLen returns the length of the longest common subsequence of x and y,
// by Myers' algorithm, or false if finding it takes more than
// maxCountWork steps.
func lcsLen(x, y []int) (int, bool) {
	n, m := len(x), len(y)
	if n == 0 || m == 0 {
		return 0, true
	}
	max := n + m
	// v[k+max] is the furthest x reached on diagonal k.
	v := make([]int, 2*max+2)
	work := 0
	for d := 0; d <= max; d++ {
		for k := -d; k <= d; k += 2 {
			var i int
			if k == -d || k != d && v[k-1+max] < v[k+1+max] {
				i = v[k+1+max]
			} else {
				i = v[k-1+max] + 1
			}
			j := i - k
			for i < n && j < m && x[i] == y[j] {
				i++
				j++
				work++
			}
			v[k+max] = i
			if i >= n && j >= m {
				return (n + m - d) / 2, true
			}
		}
		if work += d + 1; work > maxCountWork {
			return 0, false
		}
	}
	// Not reached: the ends meet once d is n+m.
	return 0, true
}
