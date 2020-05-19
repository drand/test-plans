package main

import (
	"context"
	"time"

	"github.com/testground/sdk-go/network"
	"github.com/testground/sdk-go/runtime"
	"github.com/testground/sdk-go/sync"
)

var testcases = map[string]runtime.TestCaseFn{
	"ceremony": run,
}

func main() {
	runtime.InvokeMap(testcases)
}

func run(runenv *runtime.RunEnv) error {
	timeout := time.Duration(runenv.IntParam("timeout_secs")) * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	client := sync.MustBoundClient(ctx, runenv)
	/// --- Tear down
	defer func() {
		_, err := client.SignalAndWait(ctx, "end", runenv.TestInstanceCount)
		if err != nil {
			runenv.RecordFailure(err)
		} else {
			runenv.RecordSuccess()
		}
		client.Close()
	}()

	if !runenv.TestSidecar {
		return nil
	}

	netclient := network.NewClient(client, runenv)
	netclient.MustWaitNetworkInitialized(ctx)

	config := &network.Config{
		Network:       "default",
		Enable:        true,
		CallbackState: "network-configured",
	}

	netclient.MustConfigureNetwork(ctx, config)
	seq := client.MustSignalAndWait(ctx, "ip-allocation", runenv.TestInstanceCount)

	config.IPv4 = &runenv.TestSubnet.IPNet
	config.IPv4.IP = append(config.IPv4.IP[0:3], byte(seq))
	config.CallbackState = "ip-changed"

	netclient.MustConfigureNetwork(ctx, config)

	// spin up drand

	if seq == 1 {
		// coordinator.
	} else {
		// group member.
	}

	return nil
}
