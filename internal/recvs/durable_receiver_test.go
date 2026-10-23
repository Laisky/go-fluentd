package recvs

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IBM/sarama"
	journal "github.com/Laisky/go-journal/v2"
	"github.com/Laisky/go-utils"
	"github.com/tinylib/msgp/msgp"
	"gofluentd/library"
	"reflect"
)

// TestDurableAdmissionBarrierAndOwnership verifies that publication alone is not success.
func TestDurableAdmissionBarrierAndOwnership(t *testing.T) {
	r := &BaseRecv{msgPool: recvPool()}
	out := make(chan *library.FluentMsg, 1)
	r.SetSyncOutChan(out)
	done := make(chan error, 1)
	go func() { done <- r.SubmitDurable(context.Background(), r.newMsg()) }()
	msg := recvTake(t, out)
	select {
	case <-done:
		t.Fatal("publication falsely accepted")
	case <-time.After(15 * time.Millisecond):
	}
	failure := errors.New("synthetic capacity refusal")
	msg.CompleteAcceptance(failure)
	durableCheck.ErrorIs(t, <-done, failure)
	receipt, err := r.BeginDurable(context.Background(), r.newMsg())
	durableCheck.NoError(t, err)
	msg = recvTake(t, out)
	msg.CompleteAcceptance(nil)
	durableCheck.NoError(t, WaitDurable(context.Background(), receipt))
	closed := make(chan error)
	close(closed)
	durableCheck.Error(t, WaitDurable(context.Background(), closed))
}

// TestDurableAdmissionCancellationRetainsPipelineOwnership checks both sides of handoff.
func TestDurableAdmissionCancellationRetainsPipelineOwnership(t *testing.T) {
	r := &BaseRecv{msgPool: recvPool()}
	out := make(chan *library.FluentMsg, 1)
	r.SetSyncOutChan(out)
	before, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := r.BeginDurable(before, r.newMsg())
	durableCheck.ErrorIs(t, err, context.Canceled)
	durableCheck.Empty(t, out)
	after, stop := context.WithCancel(context.Background())
	receipt, err := r.BeginDurable(after, r.newMsg())
	durableCheck.NoError(t, err)
	msg := recvTake(t, out)
	stop()
	durableCheck.ErrorIs(t, WaitDurable(after, receipt), ErrAdmissionUncertain)
	durableCheck.NotNil(t, msg.DurableAck)
	// A late result must fit the buffered receipt even after the caller has left.
	completed := make(chan struct{})
	go func() { msg.CompleteAcceptance(nil); close(completed) }()
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("late receipt blocked")
	}
	durableCheck.NoError(t, WaitDurable(context.Background(), receipt))
}

// TestDurableLegacyHTTPOverridesQueueAcknowledgment verifies the strict runtime switch.
func TestDurableLegacyHTTPOverridesQueueAcknowledgment(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "refused", true: "accepted"}[accepted], func(t *testing.T) {
			r, e, async := componentHTTP(t)
			out := make(chan *library.FluentMsg, 1)
			r.SetSyncOutChan(out)
			r.SetDurableAdmission(true)
			body, err := json.Marshal(signedHTTP(utils.Clock.GetUTCNow().Format(time.RFC3339)))
			durableCheck.NoError(t, err)
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				defer close(done)
				e.ServeHTTP(w, httptest.NewRequest("POST", "/logs/sit", bytes.NewReader(body)))
			}()
			msg := recvTake(t, out)
			durableCheck.Empty(t, async)
			select {
			case <-done:
				t.Fatal("HTTP replied before barrier")
			case <-time.After(15 * time.Millisecond):
			}
			if accepted {
				msg.CompleteAcceptance(nil)
			} else {
				msg.CompleteAcceptance(errors.New("full"))
			}
			<-done
			if accepted {
				durableCheck.Equal(t, http.StatusOK, w.Code)
			} else {
				durableCheck.Equal(t, http.StatusServiceUnavailable, w.Code)
			}
		})
	}
}

// durableFluentFixture creates a strict loopback receiver with observable admission receipts.
func durableFluentFixture(t *testing.T) (*FluentdRecv, chan *library.FluentMsg) {
	t.Helper()
	r := ingressReceiver(t, FluentIngressCfg{FrameTimeout: time.Second})
	r.SetDurableAdmission(true)
	out := make(chan *library.FluentMsg, 16)
	r.SetSyncOutChan(out)
	return r, out
}

// writeDurableFluent writes a complete synthetic frame with an independent completion result.
func writeDurableFluent(t *testing.T, c net.Conn, frame library.FluentBatchMsg) <-chan error {
	t.Helper()
	wire, err := frame.MarshalMsg(nil)
	durableCheck.NoError(t, err)
	done := make(chan error, 1)
	go func() { _, err := c.Write(wire); done <- err }()
	return done
}

// readDurableFluentACK decodes exactly one MessagePack acknowledgment.
func readDurableFluentACK(t *testing.T, c net.Conn) interface{} {
	t.Helper()
	durableCheck.NoError(t, c.SetReadDeadline(time.Now().Add(time.Second)))
	reader := msgp.NewReader(c)
	n, err := reader.ReadMapHeader()
	durableCheck.NoError(t, err)
	durableCheck.Equal(t, uint32(1), n)
	key, err := reader.ReadString()
	durableCheck.NoError(t, err)
	durableCheck.Equal(t, "ack", key)
	value, err := reader.ReadIntf()
	durableCheck.NoError(t, err)
	return value
}

// TestDurableFluentACKBarrierAndOriginalMetadata verifies ACK timing, replay duplicates and context.
func TestDurableFluentACKBarrierAndOriginalMetadata(t *testing.T) {
	r, out := durableFluentFixture(t)
	client, _ := ingressPipe(t, r)
	frame := library.FluentBatchMsg{"logs", int64(123), map[string]interface{}{"value": "original", "tag": "source-field"}, map[string]interface{}{"chunk": "same-chunk", "custom": "retained"}}
	var previous int64
	for attempt := 0; attempt < 2; attempt++ {
		written := writeDurableFluent(t, client, frame)
		msg := recvTake(t, out)
		durableCheck.NoError(t, <-written)
		durableCheck.Equal(t, "source-field", msg.Message["tag"])
		durableCheck.Equal(t, int64(123), msg.SourceMetadata["timestamp"])
		durableCheck.Equal(t, "retained", msg.SourceMetadata["options"].(map[string]interface{})["custom"])
		if attempt > 0 {
			durableCheck.NotEqual(t, previous, msg.ID)
		}
		previous = msg.ID
		durableCheck.NoError(t, client.SetReadDeadline(time.Now().Add(15*time.Millisecond)))
		var b [1]byte
		_, err := client.Read(b[:])
		durableCheck.Error(t, err)
		msg.CompleteAcceptance(nil)
		durableCheck.Equal(t, "same-chunk", readDurableFluentACK(t, client))
	}
}

// TestDurableFluentRefusalNeverACKsBatchPrefix verifies non-atomic prefix persistence is visible.
func TestDurableFluentRefusalNeverACKsBatchPrefix(t *testing.T) {
	r, out := durableFluentFixture(t)
	client, done := ingressPipe(t, r)
	frame := library.FluentBatchMsg{"logs", []interface{}{[]interface{}{0, map[string]interface{}{"n": 1}}, []interface{}{1, map[string]interface{}{"n": 2}}}, map[string]interface{}{"chunk": "batch"}}
	written := writeDurableFluent(t, client, frame)
	first := recvTake(t, out)
	first.CompleteAcceptance(nil)
	second := recvTake(t, out)
	second.CompleteAcceptance(errors.New("full after prefix"))
	durableCheck.NoError(t, <-written)
	waitIngressClosed(t, done)
	var b [1]byte
	n, err := client.Read(b[:])
	durableCheck.Zero(t, n)
	durableCheck.Error(t, err)
	durableCheck.Empty(t, out)
}

// TestDurableFluentFullValidationPublishesNothing rejects malformed or incompatible complete frames.
func TestDurableFluentFullValidationPublishesNothing(t *testing.T) {
	for name, frame := range map[string]library.FluentBatchMsg{
		"missing-chunk":       {"logs", 0, map[string]interface{}{"n": 1}},
		"oversized-chunk":     {"logs", 0, map[string]interface{}{"n": 1}, map[string]interface{}{"chunk": string(bytes.Repeat([]byte("x"), maximumFluentChunkBytes+1))}},
		"invalid-later-entry": {"logs", []interface{}{[]interface{}{0, map[string]interface{}{"n": 1}}, []interface{}{1, "invalid"}}, map[string]interface{}{"chunk": "batch"}},
		"unknown-compression": {"logs", []byte{0x92, 0, 0x80}, map[string]interface{}{"chunk": "batch", "compressed": "unknown"}},
	} {
		t.Run(name, func(t *testing.T) {
			r, out := durableFluentFixture(t)
			client, done := ingressPipe(t, r)
			written := writeDurableFluent(t, client, frame)
			durableCheck.NoError(t, <-written)
			waitIngressClosed(t, done)
			durableCheck.Empty(t, out)
		})
	}
}

// TestDurableFluentPackedGzipAndBestEffortKeepBarrier covers bounded packed decoding and explicit opt-in.
func TestDurableFluentPackedGzipAndBestEffortKeepBarrier(t *testing.T) {
	for _, compression := range []string{"text", "gzip"} {
		t.Run(compression, func(t *testing.T) {
			r, out := durableFluentFixture(t)
			client, _ := ingressPipe(t, r)
			packed, err := (library.FluentBatchMsg{int64(456), map[string]interface{}{"n": 1}}).MarshalMsg(nil)
			durableCheck.NoError(t, err)
			if compression == "gzip" {
				var b bytes.Buffer
				gz := gzip.NewWriter(&b)
				_, err = gz.Write(packed)
				durableCheck.NoError(t, err)
				durableCheck.NoError(t, gz.Close())
				packed = b.Bytes()
			}
			written := writeDurableFluent(t, client, library.FluentBatchMsg{"logs", packed, map[string]interface{}{"chunk": []byte("binary"), "compressed": compression}})
			msg := recvTake(t, out)
			durableCheck.Equal(t, int64(456), msg.SourceMetadata["timestamp"])
			msg.CompleteAcceptance(nil)
			durableCheck.NoError(t, <-written)
			durableCheck.Equal(t, []byte("binary"), readDurableFluentACK(t, client))
		})
	}
	r, out := durableFluentFixture(t)
	r.BestEffort = true
	client, done := ingressPipe(t, r)
	written := writeDurableFluent(t, client, library.FluentBatchMsg{"logs", 0, map[string]interface{}{"n": 1}})
	msg := recvTake(t, out)
	msg.CompleteAcceptance(errors.New("capacity"))
	durableCheck.NoError(t, <-written)
	waitIngressClosed(t, done)
	durableCheck.Empty(t, out)
}

// durableKafkaSession records marks safely while a concurrent claim is running.
type durableKafkaSession struct {
	sarama.ConsumerGroupSession
	ctx     context.Context
	marked  chan *sarama.ConsumerMessage
	commits atomic.Int64
}

// Context returns this synthetic session's cancellation boundary.
func (s *durableKafkaSession) Context() context.Context { return s.ctx }

// MarkMessage records only offsets explicitly admitted by the claim.
func (s *durableKafkaSession) MarkMessage(m *sarama.ConsumerMessage, _ string) { s.marked <- m }

// Commit counts broker commit requests without performing network I/O.
func (s *durableKafkaSession) Commit() { s.commits.Add(1) }

// durableKafkaClaim starts a bounded synthetic partition with the actual consumer handler.
func durableKafkaClaim(t *testing.T, ctx context.Context, bodies ...string) (*KafkaRecv, *durableKafkaSession, *upgradeKafkaClaim, chan *library.FluentMsg, <-chan error) {
	t.Helper()
	r := upgradeKafkaRecv()
	r.IsJSONFormat = true
	r.IntervalNum = 1
	r.SetDurableAdmission(true)
	out := make(chan *library.FluentMsg, 1)
	r.SetSyncOutChan(out)
	session := &durableKafkaSession{ctx: ctx, marked: make(chan *sarama.ConsumerMessage, 16)}
	claim := &upgradeKafkaClaim{messages: make(chan *sarama.ConsumerMessage, len(bodies))}
	for i, body := range bodies {
		claim.messages <- &sarama.ConsumerMessage{Topic: "synthetic", Partition: 2, Offset: int64(10 + i), Value: []byte(body), Key: []byte("key")}
	}
	close(claim.messages)
	done := make(chan error, 1)
	go func() { done <- (&kafkaGroupHandler{recv: r}).ConsumeClaim(session, claim) }()
	return r, session, claim, out, done
}

// TestDurableKafkaFailedGapBlocksLaterOffsetsAndCancels verifies no offset can skip a refusal.
func TestDurableKafkaFailedGapBlocksLaterOffsetsAndCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, session, claim, out, done := durableKafkaClaim(t, ctx, `{"n":1}`, `{"n":2}`, `{"n":3}`)
	first := recvTake(t, out)
	select {
	case <-session.marked:
		t.Fatal("marked before barrier")
	case <-time.After(15 * time.Millisecond):
	}
	first.CompleteAcceptance(nil)
	durableCheck.Equal(t, int64(10), (<-session.marked).Offset)
	failed := recvTake(t, out)
	oldID := failed.ID
	durableCheck.Equal(t, int64(11), failed.SourceMetadata["offset"])
	durableCheck.Equal(t, []byte(`{"n":2}`), failed.SourceMetadata["value"])
	failed.CompleteAcceptance(errors.New("full"))
	retry := recvTake(t, out)
	durableCheck.NotEqual(t, oldID, retry.ID)
	durableCheck.Equal(t, int64(11), retry.SourceMetadata["offset"])
	durableCheck.Len(t, claim.messages, 1)
	select {
	case <-session.marked:
		t.Fatal("failed gap advanced")
	default:
	}
	cancel()
	retry.CompleteAcceptance(nil)
	durableCheck.NoError(t, <-done)
	durableCheck.Empty(t, session.marked)
	durableCheck.Equal(t, int64(1), session.commits.Load())
}

// TestDurableKafkaRecoveryAndMalformedInputPreserveBrokerOwnership covers recovery and poison input.
func TestDurableKafkaRecoveryAndMalformedInputPreserveBrokerOwnership(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, session, _, out, done := durableKafkaClaim(t, ctx, `{"n":1}`, `{"n":2}`)
	msg := recvTake(t, out)
	msg.CompleteAcceptance(errors.New("full"))
	msg = recvTake(t, out)
	msg.CompleteAcceptance(nil)
	durableCheck.Equal(t, int64(10), (<-session.marked).Offset)
	msg = recvTake(t, out)
	msg.CompleteAcceptance(nil)
	durableCheck.Equal(t, int64(11), (<-session.marked).Offset)
	durableCheck.NoError(t, <-done)
	_, poison, claim, poisonOut, poisonDone := durableKafkaClaim(t, ctx, "null", `{"n":2}`)
	durableCheck.NoError(t, <-poisonDone)
	durableCheck.Empty(t, poison.marked)
	durableCheck.Empty(t, poisonOut)
	durableCheck.Len(t, claim.messages, 1)
}

// TestDurableKafkaMetadataBoundsAndOriginalOwnership checks raw source copies and finite limits.
func TestDurableKafkaMetadataBoundsAndOriginalOwnership(t *testing.T) {
	record := &sarama.ConsumerMessage{Topic: "t", Partition: 1, Offset: 3, Key: []byte("k"), Value: []byte("v"), Headers: []*sarama.RecordHeader{{Key: []byte("h"), Value: []byte("original")}}}
	meta, err := kafkaSourceMetadata(record)
	durableCheck.NoError(t, err)
	record.Key[0] = 'x'
	record.Value[0] = 'x'
	record.Headers[0].Value[0] = 'x'
	durableCheck.Equal(t, []byte("k"), meta["key"])
	durableCheck.Equal(t, []byte("v"), meta["value"])
	durableCheck.Equal(t, []byte("original"), meta["headers"].([]interface{})[0].(map[string]interface{})["value"])
	_, err = kafkaSourceMetadata(&sarama.ConsumerMessage{Value: make([]byte, kafkaRecordLimit), Key: []byte("overflow")})
	durableCheck.Error(t, err)
	_, err = kafkaSourceMetadata(&sarama.ConsumerMessage{Headers: make([]*sarama.RecordHeader, 4097)})
	durableCheck.Error(t, err)
}

// TestDurableReceiverPoolResetClearsReceiptAndSourceContext prevents ownership inheritance.
func TestDurableReceiverPoolResetClearsReceiptAndSourceContext(t *testing.T) {
	r := &BaseRecv{msgPool: &sync.Pool{New: func() interface{} {
		return &library.FluentMsg{DurableAck: make(chan error, 1), SourceMetadata: map[string]interface{}{"stale": true}}
	}}}
	msg := r.newMsg()
	durableCheck.Nil(t, msg.DurableAck)
	durableCheck.Nil(t, msg.SourceMetadata)
}

// durableAssertions provides local fatal assertions without adding a module dependency.
type durableAssertions struct{}

var durableCheck durableAssertions

// NoError requires successful completion.
func (durableAssertions) NoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Error requires a rejected operation.
func (durableAssertions) Error(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
}

// ErrorIs requires an error preserving its underlying cause.
func (durableAssertions) ErrorIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error %v does not wrap %v", err, target)
	}
}

// Equal requires deeply identical synthetic values.
func (durableAssertions) Equal(t *testing.T, want, got interface{}) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("value mismatch: want %#v (%T), got %#v (%T)", want, want, got, got)
	}
}

// NotEqual requires distinct synthetic values.
func (durableAssertions) NotEqual(t *testing.T, a, b interface{}) {
	t.Helper()
	if reflect.DeepEqual(a, b) {
		t.Fatalf("values unexpectedly equal: %#v", a)
	}
}

// Empty requires a zero-length collection or channel.
func (durableAssertions) Empty(t *testing.T, v interface{}) {
	t.Helper()
	if reflect.ValueOf(v).Len() != 0 {
		t.Fatalf("collection is not empty: %T", v)
	}
}

// Len requires an exact bounded collection size.
func (durableAssertions) Len(t *testing.T, v interface{}, want int) {
	t.Helper()
	if n := reflect.ValueOf(v).Len(); n != want {
		t.Fatalf("length %d, want %d", n, want)
	}
}

// Nil requires an absent pointer, map, slice or channel.
func (durableAssertions) Nil(t *testing.T, v interface{}) {
	t.Helper()
	if v != nil && !reflect.ValueOf(v).IsNil() {
		t.Fatalf("expected nil: %T", v)
	}
}

// NotNil requires a present pointer, map, slice or channel.
func (durableAssertions) NotNil(t *testing.T, v interface{}) {
	t.Helper()
	if v == nil || reflect.ValueOf(v).IsNil() {
		t.Fatalf("unexpected nil: %T", v)
	}
}

// Zero requires the type's zero value.
func (durableAssertions) Zero(t *testing.T, v interface{}) {
	t.Helper()
	if !reflect.ValueOf(v).IsZero() {
		t.Fatalf("expected zero: %#v", v)
	}
}

// TestDurableReceiverSourceMetadataNativeCodec preserves actual Kafka time and Fluent EventTime.
func TestDurableReceiverSourceMetadataNativeCodec(t *testing.T) {
	timestamp := time.Date(2026, 10, 18, 17, 30, 1, 123456789, time.UTC)
	kafka, err := kafkaSourceMetadata(&sarama.ConsumerMessage{Topic: "topic", Partition: 3, Offset: 9, Timestamp: timestamp, BlockTimestamp: timestamp.Add(-time.Second), Value: []byte("original")})
	durableCheck.NoError(t, err)
	eventTime := &msgp.RawExtension{Type: 0, Data: []byte{0, 0, 0, 12, 7, 91, 205, 21}}
	for name, metadata := range map[string]map[string]interface{}{"kafka": kafka, "fluent": {"protocol": "forward", "timestamp": eventTime, "options": map[string]interface{}{"chunk": []byte("original-chunk")}}} {
		t.Run(name, func(t *testing.T) {
			original := &journal.Data{ID: 1, Data: map[string]interface{}{"tag": "logs", "message": map[string]interface{}{"timestamp": "payload-field"}, "source_metadata": metadata}}
			wire, err := original.MarshalMsg(nil)
			durableCheck.NoError(t, err)
			var recovered journal.Data
			rest, err := recovered.UnmarshalMsg(wire)
			durableCheck.NoError(t, err)
			durableCheck.Empty(t, rest)
			actual := recovered.Data["source_metadata"].(map[string]interface{})
			if name == "kafka" {
				got, ok := actual["timestamp"].(time.Time)
				if !ok {
					t.Fatalf("timestamp type %T", actual["timestamp"])
				}
				if !timestamp.Equal(got) {
					t.Fatal("Kafka timestamp changed")
				}
				block, ok := actual["block_timestamp"].(time.Time)
				if !ok || !timestamp.Add(-time.Second).Equal(block) {
					t.Fatal("Kafka block timestamp changed")
				}
				durableCheck.Equal(t, []byte("original"), actual["value"])
			} else {
				got, ok := actual["timestamp"].(*msgp.RawExtension)
				if !ok {
					t.Fatalf("EventTime type %T", actual["timestamp"])
				}
				durableCheck.Equal(t, eventTime.Type, got.Type)
				durableCheck.Equal(t, eventTime.Data, got.Data)
				durableCheck.Equal(t, []byte("original-chunk"), actual["options"].(map[string]interface{})["chunk"])
			}
			durableCheck.Equal(t, "payload-field", recovered.Data["message"].(map[string]interface{})["timestamp"])
		})
	}
}

// TestDurableFluentFailedChunkRetryDuplicatesOnlyRetainedPrefix links refusal to whole-chunk replay.
func TestDurableFluentFailedChunkRetryDuplicatesOnlyRetainedPrefix(t *testing.T) {
	r, out := durableFluentFixture(t)
	frame := library.FluentBatchMsg{"logs", []interface{}{[]interface{}{0, map[string]interface{}{"n": 1}}, []interface{}{1, map[string]interface{}{"n": 2}}}, map[string]interface{}{"chunk": "retry-batch"}}
	firstClient, firstDone := ingressPipe(t, r)
	written := writeDurableFluent(t, firstClient, frame)
	prefix := recvTake(t, out)
	prefixID, prefixValue := prefix.ID, prefix.Message["n"]
	prefix.CompleteAcceptance(nil)
	rejected := recvTake(t, out)
	rejected.CompleteAcceptance(errors.New("synthetic full cap"))
	durableCheck.NoError(t, <-written)
	waitIngressClosed(t, firstDone)
	var b [1]byte
	n, err := firstClient.Read(b[:])
	durableCheck.Zero(t, n)
	durableCheck.Error(t, err)
	retryClient, _ := ingressPipe(t, r)
	written = writeDurableFluent(t, retryClient, frame)
	duplicate := recvTake(t, out)
	durableCheck.NotEqual(t, prefixID, duplicate.ID)
	durableCheck.Equal(t, prefixValue, duplicate.Message["n"])
	duplicate.CompleteAcceptance(nil)
	tail := recvTake(t, out)
	durableCheck.Equal(t, int64(2), tail.Message["n"])
	tail.CompleteAcceptance(nil)
	durableCheck.NoError(t, <-written)
	durableCheck.Equal(t, "retry-batch", readDurableFluentACK(t, retryClient))
	durableCheck.Equal(t, uint64(3), r.DurableAdmissionStats()["accepted"])
	durableCheck.Equal(t, uint64(1), r.DurableAdmissionStats()["refused"])
}

// TestDurableFluentCanceledPublishedFrameNeverACKs preserves late journal ownership after disconnect.
func TestDurableFluentCanceledPublishedFrameNeverACKs(t *testing.T) {
	r, out := durableFluentFixture(t)
	server, client := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer server.Close()
	defer client.Close()
	done := make(chan struct{})
	go func() { defer close(done); r.decodeMsg(ctx, server) }()
	written := writeDurableFluent(t, client, library.FluentBatchMsg{"logs", 0, map[string]interface{}{"n": 1}, map[string]interface{}{"chunk": "cancel"}})
	msg := recvTake(t, out)
	durableCheck.NoError(t, <-written)
	cancel()
	waitIngressClosed(t, done)
	var b [1]byte
	n, err := client.Read(b[:])
	durableCheck.Zero(t, n)
	durableCheck.Error(t, err)
	durableCheck.Equal(t, uint64(1), r.DurableAdmissionStats()["uncertain"])
	completed := make(chan struct{})
	go func() { msg.CompleteAcceptance(nil); close(completed) }()
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("late barrier callback blocked")
	}
}

// TestDurableFluentGzipCorruptionAndExpansionPublishNothing checks both compressed input boundaries.
func TestDurableFluentGzipCorruptionAndExpansionPublishNothing(t *testing.T) {
	for _, mode := range []string{"crc", "expansion"} {
		t.Run(mode, func(t *testing.T) {
			r, out := durableFluentFixture(t)
			r.Ingress.MaxFrameBytes = 1024
			r.Ingress.MaxValueBytes = 1024
			body := "small"
			if mode == "expansion" {
				body = string(bytes.Repeat([]byte("x"), 2048))
			}
			packed, err := (library.FluentBatchMsg{int64(1), map[string]interface{}{"body": body}}).MarshalMsg(nil)
			durableCheck.NoError(t, err)
			var b bytes.Buffer
			gz := gzip.NewWriter(&b)
			_, err = gz.Write(packed)
			durableCheck.NoError(t, err)
			durableCheck.NoError(t, gz.Close())
			wire := bytes.Clone(b.Bytes())
			if mode == "crc" {
				wire[len(wire)-8] ^= 0xff
			}
			client, done := ingressPipe(t, r)
			written := writeDurableFluent(t, client, library.FluentBatchMsg{"logs", wire, map[string]interface{}{"chunk": "reject", "compressed": "gzip"}})
			durableCheck.NoError(t, <-written)
			waitIngressClosed(t, done)
			durableCheck.Empty(t, out)
		})
	}
}

// durableReturningKafkaGroup simulates a voluntary claim exit ending a Sarama session.
type durableReturningKafkaGroup struct {
	calls  atomic.Int64
	errors chan error
}

// Consume returns an ended session without a transport error.
func (g *durableReturningKafkaGroup) Consume(context.Context, []string, sarama.ConsumerGroupHandler) error {
	g.calls.Add(1)
	return nil
}

// Errors exposes the bounded group's asynchronous error channel.
func (g *durableReturningKafkaGroup) Errors() <-chan error { return g.errors }

// Close completes without network or offset side effects.
func (g *durableReturningKafkaGroup) Close() error { return nil }

// TestDurableKafkaVoluntarySessionExitBackoff prevents hot poison-record rejoin loops.
func TestDurableKafkaVoluntarySessionExitBackoff(t *testing.T) {
	r := upgradeKafkaRecv()
	r.SetDurableAdmission(true)
	group := &durableReturningKafkaGroup{errors: make(chan error)}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	r.consumeGroup(ctx, group)
	calls := group.calls.Load()
	if calls < 1 || calls > 4 {
		t.Fatalf("unbounded strict session rejoin count %d", calls)
	}
}
