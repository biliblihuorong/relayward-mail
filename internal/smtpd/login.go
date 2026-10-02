package smtpd

import "errors"

// loginServer implements the obsolete SASL LOGIN mechanism (server side),
// which go-sasl does not provide. Flow:
//
//	server: 334 Username:
//	client: <username>
//	server: 334 Password:
//	client: <password>
type loginServer struct {
	auth     func(username, password string) error
	username string
	state    int // 0 = expect username, 1 = expect password
}

var errUnexpectedChallenge = errors.New("sasl login: unexpected client response")

// Next implements sasl.Server.
func (l *loginServer) Next(response []byte) ([]byte, bool, error) {
	switch l.state {
	case 0:
		if len(response) == 0 {
			// No initial response: ask for the username.
			return []byte("Username:"), false, nil
		}
		l.username = string(response)
		l.state = 1
		return []byte("Password:"), false, nil
	case 1:
		err := l.auth(l.username, string(response))
		return nil, true, err
	default:
		return nil, false, errUnexpectedChallenge
	}
}
