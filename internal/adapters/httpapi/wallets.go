package httpapi

import (
	"net/http"

	"github.com/lavarini/backend-challenge-go/internal/app"
)

type openWalletRequest struct {
	PlayerID       string   `json:"playerId"`
	InitialBalance moneyDTO `json:"initialBalance"`
}

func (a *api) openWallet(w http.ResponseWriter, r *http.Request) {
	var req openWalletRequest
	if err := decodeJSON(r, &req); err != nil {
		a.writeError(w, r, err)
		return
	}
	playerID, err := parseUUID("playerId", req.PlayerID)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	initial, err := req.InitialBalance.toMoney("initialBalance")
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	view, err := a.opener.Execute(r.Context(), app.OpenWalletCommand{
		PlayerID: playerID, InitialBalance: initial, CorrelationID: correlationID(r.Context()),
	})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, walletResponseOf(view))
}

func (a *api) getWallet(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID("walletId", r.PathValue("walletId"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	view, err := a.reader.Execute(r.Context(), id)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, walletResponseOf(view))
}
