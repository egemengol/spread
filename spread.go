package spread

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"sync/atomic"
)

// / commands that flow through the control channel
type handlerSync[T any] func(T)
type removeHandlerSync uintptr
type handlerAsync[T any] func(context.Context, T)
type removeHandlerAsync uintptr
type newRecvChannel[T any] chan T
type removeRecvChannel uintptr

// A Pub/Sub mechanism that carries a singular message type and supports synchronous and asynchronous subscribers.
//
// All the messages published to the topic are broadcasted to all the subscribers via a dedicated goroutine.
//
// The subscribers can be synchronous or asynchronous.
type Topic[T any] struct {
	log           *slog.Logger
	handlersSync  map[uintptr]handlerSync[T]
	handlersAsync map[uintptr]handlerAsync[T]
	recvChannels  map[uintptr]chan T
	closed        atomic.Bool

	controlChan chan interface{}
	dataChan    chan T
	closeChan   chan struct{}
}

// Sets the inbound data channel buffer size and starts the broadcast goroutine.
//
// Context is used to stop the broadcast goroutine when the context is canceled.
//
// For more details go to [Topic.Close] method.
func NewTopic[T any](ctx context.Context, logger *slog.Logger, bufSize int) *Topic[T] {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	t := &Topic[T]{
		handlersSync:  make(map[uintptr]handlerSync[T]),
		handlersAsync: make(map[uintptr]handlerAsync[T]),
		recvChannels:  make(map[uintptr]chan T),
		controlChan:   make(chan interface{}),
		dataChan:      make(chan T, bufSize),
		closeChan:     make(chan struct{}),
	}
	t.log = logger.With("module", "spread.Topic", "topic", fmt.Sprintf("%p", t))
	go t.run(ctx)
	return t
}

// Subscribes to the topic and invokes the given function for each message.
//
// ***BEWARE! The handler is executed by the broadcaster goroutine that manages the topic, which can halt the topic if the handler is expensive.
func (t *Topic[T]) HandleSync(fn func(T)) (func(), error) {
	if t.closed.Load() {
		t.log.Error("tried to add sync handler to closed topic")
		return nil, fmt.Errorf("topic is closed")
	}
	key := reflect.ValueOf(fn).Pointer()
	t.log.Debug("adding sync handler")
	t.controlChan <- handlerSync[T](fn)
	return func() {
		if !t.closed.Load() {
			t.log.Debug("sync handler is removing itself")
			t.controlChan <- removeHandlerSync(key)
		}
	}, nil
}

// Spawns a new goroutine for each message and invokes the given function.
//
// Making use of the context is recommended for graceful shutdown.
func (t *Topic[T]) HandleAsync(fn func(context.Context, T)) (func(), error) {
	if t.closed.Load() {
		t.log.Error("tried to add async handler to closed topic")
		return nil, fmt.Errorf("topic is closed")
	}
	key := reflect.ValueOf(fn).Pointer()
	t.log.Debug("adding async handler")
	t.controlChan <- handlerAsync[T](fn)
	return func() {
		if !t.closed.Load() {
			t.log.Debug("async handler is removing itself")
			t.controlChan <- removeHandlerSync(key)
		}
	}, nil
}

// Returns a receive-only channel for the topic.
//
// The channel is buffered with the size of bufSize.
//
// When the channel is full, the channel will be closed and the subscriber will be removed from the topic.
//
// The subscriber can remove the channel from the topic by calling the returned function.
//
// This is the recommended way of subscribing to the topic for most use cases.
func (t *Topic[T]) GetRecvChannel(bufSize int) (<-chan T, func(), error) {
	if t.closed.Load() {
		t.log.Error("tried to get recv channel from closed topic")
		return nil, nil, fmt.Errorf("topic is closed")
	}

	ch := make(chan T, bufSize)
	t.log.Debug("adding recv channel")
	t.controlChan <- newRecvChannel[T](ch)
	return ch, func() {
		if !t.closed.Load() {
			t.log.Debug("recv channel is removing itself")
			t.controlChan <- removeRecvChannel(reflect.ValueOf(ch).Pointer())
		}
	}, nil
}

// Closes the topic and stops the broadcast goroutine. Gets called when the context is canceled as well.
//
// Can be called multiple times.
//
//  1. Topic ignores all control commands after the close command.
//  2. Prevents any new messages
//  3. Finishes broadcasting the messages already sent.
//  4. Closes without waiting for the subscribers to finish processing.
func (t *Topic[T]) close() {
	if t.closed.CompareAndSwap(false, true) {
		t.log.Debug("closing topic")
		close(t.closeChan)
		close(t.controlChan)
		for recvChanPtr, recvChan := range t.recvChannels {
			close(recvChan)
			delete(t.recvChannels, recvChanPtr)
		}
		close(t.dataChan)
		t.log.Info("closed topic")
	}
}

// Publishes a message to the topic.
//
// Same semantics as sending to a buffered channel.
func (t *Topic[T]) Publish(data T) error {
	if t.closed.Load() {
		t.log.Error("tried to publish to closed topic", "data", data)
		return fmt.Errorf("topic is closed")
	}
	t.log.Debug("publishing data", "data", data)
	t.dataChan <- data
	t.log.Debug("published data", "data", data)
	return nil
}

func (t *Topic[T]) handle(ctx context.Context, data T) {
	t.log.Debug("handling data", "data", data)
	for i := range t.handlersSync {
		t.handlersSync[i](data)
	}
	for i := range t.handlersAsync {
		go t.handlersAsync[i](ctx, data)
	}
	for i := range t.recvChannels {
		select {
		case t.recvChannels[i] <- data:
		default:
			// TODO: notify the client in some way
			t.log.Warn("recv channel is full, removing it", "recvChan", t.recvChannels[i])
			close(t.recvChannels[i])
			delete(t.recvChannels, i)
		}
	}
	t.log.Debug("handled data", "data", data)
}

func (t *Topic[T]) run(ctx context.Context) error {
	if t.closed.Load() {
		t.log.Error("tried to start closed topic")
		return fmt.Errorf("topic is closed")
	}
	t.log.Info("starting topic")
	for {
		select {
		case <-ctx.Done():
			t.log.Info("topic context got triggered", "err", ctx.Err())
			t.close()
			return nil
		case ctrl := <-t.controlChan:
			switch v := ctrl.(type) {
			case handlerSync[T]:
				key := reflect.ValueOf(v).Pointer()
				t.handlersSync[key] = v
				t.log.Info("added sync handler")
			case removeHandlerSync:
				key := uintptr(v)
				delete(t.handlersSync, key)
				t.log.Info("removed sync handler")
			case handlerAsync[T]:
				key := reflect.ValueOf(v).Pointer()
				t.handlersAsync[key] = v
				t.log.Info("added async handler")
			case removeHandlerAsync:
				key := uintptr(v)
				delete(t.handlersAsync, key)
				t.log.Info("removed async handler")
			case newRecvChannel[T]:
				key := reflect.ValueOf(v).Pointer()
				t.recvChannels[key] = v
				t.log.Info("added recv channel")
			case removeRecvChannel:
				key := uintptr(v)
				close(t.recvChannels[key])
				delete(t.recvChannels, key)
				t.log.Info("removed recv channel")
			}
		case data := <-t.dataChan:
			t.handle(ctx, data)
			t.log.Debug("handled data", "data", data)
		case <-t.closeChan:
			t.log.Debug("topic manager goroutine is exiting")
			return nil
		}
	}
}
