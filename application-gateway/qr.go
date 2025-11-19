// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"crypto"
	"crypto/x509"
	"fmt"
	"io"
	"io/fs"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"time"
	"strings"

	"github.com/hyperledger/fabric-gateway/pkg/client"
	"github.com/hyperledger/fabric-gateway/pkg/identity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
    "encoding/json"
    "google.golang.org/grpc/status"
)

func envOrDefault(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

var (
	channelName     = envOrDefault("CHANNEL_NAME", "mychannel")
	chaincodeName   = envOrDefault("CHAINCODE_NAME", "livestock")
	mspID           = envOrDefault("MSP_ID", "Org1MSP")
	cryptoPath      = envOrDefault("CRYPTO_PATH", filepath.Join("..", "..", "test-network", "organizations", "peerOrganizations", "org1.example.com"))
	keyDirectory    = envOrDefault("KEY_DIRECTORY_PATH", filepath.Join(cryptoPath, "users", "User1@org1.example.com", "msp", "keystore"))
	certDirectory   = envOrDefault("CERT_DIRECTORY_PATH", filepath.Join(cryptoPath, "users", "User1@org1.example.com", "msp", "signcerts"))
	tlsCertPath     = envOrDefault("TLS_CERT_PATH", filepath.Join(cryptoPath, "peers", "peer0.org1.example.com", "tls", "ca.crt"))
	peerEndpoint    = envOrDefault("PEER_ENDPOINT", "localhost:7051")
	peerHostAlias   = envOrDefault("PEER_HOST_ALIAS", "peer0.org1.example.com")
	evaluateTimeout = 5 * time.Second
	endorseTimeout  = 15 * time.Second
	submitTimeout   = 5 * time.Second
	commitTimeout   = 1 * time.Minute
)

func main() {
	displayInputParameters()

	clientConn, err := newGrpcConnection()
	if err != nil {
		log.Fatalf("Failed to create gRPC connection: %v", err)
	}
	defer clientConn.Close()

	id, err := newIdentity()
	if err != nil {
		log.Fatalf("Failed to create identity: %v", err)
	}

	sign, err := newSign()
	if err != nil {
		log.Fatalf("Failed to create signer: %v", err)
	}

	gw, err := client.Connect(
		id,
		client.WithSign(sign),
		client.WithClientConnection(clientConn),
		client.WithEvaluateTimeout(evaluateTimeout),
		client.WithEndorseTimeout(endorseTimeout),
		client.WithSubmitTimeout(submitTimeout),
		client.WithCommitStatusTimeout(commitTimeout),
	)
	if err != nil {
		log.Fatalf("Failed to connect to gateway: %v", err)
	}
	defer gw.Close()

	network := gw.GetNetwork(channelName)
	contract := network.GetContract(chaincodeName)

	if err := initLedger(contract); err != nil {
		log.Printf("*** InitLedger error (continuing): %v", err)
	}

	productID := fmt.Sprintf("%d", time.Now().UnixNano()/1e6) // ms
	if err := createAsset(contract, productID); err != nil {
		log.Fatalf("*** CreateAsset failed: %v", err)
	}

	if err := readAssetByID(contract, productID); err != nil {
		log.Fatalf("*** ReadAsset failed: %v", err)
	}
}

func newGrpcConnection() (*grpc.ClientConn, error) {
    tlsPath := tlsCertPath
    if !filepath.IsAbs(tlsPath) {
        abs, err := filepath.Abs(tlsPath)
        if err == nil {
            tlsPath = abs
        }
    }

    if _, err := os.Stat(tlsPath); os.IsNotExist(err) {
        found := ""
        _ = filepath.Walk(cryptoPath, func(p string, info os.FileInfo, err error) error {
            if err != nil {
                return nil
            }
            if info.IsDir() {
                return nil
            }
            name := strings.ToLower(info.Name())
            if name == "ca.crt" && strings.Contains(p, "peer0.org1.example.com") {
                found = p
                return io.EOF 
            }
            return nil
        })

        if found == "" {
            _ = filepath.Walk(cryptoPath, func(p string, info os.FileInfo, err error) error {
                if err != nil {
                    return nil
                }
                if info.IsDir() {
                    return nil
                }
                if strings.HasSuffix(info.Name(), "ca.crt") {
                    found = p
                    return io.EOF
                }
                return nil
            })
        }

        if found == "" {
            return nil, fmt.Errorf("tls cert not found. Tried: %s and searched under crypto path: %s", tlsCertPath, cryptoPath)
        }
        log.Printf("Info: using discovered TLS cert at %s\n", found)
        tlsPath = found
    }

    certPEM, err := ioutil.ReadFile(tlsPath)
    if err != nil {
        return nil, fmt.Errorf("failed to read TLS cert at %s: %w", tlsPath, err)
    }

    certPool := x509.NewCertPool()
    if !certPool.AppendCertsFromPEM(certPEM) {
        return nil, fmt.Errorf("failed to append TLS cert from %s", tlsPath)
    }
    creds := credentials.NewClientTLSFromCert(certPool, peerHostAlias)

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    conn, err := grpc.DialContext(ctx, peerEndpoint, grpc.WithTransportCredentials(creds), grpc.WithBlock())
    if err != nil {
        return nil, fmt.Errorf("failed to dial %s: %w", peerEndpoint, err)
    }
    return conn, nil
}

func newIdentity() (*identity.X509Identity, error) {
    certPath, err := getFirstDirFileName(certDirectory)
    if err != nil {
        return nil, err
    }
    certPEM, err := os.ReadFile(certPath)
    if err != nil {
        return nil, fmt.Errorf("failed to read certificate %s: %w", certPath, err)
    }

    cert, err := identity.CertificateFromPEM(certPEM)
    if err != nil {
        return nil, fmt.Errorf("failed to parse certificate PEM: %w", err)
    }

    id, err := identity.NewX509Identity(mspID, cert)
    if err != nil {
        return nil, fmt.Errorf("failed to create X509 identity: %w", err)
    }
    return id, nil
}

func newSign() (identity.Sign, error) {
    keyPath, err := getFirstDirFileName(keyDirectory)
    if err != nil {
        return nil, err
    }
    keyPEM, err := os.ReadFile(keyPath)
    if err != nil {
        return nil, fmt.Errorf("failed to read private key %s: %w", keyPath, err)
    }

    privateKey, err := identity.PrivateKeyFromPEM(keyPEM)
    if err != nil {
        return nil, fmt.Errorf("failed to parse private key: %w", err)
    }

    sign, err := identity.NewPrivateKeySign(privateKey)
    if err != nil {
        return nil, fmt.Errorf("failed to create signer: %w", err)
    }

    return sign, nil
}

func parsePrivateKey(der []byte) (crypto.Signer, error) {
	if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		if signer, ok := k.(crypto.Signer); ok {
			return signer, nil
		}
	}
	if k, err := x509.ParseECPrivateKey(der); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return k, nil
	}
	return nil, fmt.Errorf("private key format unsupported")
}

func getFirstDirFileName(dir string) (string, error) {
	files, err := ioutil.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("failed to read dir %s: %w", dir, err)
	}
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		return filepath.Join(dir, f.Name()), nil
	}
	return "", fs.ErrNotExist
}

func initLedger(contract *client.Contract) error {
	log.Println("\n--> Submit Transaction: InitLedger")
	_, err := contract.SubmitTransaction("InitLedger")
	if err != nil {
		return err
	}
	log.Println("*** InitLedger transaction committed successfully")
	return nil
}

func createAsset(contract *client.Contract, productId string) error {
    asset := map[string]interface{}{
        "productId": productId,
        "product_name_en": "Frozen Hilsa Fish",
        "product_name_bn": "Frozen Hilsa Fish",
        "species_en": "Hilsa",
        "species_bn": "Hilsa",
        "product_image": "hilsa.jpg",
        "date_of_harvesting": "2025-09-01",
        "date_of_packaging": "2025-09-03",
        "expired_date": "2026-03-01",
        "mrp": 1200.5,
        "has_blast_freezer": true,
        "has_iqf": false,
        "has_vacuum_package": true,
        "has_food_grade_package_ldpe_4": true,
        "storage_en": "Cold Storage Dhaka",
        "storage_bn": "Cold Storage Dhaka",
        "water_source_en": []string{"Filtered water", "Arsenic"},
        "water_source_bn": []string{"Filtered water", "Arsenic"},
        "has_freezer_van_transportation": true,
        "batch_number": "BATCH-001",
        "lot_number": "LOT-001",
        "net_weight": 2.5,
        "certification_en": []string{"ISO22000", "HACCP"},
        "certification_bn": []string{"ISO22000", "HACCP"},
        "production_latitude": 23.8103,
        "production_longitude": 90.4125,
        "producer_organization_en": "Padma Fisheries Ltd",
        "producer_organization_bn": "Padma Fisheries Ltd",
        "livestock_collection_center_latitude": 23.90,
        "livestock_collection_center_longitude": 90.44,
        "collector_organization_en": "Dhaka Fish Collectors",
        "collector_organization_bn": "Dhaka Fish Collectors",
        "livestock_processing_unit_latitude": 23.75,
        "livestock_processing_unit_longitude": 90.39,
        "processor_organization_en": "Bangladesh Fish Processing Ltd",
        "processor_organization_bn": "Bangladesh Fish Processing Ltd",
    }

    payload, err := json.Marshal(asset)
    if err != nil {
        return fmt.Errorf("failed to marshal asset to JSON: %w", err)
    }

    log.Printf("\n--> Submit Transaction: CreateAsset (productId=%s)", productId)
    _, err = contract.SubmitTransaction("CreateAsset", string(payload))
    if err != nil {
        if s, ok := status.FromError(err); ok {
            log.Printf("SubmitTransaction failed: code=%v message=%q", s.Code(), s.Message())
            for i, d := range s.Details() {
                log.Printf(" - detail[%d]: %T => %+v", i, d, d)
            }
        }
        return fmt.Errorf("CreateAsset failed: %w", err)
    }

    log.Printf("*** CreateAsset transaction committed successfully (productId=%s)", productId)
    return nil
}


func readAssetByID(contract *client.Contract, productId string) error {
	log.Printf("\n--> Evaluate Transaction: ReadAsset (productId=%s)", productId)
	result, err := contract.EvaluateTransaction("ReadAsset", productId)
	if err != nil {
		return err
	}
	log.Printf("*** Result (ReadAsset): %s", string(result))
	return nil
}

func displayInputParameters() {
	fmt.Printf("channelName:       %s\n", channelName)
	fmt.Printf("chaincodeName:     %s\n", chaincodeName)
	fmt.Printf("mspId:             %s\n", mspID)
	fmt.Printf("cryptoPath:        %s\n", cryptoPath)
	fmt.Printf("keyDirectoryPath:  %s\n", keyDirectory)
	fmt.Printf("certDirectoryPath: %s\n", certDirectory)
	fmt.Printf("tlsCertPath:       %s\n", tlsCertPath)
	fmt.Printf("peerEndpoint:      %s\n", peerEndpoint)
	fmt.Printf("peerHostAlias:     %s\n", peerHostAlias)
}
