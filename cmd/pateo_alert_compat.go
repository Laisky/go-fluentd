package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap/zapcore"
)

// Preserve the go-utils v1.16 Pateo contract after its removal in v1.17:
// https://github.com/Laisky/go-utils/blob/v1.16.0/logger.go
// This remains the existing optional legacy transport, not a new alert path.
type pateoAlertMessage struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	Time    string `json:"time"`
}

type pateoAlertPusher struct {
	api, token string
	client     *http.Client
	queue      chan pateoAlertMessage
	encoders   sync.Pool
}

func newPateoAlertPusher(ctx context.Context, api, token string) (*pateoAlertPusher, error) {
	p := &pateoAlertPusher{api: api, token: token, client: &http.Client{Timeout: 10 * time.Second}, queue: make(chan pateoAlertMessage, 20)}
	p.encoders.New = func() interface{} { return zapcore.NewJSONEncoder(zapcore.EncoderConfig{}) }
	go p.runSender(ctx)
	return p, nil
}

func (p *pateoAlertPusher) runSender(ctx context.Context) {
	for {
		var message pateoAlertMessage
		select {
		case <-ctx.Done():
			return
		case message = <-p.queue:
		}
		payload, err := json.Marshal(message)
		if err != nil {
			utils.Logger.Debug("marshal pateo alert failed")
			continue
		}
		request, err := http.NewRequest(http.MethodPost, p.api, bytes.NewReader(payload))
		if err != nil {
			utils.Logger.Debug("make pateo alert request failed")
			continue
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+p.token)
		response, err := p.client.Do(request)
		if err != nil {
			utils.Logger.Debug("post pateo alert failed")
			continue
		}
		_, readErr := io.Copy(io.Discard, response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			utils.Logger.Debug("read pateo alert response failed")
		}
		if response.StatusCode/100 != 2 {
			utils.Logger.Debug("pateo alert server returned error")
		}
	}
}

func (p *pateoAlertPusher) Send(title, content string, timestamp time.Time) error {
	select {
	case p.queue <- pateoAlertMessage{Title: title, Content: content, Time: timestamp.Format(time.RFC3339Nano)}:
		return nil
	default:
		return errors.New("sender chan overflow")
	}
}

func (p *pateoAlertPusher) GetZapHook() func(zapcore.Entry, []zapcore.Field) error {
	return func(entry zapcore.Entry, fields []zapcore.Field) error {
		if !zapcore.ErrorLevel.Enabled(entry.Level) {
			return nil
		}
		encoder := p.encoders.Get().(zapcore.Encoder)
		defer p.encoders.Put(encoder)
		buffer, err := encoder.EncodeEntry(entry, fields)
		if err != nil {
			utils.Logger.Debug("encode pateo alert fields failed")
			return nil
		}
		encodedFields := buffer.String()
		buffer.Free()
		content := "logger: " + entry.LoggerName + "\n" +
			"time: " + entry.Time.Format(time.RFC3339Nano) + "\n" +
			"level: " + entry.Level.String() + "\n" +
			"caller: " + entry.Caller.FullPath() + "\n" +
			"stack: " + entry.Stack + "\n" +
			"message: " + entry.Message + "\n" + encodedFields
		if err = p.Send(entry.LoggerName+":"+entry.Message, content, entry.Time); err != nil {
			utils.Logger.Debug("enqueue pateo alert failed")
		}
		return nil
	}
}
