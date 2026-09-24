package app

import "context"

type GetWallet struct {
	uow UnitOfWork
}

func NewGetWallet(uow UnitOfWork) *GetWallet { return &GetWallet{uow: uow} }

func (g *GetWallet) Execute(ctx context.Context, walletID string) (WalletView, error) {
	var view WalletView
	err := g.uow.Do(ctx, func(ctx context.Context, tx Tx) error {
		w, err := tx.Wallets().Get(ctx, walletID)
		if err != nil {
			return err
		}
		view = viewOf(w)
		return nil
	})
	return view, err
}
