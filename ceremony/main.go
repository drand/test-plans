package main

import (
	"context"
	"fmt"
	"math"
	"net"
	"time"

	"github.com/drand/drand/beacon"
	"github.com/drand/drand/demo/node"
	"github.com/drand/drand/key"
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

	config := &network.Config{
		Network: "default",
		Enable:  true,
		Default: network.LinkShape{
			Latency:   10 * time.Millisecond,
			Bandwidth: 1 << 24,
		},
	}
	config.IPv4 = &runenv.TestSubnet.IPNet
	config.IPv4.IP = append(config.IPv4.IP[0:3], byte(seq))
	leaderIP := append(config.IPv4.IP[0:3], byte(1))
	config.CallbackState = "ip-set"

	err := netclient.ConfigureNetwork(ctx, config)
	if err != nil {
		runenv.RecordCrash(err)
		return err
	}
	runenv.RecordMessage("configured ip off seq. syncing")
	client.MustSignalAndWait(ctx, "ip-changed", runenv.TestInstanceCount)

	// spin up drand
	startTime := time.Now()
	isLeader := seq == 1

	n := node.NewLocalNode(int(seq), "10s", "~/", false, "0.0.0.0")
	defer n.Stop()

	// TODO: node tls keys would need to get shuffled all-all if using TLS.
	type NodePort struct {
		Port string
	}
	portTopic := sync.NewTopic("port-key", &NodePort{})
	var leaderPort string
	if isLeader {
		_, leaderPort, err := net.SplitHostPort(n.PrivateAddr())
		if err != nil {
			return err
		}
		_, err = client.Publish(ctx, portTopic, &NodePort{leaderPort})
		if err != nil {
			return err
		}
	}

	client.MustSignalAndWait(ctx, "port-share", runenv.TestInstanceCount)

	if !isLeader {
		tch := make(chan *NodePort)
		_, err := client.Subscribe(ctx, portTopic, tch)
		if err != nil {
			return nil
		}
		if p, ok := <-tch; !ok {
			return fmt.Errorf("Failed to learn leader port.")
		} else {
			leaderPort = p.Port
		}
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

	// run dkg
	threshold := math.Ceil(float64(runenv.TestInstanceCount) / 2.0)
	n.RunDKG(int(seq), int(threshold), "10s", isLeader, fmt.Sprintf("%s:%s", leaderIP.String(), leaderPort), 12)
	runenv.R().RecordPoint("dkg_complete", time.Now().Sub(startTime).Seconds())

	var grp *key.Group
	for i := 0; i < 10; i++ {
		grp = n.GetGroup()
		if grp != nil {
			break
		}
		time.Sleep(time.Second)
	}

	to := time.Until(time.Unix(grp.GenesisTime, 0).Add(3 * time.Second).Add(grp.Period))
	time.Sleep(to)

	beaconStart := time.Now()
	key.Save("group.toml", grp, false)
	rnd := beacon.CurrentRound(time.Now().Unix(), grp.Period, grp.GenesisTime)
	b, _ := n.GetBeacon("group.toml", rnd)
	if b == nil {
		return fmt.Errorf("Failed to get beacon.")
	}
	runenv.R().RecordPoint("beacon_lat", time.Now().Sub(beaconStart).Seconds())

	return nil
}
