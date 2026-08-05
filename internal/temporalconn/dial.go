// Package temporalconn is a tiny shared helper for cmd/worker and
// cmd/smoketest, both of which need to connect to Temporal at startup and
// tolerate it not being ready yet. temporalio/auto-setup accepts gRPC
// connections and even completes client.Dial's health check before its
// own namespace-registration step has finished, so a successful Dial
// alone is not sufficient readiness — the first real namespace-scoped RPC
// (e.g. ExecuteWorkflow) can still fail with NamespaceNotFound for a
// second or two after Dial succeeds.
package temporalconn

import (
	"context"
	"fmt"
	"time"

	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
)

// DialWithRetry retries client.Dial followed by a DescribeNamespace probe,
// on a fixed backoff bounded by maxAttempts — never an unbounded/blind
// wait — until Temporal both accepts a connection and has the target
// namespace ready, or the attempt budget is exhausted.
func DialWithRetry(ctx context.Context, hostPort, namespace string, maxAttempts int, backoff time.Duration) (client.Client, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		c, err := client.Dial(client.Options{HostPort: hostPort, Namespace: namespace})
		if err == nil {
			_, describeErr := c.WorkflowService().DescribeNamespace(ctx, &workflowservice.DescribeNamespaceRequest{Namespace: namespace})
			if describeErr == nil {
				return c, nil
			}
			lastErr = describeErr
			c.Close()
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("temporalconn: dial %s (namespace %q): %w (last attempt error: %v)", hostPort, namespace, ctx.Err(), lastErr)
		case <-time.After(backoff):
		}
	}
	return nil, fmt.Errorf("temporalconn: dial %s (namespace %q): exhausted %d attempts: %w", hostPort, namespace, maxAttempts, lastErr)
}
