package controllers

import "strings"

// DiffLine is one line of a unified-style comparison.
type DiffLine struct {
	Kind string // "same", "add", "del"
	Text string
}

// lineDiff computes a simple LCS line diff of a (old) against b (current).
// Inputs are bounded by the page quota, so the O(n·m) table is acceptable.
func lineDiff(a, b string) []DiffLine {
	al := strings.Split(strings.TrimRight(a, "\n"), "\n")
	bl := strings.Split(strings.TrimRight(b, "\n"), "\n")
	if len(al)*len(bl) > 4_000_000 {
		return []DiffLine{{Kind: "same", Text: "(diff too large to display)"}}
	}
	n, m := len(al), len(bl)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if al[i] == bl[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var out []DiffLine
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case al[i] == bl[j]:
			out = append(out, DiffLine{"same", al[i]})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			out = append(out, DiffLine{"del", al[i]})
			i++
		default:
			out = append(out, DiffLine{"add", bl[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, DiffLine{"del", al[i]})
	}
	for ; j < m; j++ {
		out = append(out, DiffLine{"add", bl[j]})
	}
	return out
}
