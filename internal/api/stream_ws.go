package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/coder/websocket"
	"github.com/flitsinc/go-agents/internal/eventbus"
)

type wsWriter interface {
	Write(ctx context.Context, msgType websocket.MessageType, data []byte) error
}

func (s *Server) handleStreamWS(w http.ResponseWriter, r *http.Request) {
	if s.Bus == nil {
		writeError(w, http.StatusInternalServerError, errNotFound("stream bus"))
		return
	}

	streamsParam := r.URL.Query().Get("streams")
	if streamsParam == "" {
		streamsParam = "task_output,errors"
	}
	streamList := splitComma(streamsParam)

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusInternalError, "closed")

	ctx := r.Context()
	if err := streamEvents(ctx, s.Bus, streamList, conn); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "stream error")
		return
	}
	_ = conn.Close(websocket.StatusNormalClosure, "done")
}

func streamEvents(ctx context.Context, bus *eventbus.Bus, streamList []string, writer wsWriter) error {
	sub := bus.Subscribe(ctx, streamList)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case evt, ok := <-sub:
			if !ok {
				return nil
			}
			payload, err := json.Marshal(evt)
			if err != nil {
				return err
			}
			if err := writer.Write(ctx, websocket.MessageText, payload); err != nil {
				return err
			}
		}
	}
}
