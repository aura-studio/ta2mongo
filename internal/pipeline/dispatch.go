package pipeline

import "context"

// Dispatch reads lines from the shared lineCh, extracts a routing key,
// and sends each line to the appropriate worker channel based on consistent hash.
// When lineCh is closed or ctx is cancelled, it closes all worker channels.
func Dispatch(ctx context.Context, lineCh <-chan string, workerChs []chan string) {
	defer func() {
		for _, ch := range workerChs {
			close(ch)
		}
	}()

	n := len(workerChs)
	for line := range lineCh {
		key := ExtractRoutingKey(line)
		idx := RouteIndex(key, n)
		select {
		case workerChs[idx] <- line:
		case <-ctx.Done():
			return
		}
	}
}
