//go:build !linux && !darwin

package controller

func otlpFilesystemCapacity(string) (uint64, uint64, bool) { return 0, 0, false }
