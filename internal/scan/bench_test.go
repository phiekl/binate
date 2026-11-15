// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package scan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkCompareContents(b *testing.B) {
	for _, size := range []int{0, 1024, blockSize, 4 << 20} {
		for _, different := range []bool{false, true} {
			if size == 0 && different {
				continue
			}
			b.Run(fmt.Sprintf("bytes=%d/different=%v", size, different), func(b *testing.B) {
				dir := b.TempDir()
				left, right := filepath.Join(dir, "left"), filepath.Join(dir, "right")
				data := make([]byte, size)
				if err := os.WriteFile(left, data, 0o600); err != nil {
					b.Fatal(err)
				}
				want := Same
				if different {
					data[0] = 1
					want = Differ
				}
				if err := os.WriteFile(right, data, 0o600); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				var comparer contentComparer
				// Allocate reusable buffers before measuring per-call costs.
				state, lErr, rErr := comparer.compareContents(context.Background(), left, right)
				if state != want || lErr != nil || rErr != nil {
					b.Fatalf("warm-up comparison: %v, %v, %v", state, lErr, rErr)
				}
				for b.Loop() {
					state, lErr, rErr := comparer.compareContents(context.Background(), left, right)
					if state != want || lErr != nil || rErr != nil {
						b.Fatalf("comparison: %v, %v, %v", state, lErr, rErr)
					}
				}
			})
		}
	}
}

// BenchmarkScanTrees reads existing trees without changing them. For example:
// BINATE_BENCH_LEFT=/path/to/left BINATE_BENCH_RIGHT=/path/to/right \
// go test -run '^$' -bench BenchmarkScanTrees -benchmem ./internal/scan
func BenchmarkScanTrees(b *testing.B) {
	left, right := os.Getenv("BINATE_BENCH_LEFT"), os.Getenv("BINATE_BENCH_RIGHT")
	if left == "" || right == "" {
		b.Skip("set BINATE_BENCH_LEFT and BINATE_BENCH_RIGHT to existing directories")
	}
	for _, dir := range []string{left, right} {
		st, err := os.Stat(dir)
		if err != nil {
			b.Fatal(err)
		}
		if !st.IsDir() {
			b.Fatalf("%s is not a directory", dir)
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		s := Start(left, right, Options{}, nil)
		s.Wait()
		if s.Root.State == Pending {
			b.Fatalf("incomplete scan: %v", s.Root.State)
		}
	}
}
