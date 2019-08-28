package concator_test

import (
	"testing"

	utils "github.com/Laisky/go-utils"
)

func TestFor(t *testing.T) {
	i := 0
	for i < 3 {
		i++
	}
	t.Log(i)
}

func init() {
	utils.Settings.Setup("/Users/laisky/repo/pateo/configs/go-fluentd")
}
