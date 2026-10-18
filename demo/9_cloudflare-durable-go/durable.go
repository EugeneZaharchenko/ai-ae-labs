// Package cloudflaredurable lists Cloudflare Durable Objects from Go.
//
// cloudflare-go is a control-plane client: it can list the objects in a
// namespace and say whether each one holds data. It cannot read or write an
// object's storage — that is only possible from code running inside the
// object (Workers runtime). So from Go this answers "which agent sessions
// have durable state?", not "what is in that state".
package cloudflaredurable

import (
	"context"
	"fmt"

	"github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/durable_objects"
)

// ObjectsWithState returns the IDs of objects in the namespace that hold
// stored data, following every page of the cursor-paginated API.
func ObjectsWithState(ctx context.Context, c *cloudflare.Client, accountID, namespaceID string) ([]string, error) {
	pager := c.DurableObjects.Namespaces.Objects.ListAutoPaging(ctx, namespaceID,
		durable_objects.NamespaceObjectListParams{AccountID: cloudflare.F(accountID)})
	var ids []string
	for pager.Next() {
		if o := pager.Current(); o.HasStoredData {
			ids = append(ids, o.ID)
		}
	}
	if err := pager.Err(); err != nil {
		return nil, fmt.Errorf("list durable objects in namespace %s: %w", namespaceID, err)
	}
	return ids, nil
}
