package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
)

type submitRequest struct {
	ProviderID                     string   `json:"providerId"`
	ExternalTransactionID          string   `json:"externalTransactionId"`
	PlayerID                       string   `json:"playerId"`
	WalletID                       string   `json:"walletId"`
	RoundID                        string   `json:"roundId"`
	GameID                         string   `json:"gameId"`
	Kind                           string   `json:"kind"`
	Money                          moneyDTO `json:"money"`
	ReferenceExternalTransactionID string   `json:"referenceExternalTransactionId,omitempty"`
}

func (a *api) submitWager(w http.ResponseWriter, r *http.Request) {
	principal := principalFrom(r.Context())
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		a.writeError(w, r, fmt.Errorf("%w: Idempotency-Key header is required", app.ErrInvalidInput))
		return
	}
	var req submitRequest
	if err := decodeJSON(r, &req); err != nil {
		a.writeError(w, r, err)
		return
	}
	// The body may not speak for another provider; checked before any I/O.
	if req.ProviderID != principal.ProviderID {
		writeProblem(w, http.StatusForbidden, "PROVIDER_MISMATCH", "providerId does not match the authenticated provider", false, "")
		return
	}
	cmd, err := req.toCommand(key, correlationID(r.Context()))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	res, err := a.submitter.Execute(r.Context(), cmd)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, statusFor(res), submitResponseOf(res))
}

func (req submitRequest) toCommand(key, correlation string) (app.SubmitCommand, error) {
	kind, err := wagering.ParseExternalKind(req.Kind)
	if err != nil {
		return app.SubmitCommand{}, fmt.Errorf("%w: kind: %w", app.ErrInvalidInput, err)
	}
	playerID, err := parseUUID("playerId", req.PlayerID)
	if err != nil {
		return app.SubmitCommand{}, err
	}
	walletID, err := parseUUID("walletId", req.WalletID)
	if err != nil {
		return app.SubmitCommand{}, err
	}
	m, err := req.Money.toMoney("money")
	if err != nil {
		return app.SubmitCommand{}, err
	}
	return app.SubmitCommand{
		ProviderID: req.ProviderID, ExternalTransactionID: req.ExternalTransactionID, IdempotencyKey: key,
		PlayerID: playerID, WalletID: walletID, RoundID: req.RoundID, GameID: req.GameID, Kind: kind, Money: m,
		ReferenceExternalTransactionID: req.ReferenceExternalTransactionID, CorrelationID: correlation, Source: app.SourceHTTP,
	}, nil
}

// statusFor: 201 new, 200 replay of a success, 202 pending, 422 persisted rejection.
func statusFor(res app.SubmitResult) int {
	switch res.Status {
	case wagering.Processed:
		if res.IdempotentReplay {
			return http.StatusOK
		}
		return http.StatusCreated
	case wagering.PendingReference:
		return http.StatusAccepted
	case wagering.Rejected, wagering.Failed:
		return http.StatusUnprocessableEntity
	}
	return http.StatusInternalServerError
}
