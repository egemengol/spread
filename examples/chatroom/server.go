package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/egemengol/spread"

	"nhooyr.io/websocket"
)

const ADDR = "localhost:8000"

func HandlePublish(topic *spread.Topic[Message]) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg Message
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.Body.Close()

		/// SPREAD SECTION START
		// Publish the message to the topic

		if err := topic.Publish(msg); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		/// SPREAD SECTION END

		w.WriteHeader(http.StatusNoContent)
	})
}

func HandleSubscribe(topic *spread.Topic[Message]) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			log.Printf("error accepting websocket: %s\n", err)
			return
		}
		defer conn.CloseNow()

		/// SPREAD SECTION START
		// Subscribe to the topic, get a receive channel

		recvChan, removeRecvChan, err := topic.GetRecvChannel(20)
		if err != nil {
			log.Printf("error getting recv channel: %s\n", err)
			return
		}
		defer removeRecvChan()

		for {
			select {
			case <-r.Context().Done():
				log.Printf("context done: %s\n", r.Context().Err())
				return
			case msg, ok := <-recvChan:
				if !ok {
					log.Fatal("topic closed the recv channel")
				}
				data, err := json.Marshal(msg)
				if err != nil {
					log.Printf("error marshaling message: %s\n", err)
					return
				}
				if err := conn.Write(r.Context(), websocket.MessageText, data); err != nil {
					// There should be a better way of handling disconnects
					// for now we just log the "broken pipe" error and return
					log.Printf("error writing message: %s\n", err)
					return
				}
			}
		}

		/// SPREAD SECTION END
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

	/// SPREAD SECTION START
	// Create a topic with a buffer size of 20 messages

	topic := spread.NewTopic[Message](ctx, 20)

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir(".")))
	mux.Handle("POST /publish", HandlePublish(topic))
	mux.Handle("/subscribe", HandleSubscribe(topic))

	// Log messages flowing through the topic
	topic.HandleSync(func(msg Message) {
		log.Printf("%s: %s\n", msg.Username, msg.Message)
	})

	/// SPREAD SECTION END

	httpServer := &http.Server{
		Addr:         ADDR,
		Handler:      mux,
		ReadTimeout:  time.Second * 10,
		WriteTimeout: time.Second * 10,
	}

	go func() {
		log.Printf("listening on %s\n", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "error listening and serving: %s\n", err)
		}
	}()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
		shutdownCtx := context.Background()
		shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "error shutting down http server: %s\n", err)
		}
	}()
	wg.Wait()
	return nil
}

func main() {
	ctx := context.Background()
	if err := Run(ctx); err != nil {
		log.Fatal(err)
	}
}
