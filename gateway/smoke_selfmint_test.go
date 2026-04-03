package gateway

import (
	"context"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/ethclient"
)

var maxUint256 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

const erc20MinimalABI = `[
  {"type":"function","name":"approve","inputs":[{"name":"spender","type":"address"},{"name":"amount","type":"uint256"}],"outputs":[{"name":"","type":"bool"}],"stateMutability":"nonpayable"},
  {"type":"function","name":"balanceOf","inputs":[{"name":"account","type":"address"}],"outputs":[{"name":"","type":"uint256"}],"stateMutability":"view"}
]`

const gatewayWalletDepositABI = `[
  {"type":"function","name":"deposit","inputs":[{"name":"token","type":"address"},{"name":"value","type":"uint256"}],"outputs":[],"stateMutability":"nonpayable"}
]`

// TestSelfmintDeposit deposits USDC into Gateway and waits for the balance to appear.
// Useful to fund the forwarding test afterwards.
//
// Requires: SMOKE=1  PRIVATE_KEY=0x...  SOURCE_RPC=...
//
//	SMOKE=1 PRIVATE_KEY=0x... SOURCE_RPC=https://sepolia.base.org \
//	  go test ./gateway/ -run TestSelfmintDeposit -v -timeout 35m
func TestSelfmintDeposit(t *testing.T) {
	cfg := loadSmokeConfig(t)
	if cfg.sourceRPC == "" {
		t.Fatal("SOURCE_RPC env required")
	}

	client, err := NewClient(Config{PrivateKey: cfg.key})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	signer := client.SignerAddress()

	info, err := client.GetInfo(ctx)
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	srcDomain := info.LookupDomain(cfg.from)
	if srcDomain == nil {
		t.Fatalf("domain %d not found", cfg.from)
	}

	walletAddr := common.HexToAddress(srcDomain.WalletContract.Address)
	usdcAddr := resolveTestnetUSDC(t, cfg.from, "SOURCE_USDC")

	depositOnChain(t, ctx, cfg, client, walletAddr, usdcAddr)

	// Poll /v1/balances until balance >= amount.
	t.Logf("polling /v1/balances for domain %d (timeout 30m)...", cfg.from)
	deadline := time.Now().Add(30 * time.Minute)
	for {
		bal, err := client.GetBalances(ctx, &BalancesRequest{
			Token:   "USDC",
			Sources: []BalanceSource{{Depositor: signer.Hex()}},
		})
		if err != nil {
			t.Fatalf("GetBalances: %v", err)
		}
		for _, b := range bal.Balances {
			if b.Domain == cfg.from {
				balance, _ := new(big.Int).SetString(b.Balance, 10)
				if balance != nil && balance.Cmp(cfg.amount) >= 0 {
					t.Logf("balance=%s on domain %d — sufficient", balance, cfg.from)
					return
				}
				t.Logf("balance=%s on domain %d — waiting...", b.Balance, cfg.from)
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for Gateway balance")
		}
		time.Sleep(15 * time.Second)
	}
}

// TestSelfmintFull runs the complete self-managed mint flow:
// deposit → wait balance → transfer → gatewayMint → verify.
//
// Requires: SMOKE=1  PRIVATE_KEY=0x...  SOURCE_RPC=...  DEST_RPC=...
//
//	SMOKE=1 PRIVATE_KEY=0x... SOURCE_RPC=https://sepolia.base.org \
//	  DEST_RPC=https://sepolia.unichain.org \
//	  go test ./gateway/ -run TestSelfmintFull -v -timeout 35m
func TestSelfmintFull(t *testing.T) {
	cfg := loadSmokeConfig(t)
	if cfg.sourceRPC == "" {
		t.Fatal("SOURCE_RPC env required")
	}
	if cfg.destRPC == "" {
		t.Fatal("DEST_RPC env required")
	}

	client, err := NewClient(Config{PrivateKey: cfg.key})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	signer := client.SignerAddress()

	// --- Info ---
	info, err := client.GetInfo(ctx)
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	srcDomain := info.LookupDomain(cfg.from)
	dstDomain := info.LookupDomain(cfg.to)
	if srcDomain == nil {
		t.Fatalf("domain %d not found", cfg.from)
	}
	if dstDomain == nil {
		t.Fatalf("domain %d not found", cfg.to)
	}

	walletAddr := common.HexToAddress(srcDomain.WalletContract.Address)
	srcUSDC := resolveTestnetUSDC(t, cfg.from, "SOURCE_USDC")
	dstUSDC := resolveTestnetUSDC(t, cfg.to, "DEST_USDC")

	// --- Deposit ---
	depositOnChain(t, ctx, cfg, client, walletAddr, srcUSDC)

	// --- Wait for balance ---
	t.Logf("polling /v1/balances for domain %d (timeout 30m)...", cfg.from)
	deadline := time.Now().Add(30 * time.Minute)
	for {
		bal, err := client.GetBalances(ctx, &BalancesRequest{
			Token:   "USDC",
			Sources: []BalanceSource{{Depositor: signer.Hex()}},
		})
		if err != nil {
			t.Fatalf("GetBalances: %v", err)
		}
		for _, b := range bal.Balances {
			if b.Domain == cfg.from {
				balance, _ := new(big.Int).SetString(b.Balance, 10)
				if balance != nil && balance.Cmp(cfg.amount) >= 0 {
					t.Logf("balance=%s — sufficient", balance)
					goto ready
				}
				t.Logf("balance=%s — waiting...", b.Balance)
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for Gateway balance")
		}
		time.Sleep(15 * time.Second)
	}
ready:

	// --- Build spec ---
	spec, err := client.BuildTransferSpec(TransferSpecParams{
		SourceDomain:      cfg.from,
		DestinationDomain: cfg.to,
		SourceWallet:      walletAddr,
		DestinationMinter: common.HexToAddress(dstDomain.MinterContract.Address),
		SourceToken:       srcUSDC,
		DestinationToken:  dstUSDC,
		Depositor:         signer,
		Recipient:         signer,
		Value:             cfg.amount,
	})
	if err != nil {
		t.Fatalf("BuildTransferSpec: %v", err)
	}

	// --- Estimate ---
	intent := BurnIntent{Spec: *spec}
	est, err := client.Estimate(ctx, []BurnIntent{intent}, nil)
	if err != nil {
		t.Fatalf("Estimate: %v", err)
	}
	filled := est.Body[0].BurnIntent
	t.Logf("estimate: maxBlockHeight=%s maxFee=%s", filled.MaxBlockHeight, filled.MaxFee)

	// --- Sign ---
	sig, err := client.SignBurnIntent(&filled)
	if err != nil {
		t.Fatalf("SignBurnIntent: %v", err)
	}

	// --- Transfer (self-managed, no forwarder) ---
	resp, err := client.Transfer(ctx, []SignedBurnIntentRequest{{
		BurnIntent: &filled,
		Signature:  hexutil.Encode(sig),
	}}, nil)
	if err != nil {
		t.Fatalf("Transfer: %v", err)
	}
	t.Logf("transferId=%s", resp.TransferID)
	t.Logf("attestation=%d bytes  signature=%d bytes", len(resp.Attestation)/2, len(resp.Signature)/2)

	// --- gatewayMint on destination ---
	attestationBytes, err := hexutil.Decode(resp.Attestation)
	if err != nil {
		t.Fatalf("decode attestation: %v", err)
	}
	sigBytes, err := hexutil.Decode(resp.Signature)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}

	minterAddr := common.HexToAddress(dstDomain.MinterContract.Address)
	t.Logf("submitting gatewayMint on domain %d (minter=%s)", cfg.to, minterAddr)

	txHash, err := client.SubmitMint(ctx, cfg.destRPC, minterAddr, attestationBytes, sigBytes)
	if err != nil {
		t.Fatalf("SubmitMint: %v", err)
	}
	t.Logf("gatewayMint tx=%s", txHash)

	// Wait for mint tx.
	destClient, err := ethclient.DialContext(ctx, cfg.destRPC)
	if err != nil {
		t.Fatalf("dial dest RPC: %v", err)
	}
	defer destClient.Close()

	mintDeadline := time.Now().Add(5 * time.Minute)
	for {
		receipt, err := destClient.TransactionReceipt(ctx, txHash)
		if err == nil {
			if receipt.Status == 0 {
				t.Fatal("gatewayMint reverted")
			}
			t.Logf("gatewayMint confirmed (block %d)", receipt.BlockNumber.Uint64())
			break
		}
		if time.Now().After(mintDeadline) {
			t.Fatal("timed out waiting for gatewayMint tx")
		}
		time.Sleep(3 * time.Second)
	}

	// --- Verify dest USDC balance ---
	erc20Parsed, _ := abi.JSON(strings.NewReader(erc20MinimalABI))
	usdcDest := bind.NewBoundContract(dstUSDC, erc20Parsed, destClient, destClient, destClient)

	var result []any
	if err := usdcDest.Call(&bind.CallOpts{Context: ctx}, &result, "balanceOf", signer); err != nil {
		t.Fatalf("balanceOf on dest: %v", err)
	}
	t.Logf("destination USDC balance: %s", result[0].(*big.Int))
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func depositOnChain(t *testing.T, ctx context.Context, cfg smokeConfig, client *Client, walletAddr, usdcAddr common.Address) {
	t.Helper()

	srcClient, err := ethclient.DialContext(ctx, cfg.sourceRPC)
	if err != nil {
		t.Fatalf("dial source RPC: %v", err)
	}
	defer srcClient.Close()

	chainID, err := srcClient.ChainID(ctx)
	if err != nil {
		t.Fatalf("get chain ID: %v", err)
	}
	t.Logf("source chainId=%s", chainID)

	auth, err := bind.NewKeyedTransactorWithChainID(cfg.key, chainID)
	if err != nil {
		t.Fatalf("create transactor: %v", err)
	}
	auth.Context = ctx

	erc20Parsed, _ := abi.JSON(strings.NewReader(erc20MinimalABI))
	walletParsed, _ := abi.JSON(strings.NewReader(gatewayWalletDepositABI))

	usdc := bind.NewBoundContract(usdcAddr, erc20Parsed, srcClient, srcClient, srcClient)
	wallet := bind.NewBoundContract(walletAddr, walletParsed, srcClient, srcClient, srcClient)

	signer := client.SignerAddress()

	// Check USDC balance.
	var balResult []any
	if err := usdc.Call(&bind.CallOpts{Context: ctx}, &balResult, "balanceOf", signer); err != nil {
		t.Fatalf("balanceOf: %v", err)
	}
	usdcBal := balResult[0].(*big.Int)
	t.Logf("USDC balance on source: %s", usdcBal)

	if usdcBal.Cmp(cfg.amount) < 0 {
		t.Fatalf("insufficient USDC: have %s, need %s — get testnet USDC from https://faucet.circle.com/", usdcBal, cfg.amount)
	}

	// Approve.
	t.Log("approving GatewayWallet...")
	tx, err := usdc.Transact(auth, "approve", walletAddr, maxUint256)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	t.Logf("approve tx=%s", tx.Hash())
	receipt, err := bind.WaitMined(ctx, srcClient, tx)
	if err != nil {
		t.Fatalf("wait approve: %v", err)
	}
	if receipt.Status == 0 {
		t.Fatal("approve reverted")
	}

	// Deposit.
	t.Logf("depositing %s USDC...", cfg.amount)
	tx, err = wallet.Transact(auth, "deposit", usdcAddr, cfg.amount)
	if err != nil {
		t.Fatalf("deposit: %v", err)
	}
	t.Logf("deposit tx=%s", tx.Hash())
	receipt, err = bind.WaitMined(ctx, srcClient, tx)
	if err != nil {
		t.Fatalf("wait deposit: %v", err)
	}
	if receipt.Status == 0 {
		t.Fatal("deposit reverted")
	}
	t.Logf("deposit confirmed (block %d)", receipt.BlockNumber.Uint64())
}
