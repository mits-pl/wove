// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/woveterm/wove/pkg/wshrpc"
	"github.com/woveterm/wove/pkg/wshrpc/wshclient"
)

var setRTInfoCmd = &cobra.Command{
	Use:     "setrtinfo [-b {blockid|blocknum|this}] key=value ...",
	Short:   "set runtime info for a block",
	Args:    cobra.MinimumNArgs(1),
	RunE:    setRTInfoRun,
	PreRunE: preRunSetupRpcClient,
}

func init() {
	rootCmd.AddCommand(setRTInfoCmd)
}

func setRTInfoRun(cmd *cobra.Command, args []string) (rtnErr error) {
	defer func() {
		sendActivity("setrtinfo", rtnErr == nil)
	}()
	rtInfoData, err := parseMetaSets(args)
	if err != nil {
		return err
	}
	if len(rtInfoData) == 0 {
		return fmt.Errorf("no rtinfo keys specified")
	}
	fullORef, err := resolveBlockArg()
	if err != nil {
		return err
	}
	setRTInfoWshCmd := wshrpc.CommandSetRTInfoData{
		ORef: *fullORef,
		Data: rtInfoData,
	}
	err = wshclient.SetRTInfoCommand(RpcClient, setRTInfoWshCmd, &wshrpc.RpcOpts{Timeout: 2000})
	if err != nil {
		return fmt.Errorf("setting rtinfo: %v", err)
	}
	WriteStdout("rtinfo set\n")
	return nil
}
