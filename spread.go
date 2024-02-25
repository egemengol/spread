package spread

import (
	"context"
	"fmt"
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
func NewTopic[T any](ctx context.Context, bufSize int) *Topic[T] {
	t := &Topic[T]{
		handlersSync:  make(map[uintptr]handlerSync[T]),
		handlersAsync: make(map[uintptr]handlerAsync[T]),
		recvChannels:  make(map[uintptr]chan T),
		controlChan:   make(chan interface{}),
		dataChan:      make(chan T, bufSize),
		closeChan:     make(chan struct{}),
	}
	go t.run(ctx)
	return t
}

// Subscribes to the topic and invokes the given function for each message.
//
// ***BEWARE! The handler is executed by the broadcaster goroutine that manages the topic, which can halt the topic if the handler is expensive.
func (t *Topic[T]) HandleSync(fn func(T)) (func(), error) {
	if t.closed.Load() {
		return nil, fmt.Errorf("topic is closed")
	}
	key := reflect.ValueOf(fn).Pointer()
	t.controlChan <- handlerSync[T](fn)
	return func() {
		if !t.closed.Load() {
			t.controlChan <- removeHandlerSync(key)
		}
	}, nil
}

// Spawns a new goroutine for each message and invokes the given function.
//
// Making use of the context is recommended for graceful shutdown.
func (t *Topic[T]) HandleAsync(fn func(context.Context, T)) (func(), error) {
	if t.closed.Load() {
		return nil, fmt.Errorf("topic is closed")
	}
	key := reflect.ValueOf(fn).Pointer()
	t.controlChan <- handlerAsync[T](fn)
	return func() {
		if !t.closed.Load() {
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
		return nil, nil, fmt.Errorf("topic is closed")
	}

	ch := make(chan T, bufSize)
	t.controlChan <- newRecvChannel[T](ch)
	return ch, func() {
		if !t.closed.Load() {
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
func (t *Topic[T]) Close() {
	if t.closed.CompareAndSwap(false, true) {
		close(t.closeChan)
		for range t.controlChan {
			// drain controlChan
		}
		close(t.controlChan)
		close(t.dataChan)
	}
}

// Publishes a message to the topic.
//
// Same semantics as sending to a buffered channel.
func (t *Topic[T]) Publish(data T) error {
	if t.closed.Load() {
		return fmt.Errorf("topic is closed")
	}
	t.dataChan <- data
	return nil
}

func (t *Topic[T]) handle(ctx context.Context, data T) {
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
			close(t.recvChannels[i])
			delete(t.recvChannels, i)
		}
	}
}

func (t *Topic[T]) run(ctx context.Context) error {
	if t.closed.Load() {
		return fmt.Errorf("topic is closed")
	}
	for {
		select {
		case <-ctx.Done():
			t.Close()
		case ctrl := <-t.controlChan:
			switch v := ctrl.(type) {
			case handlerSync[T]:
				key := reflect.ValueOf(v).Pointer()
				t.handlersSync[key] = v
			case removeHandlerSync:
				delete(t.handlersSync, uintptr(v))
			case handlerAsync[T]:
				key := reflect.ValueOf(v).Pointer()
				t.handlersAsync[key] = v
			case removeHandlerAsync:
				delete(t.handlersAsync, uintptr(v))
			case newRecvChannel[T]:
				key := reflect.ValueOf(v).Pointer()
				t.recvChannels[key] = v
			case removeRecvChannel:
				delete(t.recvChannels, uintptr(v))
			}
		case data := <-t.dataChan:
			t.handle(ctx, data)
		case <-t.closeChan:
			return nil
		}
	}
}

// func main() {
// 	ctx, cancel := context.WithCancel(context.Background())
// 	t := NewTopic[int](ctx, 5)

// 	removeSync, _ := t.HandleSync(func(data int) {
// 		fmt.Printf("sync handler: %d\n", data)
// 	})

// 	recvChan, removeRecvChan, _ := t.GetRecvChannel(2)
// 	go func() {
// 		for data := range recvChan {
// 			fmt.Printf("recv channel: %d\n", data)
// 		}
// 	}()

// 	t.Publish(1)
// 	time.Sleep(100 * time.Millisecond)

// 	removeRecvChan()
// 	time.Sleep(100 * time.Millisecond)

// 	t.Publish(2)
// 	time.Sleep(100 * time.Millisecond)

// 	removeSync()
// 	t.HandleAsync(func(_ctx context.Context, data int) {
// 		fmt.Printf("async handler: %d\n", data)
// 	})
// 	time.Sleep(100 * time.Millisecond)

// 	t.Publish(3)
// 	time.Sleep(100 * time.Millisecond)

// 	cancel()
// 	time.Sleep(100 * time.Millisecond)

// 	err := t.Publish(4)
// 	if err == nil {
// 		log.Fatal("should not be able to publish after closing the topic")
// 	}
// }
