package main

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// clusterCommand runs `localitas cluster <subcommand>`.
func clusterCommand(args []string) {
	if len(args) < 1 || args[0] != "force-active" {
		printClusterUsage()
		os.Exit(1)
	}
	forceActive(args[1:])
}

func printClusterUsage() {
	fmt.Fprintln(os.Stderr, "Usage: localitas cluster force-active [--accept-loss]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  force-active                Show whether this node may be forced active, and why")
	fmt.Fprintln(os.Stderr, "  force-active --accept-loss  Make this node the active, losing writes it never received")
}

// forceActive runs on the surviving node itself: it shows whether a forced
// flip is allowed (only when the cluster cannot form a majority) and, with
// --accept-loss, makes this node the active.
func forceActive(args []string) {
	accept := false
	for _, a := range args {
		if a != "--accept-loss" {
			printClusterUsage()
			os.Exit(1)
		}
		accept = true
	}
	c := newClient()
	ctx := context.Background()
	st, err := c.ForceActiveStatus(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
	fmt.Printf("Node %s, failover epoch %d: %s.\n", st.NodeID, st.FailoverEpoch, st.Reason)
	if !st.Allowed {
		os.Exit(1)
	}
	fmt.Printf("Not answering: %s.\n", strings.Join(st.DownVoters, ", "))
	if !accept {
		fmt.Println("Run again with --accept-loss to make this node the active, losing whatever those nodes committed but never sent here.")
		return
	}
	res, err := c.ForceActive(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
	fmt.Printf("Node %s is restarting as the active: incarnation %d, failover epoch %d, %d shards.\n", res.NodeID, res.NextIncarnation, res.FailoverEpoch, res.Shards)
}
