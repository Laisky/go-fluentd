package controller

import (
	"testing"
)

func TestFor(t *testing.T) {
	i := 0
	for i < 3 {
		i++
	}
	t.Log(i)
}
