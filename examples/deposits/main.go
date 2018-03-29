package main

import (
	"log"
	"math/rand"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/scorum/scorum-go"
	"github.com/scorum/scorum-go/apis/account_history"
	"github.com/scorum/scorum-go/sign"
	"github.com/scorum/scorum-go/transport/http"
	"github.com/scorum/scorum-go/types"
	"github.com/shopspring/decimal"
)

const (
	testNet        = "https://testnet.scorum.com"
	paymentAccount = "roselle"
	paymentWIF     = "5JwWJ2m2jGG9RPcpDix5AvkDzQZJoZvpUQScsDzzXWAKMs8Q6jH"
)

var chain = sign.TestChain

var (
	// deposits indexed with their ids
	deposits map[string]*Deposit
	// blockchain client
	client *scorumgo.Client
	// sync balances changes
	mutex sync.Mutex
	// history seq cursor
	seq uint32
)

type Deposit struct {
	// Unique deposit ID, TransferOperation Memo must contain this ID to update the balance
	ID string
	// Account, username in Scorum
	Account string
	// Balance in SCR (Scorum coins)
	Balance decimal.Decimal
}

func main() {
	deposits = map[string]*Deposit{
		"dep1": {ID: "dep1", Account: "noelle", Balance: decimal.Zero},
		"dep2": {ID: "dep2", Account: "gina", Balance: decimal.Zero},
		"dep3": {ID: "dep3", Account: "margy", Balance: decimal.Zero},
	}

	// create a blockchain rcp client
	client = scorumgo.NewClient(http.NewTransport(testNet))

	// seq is sequence number of the last processed history item
	seq = 0

	// listen for incoming payments
	go Monitor()
	// make payouts
	go Payout()

	// wait for signal to exit
	exit := make(chan os.Signal, 1)
	signal.Notify(exit, syscall.SIGINT, syscall.SIGTERM)
	<-exit
}

// Monitor payment account transactions
func Monitor() {
	for {
		var recentSeq uint32

		// passing -1 returns most recent history item
		recent, err := client.AccountHistory.GetAccountHistory(paymentAccount, -1, 0)
		if err != nil {
			log.Printf("failed to get recent account history: %s\n", err)
			goto Step
		}

		// recent contain only one item, take it sequence number
		for recentSeq = range recent {
			break
		}

		if recentSeq > seq {
			limit := recentSeq - seq - 1
			// retrieve transactions created since the last step
			history, err := client.AccountHistory.GetAccountHistory(paymentAccount, int32(recentSeq), int32(limit))
			if err != nil {
				log.Printf("failed to get recent account history: %s\n", err)
				goto Step
			}

			mutex.Lock()
			processHistory(history)
			seq = recentSeq
			mutex.Unlock()
		}

	Step:
		<-time.After(10 * time.Second)
	}
}

func processHistory(history account_history.AccountHistory) {
	// order keys (seq numbers), to process transaction in chronological order
	keys := make([]uint32, len(history))
	index := 0
	for k := range history {
		keys[index] = k
		index++
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[j] > keys[i]
	})

	// process the transfers
	for _, seq := range keys {
		trx := history[seq]
		for _, op := range trx.Operations {
			switch body := op.(type) {
			case *types.TransferOperation:
				log.Printf("transfer: %+v\n", op)
				processTransfer(seq, trx, body)
			default:
				log.Printf("operation %s: %+v\n", op.Type(), op)
			}
		}
	}
}

func processTransfer(seq uint32, trx *types.OperationObject, op *types.TransferOperation) {
	// transaction memo is a deposit
	depositID := op.Memo
	deposit, ok := deposits[depositID]
	if !ok {
		//unrecognized deposit, save it somewhere for later review
		log.Printf("unrecognized deposit: `%s`\n", op.Memo)
		return
	}

	// increase deposit balance
	deposit.Balance = deposit.Balance.Add(op.Amount.Decimal())

	log.Printf("%d %+v transfer from %s to deposit %s processed\n", seq, trx, op.From, deposit)
}

// makes random payout to the existing accounts
func Payout() {
	for {
		deposit := randomDeposit()
		amount, _ := types.AssetFromString("0.00000001 SCR")

		mutex.Lock()
		// check the balance
		if deposit.Balance.LessThan(amount.Decimal()) {
			log.Printf("not enough SCR to transfer to %s\n", deposit.Account)
			mutex.Unlock()
		} else {
			// decrease deposit balance
			deposit.Balance = deposit.Balance.Sub(amount.Decimal())
			mutex.Unlock()
			// run transfer
			go transfer(deposit, *amount)
		}

		<-time.After(time.Second * 5)
	}
}

func randomDeposit() *Deposit {
	// get a random deposit
	depositIDs := make([]string, len(deposits))
	idx := 0
	for d := range deposits {
		depositIDs[idx] = d
		idx++
	}
	id := depositIDs[rand.Intn(len(depositIDs))]
	return deposits[id]
}

func transfer(deposit *Deposit, amount types.Asset) {
	transferOp := types.TransferOperation{
		From:   paymentAccount,
		To:     deposit.Account,
		Amount: amount,
		Memo:   "payout from", //specify needed memo
	}

	revertBalance := func() {
		mutex.Lock()
		deposit.Balance = deposit.Balance.Add(amount.Decimal())
		mutex.Unlock()
	}

	// broadcast the transfer operation
	resp, err := client.Broadcast(chain, []string{paymentWIF}, &transferOp)
	if err != nil {
		log.Printf("failed to transfer %s to %s: %v", amount, deposit, err)
		revertBalance()

	} else {
		// Run a cycle to make sure that the transaction is irreversible
		for {
			prop, err := client.Database.GetDynamicGlobalProperties()
			if err != nil {
				log.Printf("failed to get dynamic global propeties: %s\n", err)
				goto Step
			}

			if resp.BlockNum > prop.LastIrreversibleBlockNum {
				// get operation in block
				ops, err := client.Database.GetOperationsInBlock(resp.BlockNum, false)
				if err != nil {
					log.Printf("failed to get operations in a block %d: %s\n", resp.BlockNum, err)
					goto Step
				}

				// find the transfer op in the list of operations
				for _, op := range ops.Operations {
					switch body := op.(type) {
					case *types.TransferOperation:
						if body.Equals(transferOp) {
							// transfer successful
							return
						}
					}
				}

				log.Printf("%+v has not been accepted", transferOp)
				revertBalance()
				return
			}

		Step:
			<-time.After(3 * time.Second)
		}
	}
}
