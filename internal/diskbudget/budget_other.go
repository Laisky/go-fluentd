//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package diskbudget

import (
	"context"
	"errors"
)

const DefaultLimitBytes int64 = 3000000000
const MaximumLimitBytes int64 = 1 << 50

var ErrCapacity = errors.New("overall go-fluentd storage budget full")
var ErrMigration = errors.New("existing OTLP state requires qualified shared-budget migration")

type Snapshot struct{ Limit, ChargedBytes, ReservedBytes int64 }
type Budget struct{}

var unsupported = errors.New("overall go-fluentd storage budget requires a supported Unix filesystem")

func Open(context.Context, string, string, int64) (*Budget, error) { return nil, unsupported }
func (*Budget) Admit(context.Context, int64, int64) error          { return unsupported }
func (*Budget) Reserve(context.Context, int64) error               { return unsupported }
func (*Budget) Charge(context.Context, int64) error                { return unsupported }
func (*Budget) Reconcile(context.Context, bool) error              { return unsupported }
func (*Budget) Snapshot(context.Context) (Snapshot, error)         { return Snapshot{}, unsupported }
func (*Budget) Close() error                                       { return nil }
