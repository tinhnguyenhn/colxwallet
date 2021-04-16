module github.com/tinhnguyenhn/colxwallet

require (
	github.com/tinhnguyenhn/colxd v0.21.0-beta.0.20210316172410-f86ae60936d7
	github.com/btcsuite/btclog v0.0.0-20170628155309-84c8d2346e9f
	github.com/tinhnguyenhn/colxutil v1.0.3-0.20201208143702-a53e38424cce
	github.com/tinhnguyenhn/colxutil/psbt v1.0.3-0.20201208143702-a53e38424cce
	github.com/tinhnguyenhn/colxwallet/wallet/txauthor v1.0.0
	github.com/tinhnguyenhn/colxwallet/wallet/txrules v1.0.0
	github.com/tinhnguyenhn/colxwallet/wallet/txsizes v1.0.0
	github.com/tinhnguyenhn/colxwallet/walletdb v1.3.4
	github.com/tinhnguyenhn/colxwallet/wtxmgr v1.2.0
	github.com/btcsuite/websocket v0.0.0-20150119174127-31079b680792
	github.com/davecgh/go-spew v1.1.1
	github.com/golang/protobuf v1.2.0
	github.com/jessevdk/go-flags v1.4.0
	github.com/jrick/logrotate v1.0.0
	github.com/kkdai/bstream v0.0.0-20181106074824-b3251f7901ec // indirect
	github.com/lightninglabs/gozmq v0.0.0-20191113021534-d20a764486bf
	github.com/lightninglabs/neutrino v0.11.0
	github.com/lightningnetwork/lnd/ticker v1.0.0
	github.com/stretchr/testify v1.5.1
	golang.org/x/crypto v0.0.0-20200510223506-06a226fb4e37
	golang.org/x/net v0.0.0-20190404232315-eb5bcb51f2a3
	google.golang.org/genproto v0.0.0-20190201180003-4b09977fb922 // indirect
	google.golang.org/grpc v1.18.0
)

replace github.com/tinhnguyenhn/colxwallet/walletdb => ./walletdb

replace github.com/tinhnguyenhn/colxwallet/wtxmgr => ./wtxmgr

replace github.com/tinhnguyenhn/colxwallet/wallet/txauthor => ./wallet/txauthor

replace github.com/tinhnguyenhn/colxwallet/wallet/txrules => ./wallet/txrules

replace github.com/tinhnguyenhn/colxwallet/wallet/txsizes => ./wallet/txsizes

go 1.13
