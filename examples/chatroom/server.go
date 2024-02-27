package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/egemengol/spread"

	"nhooyr.io/websocket"
)

const ADDR = "localhost:8000"

func HandlePublish(logger *slog.Logger, topic *spread.Topic[Message]) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg Message
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			logger.Warn("error decoding message", "err", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.Body.Close()

		logger.Info("publishing message", "msg", msg)

		if err := topic.Publish(msg); err != nil {
			logger.Error("error publishing message", "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	})
}

func HandleSubscribe(logger *slog.Logger, topic *spread.Topic[Message]) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			logger.Error("error accepting websocket", "err", err)
			return
		}
		defer conn.CloseNow()

		recvChan, removeRecvChan, err := topic.GetRecvChannel(20)
		if err != nil {
			logger.Error("error getting recv channel", "err", err)
			return
		}
		defer removeRecvChan()

		ctx := conn.CloseRead(r.Context())

		for {
			select {
			case <-ctx.Done():
				logger.Info("client disconnected", "err", ctx.Err())
				return
			case msg, ok := <-recvChan:
				if !ok {
					logger.Info("recv channel closed")
					conn.Close(websocket.StatusGoingAway, "")
					return
				}
				data, err := json.Marshal(msg)
				if err != nil {
					logger.Error("error marshaling message", "err", err)
					return
				}
				if err := conn.Write(r.Context(), websocket.MessageText, data); err != nil {
					logger.Warn("error writing message", "err", err)
					return
				}
				logger.Info("forwarded to listener", "fromUser", msg.Username, "msg", msg.Message)
			}
		}
	})
}

type Message struct {
	Username string `json:"name"`
	Message  string `json:"msg"`
}

func (m *Message) UnmarshalJSON(data []byte) error {
	var raw struct {
		Username string `json:"name"`
		Message  string `json:"msg"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	if raw.Username == "" || raw.Message == "" {
		return errors.New("name and msg are required")
	}

	*m = Message{
		Username: raw.Username,
		Message:  raw.Message,
	}

	return nil
}

func Run(ctx context.Context) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	baseLogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger := baseLogger.With("module", "chatroom")

	topic := spread.NewTopic[Message](ctx, nil, 20)
	// topic := spread.NewTopic[Message](ctx, baseLogger, 20)

	// Log messages flowing through the topic
	topic.HandleSync(func(msg Message) {
		logger.Info("processed by the sync handler", "user", msg.Username, "msg", msg.Message)
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "chatroom/index.html")
	})
	mux.Handle("POST /publish", HandlePublish(logger, topic))
	mux.Handle("/subscribe", HandleSubscribe(logger, topic))

	httpServer := &http.Server{
		Addr:         ADDR,
		Handler:      mux,
		ReadTimeout:  time.Second * 10,
		WriteTimeout: time.Second * 10,
	}

	go func() {
		logger.Info("http server started listening on", "addr", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("error starting http server", "err", err)
		}
	}()

	<-ctx.Done()
	logger.Info("received interrupt, shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("error shutting down http server", "err", err)
	}
	time.Sleep(100 * time.Millisecond) // Wait for websocket connections to close
	logger.Info("shutdown complete")

	return shutdownCtx.Err()

	// var wg sync.WaitGroup
	// wg.Add(1)
	// go func() {
	// 	defer wg.Done()
	// 	<-ctx.Done()
	// 	logger.Info("received interrupt, shutting down")
	// 	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	// 	defer cancel()
	// 	if err := httpServer.Shutdown(shutdownCtx); err != nil {
	// 		logger.Error("error shutting down http server", "err", err)
	// 	}
	// 	time.Sleep(100 * time.Millisecond) // Wait for websocket connections to close
	// }()
	// wg.Wait()
	// return nil
}

func main() {
	ctx := context.Background()
	if err := Run(ctx); err != nil {
		log.Fatal(err)
	}
}
