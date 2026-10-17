//go:build race

package escapes_test

// The race detector slows the analysis down by an order of magnitude.
const slowdown = 15
