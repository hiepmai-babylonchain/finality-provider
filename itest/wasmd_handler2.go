package e2etest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type wasmdNode struct {
	cmd     *exec.Cmd
	pidFile string
	dataDir string
}

func newWasmdNode(dataDir string, cmd *exec.Cmd) *wasmdNode {
	return &wasmdNode{
		dataDir: dataDir,
		cmd:     cmd,
	}
}

func (n *wasmdNode) start() error {
	if err := n.cmd.Start(); err != nil {
		return err
	}

	pid, err := os.Create(filepath.Join(n.dataDir, fmt.Sprintf("%s.pid", "wasmd")))
	if err != nil {
		return err
	}

	n.pidFile = pid.Name()
	if _, err = fmt.Fprintf(pid, "%d\n", n.cmd.Process.Pid); err != nil {
		return err
	}

	if err := pid.Close(); err != nil {
		return err
	}

	return nil
}

func (n *wasmdNode) stop() (err error) {
	if n.cmd == nil || n.cmd.Process == nil {
		// return if not properly initialized
		// or error starting the process
		return nil
	}

	defer func() {
		err = n.cmd.Wait()
	}()

	return n.cmd.Process.Signal(os.Interrupt)
}

func (n *wasmdNode) cleanup() error {
	if n.pidFile != "" {
		if err := os.Remove(n.pidFile); err != nil {
			log.Printf("unable to remove file %s: %v", n.pidFile, err)
		}
	}

	dirs := []string{
		n.dataDir,
	}
	var err error
	for _, dir := range dirs {
		if err = os.RemoveAll(dir); err != nil {
			log.Printf("Cannot remove dir %s: %v", dir, err)
		}
	}
	return nil
}

func (n *wasmdNode) shutdown() error {
	if err := n.stop(); err != nil {
		return err
	}
	if err := n.cleanup(); err != nil {
		return err
	}
	return nil
}

type WasmdNodeHandler struct {
	wasmdNode *wasmdNode
}

func NewWasmdNodeHandler(t *testing.T) *WasmdNodeHandler {
	testDir, err := baseDir("ZWasmdTest")
	require.NoError(t, err)
	defer func() {
		if err != nil {
			err := os.RemoveAll(testDir)
			require.NoError(t, err)
		}
	}()

	setupWasmd(testDir)
	wh, err := startWasmd(testDir)
	require.NoError(t, err)
	time.Sleep(5 * time.Second)

	//setupScript := filepath.Join("wasmd_scripts", "setup_wasmd.sh")
	//startNodeScript := filepath.Join("wasmd_scripts", "start_node.sh")
	//
	////var stderr bytes.Buffer
	////initTestnetCmd := exec.Command("/bin/sh", "-c", setupScript)
	////initTestnetCmd.Stderr = &stderr
	////
	////err = initTestnetCmd.Run()
	////if err != nil {
	////	fmt.Printf("setup wasmd failed: %s \n", stderr.String())
	////}
	////require.NoError(t, err)
	//
	//time.Sleep(5 * time.Second)
	//
	//startCmd := exec.Command("/bin/sh", "-c", startNodeScript)
	////startCmd.Dir = nodeDataDir
	//startCmd.Stdout = os.Stdout
	//startCmd.Stderr = os.Stderr
	//
	//err = startCmd.Start()
	//if err != nil {
	//	log.Fatalf("Error starting wasmd node: %v", err)
	//}
	//
	//time.Sleep(5 * time.Second)

	return &WasmdNodeHandler{
		wasmdNode: newWasmdNode(testDir, wh),
	}
}

func (w *WasmdNodeHandler) Start() error {
	if err := w.wasmdNode.start(); err != nil {
		// try to cleanup after start error, but return original error
		_ = w.wasmdNode.cleanup()
		return err
	}
	return nil
}

func (w *WasmdNodeHandler) Stop() error {
	if err := w.wasmdNode.shutdown(); err != nil {
		return err
	}

	return nil
}

type TxResponse struct {
	TxHash string `json:"txhash"`
	Events []struct {
		Type       string `json:"type"`
		Attributes []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"attributes"`
	} `json:"events"`
}

func (w *WasmdNodeHandler) StoreWasmCode(homeDir, wasmFile string) (string, string, error) {
	cmd := exec.Command("wasmd", "tx", "wasm", "store", wasmFile,
		"--from", "validator", "--gas=auto", "--gas-prices=1ustake", "--gas-adjustment=1.3", "-y", "--chain-id=testing",
		"--node=http://localhost:26657", "-b", "sync", "-o", "json", "--keyring-backend=test")

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err != nil {
		return "", "", fmt.Errorf("error running wasmd store command: %v", err)
	}

	var txResp TxResponse
	resp := out.String()
	err = json.Unmarshal([]byte(resp), &txResp)
	if err != nil {
		return "", "", fmt.Errorf("error unmarshalling store wasm response: %v", err)
	}

	// Wait for a few seconds to ensure the transaction is processed
	time.Sleep(6 * time.Second)

	txhash := txResp.TxHash
	queryCmd := exec.Command("wasmd", "q", "tx", txhash, "-o", "json")

	var queryOut bytes.Buffer
	queryCmd.Stdout = &queryOut
	queryCmd.Stderr = os.Stderr

	err = queryCmd.Run()
	if err != nil {
		return "", "", fmt.Errorf("error querying transaction: %v", err)
	}

	var queryResp TxResponse
	err = json.Unmarshal(queryOut.Bytes(), &queryResp)
	if err != nil {
		return "", "", fmt.Errorf("error unmarshalling query response: %v", err)
	}

	var codeID, codeHash string
	for _, event := range queryResp.Events {
		if event.Type == "store_code" {
			for _, attr := range event.Attributes {
				if attr.Key == "code_id" {
					codeID = attr.Value
				} else if attr.Key == "code_checksum" {
					codeHash = attr.Value
				}
			}
		}
	}

	if codeID == "" || codeHash == "" {
		return "", "", fmt.Errorf("code ID or code checksum not found in transaction events")
	}

	return codeID, codeHash, nil
}

const (
	password = "1234567890" // Default password, can be replaced with an environment variable or parameter
	stake    = "ustake"     // Default staking token
	fee      = "ucosm"      // Default fee token
	moniker  = "node001"    // Default moniker
)

func runCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func wasmdInit(homeDir string) error {
	return runCommand("wasmd", "init", "--home", homeDir, "--chain-id", chainID, moniker)
}

func updateGenesisFile(homeDir string) error {
	genesisPath := filepath.Join(homeDir, "config", "genesis.json")
	sedCmd := fmt.Sprintf("sed -i. 's/\"stake\"/\"%s\"/' %s", stake, genesisPath)
	cmd := exec.Command("sh", "-c", sedCmd)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func wasmdKeysShow(homeDir string) error {
	return runCommand("wasmd", "keys", "show", "validator", "--home", homeDir, "--keyring-backend=test")
}

func wasmdKeysAdd(homeDir string) error {
	cmd := exec.Command("wasmd", "keys", "add", "validator", "--home", homeDir, "--keyring-backend=test")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("%s\n%s\n", password, password))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func addGenesisAccount(homeDir, address string) error {
	return runCommand("wasmd", "genesis", "add-genesis-account", address, fmt.Sprintf("1000000000000%s,1000000000000%s", stake, fee), "--home", homeDir, "--keyring-backend=test")
}

func addValidatorGenesisAccount(homeDir string) error {
	cmd := exec.Command("wasmd", "genesis", "add-genesis-account", "validator", fmt.Sprintf("1000000000000%s,1000000000000%s", stake, fee), "--home", homeDir, "--keyring-backend=test")
	cmd.Stdin = strings.NewReader(password)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func gentxValidator(homeDir string) error {
	cmd := exec.Command("wasmd", "genesis", "gentx", "validator", fmt.Sprintf("250000000%s", stake), "--chain-id="+chainID, "--amount="+fmt.Sprintf("250000000%s", stake), "--home", homeDir, "--keyring-backend=test")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("%s\n%s\n%s\n", password, password, password))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func collectGentxs(homeDir string) error {
	return runCommand("wasmd", "genesis", "collect-gentxs", "--home", homeDir)
}

func setupWasmd(homeDir string) {
	if err := wasmdInit(homeDir); err != nil {
		fmt.Printf("Error initializing wasmd: %v\n", err)
		return
	}

	if err := updateGenesisFile(homeDir); err != nil {
		fmt.Printf("Error updating genesis file: %v\n", err)
		return
	}

	if err := wasmdKeysAdd(homeDir); err != nil {
		fmt.Printf("Error adding validator key: %v\n", err)
		return
	}

	if err := addValidatorGenesisAccount(homeDir); err != nil {
		fmt.Printf("Error adding validator genesis account: %v\n", err)
		return
	}

	if err := gentxValidator(homeDir); err != nil {
		fmt.Printf("Error creating gentx for validator: %v\n", err)
		return
	}

	if err := collectGentxs(homeDir); err != nil {
		fmt.Printf("Error collecting gentxs: %v\n", err)
		return
	}
}

func startWasmd(homeDir string) (*exec.Cmd, error) {
	args := []string{
		"start",
		"--home", homeDir,
		"--rpc.laddr", "tcp://0.0.0.0:26657",
		"--log_level=info",
		"--trace",
	}

	cmd := exec.Command("wasmd", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd, nil

	//if err := cmd.Start(); err != nil {
	//	return fmt.Errorf("failed to start wasmd: %v", err)
	//}
	//
	//if err := cmd.Wait(); err != nil {
	//	return fmt.Errorf("wasmd exited with error: %v", err)
	//}
	//
	//return nil
}
