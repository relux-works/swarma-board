// This example checks synthetic protocol values only; it opens no storage.
package main

import (
	"fmt"

	dp "github.com/relux-works/swarma-board/dataprovider"
)

func main() {
	scope := dp.Scope{ProductID: "example-product", BoardID: "example-board"}
	current := dp.Revision{Scope: scope}
	next, err := dp.NextRevision(scope, current, current)
	if err != nil || next.Scope != scope || next.Counter != 1 {
		panic("synthetic revision check failed")
	}
	fmt.Printf("next revision: %d\n", next.Counter)

	// Equal counters do not make a revision from another board valid.
	foreign := dp.Revision{Scope: dp.Scope{ProductID: scope.ProductID, BoardID: "other-board"}}
	refused, err := dp.NextRevision(scope, current, foreign)
	code, ok := dp.CodeOf(err)
	if !ok || code != dp.InvalidRequest || refused != (dp.Revision{}) {
		panic("cross-board revision was not refused")
	}
	fmt.Printf("cross-board revision: %s\n", code)
}
