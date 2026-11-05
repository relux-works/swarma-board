module github.com/relux-works/swarma-board

go 1.25.5

// P0 freezes only the libraries needed by the four agreed producer packets.
require (
	github.com/asdine/storm/v3 v3.2.1
	github.com/go-chi/chi/v5 v5.2.4
	github.com/relux-works/skill-agent-facing-api/agentquery v1.6.1
	github.com/spf13/cobra v1.10.2
	go.etcd.io/bbolt v1.3.4
)
