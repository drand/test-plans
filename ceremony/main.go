package main

import (
	"context"
	"fmt"
	"math"
	"net"
	"time"

	"github.com/drand/drand/chain"
	"github.com/drand/drand/demo/node"
	"github.com/drand/drand/key"
	"github.com/drand/drand/metrics"
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

	seq := client.MustSignalAndWait(ctx, "ip-allocation", runenv.TestInstanceCount)
	runenv.RecordMessage("I am %d", seq)
	isLeader := seq == 1

	// spin up drand
	SavePrometheusAsDiagnostics(runenv, metrics.PrivateMetrics, "diagnostics.out")
	startTime := time.Now()

	myAddr := netclient.MustGetDataNetworkIP()
	n := node.NewLocalNode(int(seq), "10s", "~/", false, myAddr.String())
	defer n.Stop()

	// TODO: node tls keys would need to get shuffled all-all if using TLS.
	threshold := math.Ceil(float64(runenv.TestInstanceCount) / 2.0)

	type LeaderAddr struct {
		Addr string
	}
	leaderTopic := sync.NewTopic("leader", &LeaderAddr{})
	var leaderAddr string
	if isLeader {
		_, leaderPort, err := net.SplitHostPort(n.PrivateAddr())
		if err != nil {
			return err
		}
		leaderIP := netclient.MustGetDataNetworkIP()
		leaderAddr = fmt.Sprintf("%s:%s", leaderIP, leaderPort)
		_, err = client.Publish(ctx, leaderTopic, &LeaderAddr{leaderAddr})
		if err != nil {
			return err
		}
	}

	tch := make(chan *LeaderAddr)
	if !isLeader {
		_, err := client.Subscribe(ctx, leaderTopic, tch)
		if err != nil {
			return nil
		}
		p, ok := <-tch
		if !ok {
			return fmt.Errorf("failed to learn leader")
		}
		leaderAddr = p.Addr
	}

	client.MustSignalAndWait(ctx, "drand-start", runenv.TestInstanceCount)
	runenv.RecordMessage("Node started and running drand ceremony scenario.")
	n.Start("~/")

	alive := false
	for i := 0; i < 10; i++ {
		if !n.Ping() {
			time.Sleep(time.Second)
		} else {
			runenv.R().RecordPoint("first_ping", time.Now().Sub(startTime).Seconds())
			alive = true
			break
		}
	}
	if !alive {
		return fmt.Errorf("Node %d failed to start", seq)
	}

	var grp *key.Group
	// run dkg
	if isLeader {
		client.Publish(ctx, leaderTopic, &LeaderAddr{leaderAddr})
		grp = n.RunDKG(runenv.TestInstanceCount, int(threshold), "10s", isLeader, leaderAddr, 12)
		runenv.R().RecordPoint("dkg_complete", time.Now().Sub(startTime).Seconds())
	} else {
		<-tch
		grp = n.RunDKG(runenv.TestInstanceCount, int(threshold), "10s", isLeader, leaderAddr, 12)
		runenv.R().RecordPoint("dkg_complete", time.Now().Sub(startTime).Seconds())
	}

	to := time.Until(time.Unix(grp.GenesisTime, 0).Add(3 * time.Second).Add(grp.Period))
	time.Sleep(to)

	beaconStart := time.Now()
	key.Save("group.toml", grp, false)
	rnd := chain.CurrentRound(time.Now().Unix(), grp.Period, grp.GenesisTime)
	b, _ := n.GetBeacon("group.toml", rnd)
	if b == nil {
		return fmt.Errorf("failed to get beacon")
	}
	runenv.R().RecordPoint("beacon_lat", time.Now().Sub(beaconStart).Seconds())

	return nil
}
