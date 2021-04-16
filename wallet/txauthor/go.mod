module github.com/tinhnguyenhn/colxwallet/wallet/txauthor

go 1.12

require (
	github.com/tinhnguyenhn/colxd v0.0.0-20190824003749-130ea5bddde3
	github.com/tinhnguyenhn/colxutil v0.0.0-20190425235716-9e5f4b9a998d
	github.com/tinhnguyenhn/colxwallet/wallet/txrules v1.0.0
	github.com/tinhnguyenhn/colxwallet/wallet/txsizes v1.0.0
)

replace github.com/tinhnguyenhn/colxwallet/wallet/txrules => ../txrules

replace github.com/tinhnguyenhn/colxwallet/wallet/txsizes => ../txsizes
