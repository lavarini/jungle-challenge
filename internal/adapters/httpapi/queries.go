package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/lavarini/backend-challenge-go/internal/adapters/wire"
	"github.com/lavarini/backend-challenge-go/internal/app"
	"github.com/lavarini/backend-challenge-go/internal/money"
	"github.com/lavarini/backend-challenge-go/internal/wagering"
)

type transactionResponse struct {
	TransactionID                  string       `json:"transactionId"`
	Origin                         string       `json:"origin"`
	Kind                           string       `json:"kind"`
	Status                         string       `json:"status"`
	WalletID                       string       `json:"walletId"`
	PlayerID                       string       `json:"playerId"`
	ProviderID                     string       `json:"providerId,omitempty"`
	ExternalTransactionID          string       `json:"externalTransactionId,omitempty"`
	RoundID                        string       `json:"roundId,omitempty"`
	GameID                         string       `json:"gameId,omitempty"`
	ReferenceExternalTransactionID string       `json:"referenceExternalTransactionId,omitempty"`
	Money                          money.Money  `json:"money"`
	FailureCode                    string       `json:"failureCode,omitempty"`
	Balance                        *money.Money `json:"balance,omitempty"`
	WalletVersion                  int64        `json:"walletVersion,omitempty"`
	Attempts                       int          `json:"attempts,omitempty"`
	NextAttemptAt                  *time.Time   `json:"nextAttemptAt,omitempty"`
	DeadlineAt                     *time.Time   `json:"deadlineAt,omitempty"`
	CreatedAt                      time.Time    `json:"createdAt"`
	CompletedAt                    *time.Time   `json:"completedAt,omitempty"`
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func transactionResponseOf(t *wagering.Transaction) transactionResponse {
	p := t.Provider()
	resp := transactionResponse{
		TransactionID: t.ID(), Origin: string(t.Origin()), Kind: string(t.Kind()), Status: string(t.Status()),
		WalletID: t.WalletID(), PlayerID: t.PlayerID(), ProviderID: p.ProviderID, ExternalTransactionID: p.ExternalID,
		RoundID: p.RoundID, GameID: p.GameID, ReferenceExternalTransactionID: p.ReferenceExternalID,
		Money: t.Amount(), FailureCode: string(t.FailureCode()), Attempts: t.Attempts(),
		CreatedAt: t.CreatedAt(), CompletedAt: optionalTime(t.CompletedAt()),
	}
	if balance, version, ok := t.Result(); ok {
		resp.Balance, resp.WalletVersion = &balance, version
	}
	if t.Status() == wagering.PendingReference {
		resp.NextAttemptAt, resp.DeadlineAt = optionalTime(t.NextAttemptAt()), optionalTime(t.DeadlineAt())
	}
	return resp
}

// providerScope: an internal caller sees everything; a provider only itself.
func providerScope(p app.Principal) string {
	if p.Has(app.RoleInternal) {
		return ""
	}
	return p.ProviderID
}

// maxExternalTransactionIDLength is the bound the use cases apply to every
// provider identifier, so a lookup accepts exactly what a submit accepts.
const maxExternalTransactionIDLength = app.MaxFieldLength

func (a *api) getTransaction(w http.ResponseWriter, r *http.Request) {
	id, err := wire.ParseUUID("transactionId", r.PathValue("transactionId"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	t, err := a.transactions.ByID(r.Context(), id, providerScope(principalFrom(r.Context())))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, transactionResponseOf(t))
}

func (a *api) getByExternalID(w http.ResponseWriter, r *http.Request) {
	externalID := r.PathValue("externalTransactionId")
	if len(externalID) > maxExternalTransactionIDLength {
		a.writeError(w, r, fmt.Errorf("%w: externalTransactionId exceeds %d characters", app.ErrInvalidInput, maxExternalTransactionIDLength))
		return
	}
	p := principalFrom(r.Context())
	providerID := r.PathValue("providerId")
	if scope := providerScope(p); scope != "" && scope != providerID {
		writeProblem(w, http.StatusForbidden, "PROVIDER_MISMATCH", "path provider does not match the authenticated provider", false, "")
		return
	}
	t, err := a.transactions.ByExternalID(r.Context(), providerID, externalID)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, transactionResponseOf(t))
}

type ledgerEntryResponse struct {
	ID            string      `json:"id"`
	TransactionID string      `json:"transactionId"`
	Direction     string      `json:"direction"`
	Amount        money.Money `json:"amount"`
	BalanceBefore money.Money `json:"balanceBefore"`
	BalanceAfter  money.Money `json:"balanceAfter"`
	WalletVersion int64       `json:"walletVersion"`
	CreatedAt     time.Time   `json:"createdAt"`
}

type ledgerResponse struct {
	Entries    []ledgerEntryResponse `json:"entries"`
	NextCursor string                `json:"nextCursor,omitempty"`
}

func (a *api) listLedger(w http.ResponseWriter, r *http.Request) {
	walletID, err := wire.ParseUUID("walletId", r.PathValue("walletId"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	// An absent limit defaults; an explicit one, including 0, is passed
	// through as-is and validated by the use case like any other value.
	limit := app.DefaultLedgerLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if limit, err = strconv.Atoi(raw); err != nil {
			a.writeError(w, r, fmt.Errorf("%w: limit must be an integer", app.ErrInvalidInput))
			return
		}
	}
	page, err := a.ledger.Execute(r.Context(), walletID, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	resp := ledgerResponse{Entries: []ledgerEntryResponse{}, NextCursor: page.NextCursor}
	for _, row := range page.Rows {
		e := row.Entry
		resp.Entries = append(resp.Entries, ledgerEntryResponse{
			ID: e.ID(), TransactionID: e.TransactionID(), Direction: string(e.Direction()), Amount: e.Amount(),
			BalanceBefore: e.BalanceBefore(), BalanceAfter: e.BalanceAfter(), WalletVersion: e.WalletVersion(), CreatedAt: e.CreatedAt(),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

type reconciliationResponse struct {
	WalletID           string      `json:"walletId"`
	StoredBalance      money.Money `json:"storedBalance"`
	CalculatedBalance  money.Money `json:"calculatedBalance"`
	Difference         money.Money `json:"difference"`
	Consistent         bool        `json:"consistent"`
	CheckedEntries     int         `json:"checkedEntries"`
	VersionMismatch    bool        `json:"versionMismatch"`
	ChainMismatch      bool        `json:"chainMismatch"`
	CurrencyMismatches int         `json:"currencyMismatches"`
}

func (a *api) reconcile(w http.ResponseWriter, r *http.Request) {
	walletID, err := wire.ParseUUID("walletId", r.PathValue("walletId"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	res, err := a.reconciler.Execute(r.Context(), walletID)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, reconciliationResponse{
		WalletID: res.WalletID, StoredBalance: res.Stored, CalculatedBalance: res.Calculated,
		Difference: res.Difference, Consistent: res.Consistent, CheckedEntries: res.CheckedEntries,
		VersionMismatch: res.VersionMismatch, ChainMismatch: res.ChainMismatch, CurrencyMismatches: res.CurrencyMismatches,
	})
}
