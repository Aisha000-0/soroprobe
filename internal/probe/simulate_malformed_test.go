package probe

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

func TestSimulateMalformedTransactionData(t *testing.T) {
	result, err := simulateWithFixture(t, "malformed-transaction-data", protocol.SimulateTransactionResponse{
		TransactionDataXDR: "not-base64!",
	})
	if err == nil {
		t.Fatalf("Simulate() result = %#v, want transaction data error", result)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "transaction data") {
		t.Fatalf("Simulate() error = %q, want it to mention transaction data", err)
	}
}

func TestSimulateMalformedReturnValue(t *testing.T) {
	transactionData, err := (xdr.SorobanTransactionData{}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	returnValue := "not-base64!"
	result, err := simulateWithFixture(t, "malformed-return-value", protocol.SimulateTransactionResponse{
		TransactionDataXDR: base64.StdEncoding.EncodeToString(transactionData),
		Results:            []protocol.SimulateHostFunctionResult{{ReturnValueXDR: &returnValue}},
	})
	if err == nil {
		t.Fatalf("Simulate() result = %#v, want return value error", result)
	}
}

func TestSimulateErrorWithoutTransactionData(t *testing.T) {
	result, err := simulateWithFixture(t, "failed-without-transaction-data", protocol.SimulateTransactionResponse{
		Error: "contract execution failed",
	})
	if err != nil {
		t.Fatalf("Simulate() error = %v, want a failed result", err)
	}
	if result.Success || result.Error != "contract execution failed" {
		t.Fatalf("Simulate() result = %#v, want a failed result with the RPC error", result)
	}
}

func simulateWithFixture(t *testing.T, function string, response protocol.SimulateTransactionResponse) (*SimulateResult, error) {
	t.Helper()

	accountID, err := strkey.Encode(strkey.VersionByteAccountID, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	contractID, err := strkey.Encode(strkey.VersionByteContract, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}

	fake := stellartest.NewFake(t)
	fake.Simulations = map[string]protocol.SimulateTransactionResponse{function: response}
	prober, err := New(Options{Client: fake, SourceAccount: accountID})
	if err != nil {
		t.Fatal(err)
	}
	return prober.Simulate(context.Background(), SimulateRequest{ContractID: contractID, Function: function})
}
