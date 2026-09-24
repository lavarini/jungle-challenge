package httpapi

import (
	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/money"
)

type walletResponse struct {
	ID       string      `json:"id"`
	PlayerID string      `json:"playerId"`
	Balance  money.Money `json:"balance"`
	Version  int64       `json:"version"`
}

func walletResponseOf(v app.WalletView) walletResponse {
	return walletResponse{ID: v.ID, PlayerID: v.PlayerID, Balance: v.Balance, Version: v.Version}
}

type submitResponse struct {
	TransactionID    string       `json:"transactionId"`
	Status           string       `json:"status"`
	FailureCode      string       `json:"failureCode,omitempty"`
	Balance          *money.Money `json:"balance,omitempty"`
	IdempotentReplay bool         `json:"idempotentReplay"`
}

func submitResponseOf(r app.SubmitResult) submitResponse {
	resp := submitResponse{TransactionID: r.TransactionID, Status: string(r.Status), FailureCode: string(r.FailureCode), IdempotentReplay: r.IdempotentReplay}
	if r.Balance.Valid() {
		b := r.Balance
		resp.Balance = &b
	}
	return resp
}
