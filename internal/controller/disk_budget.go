package controller

import (
	"context"
	"errors"
	"github.com/go-viper/mapstructure/v2"
	"gofluentd/internal/diskbudget"
	"gofluentd/internal/monitor"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"time"
)

// DiskBudgetConfig applies once to ALL managed journal and OTLP roots.
// The same directory and max_bytes coordinate independent collector processes.
type DiskBudgetConfig struct {
	Directory    string `mapstructure:"directory"`
	MaxBytes     int64  `mapstructure:"max_bytes"`
	SegmentBytes int64  `mapstructure:"segment_bytes"`
}

func ParseDiskBudgetConfig(raw interface{}) (*DiskBudgetConfig, error) {
	if raw == nil {
		return nil, nil
	}
	c := new(DiskBudgetConfig)
	var d *mapstructure.Decoder
	var e error
	// Use the same strict integer contract as the OTLP config without coercions.
	d, e = mapstructure.NewDecoder(&mapstructure.DecoderConfig{Result: c, TagName: "mapstructure", ErrorUnused: true,
		DecodeHook: func(from, to reflect.Type, value interface{}) (interface{}, error) {
			if (from.Kind() == reflect.Float64 || from.Kind() == reflect.Float32) && to.Kind() == reflect.Int64 {
				n := reflect.ValueOf(value).Float()
				if math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) || math.Abs(n) >= 1<<53 {
					return nil, errors.New("exact integer required")
				}
			}
			return value, nil
		}})
	if e != nil {
		return nil, errors.New("invalid overall disk budget configuration")
	}
	if e = d.Decode(raw); e != nil {
		return nil, errors.New("invalid overall disk budget configuration")
	}
	if c.MaxBytes == 0 {
		c.MaxBytes = diskbudget.DefaultLimitBytes
	}
	if c.SegmentBytes == 0 {
		c.SegmentBytes = 16 << 20
	}
	if c.Directory == "" || !filepath.IsAbs(c.Directory) || c.MaxBytes < 4<<20 || c.MaxBytes > diskbudget.MaximumLimitBytes || c.SegmentBytes < 64<<10 || c.SegmentBytes > 64<<20 {
		return nil, errors.New("invalid overall disk budget limits or directory")
	}
	return c, nil
}
func (c *DiskBudgetConfig) ApplyOTLP(o *OTLPServiceConfig) error {
	if c == nil || o == nil {
		return nil
	}
	if o.SharedStorageDirectory != "" && (o.SharedStorageDirectory != c.Directory || (o.SharedStorageBytes != 0 && o.SharedStorageBytes != c.MaxBytes)) {
		return errors.New("OTLP storage budget conflicts with overall disk budget")
	}
	o.SharedStorageDirectory = c.Directory
	o.SharedStorageBytes = c.MaxBytes
	o.WALSegmentBytes = c.SegmentBytes
	o.RetainAccepted = true
	return nil
}
func (j *Journal) openOverallBudget(ctx context.Context) error {
	if j.DiskBudget == nil {
		return nil
	}
	b, e := diskbudget.Open(ctx, j.DiskBudget.Directory, j.BufDirPath, j.DiskBudget.MaxBytes)
	if e != nil {
		return e
	}
	j.diskBudget = b
	j.diskCtx = ctx
	if e = b.Reconcile(ctx, false); e != nil {
		b.Close()
		j.diskBudget = nil
		return e
	}
	// Metadata for an empty root and future progress is reserved by the owner;
	// child journals do not speculatively preallocate large empty segments.
	monitor.AddMetric("overallDisk", func() map[string]interface{} {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		defer cancel()
		snap, e := b.Snapshot(ctx)
		return map[string]interface{}{"known": e == nil, "limitBytes": snap.Limit, "chargedBytes": snap.ChargedBytes, "reservedBytes": snap.ReservedBytes, "refused": j.diskRefused.Load(), "writeFailures": j.diskWriteFailures.Load(), "archiveRetention": "until_durable_commit"}
	})
	return nil
}
func (j *Journal) diskGuard(kind string, n int64) (func(), error) {
	if j.diskBudget == nil {
		return func() {}, nil
	}
	j.diskGate.RLock()
	var e error
	if kind == "data" {
		// Initial WAL plus future ACK, sync, rotation/recovery metadata is funded
		// before the caller may publish a durable admission receipt.
		if n < 0 || n > math.MaxInt64/8 {
			e = diskbudget.ErrCapacity
		} else {
			e = j.diskBudget.Admit(j.diskCtx, walGrowth(n)+64<<10, 2*walGrowth(n)+256<<10)
		}
	} else {
		e = j.diskBudget.Charge(j.diskCtx, max(n, 64<<10))
	}
	if e != nil {
		j.diskRefused.Add(1)
		j.diskGate.RUnlock()
		return nil, e
	}
	return j.diskGate.RUnlock, nil
}
func (j *Journal) reconcileOverallBudget(ctx context.Context) error {
	if j.diskBudget == nil {
		return nil
	}
	j.diskGate.Lock()
	defer j.diskGate.Unlock()
	empty := true
	j.tag2JMap.Range(func(_, value interface{}) bool {
		backend, ok := value.(interface{ HasUnacknowledgedData() bool })
		if !ok {
			empty = false
			return false
		}
		if backend.HasUnacknowledgedData() {
			empty = false
			return false
		}
		return true
	})
	return j.diskBudget.Reconcile(ctx, empty)
}
func (j *Journal) requireBudgetRoot() error {
	if j.DiskBudget != nil {
		st, e := os.Lstat(j.BufDirPath)
		if e != nil {
			return e
		}
		if !st.IsDir() || st.Mode().Perm()&0077 != 0 {
			return errors.New("overall budget requires a preprovisioned private journal root")
		}
	} else {
		if _, e := os.Lstat(filepath.Join(j.BufDirPath, ".go-fluentd-disk-owner.lock")); e == nil {
			return errors.New("journal root requires its retained overall disk budget")
		}
	}
	return nil
}
