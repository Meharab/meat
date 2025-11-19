package web

import (
	"fmt"
	"net/http"

	"github.com/hyperledger/fabric-gateway/pkg/client"
)

type OrgSetup struct {
	OrgName      string
	MSPID        string
	CryptoPath   string
	CertPath     string
	KeyPath      string
	TLSCertPath  string
	PeerEndpoint string
	GatewayPeer  string
	Gateway      client.Gateway
}

func Serve(setups OrgSetup) {
	http.HandleFunc("/query", setups.Query)
	http.HandleFunc("/invoke", setups.Invoke)
	fmt.Println("Listening (http://localhost:3002/)...")
	if err := http.ListenAndServe(":3002", nil); err != nil {
		fmt.Println(err)
	}
}
