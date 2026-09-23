//go:build !darwin && !linux

package index

func readBootID() string { return "" }
