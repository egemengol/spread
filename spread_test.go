package spread_test

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/egemengol/spread"
)

func Example() {
	ctx, cancel := context.WithCancel(context.Background())
	t := spread.NewTopic[int](ctx, 5)

	removeSync, _ := t.HandleSync(func(data int) {
		fmt.Printf("sync handler: %d\n", data)
	})

	recvChan, removeRecvChan, _ := t.GetRecvChannel(2)
	go func() {
		for data := range recvChan {
			fmt.Printf("recv channel: %d\n", data)
		}
	}()

	t.Publish(1)
	time.Sleep(100 * time.Millisecond)

	removeRecvChan()
	time.Sleep(100 * time.Millisecond)

	t.Publish(2)
	time.Sleep(100 * time.Millisecond)

	removeSync()
	t.HandleAsync(func(_ctx context.Context, data int) {
		fmt.Printf("async handler: %d\n", data)
	})
	time.Sleep(100 * time.Millisecond)

	t.Publish(3)
	time.Sleep(100 * time.Millisecond)

	cancel()
	time.Sleep(100 * time.Millisecond)

	err := t.Publish(4)
	if err == nil {
		log.Fatal("should not be able to publish after closing the topic")
	}

	// Output:
	// sync handler: 1
	// recv channel: 1
	// sync handler: 2
	// async handler: 3
}
