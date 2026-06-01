package pipeline

import "context"

// Dispatch reads lines from the shared lineCh, extracts a routing key,
// and sends each line to the appropriate worker channel based on consistent hash.
// When lineCh is closed or ctx is cancelled, it closes all worker channels.
//
// To prevent head-of-line blocking when a worker's channel is full (e.g. during
// a slow MongoDB flush), Dispatch first attempts a non-blocking send to the
// affinity worker. If that fails, it tries other workers. Only when all workers
// are full does it fall back to a blocking send on the affinity worker.
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
			continue
		default:
		}

		sent := false
		for offset := 1; offset < n && !sent; offset++ {
			altIdx := (idx + offset) % n
			select {
			case workerChs[altIdx] <- line:
				sent = true
			default:
			}
		}
		if sent {
			continue
		}

		select {
		case workerChs[idx] <- line:
		case <-ctx.Done():
			return
		}
	}
}
