package rpc

import (
	"time"

	"github.com/scorum/scorum-go/apis/account_history"
	"github.com/scorum/scorum-go/apis/database"
	"github.com/scorum/scorum-go/apis/network_broadcast"
	"github.com/scorum/scorum-go/caller"
	"github.com/scorum/scorum-go/sign"
	"github.com/scorum/scorum-go/types"
)

// Client can be used to access Scorum remote APIs.
//
// There is a public field for every Scorum API available,
// e.g. Client.Database corresponds to database_api.
type Client struct {
	cc caller.CallCloser

	// Database represents database_api
	Database *database.API

	// AccountHistory represents account_history_api
	AccountHistory *account_history.API

	// NetworkBroadcast represents network_broadcast_api
	NetworkBroadcast *network_broadcast.API
}

// NewClient creates a new RPC client that use the given CallCloser internally.
func NewClient(cc caller.CallCloser) *Client {
	client := &Client{cc: cc}
	client.Database = database.NewAPI(client.cc)
	client.AccountHistory = account_history.NewAPI(client.cc)
	client.NetworkBroadcast = network_broadcast.NewAPI(client.cc)
	return client
}

// Close should be used to close the client when no longer needed.
// It simply calls Close() on the underlying CallCloser.
func (client *Client) Close() error {
	return client.cc.Close()
}

func (client *Client) Broadcast(chain *sign.Chain, wifs []string, operations ...types.Operation) (*network_broadcast.BroadcastResponse, error) {
	props, err := client.Database.GetDynamicGlobalProperties()
	if err != nil {
		return nil, err
	}

	block, err := client.Database.GetBlock(props.LastIrreversibleBlockNum)
	if err != nil {
		return nil, err
	}

	refBlockPrefix, err := sign.RefBlockPrefix(block.Previous)
	if err != nil {
		return nil, err
	}

	expiration := props.Time.Add(10 * time.Minute)
	stx := sign.NewSignedTransaction(&types.Transaction{
		RefBlockNum:    sign.RefBlockNum(props.LastIrreversibleBlockNum - 1&0xffff),
		RefBlockPrefix: refBlockPrefix,
		Expiration:     &types.Time{Time: &expiration},
	})

	for _, op := range operations {
		stx.PushOperation(op)
	}

	if err = stx.Sign(wifs, chain); err != nil {
		return nil, err
	}

	return client.NetworkBroadcast.BroadcastTransactionSynchronous(stx.Transaction)
}
