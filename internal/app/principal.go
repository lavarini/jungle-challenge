package app

type Role string

const (
	RoleProvider Role = "wager:provider"
	RoleInternal Role = "wallet:internal"
)

// Principal is the authenticated caller. ProviderID comes from a verified
// token claim, never from the request body.
type Principal struct {
	Subject    string
	ClientID   string
	ProviderID string
	Roles      []Role
}

func (p Principal) Has(r Role) bool {
	for _, have := range p.Roles {
		if have == r {
			return true
		}
	}
	return false
}
