// Copyright (c) 2018 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package wallet

import (
	"bytes"
	"testing"
	"time"

	"github.com/tinhnguyenhn/colxd/chaincfg/chainhash"
	"github.com/tinhnguyenhn/colxd/txscript"
	"github.com/tinhnguyenhn/colxd/wire"
	"github.com/tinhnguyenhn/colxwallet/waddrmgr"
	"github.com/tinhnguyenhn/colxwallet/walletdb"
	_ "github.com/tinhnguyenhn/colxwallet/walletdb/bdb"
	"github.com/tinhnguyenhn/colxwallet/wtxmgr"
)

var (
	testBlockHash, _ = chainhash.NewHashFromStr(
		"00000000000000017188b968a371bab95aa43522665353b646e41865abae" +
			"02a4",
	)
	testBlockHeight int32 = 276425
)

// TestTxToOutput checks that no new address is added to he database if we
// request a dry run of the txToOutputs call. It also makes sure a subsequent
// non-dry run call produces a similar transaction to the dry-run.
func TestTxToOutputsDryRun(t *testing.T) {
	w, cleanup := testWallet(t)
	defer cleanup()

	// Create an address we can use to send some coins to.
	keyScope := waddrmgr.KeyScopeBIP0049Plus
	addr, err := w.CurrentAddress(0, keyScope)
	if err != nil {
		t.Fatalf("unable to get current address: %v", addr)
	}
	p2shAddr, err := txscript.PayToAddrScript(addr)
	if err != nil {
		t.Fatalf("unable to convert wallet address to p2sh: %v", err)
	}

	// Add an output paying to the wallet's address to the database.
	txOut := wire.NewTxOut(100000, p2shAddr)
	incomingTx := &wire.MsgTx{
		TxIn: []*wire.TxIn{
			{},
		},
		TxOut: []*wire.TxOut{
			txOut,
		},
	}
	addUtxo(t, w, incomingTx)

	// Now tell the wallet to create a transaction paying to the specified
	// outputs.
	txOuts := []*wire.TxOut{
		{
			PkScript: p2shAddr,
			Value:    10000,
		},
		{
			PkScript: p2shAddr,
			Value:    20000,
		},
	}

	// First do a few dry-runs, making sure the number of addresses in the
	// database us not inflated.
	dryRunTx, err := w.txToOutputs(txOuts, nil, 0, 1, 1000, true)
	if err != nil {
		t.Fatalf("unable to author tx: %v", err)
	}
	change := dryRunTx.Tx.TxOut[dryRunTx.ChangeIndex]

	addresses, err := w.AccountAddresses(0)
	if err != nil {
		t.Fatalf("unable to get addresses: %v", err)
	}

	if len(addresses) != 1 {
		t.Fatalf("expected 1 address, found %v", len(addresses))
	}

	dryRunTx2, err := w.txToOutputs(txOuts, nil, 0, 1, 1000, true)
	if err != nil {
		t.Fatalf("unable to author tx: %v", err)
	}
	change2 := dryRunTx2.Tx.TxOut[dryRunTx2.ChangeIndex]

	addresses, err = w.AccountAddresses(0)
	if err != nil {
		t.Fatalf("unable to get addresses: %v", err)
	}

	if len(addresses) != 1 {
		t.Fatalf("expected 1 address, found %v", len(addresses))
	}

	// The two dry-run TXs should be invalid, since they don't have
	// signatures.
	err = validateMsgTx(
		dryRunTx.Tx, dryRunTx.PrevScripts, dryRunTx.PrevInputValues,
	)
	if err == nil {
		t.Fatalf("Expected tx to be invalid")
	}

	err = validateMsgTx(
		dryRunTx2.Tx, dryRunTx2.PrevScripts, dryRunTx2.PrevInputValues,
	)
	if err == nil {
		t.Fatalf("Expected tx to be invalid")
	}

	// Now we do a proper, non-dry run. This should add a change address
	// to the database.
	tx, err := w.txToOutputs(txOuts, nil, 0, 1, 1000, false)
	if err != nil {
		t.Fatalf("unable to author tx: %v", err)
	}
	change3 := tx.Tx.TxOut[tx.ChangeIndex]

	addresses, err = w.AccountAddresses(0)
	if err != nil {
		t.Fatalf("unable to get addresses: %v", err)
	}

	if len(addresses) != 2 {
		t.Fatalf("expected 2 addresses, found %v", len(addresses))
	}

	err = validateMsgTx(tx.Tx, tx.PrevScripts, tx.PrevInputValues)
	if err != nil {
		t.Fatalf("Expected tx to be valid: %v", err)
	}

	// Finally, we check that all the transaction were using the same
	// change address.
	if !bytes.Equal(change.PkScript, change2.PkScript) {
		t.Fatalf("first dry-run using different change address " +
			"than second")
	}
	if !bytes.Equal(change2.PkScript, change3.PkScript) {
		t.Fatalf("dry-run using different change address " +
			"than wet run")
	}
}

// addUtxo add the given transaction to the wallet's database marked as a
// confirmed UTXO .
func addUtxo(t *testing.T, w *Wallet, incomingTx *wire.MsgTx) {
	var b bytes.Buffer
	if err := incomingTx.Serialize(&b); err != nil {
		t.Fatalf("unable to serialize tx: %v", err)
	}
	txBytes := b.Bytes()

	rec, err := wtxmgr.NewTxRecord(txBytes, time.Now())
	if err != nil {
		t.Fatalf("unable to create tx record: %v", err)
	}

	// The block meta will be inserted to tell the wallet this is a
	// confirmed transaction.
	block := &wtxmgr.BlockMeta{
		Block: wtxmgr.Block{
			Hash:   *testBlockHash,
			Height: testBlockHeight,
		},
		Time: time.Unix(1387737310, 0),
	}

	if err := walletdb.Update(w.db, func(tx walletdb.ReadWriteTx) error {
		ns := tx.ReadWriteBucket(wtxmgrNamespaceKey)
		err = w.TxStore.InsertTx(ns, rec, block)
		if err != nil {
			return err
		}
		err = w.TxStore.AddCredit(ns, rec, block, 0, false)
		if err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("failed inserting tx: %v", err)
	}
}
