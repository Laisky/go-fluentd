// Package recvs defines different kind of receivers.
//
// recvs are components applied in acceptor. Each recv can
// receiving specific kind of messages. All recv should
// satisfy `library.AcceptorRecvItf`.
package recvs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"gofluentd/library"

	"github.com/Laisky/go-utils"
)

var json = utils.JSON

type AcceptorRecvItf interface {
	SetDurableAdmission(bool)
	SetSyncOutChan(chan<- *library.FluentMsg)
	SetAsyncOutChan(chan<- *library.FluentMsg)
	SetMsgPool(*sync.Pool)
	SetCounter(library.CounterIft)
	Run(context.Context)
	GetName() string
}

type BaseRecv struct {
	syncOutChan        chan<- *library.FluentMsg
	asyncOutChan       chan<- *library.FluentMsg
	msgPool            *sync.Pool
	counter            library.CounterIft
	durableAdmission   bool
	admissionAccepted  atomic.Uint64
	admissionRefused   atomic.Uint64
	admissionUncertain atomic.Uint64
}

func (r *BaseRecv) SetSyncOutChan(outchan chan<- *library.FluentMsg) {
	r.syncOutChan = outchan
}

func (r *BaseRecv) SetAsyncOutChan(outchan chan<- *library.FluentMsg) {
	r.asyncOutChan = outchan
}

func (r *BaseRecv) SetMsgPool(msgPool *sync.Pool) {
	r.msgPool = msgPool
}

func (r *BaseRecv) SetCounter(counter library.CounterIft) {
	r.counter = counter
}

// newMsg starts a new receiver-owned record. Pool contents may come from failed
// deliveries or concatenation and must never carry acknowledgement IDs forward.
func (r *BaseRecv) newMsg() *library.FluentMsg {
	msg := r.msgPool.Get().(*library.FluentMsg)
	*msg = library.FluentMsg{}
	return msg
}

// ErrAdmissionUncertain means publication transferred ownership before the caller stopped waiting.
// A retry may duplicate a record already appended or synchronized by the pipeline.
var ErrAdmissionUncertain = errors.New("durable admission outcome uncertain")

// SetDurableAdmission enables the common persistence barrier before receivers acknowledge input.
// Configure it before Run or HTTP serving; runtime mutation is not supported.
func (r *BaseRecv) SetDurableAdmission(enabled bool) { r.durableAdmission = enabled }

// BeginDurable takes ownership of msg and publishes it with a one-shot buffered receipt.
// Before publication only this helper may recycle msg. After publication the pipeline
// owns it even when the caller is canceled; callers retain only the returned receipt.
func (r *BaseRecv) BeginDurable(ctx context.Context, msg *library.FluentMsg) (receiptOut <-chan error, resultErr error) {
	defer func() {
		if resultErr != nil {
			r.admissionRefused.Add(1)
		}
	}()
	if msg == nil {
		return nil, errors.New("nil durable admission message")
	}
	recycle := func() {
		if r.msgPool != nil {
			r.msgPool.Put(msg)
		}
	}
	if ctx == nil || r.syncOutChan == nil {
		recycle()
		return nil, errors.New("durable admission unavailable")
	}
	if err := ctx.Err(); err != nil {
		recycle()
		return nil, err
	}
	receipt := make(chan error, 1)
	msg.DurableAck = receipt
	select {
	case r.syncOutChan <- msg:
		return receipt, nil
	case <-ctx.Done():
		msg.DurableAck = nil
		recycle()
		return nil, ctx.Err()
	}
}

// WaitDurable waits for the journal's completed barrier without reclaiming pipeline ownership.
func WaitDurable(ctx context.Context, receipt <-chan error) error {
	if ctx == nil || receipt == nil {
		return errors.New("durable admission receipt unavailable")
	}
	select {
	case err, ok := <-receipt:
		if !ok {
			return errors.New("durable admission receipt closed without result")
		}
		return err
	default:
	}
	select {
	case err, ok := <-receipt:
		if !ok {
			return errors.New("durable admission receipt closed without result")
		}
		return err
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", ErrAdmissionUncertain, ctx.Err())
	}
}

// SubmitDurable takes ownership of msg and succeeds only after its journal barrier succeeds.
func (r *BaseRecv) SubmitDurable(ctx context.Context, msg *library.FluentMsg) error {
	receipt, err := r.BeginDurable(ctx, msg)
	if err != nil {
		return err
	}
	return r.WaitDurable(ctx, receipt)
}

// WaitDurable classifies this receiver's observed admission result without exposing payloads.
func (r *BaseRecv) WaitDurable(ctx context.Context, receipt <-chan error) error {
	err := WaitDurable(ctx, receipt)
	if errors.Is(err, ErrAdmissionUncertain) {
		r.admissionUncertain.Add(1)
	} else if err != nil {
		r.admissionRefused.Add(1)
	} else {
		r.admissionAccepted.Add(1)
	}
	return err
}

// DurableAdmissionStats returns monotonic local helper outcomes, not backend delivery totals.
// An uncertain caller may leave a subsequently accepted WAL record and cannot count its final outcome.
func (r *BaseRecv) DurableAdmissionStats() map[string]uint64 {
	return map[string]uint64{"accepted": r.admissionAccepted.Load(), "refused": r.admissionRefused.Load(), "uncertain": r.admissionUncertain.Load()}
}
