## Spread

[![GoDoc](https://godoc.org/github.com/egemengol/spread?status.svg&style=flat-square)](http://godoc.org/github.com/egemengol/spread)
[![GitHub](https://img.shields.io/badge/GitHub-100000?style=for-the-badge&logo=github&logoColor=white)](https://github.com/egemengol/spread)

An in-process and in-memory PubSub, Broadcast, EventBus or Fanout implementation with type-safe topics implemented with generics. Respects context.

### Subscriber Patterns

These patterns can be used for a given topic at the same time with multiple instances.

#### Channel-based
Every `recvChan` gets its own channel for reading.
```golang
var topic *spread.Topic[int]

recvChan, removeRecvChan, err := topic.GetRecvChannel(20)
for number := range recvChan {
    fmt.Printf("Got from channel: %d\n", number)
}
```

#### Asynchronous

```golang
var topic *spread.Topic[int]

topic.HandleAsync(func(_ctx context.Context, number int) {
    fmt.Printf("Handling in async handler: %d\n", number)
})
```

#### Synchronous

This blocks the topic's progress so better to keep it non-blocking.
```golang
var topic *spread.Topic[int]

topic.HandleSync(func(number int) {
    fmt.Printf("Handling in sync handler: %d\n", number)
})
```

### Performance Characteristics

- Every topic has a inbound channel with a dedicated goroutine for broadcasting.
- Synchronous handlers in `HandleSync` get executed in this goroutine.
- Asynchronous handlers or receiver channels that cannot keep up (with full buffers) get eliminated from the subscribers.
- Publishing is the same as sending to a buffered channel, blocks when full.
